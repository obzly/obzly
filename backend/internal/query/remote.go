package query

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"obzly/internal/telemetry"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"
)

// Executors register as the NATS micro service ServiceName, each with its
// own fragment subject and its result address in the service metadata;
// coordinators find them by pinging the service.
const (
	ServiceName     = "obzly-query"
	execSubjectBase = "obzly.query.exec."
	// cancelSubjectBase + fragment id: the coordinator gave up on it (the
	// user left, a newer search replaced it, the deadline passed), so the
	// executor stops it instead of using a slot until fragmentTimeout.
	cancelSubjectBase = "obzly.query.cancel."
	resultAddrKey     = "results"
	affinityKey       = "affinity"
	discoveryWait     = 300 * time.Millisecond
	discoveryMissing  = 3
	drainTimeout      = 30 * time.Second
	// signatureHeader carries a fragment's HMAC-SHA256, keyed with
	// QUERIER_TOKEN: any client holding the NATS token can publish to an
	// executor's subject, so an executor runs only SQL a coordinator signed.
	signatureHeader = "Obzly-Fragment-Signature"
)

// discoveryEvery: how often a registry re-pings executors (tests shorten it).
var discoveryEvery = 5 * time.Second

// ErrExecutorLeft: the executor running a fragment dropped out of discovery
// (killed, or its node went away), so the fragment is retried elsewhere
// instead of waiting for fragmentTimeout.
var ErrExecutorLeft = errors.New("query executor left")

// ServeConfig runs this process as a query executor (SERVICE_MODE=querier).
type ServeConfig struct {
	Conn     *nats.Conn
	Executor Executor
	// ResultsDir holds results until their coordinator fetches them.
	ResultsDir string
	// ListenAddr binds the results listener; AdvertiseAddr is how
	// coordinators reach it (defaults to the bound address, or this host's
	// name and the bound port when bound to every interface).
	ListenAddr    string
	AdvertiseAddr string
	Token         string
	// Concurrency bounds fragments running at once; more wait.
	Concurrency int
	Log         *slog.Logger
	// Affinity is what segments are spread over executors by (see Ranked):
	// stable across restarts on the same disk, so a persistent cache keeps
	// serving the segments it holds. Defaults to the instance ID.
	Affinity string
	// TLS serves the results listener over TLS; nil is plaintext.
	TLS *tls.Config
}

// Registry lists the live executors, refreshed in the background.
type Registry struct {
	conn    *nats.Conn
	results *resultsClient
	log     *slog.Logger
	token   string

	mu        sync.Mutex
	executors map[string]*remoteExecutor
	missed    map[string]int
	started   bool
}

// remoteExecutor runs fragments on an executor over NATS and fetches the
// results over gRPC.
type remoteExecutor struct {
	id         string
	affinity   string
	resultAddr string
	conn       *nats.Conn
	results    *resultsClient
	token      string
	// gone is closed when the registry drops the executor.
	gone chan struct{}
}

var defaultRegistry struct {
	sync.Mutex
	registry *Registry
}

// SetDefaultRegistry makes RemoteExecutors list registry's executors.
func SetDefaultRegistry(registry *Registry) {
	defaultRegistry.Lock()
	defer defaultRegistry.Unlock()
	defaultRegistry.registry = registry
}

// RemoteExecutors lists the default registry's executors, none until one
// is set (no broker, or remote query off).
func RemoteExecutors() []Executor {
	defaultRegistry.Lock()
	registry := defaultRegistry.registry
	defaultRegistry.Unlock()
	if registry == nil {
		return nil
	}
	return registry.Executors()
}

