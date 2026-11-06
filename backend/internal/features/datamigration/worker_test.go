package datamigration

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	dashboards_models "obzly/internal/features/dashboards/models"
	logs_core "obzly/internal/features/logs/core"
	logs_testsupport "obzly/internal/features/logs/testsupport"
	metrics_core "obzly/internal/features/metrics/core"
	metrics_promql "obzly/internal/features/metrics/promql"
	metrics_testsupport "obzly/internal/features/metrics/testsupport"
	spans_testsupport "obzly/internal/features/spans/testsupport"
	"obzly/internal/storage"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/prometheus/prometheus/promql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	logs_testsupport.SetupRepository()
	metrics_testsupport.SetupRepository()
	spans_testsupport.SetupRepository()
	os.Exit(m.Run())
}

// A segment written before the naming change is readable by new names at
// once (read-time rewrite) and is rewritten in place by the worker.
func Test_LegacyLogSegment_ReadableThenMigrated(t *testing.T) {
	projectID := uuid.New()
	now := time.Now().UTC()
	repository := logs_core.GetLogCoreRepository()
	require.NoError(t, repository.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: {{
		ID: uuid.New(), ProjectID: projectID, Timestamp: now, Level: logs_core.LogLevelError,
		Message: "payment failed", Fields: map[string]any{"placeholder": "x"},
	}}}))
	require.NoError(t, repository.ForceFlush())
	legacyPath := makeLegacy(t, projectID)

	query := &logs_core.LogQueryRequestDTO{
		Query: &logs_core.QueryNode{Type: logs_core.QueryNodeTypeLogical, Logic: &logs_core.LogicalNode{
			Operator: logs_core.LogicalOperatorAnd, Children: []logs_core.QueryNode{
				{Type: logs_core.QueryNodeTypeCondition, Condition: &logs_core.ConditionNode{
					Field: "log_iostream", Operator: logs_core.ConditionOperatorEquals, Value: "stderr"}},
				{Type: logs_core.QueryNodeTypeCondition, Condition: &logs_core.ConditionNode{
					Field: "level", Operator: logs_core.ConditionOperatorEquals, Value: "error"}},
			}}},
		TimeRange: &logs_core.TimeRangeDTO{From: ptr(now.Add(-time.Hour)), To: ptr(now.Add(time.Hour))},
		Limit:     10,
	}
	before, err := repository.ExecuteQueryForProject(projectID, query)
	require.NoError(t, err)
	require.Len(t, before.Logs, 1, "legacy segment must match new names before migration")
	assert.Equal(t, "error", before.Logs[0].Level)
	assert.Equal(t, "checkout-1", before.Logs[0].Fields["k8s_pod_name"])

	for {
		remaining, err := GetWorker().migrateBatch(context.Background())
		require.NoError(t, err)
		legacy := countLegacy(t, projectID)
		if legacy == 0 || !remaining {
			break
		}
	}
	assert.Zero(t, countLegacy(t, projectID))
	assert.NoFileExists(t, legacyPath, "the old file is removed")

	after, err := repository.ExecuteQueryForProject(projectID, query)
	require.NoError(t, err)
	require.Len(t, after.Logs, 1)
	assert.Equal(t, "checkout-1", after.Logs[0].Fields["k8s_pod_name"])
	_, hasOld := after.Logs[0].Fields["resource.k8s.pod.name"]
	assert.False(t, hasOld)
}

