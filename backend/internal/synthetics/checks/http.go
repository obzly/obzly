package checks

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"obzly/internal/synthetics/spec"
	"obzly/internal/util/egress"
)

const (
	maxBodyRead    = 1 << 20
	maxBodyCounted = 10 << 20
	bodySnippetLen = 1024
	maxRedirects   = 10
	maxHeaders     = 40
)

// Response headers left out of results: they can carry session secrets.
var hiddenHeaders = map[string]bool{"Set-Cookie": true, "Authorization": true, "Proxy-Authenticate": true}

// httpSession makes a run's requests: one fresh connection for an HTTP
// check; for a multi-step run, connections and cookies kept across steps.
type httpSession struct {
	transport *http.Transport
	jar       http.CookieJar
	verifyTLS bool
}

// exchange is one request and the response, as far as it got.
type exchange struct {
	status      int
	header      http.Header
	body        []byte
	bytes       int64
	redirects   int
	durationMs  float64
	phases      map[string]float64
	resolvedIPs []string
	tls         *tls.ConnectionState
	host        string
	err         string
}

// dialTimings collects one request's DNS and connect time; the dial runs
// on its own goroutine, hence the lock.
type dialTimings struct {
	mu                    sync.Mutex
	dns, connect, tlsTime time.Duration
	ips                   []net.IP
}

type dialTimingsKey struct{}

func runHTTP(ctx context.Context, policy egress.Policy, definition spec.Definition, result *spec.RunResult) {
	session := newHTTPSession(policy, definition.VerifiesTLS(), false, nil)
	defer session.close()
	outcome := session.do(ctx, *definition.Request)
	result.DurationMs, result.PhasesMs, result.Redirects = outcome.durationMs, outcome.phases, outcome.redirects
	if outcome.resolvedIPs != nil {
		result.ResolvedIPs = outcome.resolvedIPs
	}
	if outcome.status == 0 {
		result.Error = outcome.err
		return
	}
	result.HTTPStatus = outcome.status
	result.ResponseBytes = outcome.bytes
	result.Headers = reportedHeaders(outcome.header)
	result.BodySnippet = snippet(outcome.body)
	if outcome.tls != nil {
		result.TLS = tlsInfo(*outcome.tls, outcome.host, definition.VerifiesTLS(), true)
	}
	if outcome.err != "" {
		result.Error = outcome.err
		return
	}
	result.Assertions = evaluate(definition.Assertions, outcome.status, result.DurationMs, outcome.header, outcome.body)
}

func newHTTPSession(policy egress.Policy, verifyTLS, keepAlive bool, jar http.CookieJar) *httpSession {
	transport := &http.Transport{
		Proxy:             nil,
		DisableKeepAlives: !keepAlive,
		ForceAttemptHTTP2: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			timings, _ := ctx.Value(dialTimingsKey{}).(*dialTimings)
			if timings == nil {
				timings = &dialTimings{}
			}
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			started := time.Now()
			ips, err := resolve(ctx, policy, host)
			timings.mu.Lock()
			timings.dns += time.Since(started)
			if err == nil {
				timings.ips = ips
			}
			timings.mu.Unlock()
			if err != nil {
				return nil, err
			}
			started = time.Now()
			defer func() {
				timings.mu.Lock()
				timings.connect += time.Since(started)
				timings.mu.Unlock()
			}()
			return dialAny(ctx, network, ips, port)
		},
		// TLS 1.0 and up, so an old server is measured (and min_protocol
		// can flag it) rather than refused.
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS10,
			InsecureSkipVerify: !verifyTLS,
		}, //nolint:gosec // tls.verify: false is the user's explicit choice
	}
	return &httpSession{transport: transport, jar: jar, verifyTLS: verifyTLS}
}

func (s *httpSession) close() {
	s.transport.CloseIdleConnections()
}

