package discovery

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

const (
	// Steady calls per second per endpoint (one account's API in one
	// region, or one Azure host), well under what AWS and Azure allow.
	callsPerSecond = 10
	callBurst      = 20
	// Resource Graph allows a user about 15 queries per 5 seconds.
	graphCallsPerSecond = 3
	graphBurst          = 15
	// maxStandDown caps how long one answer can make an endpoint wait.
	maxStandDown = 5 * time.Minute
	// maxThrottleRetries: retries of a call the cloud told to wait (not for
	// AWS, whose SDK retries).
	maxThrottleRetries = 4
	// defaultStandDown when a 429 doesn't say how long.
	defaultStandDown   = 5 * time.Second
	defaultCallTimeout = 60 * time.Second
	throttlePeekBytes  = 4 << 10
)

// AWS error codes meaning "slow down".
var awsThrottleCodes = []string{
	"Throttling", "ThrottlingException", "ThrottledException", "RequestLimitExceeded", "TooManyRequestsException",
	"RequestThrottled", "RequestThrottledException", "SlowDown", "ProvisionedThroughputExceededException",
	"EC2ThrottledException", "BandwidthLimitExceeded", "PriorRequestNotComplete",
}

// Throttle paces one connection's calls per endpoint and stands an
// endpoint down for as long as the cloud asks (Retry-After, Azure's quota
// headers). One Throttle serves all of a connection's scans, so discovery
// and health scans share the same budget.
type Throttle struct {
	mu        sync.Mutex
	endpoints map[string]*endpoint
}

type endpoint struct {
	limiter *rate.Limiter
	until   time.Time
}

// scanStats counts, for one scan, the calls that were throttled and the
// time spent standing down.
type scanStats struct {
	throttled atomic.Int64
	stoodDown atomic.Int64
}

type statsKey struct{}

type scopeKey struct{}

func NewThrottle() *Throttle { return &Throttle{endpoints: map[string]*endpoint{}} }

// client wraps base's transport; base's timeout applies to each attempt,
// not to the waits between them.
func (t *Throttle) client(base *http.Client) *http.Client {
	transport, timeout := http.DefaultTransport, defaultCallTimeout
	if base != nil {
		if base.Transport != nil {
			transport = base.Transport
		}
		if base.Timeout > 0 {
			timeout = base.Timeout
		}
	}
	return &http.Client{Transport: &throttledTransport{throttle: t, base: transport, timeout: timeout}}
}

