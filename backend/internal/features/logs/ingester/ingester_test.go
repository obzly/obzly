package logs_ingester_test

import (
	"database/sql"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	logs_ingester "obzly/internal/features/logs/ingester"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/stretchr/testify/require"
)

func newTestIngester(t *testing.T) *logs_ingester.LogIngester {
	t.Helper()

	ingester, err := logs_ingester.NewLogIngester(t.TempDir(), segments.WithMaxRows(1_000_000))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })

	return ingester
}

func Test_StoreLogsBatch_ThenFlush_WritesReadableParquetSegment(t *testing.T) {
	ingester := newTestIngester(t)
	projectID := uuid.New()
	now := time.Now().UTC().Truncate(time.Millisecond)

	batch := map[uuid.UUID][]*logs_core.LogItem{
		projectID: {
			{
				ID:        uuid.New(),
				ProjectID: projectID,
				Timestamp: now,
				Level:     logs_core.LogLevelError,
				Message:   "connection timeout while calling upstream",
				ClientIP:  "10.0.0.42",
				Fields:    map[string]any{"user_id": "42"},
			},
			{
				ID:        uuid.New(),
				ProjectID: projectID,
				Timestamp: now.Add(time.Second),
				Level:     logs_core.LogLevelInfo,
				Message:   "request completed successfully",
				ClientIP:  "10.0.0.7",
				Fields:    nil,
			},
		},
	}

	require.NoError(t, ingester.StoreLogsBatch(batch))

	segmentsFlushed, err := ingester.FlushAll()
	require.NoError(t, err)
	require.Len(t, segmentsFlushed, 1)
	require.Equal(t, 2, segmentsFlushed[0].RowCount)
	require.FileExists(t, segmentsFlushed[0].Path)

	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer db.Close()

	var errorCount int64
	err = db.QueryRow(
		`SELECT count(*) FROM read_parquet(?) WHERE level = 'error' AND message LIKE '%timeout%'`,
		segmentsFlushed[0].Path,
	).Scan(&errorCount)
	require.NoError(t, err)
	require.Equal(t, int64(1), errorCount)

	var clientIPCount int64
	err = db.QueryRow(
		`SELECT count(*) FROM read_parquet(?) WHERE client_ip = '10.0.0.7'`,
		segmentsFlushed[0].Path,
	).Scan(&clientIPCount)
	require.NoError(t, err)
	require.Equal(t, int64(1), clientIPCount)

	var withUserID int64
	err = db.QueryRow(
		`SELECT count(*) FROM read_parquet(?) WHERE json_extract(fields, '$.user_id') IS NOT NULL`,
		segmentsFlushed[0].Path,
	).Scan(&withUserID)
	require.NoError(t, err)
	require.Equal(t, int64(1), withUserID)

	// The flush also catalogs the segment in Postgres.
	segmentRepo := &segments.SegmentRepository{}
	catalogued, err := segmentRepo.ListByTimeRange(projectID, "logs", now.Add(-time.Minute), now.Add(time.Minute))
	require.NoError(t, err)
	require.Len(t, catalogued, 1)
	require.Equal(t, segmentsFlushed[0].Path, catalogued[0].Path)
	require.Equal(t, int64(2), catalogued[0].RowCount)
}

func Test_StoreLogsBatch_MultipleProjects_KeepsRowsSeparate(t *testing.T) {
	ingester := newTestIngester(t)
	projectA := uuid.New()
	projectB := uuid.New()
	now := time.Now().UTC()

	batch := map[uuid.UUID][]*logs_core.LogItem{
		projectA: {
			{ID: uuid.New(), ProjectID: projectA, Timestamp: now, Level: logs_core.LogLevelInfo, Message: "from A"},
		},
		projectB: {
			{ID: uuid.New(), ProjectID: projectB, Timestamp: now, Level: logs_core.LogLevelInfo, Message: "from B"},
		},
	}

	require.NoError(t, ingester.StoreLogsBatch(batch))

	segmentsFlushed, err := ingester.FlushAll()
	require.NoError(t, err)
	require.Len(t, segmentsFlushed, 2, "each project should flush to its own segment file")
}

func Test_StoreLogsBatch_EmptyBatch_NoError(t *testing.T) {
	ingester := newTestIngester(t)

	require.NoError(t, ingester.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{}))
}

