package checks

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"

	"obzly/internal/synthetics/spec"
)

func Test_RunHTTP_AllAssertionsPass_IsUpWithPhaseTimings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "session=secret")
		_, _ = w.Write([]byte(`{"status":"ok","checks":{"database":"ok"},"items":[{"id":7}]}`))
	}))
	defer server.Close()

	result := testRunner().Run(context.Background(), httpCheck(server.URL,
		spec.Assertion{Kind: spec.AssertStatus, Op: spec.OpEquals, Value: "200"},
		spec.Assertion{Kind: spec.AssertResponseTime, Op: spec.OpBelow, Number: 5000},
		spec.Assertion{Kind: spec.AssertHeader, Op: spec.OpContains, Name: "content-type", Value: "json"},
		spec.Assertion{Kind: spec.AssertJSON, Op: spec.OpEquals, Path: "$.status", Value: "ok"},
		spec.Assertion{Kind: spec.AssertJSON, Op: spec.OpEquals, Path: "$.items[0].id", Value: "7"},
		spec.Assertion{Kind: spec.AssertJSON, Op: spec.OpExists, Path: "$.checks.database"},
		spec.Assertion{Kind: spec.AssertBody, Op: spec.OpMatches, Value: `"status":\s*"ok"`},
	))

	require.Equal(t, spec.StatusUp, result.Status, result.Error)
	require.Equal(t, 200, result.HTTPStatus)
	require.Len(t, result.Assertions, 7)
	for _, assertion := range result.Assertions {
		require.True(t, assertion.Passed, assertion.Description)
	}
	require.Contains(t, result.PhasesMs, "connect")
	require.Contains(t, result.PhasesMs, "ttfb")
	require.Greater(t, result.DurationMs, 0.0)
	require.Equal(t, []string{"127.0.0.1"}, result.ResolvedIPs)
	require.NotContains(t, result.Headers, "Set-Cookie")
}

func Test_RunHTTP_AWarnAssertionFails_IsAmber_AFailAssertionFails_IsRed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	warned := testRunner().Run(context.Background(), httpCheck(server.URL,
		spec.Assertion{Kind: spec.AssertStatus, Op: spec.OpEquals, Value: "200", Severity: spec.SeverityWarn}))
	failed := testRunner().Run(context.Background(), httpCheck(server.URL,
		spec.Assertion{Kind: spec.AssertStatus, Op: spec.OpIn, Values: []int{200, 201}, Severity: spec.SeverityFail}))

	require.Equal(t, spec.StatusWarn, warned.Status)
	require.Equal(t, spec.StatusDown, failed.Status)
	require.Equal(t, "503", failed.Assertions[0].Actual)
}

func Test_RunHTTP_SelfSignedCertificate_FailsWhenVerifying_PassesWithVerifyOff(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()
	off := false

	verifying := testRunner().Run(context.Background(), httpCheck(server.URL))
	check := httpCheck(server.URL)
	check.Definition.TLS = &spec.TLSOptions{Verify: &off}
	skipping := testRunner().Run(context.Background(), check)

	require.Equal(t, spec.StatusDown, verifying.Status)
	require.Contains(t, verifying.Error, "certificate")
	require.Equal(t, spec.StatusUp, skipping.Status, skipping.Error)
	require.NotNil(t, skipping.TLS)
	require.False(t, skipping.TLS.Verified)
	require.NotEmpty(t, skipping.TLS.VerifyError)
	require.Contains(t, skipping.PhasesMs, "tls")
}

func Test_RunTLS_ExpiredCertificate_IsRedButReportsItsDetails(t *testing.T) {
	address := tlsServer(t, certificate(t, time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour)))
	off := false

	result := testRunner().Run(context.Background(), spec.ProbeCheck{Name: "expired", Definition: spec.Definition{
		Type: spec.TypeTLS, Target: address, TimeoutSeconds: 5, TLS: &spec.TLSOptions{Verify: &off},
	}})

	require.Equal(t, spec.StatusDown, result.Status)
	require.Equal(t, "certificate has expired", result.Error)
	require.Equal(t, "obzly-test", result.TLS.Subject)
	require.Less(t, result.TLS.DaysLeft, 0.0)
}