func Test_MigrateDashboards_RewritesNamesOnce(t *testing.T) {
	db := storage.GetDb()
	require.NoError(t, db.Exec("DELETE FROM data_migrations WHERE name = ?", dashboardsMarker).Error)

	projectID := createProject(t)
	dashboard := &dashboards_models.Dashboard{
		ID: uuid.New(), ProjectID: projectID, Title: "Legacy",
		Tabs: dashboards_models.Tabs{{ID: "t", Title: "Default", Panels: dashboards_models.Panels{{
			ID: "p", Title: "cpu", Type: "timeseries", Source: "metrics",
			PromQuery: `sum(rate({"__name__"="system.cpu.time"}[5m])) by ("state")`,
		}, {
			ID: "q", Title: "errors", Type: "table", Source: "logs", GroupField: "resource.k8s.pod.name",
			Query: &logs_core.QueryNode{Type: logs_core.QueryNodeTypeCondition, Condition: &logs_core.ConditionNode{
				Field: "level", Operator: logs_core.ConditionOperatorIn, Value: []any{"ERROR", "FATAL"}}},
		}}}},
		Variables: dashboards_models.Variables{{Key: "ns", Label: "Namespace", Source: "metricLabel",
			MetricLabelName: "k8s.namespace.name"}},
	}
	require.NoError(t, db.Create(dashboard).Error)

	require.NoError(t, migrateDashboards(context.Background()))

	var stored dashboards_models.Dashboard
	require.NoError(t, db.First(&stored, "id = ?", dashboard.ID).Error)
	panels := stored.Tabs[0].Panels
	assert.Equal(t, "sum by (state) (rate(system_cpu_time[5m]))", panels[0].PromQuery)
	assert.Equal(t, "k8s_pod_name", panels[1].GroupField)
	assert.Equal(t, []any{"error", "fatal"}, panels[1].Query.Condition.Value)
	assert.Equal(t, "k8s_namespace_name", stored.Variables[0].MetricLabelName)

	var marker int64
	db.Table("data_migrations").Where("name = ?", dashboardsMarker).Count(&marker)
	assert.EqualValues(t, 1, marker)
}

// makeLegacy rewrites the project's only log segment with pre-change names
// and uppercase levels and marks it schema version 1. Returns its path.
func makeLegacy(t *testing.T, projectID uuid.UUID) string {
	t.Helper()
	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.Len(t, records, 1)
	record := records[0]

	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	legacyPath := filepath.Join(filepath.Dir(record.Path), "legacy-"+uuid.NewString()[:8]+".parquet")
	_, err = db.Exec(`COPY (SELECT * REPLACE ('ERROR' AS level, 'checkout-1' AS k8s_pod_name,
		'{"resource.k8s.pod.name":"checkout-1","log.iostream":"stderr"}' AS fields) FROM read_parquet(?))
		TO '`+legacyPath+`' (FORMAT PARQUET)`, record.Path)
	require.NoError(t, err)
	require.NoError(t, os.Remove(record.Path))
	require.NoError(t, storage.GetDb().Model(&segments.SegmentRecord{}).Where("id = ?", record.ID).
		Updates(map[string]any{"path": legacyPath, "schema_version": segments.SchemaVersionLegacy}).Error)
	return legacyPath
}

func countLegacy(t *testing.T, projectID uuid.UUID) int64 {
	var count int64
	require.NoError(t, storage.GetDb().Model(&segments.SegmentRecord{}).
		Where("project_id = ? AND schema_version = ?", projectID, segments.SchemaVersionLegacy).Count(&count).Error)
	return count
}

func createProject(t *testing.T) uuid.UUID {
	id := uuid.New()
	require.NoError(t, storage.GetDb().Exec(
		"INSERT INTO projects (id, name, created_at) VALUES (?, ?, NOW())", id, "migration "+id.String()[:8]).Error)
	return id
}

func ptr(t time.Time) *time.Time { return &t }

