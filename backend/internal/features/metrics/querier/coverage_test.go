package metrics_querier_test

import (
	"context"
	"os"
	"testing"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	metrics_ingester "obzly/internal/features/metrics/ingester"
	metrics_querier "obzly/internal/features/metrics/querier"
	"obzly/internal/storage"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type coverageState struct {
	Since        time.Time
	Backfillable bool
}

// A new project's backfill is covered from its oldest sample, and a later
// backfill further back moves coverage back with it: label values come
// from the index and series from the series stream for the backfilled
// days. The sample files are removed, so nothing else could answer.
func Test_Coverage_BackfilledProject_LabelValuesAndSeriesFromTheIndex(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	projectID := uuid.New()
	ingester := newCoverageIngester(t)
	flushSamples(t, ingester, projectID, "web-1", now.Add(-72*time.Hour), now.Add(-71*time.Hour))
	require.Equal(t, coverageState{Since: now.Add(-72 * time.Hour), Backfillable: true}, coverageOf(t, projectID))

	flushSamples(t, ingester, projectID, "web-0", now.Add(-120*time.Hour))
	require.Equal(t, coverageState{Since: now.Add(-120 * time.Hour), Backfillable: true}, coverageOf(t, projectID))

	removeSampleFiles(t, projectID)
	querier, err := metrics_querier.NewMetricQuerier()
	require.NoError(t, err)
	t.Cleanup(func() { _ = querier.Close() })
	from, to := now.Add(-120*time.Hour), now.Add(-70*time.Hour)
	window := &metrics_core.TimeRangeDTO{From: &from, To: &to}
	values, err := querier.ListLabelValues(context.Background(), projectID,
		&metrics_core.ListLabelValuesRequestDTO{LabelName: "k8s_pod_name", TimeRange: window})
	require.NoError(t, err)
	require.Equal(t, []string{"web-0", "web-1"}, values.Values, "from the label index")

	values, err = querier.ListLabelValues(context.Background(), projectID, &metrics_core.ListLabelValuesRequestDTO{
		LabelName: "k8s_pod_name", TimeRange: window,
		Matchers: []metrics_core.MatcherDTO{
			{Name: "__name__", Type: metrics_core.MatchEqual, Value: "k8s_pod_cpu_usage"},
			{Name: "k8s_pod_name", Type: metrics_core.MatchRegexp, Value: "web-.*"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"web-0", "web-1"}, values.Values, "from the series stream")
}

// A project whose samples predate its series stream (a segment older than
// the first series flush) keeps coverage from that flush on, whatever a
// backfill brings: its old samples have no series rows.
func Test_Coverage_ProjectWithSegmentsBeforeTheSeriesStream_IsNotBackfilled(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	projectID := uuid.New()
	sampleOnly, err := metrics_ingester.NewSampleOnlyIngester(t.TempDir(), segments.WithMaxRows(1_000_000))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sampleOnly.Close() })
	flushSamples(t, sampleOnly, projectID, "web-old", now.Add(-200*time.Hour))
	require.NoError(t, storage.GetDb().Exec(
		"UPDATE stream_segments SET created_at = ? WHERE project_id = ? AND stream = 'metrics'",
		now.Add(-30*24*time.Hour), projectID).Error)

	ingester := newCoverageIngester(t)
	started := time.Now().UTC()
	flushSamples(t, ingester, projectID, "web-1", now.Add(-72*time.Hour))
	coverage := coverageOf(t, projectID)
	require.False(t, coverage.Backfillable)
	require.WithinDuration(t, started, coverage.Since, time.Minute)

	flushSamples(t, ingester, projectID, "web-0", now.Add(-120*time.Hour))
	require.Equal(t, coverage, coverageOf(t, projectID))
}

type coverageFlusher interface {
	StoreSamplesBatch(map[uuid.UUID][]*metrics_core.MetricSample) error
	FlushAll() ([]segments.Segment, error)
}

func newCoverageIngester(t *testing.T) *metrics_ingester.MetricIngester {
	t.Helper()
	ingester, err := metrics_ingester.NewMetricIngester(t.TempDir(), segments.WithMaxRows(1_000_000))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })
	return ingester
}

func flushSamples(t *testing.T, ingester coverageFlusher, projectID uuid.UUID, pod string, times ...time.Time) {
	t.Helper()
	var samples []*metrics_core.MetricSample
	for _, at := range times {
		samples = append(samples, &metrics_core.MetricSample{
			ID: uuid.New(), ProjectID: projectID, MetricName: "k8s_pod_cpu_usage",
			Labels: map[string]string{"k8s_pod_name": pod}, Value: 1, Timestamp: at, Type: metrics_core.MetricTypeGauge,
		})
	}
	require.NoError(t, ingester.StoreSamplesBatch(map[uuid.UUID][]*metrics_core.MetricSample{projectID: samples}))
	_, err := ingester.FlushAll()
	require.NoError(t, err)
}

func coverageOf(t *testing.T, projectID uuid.UUID) coverageState {
	t.Helper()
	var rows []coverageState
	require.NoError(t, storage.GetDb().Raw(
		"SELECT since, backfillable FROM metric_series_coverage WHERE project_id = ?", projectID).Scan(&rows).Error)
	require.Len(t, rows, 1)
	rows[0].Since = rows[0].Since.UTC()
	return rows[0]
}

func removeSampleFiles(t *testing.T, projectID uuid.UUID) {
	t.Helper()
	records, err := (&segments.SegmentRepository{}).ListByProject(projectID, "metrics")
	require.NoError(t, err)
	require.NotEmpty(t, records)
	for _, record := range records {
		require.NoError(t, os.Remove(record.Path))
	}
}