func Test_RunTLS_CertificateInsideItsWarningWindow_IsAmber(t *testing.T) {
	address := tlsServer(t, certificate(t, time.Now().Add(-time.Hour), time.Now().Add(10*24*time.Hour)))
	off := false

	result := testRunner().Run(context.Background(), spec.ProbeCheck{Name: "soon", Definition: spec.Definition{
		Type: spec.TypeTLS, Target: address, TimeoutSeconds: 5, TLS: &spec.TLSOptions{Verify: &off, WarnDays: 30},
	}})

	require.Equal(t, spec.StatusWarn, result.Status, result.Error)
	require.Equal(t, "TLS 1.3", result.TLS.Protocol)
}

func Test_RunTCP_OpenAndClosedPorts(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	open := listener.Addr().String()
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	closedAddress := closed.Addr().String()
	require.NoError(t, closed.Close())
	defer func() { _ = listener.Close() }()

	up := testRunner().Run(context.Background(), spec.ProbeCheck{Definition: spec.Definition{Type: spec.TypeTCP, Target: open, TimeoutSeconds: 3}})
	down := testRunner().Run(context.Background(), spec.ProbeCheck{Definition: spec.Definition{Type: spec.TypeTCP, Target: closedAddress, TimeoutSeconds: 3}})

	require.Equal(t, spec.StatusUp, up.Status, up.Error)
	require.Equal(t, spec.StatusDown, down.Status)
	require.Contains(t, down.Error, "refused")
}

// A host whose IPv6 address is unreachable (Docker without IPv6) must not spend the whole timeout
// on it before trying IPv4.
func Test_DialAny_UnreachableAddressFirst_StillReachesTheNext(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	blackHole := net.ParseIP("100::1") // the IPv6 discard prefix

	conn, err := dialAny(ctx, "tcp", []net.IP{blackHole, net.ParseIP("127.0.0.1")}, port)

	require.NoError(t, err)
	_ = conn.Close()
}

func Test_Run_DefaultProbePolicy_RefusesLoopbackAndMetadata(t *testing.T) {
	runner := &Runner{Location: "test", Probe: "p"}

	loopback := runner.Run(
		context.Background(),
		spec.ProbeCheck{Definition: spec.Definition{Type: spec.TypeTCP, Target: "127.0.0.1:22", TimeoutSeconds: 2}},
	)
	metadata := runner.Run(context.Background(), httpCheck("http://169.254.169.254/latest/meta-data/"))

	require.Equal(t, spec.StatusDown, loopback.Status)
	require.Contains(t, loopback.Error, "not allowed")
	require.Equal(t, spec.StatusDown, metadata.Status)
	require.Contains(t, metadata.Error, "not allowed")
}

func Test_Run_UnresolvedSecret_FailsWithItsName(t *testing.T) {
	check := httpCheck("https://api.example.com/")
	check.Definition.Request.Headers = map[string]string{"Authorization": "Bearer ${env:MISSING_TOKEN}"}

	result := testRunner().Run(context.Background(), check)

	require.Equal(t, spec.StatusDown, result.Status)
	require.Contains(t, result.Error, "MISSING_TOKEN")
}

func Test_RunDNS_ResolvesLocalhostThroughTheSystemResolver(t *testing.T) {
	result := testRunner().Run(context.Background(), spec.ProbeCheck{Definition: spec.Definition{
		Type: spec.TypeDNS, Target: "localhost", TimeoutSeconds: 3,
		DNS: &spec.DNSOptions{RecordType: "A", ExpectAnyOf: []string{"127.0.0.1"}},
	}})

	require.Equal(t, spec.StatusUp, result.Status, result.Error)
	require.Contains(t, result.DNSAnswers, "127.0.0.1")
}

func Test_IcmpStats_LossAndJitter(t *testing.T) {
	info := icmpStats(4, []time.Duration{10 * time.Millisecond, 14 * time.Millisecond, 12 * time.Millisecond})

	require.Equal(t, 3, info.Received)
	require.InDelta(t, 0.25, info.LossRatio, 0.001)
	require.InDelta(t, 12, info.RTTAvgMs, 0.001)
	require.InDelta(t, 3, info.JitterMs, 0.001)
}