// Metric names and labels written with dots are queryable unquoted
// (system_cpu_time, by (k8s_pod_name)) before and after migration.
func Test_LegacyMetricSegment_QueryableByNormalizedPromQL(t *testing.T) {
	projectID := uuid.New()
	now := time.Now().UTC()
	repository := metrics_core.GetMetricCoreRepository()
	require.NoError(t, repository.StoreSamplesBatch(map[uuid.UUID][]*metrics_core.MetricSample{projectID: {{
		ID: uuid.New(), ProjectID: projectID, MetricName: "placeholder", Value: 7,
		Timestamp: now.Add(-30 * time.Second), Type: metrics_core.MetricTypeGauge,
	}}}))
	require.NoError(t, repository.ForceFlush())

	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "metrics")
	require.NoError(t, err)
	require.Len(t, records, 1)
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	legacyPath := filepath.Join(filepath.Dir(records[0].Path), "legacy-"+uuid.NewString()[:8]+".parquet")
	_, err = db.Exec(`COPY (SELECT * REPLACE ('system.cpu.time' AS metric_name,
		'{"k8s.pod.name":"checkout-1"}' AS labels) FROM read_parquet(?)) TO '`+legacyPath+`' (FORMAT PARQUET)`,
		records[0].Path)
	require.NoError(t, err)
	require.NoError(t, os.Remove(records[0].Path))
	require.NoError(t, storage.GetDb().Model(&segments.SegmentRecord{}).Where("id = ?", records[0].ID).
		Updates(map[string]any{"path": legacyPath, "schema_version": segments.SchemaVersionLegacy}).Error)

	query := func() map[string]string {
		expr := metrics_promql.NormalizePromQL(`sum by ("k8s.pod.name") ({"__name__"="system.cpu.time"})`)
		q, err := metrics_promql.GetEngine().NewInstantQuery(context.Background(),
			metrics_promql.NewQueryable(projectID), nil, expr, now)
		require.NoError(t, err)
		defer q.Close()
		result := q.Exec(context.Background())
		require.NoError(t, result.Err)
		vector, ok := result.Value.(promql.Vector)
		require.True(t, ok)
		require.Len(t, vector, 1, expr)
		return vector[0].Metric.Map()
	}
	assert.Equal(t, map[string]string{"k8s_pod_name": "checkout-1"}, query(), "legacy segment, read-time rewrite")

	for i := 0; i < 5 && countLegacyStream(t, projectID, "metrics") > 0; i++ {
		_, err := GetWorker().migrateBatch(context.Background())
		require.NoError(t, err)
	}
	assert.Zero(t, countLegacyStream(t, projectID, "metrics"))
	assert.Equal(t, map[string]string{"k8s_pod_name": "checkout-1"}, query(), "after migration")
}

func countLegacyStream(t *testing.T, projectID uuid.UUID, stream string) int64 {
	var count int64
	require.NoError(t, storage.GetDb().Model(&segments.SegmentRecord{}).
		Where("project_id = ? AND stream = ? AND schema_version = ?", projectID, stream, segments.SchemaVersionLegacy).
		Count(&count).Error)
	return count
}

// A segment the worker can't rewrite is recorded in the catalog, so a
// replica that never ran the worker reports it too.
func Test_RewriteFailure_ReportedByEveryReplica(t *testing.T) {
	projectID := uuid.New()
	repository := logs_core.GetLogCoreRepository()
	require.NoError(t, repository.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: {{
		ID: uuid.New(), ProjectID: projectID, Timestamp: time.Now().UTC(), Level: logs_core.LogLevelInfo,
		Message: "unreadable later",
	}}}))
	require.NoError(t, repository.ForceFlush())
	legacyPath := makeLegacy(t, projectID)
	require.NoError(t, os.WriteFile(legacyPath, []byte("not parquet"), 0o644))
	t.Cleanup(func() { storage.GetDb().Exec("DELETE FROM stream_segments WHERE project_id = ?", projectID) })
	other := &Worker{repo: &segments.SegmentRepository{}, failed: map[string][]uuid.UUID{}}
	before := skippedLogs(t, other)

	for range 20 {
		remaining, err := GetWorker().migrateBatch(context.Background())
		require.NoError(t, err)
		if !remaining {
			break
		}
	}
	var failedAt *time.Time
	var cause string
	require.NoError(t, storage.GetDb().Raw(
		"SELECT rewrite_failed_at, rewrite_error FROM stream_segments WHERE project_id = ? AND stream = 'logs'",
		projectID).Row().Scan(&failedAt, &cause))
	require.NotNil(t, failedAt)
	assert.NotEmpty(t, cause)
	assert.Equal(t, before+1, skippedLogs(t, other), "a replica without the worker's memory counts it")
}

func skippedLogs(t *testing.T, w *Worker) int {
	statuses, err := w.Status()
	require.NoError(t, err)
	for _, status := range statuses {
		if status.Stream == "logs" {
			return status.Skipped
		}
	}
	return 0
}