// rowFor promotes the curated resource attributes to columns ("resource."
// prefix, else the bare key; "" when absent) and still keeps them in fields.
func Test_StoreLogsBatch_PromotesResourceAttributesToColumns(t *testing.T) {
	ingester := newTestIngester(t)
	projectID := uuid.New()
	now := time.Now().UTC().Truncate(time.Millisecond)

	batch := map[uuid.UUID][]*logs_core.LogItem{
		projectID: {
			{
				ID:        uuid.New(),
				ProjectID: projectID,
				Timestamp: now,
				Level:     logs_core.LogLevelInfo,
				Message:   "otlp-sourced log",
				Fields: map[string]any{
					"resource.service.name":       "checkout-api",
					"resource.service.version":    "1.4.2",
					"resource.k8s.namespace.name": "prod",
					"resource.k8s.pod.name":       "checkout-api-7d9f6",
					"resource.k8s.container.name": "checkout-api",
					"resource.k8s.node.name":      "ip-10-0-1-2",
					"resource.k8s.cluster.name":   "prod-eks",
					"resource.cloud.provider":     "aws",
					"resource.cloud.region":       "eu-west-1",
				},
			},
			{
				// Non-OTLP REST caller: bare dotted keys, no "resource." prefix.
				ID:        uuid.New(),
				ProjectID: projectID,
				Timestamp: now.Add(time.Second),
				Level:     logs_core.LogLevelInfo,
				Message:   "rest-sourced log",
				Fields: map[string]any{
					"service.name":       "checkout-api",
					"k8s.namespace.name": "prod",
				},
			},
			{
				// No resource context: every promoted column is "".
				ID:        uuid.New(),
				ProjectID: projectID,
				Timestamp: now.Add(2 * time.Second),
				Level:     logs_core.LogLevelInfo,
				Message:   "no-resource-context log",
			},
		},
	}

	require.NoError(t, ingester.StoreLogsBatch(batch))
	segmentsFlushed, err := ingester.FlushAll()
	require.NoError(t, err)
	require.Len(t, segmentsFlushed, 1)

	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer db.Close()

	var otlpCount int64
	err = db.QueryRow(
		`SELECT count(*) FROM read_parquet(?)
		 WHERE message = 'otlp-sourced log'
		   AND service_name = 'checkout-api'
		   AND service_version = '1.4.2'
		   AND k8s_namespace_name = 'prod'
		   AND k8s_pod_name = 'checkout-api-7d9f6'
		   AND k8s_container_name = 'checkout-api'
		   AND k8s_node_name = 'ip-10-0-1-2'
		   AND k8s_cluster_name = 'prod-eks'
		   AND cloud_provider = 'aws'
		   AND cloud_region = 'eu-west-1'
		   AND json_extract_string(fields, '$.k8s_pod_name') = 'checkout-api-7d9f6'`,
		segmentsFlushed[0].Path,
	).Scan(&otlpCount)
	require.NoError(t, err)
	require.Equal(
		t,
		int64(1),
		otlpCount,
		"OTLP-style resource.-prefixed fields must populate the promoted columns AND stay in the fields JSON blob under their normalized name",
	)

	var restCount int64
	err = db.QueryRow(
		`SELECT count(*) FROM read_parquet(?)
		 WHERE message = 'rest-sourced log'
		   AND service_name = 'checkout-api'
		   AND k8s_namespace_name = 'prod'`,
		segmentsFlushed[0].Path,
	).Scan(&restCount)
	require.NoError(t, err)
	require.Equal(
		t,
		int64(1),
		restCount,
		"a bare dotted key (no OTLP resource. prefix) must still populate the promoted column as a fallback",
	)

	var emptyCount int64
	err = db.QueryRow(
		`SELECT count(*) FROM read_parquet(?)
		 WHERE message = 'no-resource-context log'
		   AND service_name = ''
		   AND k8s_pod_name = ''
		   AND cloud_provider = ''
		   AND cloud_region = ''`,
		segmentsFlushed[0].Path,
	).Scan(&emptyCount)
	require.NoError(t, err)
	require.Equal(
		t,
		int64(1),
		emptyCount,
		"a log with no resource context should get empty string, not NULL or an error",
	)
}
