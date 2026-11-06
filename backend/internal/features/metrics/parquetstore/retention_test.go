package metrics_parquetstore_test

import (
	"testing"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	metrics_parquetstore "obzly/internal/features/metrics/parquetstore"
	"obzly/internal/storage"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Retention prunes the label index with the samples: a label value or
// metric last seen before the cutoff is forgotten, a recent one is kept.
func Test_DeleteOldMetrics_PrunesTheLabelIndex(t *testing.T) {
	store, err := metrics_parquetstore.New(t.TempDir())
	require.NoError(t, err)
	projectID := uuid.New()
	now := time.Now().UTC()
	sample := func(name, value string, at time.Time) *metrics_core.MetricSample {
		return &metrics_core.MetricSample{ID: uuid.New(), ProjectID: projectID, MetricName: name,
			Labels: map[string]string{"v": value}, Value: 1, Timestamp: at, Type: metrics_core.MetricTypeGauge}
	}
	require.NoError(t, store.StoreSamplesBatch(map[uuid.UUID][]*metrics_core.MetricSample{projectID: {
		sample("old_metric", "old", now.Add(-48*time.Hour)),
		sample("new_metric", "new", now),
	}}))
	require.NoError(t, store.ForceFlush())

	require.NoError(t, store.DeleteOldMetrics(projectID, now.Add(-24*time.Hour), now.Add(-24*time.Hour)))

	var values, names []string
	require.NoError(t, storage.GetDb().Raw(
		"SELECT label_value FROM metric_label_index WHERE project_id = ?", projectID).Scan(&values).Error)
	require.NoError(t, storage.GetDb().Raw(
		"SELECT metric_name FROM metric_name_index WHERE project_id = ?", projectID).Scan(&names).Error)
	require.Equal(t, []string{"new"}, values)
	require.Equal(t, []string{"new_metric"}, names)
	require.NoError(t, store.DeleteMetricsByProject(projectID))
}

// Postgres text rejects NUL: a label value holding one used to fail the
// series flush, so the batch was never acked and was redelivered forever.
func Test_ForceFlush_LabelValueWithNULByte_FlushesAndIndexesIt(t *testing.T) {
	store, err := metrics_parquetstore.New(t.TempDir())
	require.NoError(t, err)
	projectID := uuid.New()
	require.NoError(t, store.StoreSamplesBatch(map[uuid.UUID][]*metrics_core.MetricSample{projectID: {{
		ID: uuid.New(), ProjectID: projectID, MetricName: "nul_metric", Labels: map[string]string{"v": "a\x00b"},
		Value: 1, Timestamp: time.Now().UTC(), Type: metrics_core.MetricTypeGauge,
	}}}))

	require.NoError(t, store.ForceFlush())

	var values []string
	require.NoError(t, storage.GetDb().Raw(
		"SELECT label_value FROM metric_label_index WHERE project_id = ?", projectID).Scan(&values).Error)
	require.Equal(t, []string{"ab"}, values)
	require.NoError(t, store.DeleteMetricsByProject(projectID))
}