// do sends request and reads at most maxBodyRead of the response.
func (s *httpSession) do(ctx context.Context, request spec.HTTPRequest) exchange {
	var outcome exchange
	parsed, err := url.Parse(request.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		outcome.err = fmt.Sprintf("invalid URL %q", request.URL)
		return outcome
	}
	outcome.host = parsed.Hostname()

	timings := &dialTimings{}
	// ttfb is summed over redirect hops like dns, connect and tls, so the phases add up to the
	// total. The transport writes and reads on different goroutines, hence the lock.
	var ttfb time.Duration
	var tlsStarted, gotConnAt, wroteAt time.Time
	trace := &httptrace.ClientTrace{
		GotConn: func(httptrace.GotConnInfo) {
			timings.mu.Lock()
			gotConnAt = time.Now()
			timings.mu.Unlock()
		},
		TLSHandshakeStart: func() {
			timings.mu.Lock()
			tlsStarted = time.Now()
			timings.mu.Unlock()
		},
		TLSHandshakeDone: func(tls.ConnectionState, error) {
			timings.mu.Lock()
			if !tlsStarted.IsZero() {
				timings.tlsTime += time.Since(tlsStarted)
			}
			timings.mu.Unlock()
		},
		WroteRequest: func(httptrace.WroteRequestInfo) {
			timings.mu.Lock()
			wroteAt = time.Now()
			timings.mu.Unlock()
		},
		GotFirstResponseByte: func() {
			timings.mu.Lock()
			defer timings.mu.Unlock()
			from := wroteAt
			if from.IsZero() {
				from = gotConnAt
			}
			if !from.IsZero() {
				ttfb += time.Since(from)
			}
			gotConnAt, wroteAt = time.Time{}, time.Time{}
		},
	}
	client := &http.Client{
		Transport: s.transport,
		Jar:       s.jar,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if !request.FollowsRedirects() {
				return http.ErrUseLastResponse
			}
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			outcome.redirects = len(via)
			return nil
		},
	}

	var body io.Reader
	if request.Body != "" {
		body = strings.NewReader(request.Body)
	}
	requestCtx := httptrace.WithClientTrace(context.WithValue(ctx, dialTimingsKey{}, timings), trace)
	httpRequest, err := http.NewRequestWithContext(requestCtx, request.Method, request.URL, body)
	if err != nil {
		outcome.err = err.Error()
		return outcome
	}
	httpRequest.Header.Set("User-Agent", "obzly-probe")
	for name, value := range request.Headers {
		httpRequest.Header.Set(name, value)
	}

	started := time.Now()
	response, err := client.Do(httpRequest)
	snapshot := func(transfer time.Duration) {
		timings.mu.Lock()
		defer timings.mu.Unlock()
		outcome.phases = phases(timings.dns, timings.connect, timings.tlsTime, ttfb, transfer)
		if timings.ips != nil {
			outcome.resolvedIPs = ipStrings(timings.ips)
		}
	}
	if err != nil {
		outcome.durationMs = millis(time.Since(started))
		snapshot(0)
		outcome.err = describeHTTPError(err)
		return outcome
	}
	defer func() { _ = response.Body.Close() }()
	bodyStarted := time.Now()
	data, readErr := io.ReadAll(io.LimitReader(response.Body, maxBodyRead))
	rest, _ := io.Copy(io.Discard, io.LimitReader(response.Body, maxBodyCounted))
	transfer := time.Since(bodyStarted)

	outcome.durationMs = millis(time.Since(started))
	snapshot(transfer)
	outcome.status, outcome.header, outcome.body = response.StatusCode, response.Header, data
	outcome.bytes = int64(len(data)) + rest
	outcome.tls = response.TLS
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		outcome.err = "failed to read the response: " + readErr.Error()
	}
	return outcome
}

// tlsInfo describes the peer certificate. With verification off it is checked here anyway, so a
// self-signed or expired certificate is still reported as such.
func tlsInfo(state tls.ConnectionState, host string, handshakeVerified, checkHostname bool) *spec.TLSInfo {
	if len(state.PeerCertificates) == 0 {
		return nil
	}
	leaf := state.PeerCertificates[0]
	info := &spec.TLSInfo{
		Subject:    leaf.Subject.CommonName,
		Issuer:     leaf.Issuer.CommonName,
		NotAfter:   leaf.NotAfter.UTC(),
		DaysLeft:   time.Until(leaf.NotAfter).Hours() / 24,
		Protocol:   tls.VersionName(state.Version),
		Cipher:     tls.CipherSuiteName(state.CipherSuite),
		Verified:   handshakeVerified,
		SelfSigned: string(leaf.RawIssuer) == string(leaf.RawSubject),
	}
	if info.Subject == "" {
		info.Subject = leaf.Subject.String()
	}
	if info.Issuer == "" {
		info.Issuer = leaf.Issuer.String()
	}
	info.SANs = append(info.SANs, leaf.DNSNames...)
	for _, ip := range leaf.IPAddresses {
		info.SANs = append(info.SANs, ip.String())
	}
	if !handshakeVerified {
		options := x509.VerifyOptions{Intermediates: x509.NewCertPool()}
		if checkHostname {
			options.DNSName = host
		}
		for _, certificate := range state.PeerCertificates[1:] {
			options.Intermediates.AddCert(certificate)
		}
		if _, err := leaf.Verify(options); err != nil {
			info.VerifyError = err.Error()
		} else {
			info.Verified = true
		}
	}
	return info
}

func phases(dns, connect, tlsTime, ttfb, transfer time.Duration) map[string]float64 {
	result := map[string]float64{
		"dns":      millis(dns),
		"connect":  millis(connect),
		"ttfb":     millis(ttfb),
		"transfer": millis(transfer),
	}
	if tlsTime > 0 {
		result["tls"] = millis(tlsTime)
	}
	return result
}

func reportedHeaders(header http.Header) map[string]string {
	result := map[string]string{}
	for name, values := range header {
		if hiddenHeaders[name] || len(values) == 0 {
			continue
		}
		if len(result) >= maxHeaders {
			break
		}
		result[name] = values[0]
	}
	return result
}

func snippet(data []byte) string {
	if len(data) > bodySnippetLen {
		data = data[:bodySnippetLen]
	}
	for len(data) > 0 && !utf8.Valid(data) {
		data = data[:len(data)-1]
	}
	return string(data)
}

func describeHTTPError(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return "TLS certificate verification failed: " + certErr.Err.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	return err.Error()
}
