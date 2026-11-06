package query_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"obzly/internal/query"
	"obzly/internal/storage/segments"
	"obzly/internal/util/tlsutil"
	tlsutil_testing "obzly/internal/util/tlsutil/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func Test_Coordinator_RemoteExecutorsOverTLS_MatchASingleQuery(t *testing.T) {
	dir := t.TempDir()
	records := writeSegments(t, dir, 9, 400)
	serverTLS, caFile := resultsServerTLS(t, "127.0.0.1")
	client := startBroker(t)
	var served atomic.Int64
	for i := range 2 {
		serveExecutorTLS(t, client, filepath.Join(dir, fmt.Sprintf("executor-%d", i)), "secret", &served, serverTLS)
	}
	registry := query.NewRegistry(client.Conn, "secret", resultsClientTLS(t, caFile, ""), discard())
	require.Eventually(t, func() bool { return len(registry.Executors()) == 2 }, 10*time.Second, 100*time.Millisecond)
	coordinator, err := query.NewCoordinator(filepath.Join(dir, "results"), duckDB(t),
		failingExecutor{id: "local"}, registry.Executors, discard())
	require.NoError(t, err)

	total, page := countAndPage(t, coordinator, records, nil)

	wantTotal, wantPage := singleQuery(t, records)
	require.Equal(t, wantTotal, total)
	require.Equal(t, wantPage, page)
	require.Positive(t, served.Load())
}

func Test_RemoteExecutor_UntrustedCertificate_FailsToFetch(t *testing.T) {
	dir := t.TempDir()
	records := writeSegments(t, dir, 2, 10)
	serverTLS, _ := resultsServerTLS(t, "127.0.0.1")
	_, otherCA := resultsServerTLS(t, "127.0.0.1")
	client := startBroker(t)
	serveExecutorTLS(t, client, filepath.Join(dir, "executor"), "secret", nil, serverTLS)
	registry := query.NewRegistry(client.Conn, "secret", resultsClientTLS(t, otherCA, ""), discard())
	require.Eventually(t, func() bool { return len(registry.Executors()) == 1 }, 10*time.Second, 100*time.Millisecond)

	err := executeCount(registry, records, filepath.Join(dir, "result.parquet"))

	require.ErrorContains(t, err, "certificate")
}

// Executors advertise pod IPs, which a cert-manager certificate usually
// lacks: QUERY_RESULTS_TLS_SERVER_NAME verifies against a DNS name instead.
func Test_RemoteExecutor_CertWithoutIPAndServerNameSet_FetchesOverTLS(t *testing.T) {
	dir := t.TempDir()
	records := writeSegments(t, dir, 2, 10)
	const serverName = "obzly-querier.example.svc"
	serverTLS, caFile := resultsServerTLS(t, serverName)
	client := startBroker(t)
	serveExecutorTLS(t, client, filepath.Join(dir, "executor"), "secret", nil, serverTLS)

	withoutName := query.NewRegistry(client.Conn, "secret", resultsClientTLS(t, caFile, ""), discard())
	require.Eventually(t, func() bool { return len(withoutName.Executors()) == 1 }, 10*time.Second, 100*time.Millisecond)
	require.ErrorContains(t, executeCount(withoutName, records, filepath.Join(dir, "a.parquet")), "certificate")

	withName := query.NewRegistry(client.Conn, "secret", resultsClientTLS(t, caFile, serverName), discard())
	require.Eventually(t, func() bool { return len(withName.Executors()) == 1 }, 10*time.Second, 100*time.Millisecond)
	require.NoError(t, executeCount(withName, records, filepath.Join(dir, "b.parquet")))
}

func resultsServerTLS(t *testing.T, hosts ...string) (*tls.Config, string) {
	t.Helper()
	certFile, keyFile := tlsutil_testing.WriteSelfSignedCert(t, t.TempDir(), "querier", hosts...)
	serverTLS, err := tlsutil.ServerConfig(certFile, keyFile)
	require.NoError(t, err)
	return serverTLS, certFile
}

func resultsClientTLS(t *testing.T, caFile, serverName string) *tls.Config {
	t.Helper()
	clientTLS, err := tlsutil.ClientConfig(caFile, serverName)
	require.NoError(t, err)
	return clientTLS
}

func executeCount(registry *query.Registry, records []*segments.SegmentRecord, path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return registry.Executors()[0].Execute(ctx, query.Fragment{
		ID: uuid.NewString(), Stream: "logs", Segments: records,
		SQL: countSQL, Args: []any{"error", time.Time{}},
	}, path)
}