// Serve registers the executor and runs fragments until ctx ends.
func Serve(ctx context.Context, config ServeConfig) error {
	if config.Token == "" {
		return errors.New("a query executor needs QUERIER_TOKEN")
	}
	if config.Concurrency <= 0 {
		config.Concurrency = 1
	}
	if err := os.MkdirAll(config.ResultsDir, 0o755); err != nil {
		return fmt.Errorf("failed to create query results directory: %w", err)
	}
	bound, err := startResultsServer(ctx, config.ListenAddr, config.ResultsDir, config.Token, config.TLS)
	if err != nil {
		return err
	}
	advertise := config.AdvertiseAddr
	if advertise == "" && !bound.IP.IsUnspecified() {
		advertise = bound.String()
	}
	if advertise == "" {
		host, err := os.Hostname()
		if err != nil {
			return fmt.Errorf("failed to read hostname for QUERIER_ADVERTISE_ADDR: %w", err)
		}
		advertise = net.JoinHostPort(host, strconv.Itoa(bound.Port))
	}

	service, err := micro.AddService(config.Conn, micro.Config{
		Name:     ServiceName,
		Version:  "1.0.0",
		Metadata: map[string]string{resultAddrKey: advertise, affinityKey: config.Affinity},
	})
	if err != nil {
		return fmt.Errorf("failed to register query executor: %w", err)
	}
	slots := make(chan struct{}, config.Concurrency)
	var running sync.Map // fragment id -> context.CancelFunc
	cancellations, err := config.Conn.Subscribe(cancelSubjectBase+"*", func(message *nats.Msg) {
		value, _ := running.Load(strings.TrimPrefix(message.Subject, cancelSubjectBase))
		if cancel, ok := value.(context.CancelFunc); ok {
			cancel()
		}
	})
	if err != nil {
		return fmt.Errorf("failed to subscribe to query cancellations: %w", err)
	}
	defer func() { _ = cancellations.Unsubscribe() }()
	handler := func(request micro.Request) {
		// micro calls handlers one at a time per endpoint.
		go func() {
			if !validSignature(config.Token, request.Data(), request.Headers().Get(signatureHeader)) {
				telemetry.QueryExecutorFragments.WithLabelValues(telemetry.ResultError).Inc()
				_ = request.Error("401", "invalid query executor token: the fragment is not signed with it", nil)
				return
			}
			fragment, err := decodeFragment(request.Data())
			if err != nil {
				_ = request.Error("400", err.Error(), nil)
				return
			}
			// Not cancelled with ctx: shutdown lets running fragments finish.
			// Cancellable while queued for a slot too.
			fragmentCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fragmentTimeout)
			defer cancel()
			running.Store(fragment.ID, cancel)
			defer running.Delete(fragment.ID)
			select {
			case slots <- struct{}{}:
			case <-fragmentCtx.Done():
				telemetry.QueryExecutorFragments.WithLabelValues(telemetry.ResultCancelled).Inc()
				_ = request.Error("499", "fragment cancelled before it started", nil)
				return
			}
			defer func() { <-slots }()
			runFragment(fragmentCtx, config, request, fragment)
		}()
	}
	subject := execSubjectBase + service.Info().ID
	if err := service.AddEndpoint("exec", micro.HandlerFunc(handler),
		micro.WithEndpointSubject(subject), micro.WithEndpointQueueGroupDisabled()); err != nil {
		_ = service.Stop()
		return fmt.Errorf("failed to register query executor endpoint: %w", err)
	}
	config.Log.Info("query executor serving", "id", service.Info().ID, "results", advertise,
		"concurrency", config.Concurrency)

	<-ctx.Done()
	// Deregister first so no new fragments arrive, then let running ones
	// answer; a coordinator retries any cut off at drainTimeout elsewhere.
	err = service.Stop()
	drained := time.After(drainTimeout)
	for range config.Concurrency {
		select {
		case slots <- struct{}{}:
		case <-drained:
			return err
		}
	}
	return err
}

// NewRegistry discovers executors over conn; fragments are signed and
// results fetched with token, over TLS when clientTLS is set. Whether to
// use TLS is local config, never what an executor advertises: anyone with
// the NATS token can register one, and the token goes to its address.
func NewRegistry(conn *nats.Conn, token string, clientTLS *tls.Config, log *slog.Logger) *Registry {
	return &Registry{
		conn: conn, results: newResultsClient(token, clientTLS), log: log, token: token,
		executors: map[string]*remoteExecutor{}, missed: map[string]int{},
	}
}

// Executors lists the live executors. The first call discovers them and
// starts the background refresh; an executor drops out after
// discoveryMissing pings without an answer.
func (r *Registry) Executors() []Executor {
	r.mu.Lock()
	if !r.started {
		r.started = true
		r.mu.Unlock()
		r.refresh()
		go func() {
			for range time.Tick(discoveryEvery) {
				r.refresh()
			}
		}()
		r.mu.Lock()
	}
	defer r.mu.Unlock()
	result := make([]Executor, 0, len(r.executors))
	for _, executor := range r.executors {
		result = append(result, executor)
	}
	return result
}

func (e *remoteExecutor) ID() string {
	return e.id
}

func (e *remoteExecutor) Affinity() string {
	if e.affinity != "" {
		return e.affinity
	}
	return e.id
}