// wait blocks until the endpoint's stand-down is over and it has a token.
func (t *Throttle) wait(ctx context.Context, key string, graph bool, stats *scanStats) error {
	t.mu.Lock()
	e, ok := t.endpoints[key]
	if !ok {
		limit, burst := rate.Limit(callsPerSecond), callBurst
		if graph {
			limit, burst = graphCallsPerSecond, graphBurst
		}
		e = &endpoint{limiter: rate.NewLimiter(limit, burst)}
		t.endpoints[key] = e
	}
	pause := time.Until(e.until)
	t.mu.Unlock()
	if pause > 0 {
		if stats != nil {
			stats.stoodDown.Add(int64(pause))
		}
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return e.limiter.Wait(ctx)
}

func (t *Throttle) standDown(key string, pause time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.endpoints[key]; ok {
		if until := time.Now().Add(min(pause, maxStandDown)); until.After(e.until) {
			e.until = until
		}
	}
}

type throttledTransport struct {
	throttle *Throttle
	base     http.RoundTripper
	timeout  time.Duration
}

func (t *throttledTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	ctx := request.Context()
	stats, _ := ctx.Value(statsKey{}).(*scanStats)
	scope, _ := ctx.Value(scopeKey{}).(string)
	key := scope + "|" + request.URL.Host
	graph := strings.Contains(strings.ToLower(request.URL.Path), "/microsoft.resourcegraph/")
	if graph {
		key += "|graph"
	}
	replayable := request.Body == nil || request.Body == http.NoBody || request.GetBody != nil
	// AWS's SDK retries with its own backoff (up to awsMaxAttempts), so a
	// retry here would multiply its attempts: for AWS, only stand down.
	retries := maxThrottleRetries
	if fromAWS, _ := ctx.Value(awsKey{}).(bool); fromAWS ||
		strings.HasSuffix(request.URL.Hostname(), ".amazonaws.com") {
		retries = 0
	}
	for attempt := 0; ; attempt++ {
		if err := t.throttle.wait(ctx, key, graph, stats); err != nil {
			return nil, err
		}
		attemptRequest := request
		if attempt > 0 && request.GetBody != nil {
			body, err := request.GetBody()
			if err != nil {
				return nil, err
			}
			attemptRequest = request.Clone(ctx)
			attemptRequest.Body = body
		}
		attemptCtx, cancel := context.WithTimeout(ctx, t.timeout)
		response, err := t.base.RoundTrip(attemptRequest.WithContext(attemptCtx))
		if err != nil {
			cancel()
			return nil, err
		}
		response.Body = &cancelOnClose{ReadCloser: response.Body, cancel: cancel}
		pause, throttled := standDownFor(response)
		if throttled && stats != nil {
			stats.throttled.Add(1)
		}
		if pause > 0 {
			t.throttle.standDown(key, pause)
		}
		// Azure's SDK-less calls are retried here, after the wait asked for.
		if !throttled || pause == 0 || attempt >= retries || !replayable {
			return response, nil
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
	}
}

// standDownFor reads how long the cloud wants this endpoint left alone,
// and whether the call itself was throttled.
func standDownFor(response *http.Response) (time.Duration, bool) {
	throttled := response.StatusCode == http.StatusTooManyRequests
	if !throttled && (response.StatusCode == http.StatusBadRequest ||
		response.StatusCode == http.StatusServiceUnavailable || response.StatusCode == http.StatusForbidden) {
		throttled = peekThrottled(response)
	}
	pause := retryAfter(response.Header, time.Now())
	if pause == 0 && (throttled || response.Header.Get("x-ms-user-quota-remaining") == "0") {
		// Resource Graph: the quota left in this window, and when it resets.
		pause = quotaResetsAfter(response.Header.Get("x-ms-user-quota-resets-after"))
	}
	if pause == 0 && response.StatusCode == http.StatusTooManyRequests {
		pause = defaultStandDown
	}
	return pause, throttled
}

// peekThrottled looks for an AWS throttling code in an error body, leaving
// the body readable for the SDK.
func peekThrottled(response *http.Response) bool {
	if code := response.Header.Get("x-amzn-ErrorType"); code != "" {
		return slices.Contains(awsThrottleCodes, strings.SplitN(code, ":", 2)[0])
	}
	head, _ := io.ReadAll(io.LimitReader(response.Body, throttlePeekBytes))
	response.Body = &cancelOnClose{
		ReadCloser: readCloser{Reader: io.MultiReader(bytes.NewReader(head), response.Body), Closer: response.Body},
		cancel:     func() {},
	}
	text := string(head)
	for _, code := range awsThrottleCodes {
		if strings.Contains(text, "<Code>"+code+"</Code>") || strings.Contains(text, `"`+code+`"`) ||
			strings.Contains(text, "#"+code) {
			return true
		}
	}
	return false
}

// costManagementRetryAfter: Azure Cost Management says when each of its
// quotas (per client type, entity and tenant) allows the next query.
var costManagementRetryAfter = []string{
	"x-ms-ratelimit-microsoft.costmanagement-clienttype-retry-after",
	"x-ms-ratelimit-microsoft.costmanagement-entity-retry-after",
	"x-ms-ratelimit-microsoft.costmanagement-tenant-retry-after",
	"x-ms-ratelimit-microsoft.costmanagement-qpu-retry-after",
}

// retryAfter is Retry-After (seconds or an HTTP date), Azure's
// x-ms-retry-after-ms, or the longest of Cost Management's own.
func retryAfter(header http.Header, now time.Time) time.Duration {
	if ms, err := strconv.Atoi(header.Get("x-ms-retry-after-ms")); err == nil && ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	var costManagement time.Duration
	for _, name := range costManagementRetryAfter {
		if seconds, err := strconv.Atoi(strings.TrimSpace(header.Get(name))); err == nil && seconds > 0 {
			costManagement = max(costManagement, time.Duration(seconds)*time.Second)
		}
	}
	if costManagement > 0 {
		return costManagement
	}
	value := strings.TrimSpace(header.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		return time.Duration(max(seconds, 0)) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}

// quotaResetsAfter parses Resource Graph's hh:mm:ss.
func quotaResetsAfter(value string) time.Duration {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 3 {
		return 0
	}
	var total time.Duration
	for i, unit := range []time.Duration{time.Hour, time.Minute, time.Second} {
		number, err := strconv.ParseFloat(parts[i], 64)
		if err != nil || number < 0 {
			return 0
		}
		total += time.Duration(number * float64(unit))
	}
	return total
}

// withStats starts a scan's counters.
func withStats(ctx context.Context) (context.Context, *scanStats) {
	stats := &scanStats{}
	return context.WithValue(ctx, statsKey{}, stats), stats
}

// withScope keys pacing by account too: AWS throttles each account apart.
func withScope(ctx context.Context, scope string) context.Context {
	return context.WithValue(ctx, scopeKey{}, scope)
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

type readCloser struct {
	io.Reader
	io.Closer
}
