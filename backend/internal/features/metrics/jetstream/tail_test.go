package metrics_jetstream

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"obzly/internal/broker"
	metrics_core "obzly/internal/features/metrics/core"
	metrics_parquetstore "obzly/internal/features/metrics/parquetstore"
	"obzly/internal/ingest"
	"obzly/internal/ingest/ingesttest"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Samples a worker hasn't written yet are on dashboards, and once it has
// they are counted once.
func Test_Tail_PendingSamplesAreQueryable_AndCountedOnceAfterAWorkerWritesThem(t *testing.T) {
	test_utils.UseIsolatedSegmentWriter(t)
	client := ingesttest.StartBroker(t, 64<<20)
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tail, err := NewTail(filepath.Join(dir, "tail"), logger)
	require.NoError(t, err)
	store, err := metrics_parquetstore.New(dir, metrics_parquetstore.WithTail(tail))
	require.NoError(t, err)
	projectID := uuid.New()
	now := time.Now().UTC()
	var samples []*metrics_core.MetricSample
	for i := range 3 {
		samples = append(samples, &metrics_core.MetricSample{ID: uuid.New(), ProjectID: projectID,
			MetricName: "tail_gauge", Labels: map[string]string{"host": "a"}, Value: float64(i),
			Timestamp: now.Add(time.Duration(i) * time.Second), Type: metrics_core.MetricTypeGauge})
	}
	require.NoError(t, ingest.Publish(client, broker.SignalMetrics, projectID, samples))

	require.Eventually(t, func() bool { return storedSamples(t, store, projectID) == 3 },
		15*time.Second, 100*time.Millisecond, "pending samples are queryable")

	writer, err := store.NewDurableWriter()
	require.NoError(t, err)
	ingesttest.StartConsumer(
		t,
		client,
		broker.SignalMetrics,
		NewSink(writer, writer.Series(), 64<<20, ingesttest.MaxAge, logger),
	)
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		require.LessOrEqual(t, storedSamples(t, store, projectID), 3, "written samples are never counted twice")
		time.Sleep(50 * time.Millisecond)
	}
	require.Zero(t, ingesttest.StreamMessages(t, client, broker.SignalMetrics))
	require.Equal(t, 3, storedSamples(t, store, projectID))
}

func storedSamples(t *testing.T, store *metrics_parquetstore.MetricParquetStore, projectID uuid.UUID) int {
	t.Helper()
	from, to := time.Now().UTC().Add(-time.Hour), time.Now().UTC().Add(time.Hour)
	response, err := store.SelectSeries(context.Background(), projectID, &metrics_core.SelectSeriesRequestDTO{
		Matchers:  []metrics_core.MatcherDTO{{Name: "__name__", Type: metrics_core.MatchEqual, Value: "tail_gauge"}},
		TimeRange: &metrics_core.TimeRangeDTO{From: &from, To: &to},
	})
	require.NoError(t, err)
	total := 0
	for _, series := range response.Series {
		total += len(series.Samples)
	}
	return total
}