func (e *remoteExecutor) Execute(ctx context.Context, fragment Fragment, resultPath string) error {
	payload, err := encodeFragment(fragment)
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	go func() {
		select {
		case <-e.gone:
			cancel(ErrExecutorLeft)
		case <-requestCtx.Done():
		}
	}()
	request := nats.NewMsg(execSubjectBase + e.id)
	request.Data = payload
	request.Header.Set(signatureHeader, sign(e.token, payload))
	reply, err := e.conn.RequestMsgWithContext(requestCtx, request)
	if err != nil {
		if errors.Is(context.Cause(requestCtx), ErrExecutorLeft) {
			return fmt.Errorf("executor %s: %w", e.id, ErrExecutorLeft)
		}
		if ctx.Err() != nil {
			_ = e.conn.Publish(cancelSubjectBase+fragment.ID, nil)
		}
		return fmt.Errorf("executor %s: %w", e.id, err)
	}
	if description := reply.Header.Get(micro.ErrorHeader); description != "" {
		return fmt.Errorf("executor %s: %s", e.id, description)
	}
	var answer wireReply
	if err := json.Unmarshal(reply.Data, &answer); err != nil {
		return fmt.Errorf("executor %s: invalid reply: %w", e.id, err)
	}
	if answer.Empty {
		return ErrNoFiles
	}
	if err := e.results.fetch(requestCtx, e.resultAddr, answer.ResultID, resultPath); err != nil {
		if errors.Is(context.Cause(requestCtx), ErrExecutorLeft) {
			return fmt.Errorf("executor %s: %w", e.id, ErrExecutorLeft)
		}
		return err
	}
	return nil
}

// sign is payload's HMAC-SHA256 under token, hex-encoded.
func sign(token string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func validSignature(token string, payload []byte, signature string) bool {
	return signature != "" && hmac.Equal([]byte(sign(token, payload)), []byte(signature))
}

func runFragment(fragmentCtx context.Context, config ServeConfig, request micro.Request, fragment Fragment) {
	resultID := uuid.NewString()
	telemetry.QueryExecutorRunning.Inc()
	err := config.Executor.Execute(fragmentCtx, fragment, resultPath(config.ResultsDir, resultID))
	telemetry.QueryExecutorRunning.Dec()
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(fragmentCtx.Err(), context.Canceled):
		telemetry.QueryExecutorFragments.WithLabelValues(telemetry.ResultCancelled).Inc()
		_ = request.Error("499", "fragment cancelled", nil)
	case errors.Is(err, ErrNoFiles):
		telemetry.QueryExecutorFragments.WithLabelValues(telemetry.ResultEmpty).Inc()
		_ = request.RespondJSON(wireReply{Empty: true})
	case err != nil:
		telemetry.QueryExecutorFragments.WithLabelValues(telemetry.ResultError).Inc()
		config.Log.Warn("query fragment failed", "fragment", fragment.ID, "error", err)
		_ = request.Error("500", err.Error(), nil)
	default:
		telemetry.QueryExecutorFragments.WithLabelValues(telemetry.ResultOK).Inc()
		_ = request.RespondJSON(wireReply{ResultID: resultID})
	}
}

func (r *Registry) refresh() {
	subject, err := micro.ControlSubject(micro.PingVerb, ServiceName, "")
	if err != nil {
		return
	}
	inbox := r.conn.NewRespInbox()
	subscription, err := r.conn.SubscribeSync(inbox)
	if err != nil {
		r.log.Warn("query executor discovery failed", "error", err)
		return
	}
	defer func() { _ = subscription.Unsubscribe() }()
	if err := r.conn.PublishRequest(subject, inbox, nil); err != nil {
		r.log.Warn("query executor discovery failed", "error", err)
		return
	}

	seen := map[string]micro.Ping{}
	deadline := time.Now().Add(discoveryWait)
	for {
		message, err := subscription.NextMsg(time.Until(deadline))
		if err != nil {
			break
		}
		var ping micro.Ping
		if json.Unmarshal(message.Data, &ping) == nil && ping.ID != "" && ping.Metadata[resultAddrKey] != "" {
			seen[ping.ID] = ping
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	defer func() { telemetry.QueryExecutors.Set(float64(len(r.executors))) }()
	for id, ping := range seen {
		r.missed[id] = 0
		if _, ok := r.executors[id]; !ok {
			r.executors[id] = &remoteExecutor{
				id:         id,
				affinity:   ping.Metadata[affinityKey],
				resultAddr: ping.Metadata[resultAddrKey],
				conn:       r.conn,
				results:    r.results,
				token:      r.token,
				gone:       make(chan struct{}),
			}
			r.log.Info("query executor joined", "id", id, "results", ping.Metadata[resultAddrKey])
		}
	}
	for id := range r.executors {
		if _, ok := seen[id]; ok {
			continue
		}
		r.missed[id]++
		if r.missed[id] >= discoveryMissing {
			close(r.executors[id].gone)
			delete(r.executors, id)
			delete(r.missed, id)
			r.log.Info("query executor left", "id", id)
		}
	}
}