func Test_RunHTTP_Redirected_TTFBCoversEveryHop_RedirectsCountOnlyWhenFollowed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(80 * time.Millisecond)
		http.Redirect(w, r, "/final", http.StatusFound)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(80 * time.Millisecond)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	off := false

	followed := testRunner().Run(context.Background(), httpCheck(server.URL+"/start"))
	notFollowedCheck := httpCheck(server.URL + "/start")
	notFollowedCheck.Definition.Request.FollowRedirects = &off
	notFollowed := testRunner().Run(context.Background(), notFollowedCheck)

	require.Equal(t, spec.StatusUp, followed.Status, followed.Error)
	require.Equal(t, 1, followed.Redirects)
	require.GreaterOrEqual(t, followed.PhasesMs["ttfb"], 160.0, "both hops' wait")
	phases := followed.PhasesMs["dns"] + followed.PhasesMs["connect"] + followed.PhasesMs["ttfb"] +
		followed.PhasesMs["transfer"]
	require.InDelta(t, followed.DurationMs, phases, 30, "the phases add up to about the total")
	require.Equal(t, http.StatusFound, notFollowed.HTTPStatus)
	require.Equal(t, 0, notFollowed.Redirects, "a redirect not followed isn't counted")
}

func Test_IsEchoReply_MatchesSenderAndSequence_AndTheIDOnRawSockets(t *testing.T) {
	target := net.ParseIP("192.0.2.10")
	reply := func(id, seq int) *icmp.Message {
		return &icmp.Message{Type: ipv4.ICMPTypeEchoReply, Body: &icmp.Echo{ID: id, Seq: seq}}
	}
	raw := &net.IPAddr{IP: target}
	ping := &net.UDPAddr{IP: target}
	other := &net.IPAddr{IP: net.ParseIP("192.0.2.99")}

	require.True(t, isEchoReply(reply(7, 2), ipv4.ICMPTypeEchoReply, raw, target, true, 7, 2))
	require.False(t, isEchoReply(reply(8, 2), ipv4.ICMPTypeEchoReply, raw, target, true, 7, 2),
		"another process's ping on a raw socket")
	require.False(t, isEchoReply(reply(7, 2), ipv4.ICMPTypeEchoReply, other, target, true, 7, 2),
		"a reply from another host")
	require.False(t, isEchoReply(reply(7, 1), ipv4.ICMPTypeEchoReply, raw, target, true, 7, 2))
	require.True(t, isEchoReply(reply(4242, 2), ipv4.ICMPTypeEchoReply, ping, target, false, 7, 2),
		"ping sockets rewrite the ID")
	require.False(t, isEchoReply(reply(4242, 2), ipv4.ICMPTypeEchoReply, &net.UDPAddr{IP: other.IP}, target,
		false, 7, 2))
	require.False(t, isEchoReply(&icmp.Message{Type: ipv4.ICMPTypeEcho, Body: &icmp.Echo{ID: 7, Seq: 2}},
		ipv4.ICMPTypeEchoReply, raw, target, true, 7, 2))
}

func testRunner() *Runner {
	return &Runner{Location: "test", Probe: "test-probe", Policy: func(net.IP) error { return nil }}
}

func httpCheck(url string, assertions ...spec.Assertion) spec.ProbeCheck {
	definition := spec.Definition{
		Type:           spec.TypeHTTP,
		TimeoutSeconds: 5,
		Request:        &spec.HTTPRequest{Method: "GET", URL: url},
		Assertions:     assertions,
	}
	for i := range definition.Assertions {
		if definition.Assertions[i].Severity == "" {
			definition.Assertions[i].Severity = spec.SeverityFail
		}
	}
	return spec.ProbeCheck{ID: "c1", Name: "test", Definition: definition}
}

func tlsServer(t *testing.T, cert tls.Certificate) string {
	t.Helper()
	listener, err := tls.Listen(
		"tcp",
		"127.0.0.1:0",
		&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				_ = conn.(*tls.Conn).Handshake()
				_ = conn.Close()
			}()
		}
	}()
	return listener.Addr().String()
}

func certificate(t *testing.T, notBefore, notAfter time.Time) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "obzly-test"},
		NotBefore: notBefore, NotAfter: notAfter, DNSNames: []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
