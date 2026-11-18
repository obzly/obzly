package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	logs_core "obzly/internal/features/logs/core"
	logs_parquetstore "obzly/internal/features/logs/parquetstore"
	metrics_parquetstore "obzly/internal/features/metrics/parquetstore"
	spans_parquetstore "obzly/internal/features/spans/parquetstore"
	"obzly/internal/storage/segments"
	"obzly/internal/telemetry"

	"github.com/stretchr/testify/require"
)

// The worker's storage, opened as setUp*Storage and startIngestConsumers do
// on a worker with object storage, fits a 4 GiB and a 6 GiB pod with the
// default caps: one shared writer, compactor and token filter database,
// and four search handles at 1/16 of the pod each.
func Test_DuckDBBudget_WorkerStorage_FitsA4GiBAnd6GiBPod(t *testing.T) {
	const gib = int64(1) << 30
	handles := openWorkerStorage(t, workerSearchMemoryLimit(4*gib))

	require.Equal(t, []telemetry.DuckDBHandleKind{
		{Handle: "compactor", Count: 1, LimitBytes: 1_000_000_000},
		{Handle: "querier_ai_logs", Count: 1, LimitBytes: 268_000_000},
		{Handle: "querier_logs", Count: 1, LimitBytes: 268_000_000},
		{Handle: "querier_metrics", Count: 1, LimitBytes: 268_000_000},
		{Handle: "querier_spans", Count: 1, LimitBytes: 268_000_000},
		{Handle: "token_bloom", Count: 1, LimitBytes: 384_000_000},
		{Handle: "writer", Count: 1, LimitBytes: 384_000_000},
	}, handles, "ten writers, four compactors and two filter builders share one database per kind")

	cases := []struct {
		container        int64
		searchLimit      string
		duckDB, total    int64
		containerLimitGB string
	}{
		// 1000 + 384 + 384 + 5 x 268 (four queriers, one sandbox) + 192 on demand.
		{4 * gib, "268MB", 3_300_000_000, 4_125_000_000, "4.3 GB"},
		{6 * gib, "402MB", 3_970_000_000, 4_962_500_000, "6.4 GB"},
	}
	for _, tc := range cases {
		limit := workerSearchMemoryLimit(tc.container)
		require.Equal(t, tc.searchLimit, limit)

		budget := duckDBBudget(withSearchLimit(handles, limit), limit, tc.container)

		require.Equal(t, tc.duckDB, budget.DuckDBBytes, budget.Summary())
		require.Equal(t, tc.total, budget.TotalBytes)
		require.False(t, budget.Exceeds(), "%s of DuckDB and Go on a %s pod", segments.FormatBytes(budget.TotalBytes),
			tc.containerLimitGB)
	}
}

func Test_WorkerSearchMemoryLimit_IsASixteenthOfThePodWithinFloorAndCap(t *testing.T) {
	const gib = int64(1) << 30
	require.Equal(t, "256MB", workerSearchMemoryLimit(3*gib), "floor")
	require.Equal(t, "268MB", workerSearchMemoryLimit(4*gib))
	require.Equal(t, "512MB", workerSearchMemoryLimit(16*gib), "cap")
	require.Equal(t, "512MB", workerSearchMemoryLimit(0), "no container limit: the cap")
}

// openWorkerStorage opens the logs, AI logs, spans and metrics stores and
// their JetStream writers with a worker's search caps, and returns the
// registered DuckDB handles. The stores live for the test process, like a
// worker's.
func openWorkerStorage(t *testing.T, searchLimit string) []telemetry.DuckDBHandleKind {
	t.Helper()
	dataDir := t.TempDir()
	store, err := segments.NewObjectStore(context.Background(), "file://"+filepath.Join(t.TempDir()))
	require.NoError(t, err)
	cacheDir := filepath.Join(dataDir, "cache")

	logsOpts := []logs_parquetstore.Option{
		logs_parquetstore.WithObjectStore(store, cacheDir),
		logs_parquetstore.WithQuerierMemoryLimit(searchLimit),
		logs_parquetstore.WithQuerierThreads(workerQuerierThreads),
	}
	logsStore, err := logs_parquetstore.New(dataDir, logsOpts...)
	require.NoError(t, err)
	aiStore, err := logs_parquetstore.New(dataDir,
		append(logsOpts, logs_parquetstore.WithStream(logs_core.StreamAILogs))...)
	require.NoError(t, err)
	spansStore, err := spans_parquetstore.New(dataDir,
		spans_parquetstore.WithObjectStore(store, cacheDir),
		spans_parquetstore.WithQuerierMemoryLimit(searchLimit),
		spans_parquetstore.WithQuerierThreads(workerQuerierThreads))
	require.NoError(t, err)
	metricsStore, err := metrics_parquetstore.New(dataDir,
		metrics_parquetstore.WithObjectStore(store, cacheDir),
		metrics_parquetstore.WithQuerierMemoryLimit(searchLimit),
		metrics_parquetstore.WithQuerierThreads(workerQuerierThreads))
	require.NoError(t, err)

	_, err = logsStore.NewDurableWriter()
	require.NoError(t, err)
	_, err = aiStore.NewDurableWriter()
	require.NoError(t, err)
	_, err = spansStore.NewDurableWriter()
	require.NoError(t, err)
	_, err = metricsStore.NewDurableWriter()
	require.NoError(t, err)

	return telemetry.DuckDBHandles()
}

// withSearchLimit is handles with every querier capped at limit instead.
func withSearchLimit(handles []telemetry.DuckDBHandleKind, limit string) []telemetry.DuckDBHandleKind {
	result := make([]telemetry.DuckDBHandleKind, len(handles))
	for i, kind := range handles {
		if strings.HasPrefix(kind.Handle, "querier_") {
			kind.LimitBytes = segments.MemoryLimitBytes(limit)
		}
		result[i] = kind
	}
	return result
}
