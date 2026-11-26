package logs_jetstream

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"obzly/internal/broker"
	logs_core "obzly/internal/features/logs/core"
	logs_parquetstore "obzly/internal/features/logs/parquetstore"
	"obzly/internal/ingest"
	"obzly/internal/ingest/ingesttest"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Rows a worker hasn't written yet become searchable, new batches are added
// incrementally (past ingest.TailMaxDeltas they are merged), and once the worker
// writes them they are never counted twice.
func Test_Tail_PendingLogsAreQueryable_AndCountedOnceAfterAWorkerWritesThem(t *testing.T) {
	test_utils.UseIsolatedSegmentWriter(t)
	client := ingesttest.StartBroker(t, 64<<20)
	dir := t.TempDir()
	tail, err := NewTail(filepath.Join(dir, "tail"), discardLogger())
	require.NoError(t, err)
	store, err := logs_parquetstore.New(dir, logs_parquetstore.WithTail(tail))
	require.NoError(t, err)
	projectID := uuid.New()

	publishLogs(t, client, projectID, 3)
	requireEventuallyTotal(t, store, projectID, 3, "pending rows become searchable")

	for range ingest.TailMaxDeltas + 2 {
		publishLogs(t, client, projectID, 1)
		time.Sleep(ingest.TailRefreshAfter + 100*time.Millisecond)
		searchTotal(t, store, projectID)
	}
	requireEventuallyTotal(t, store, projectID, 3+ingest.TailMaxDeltas+2, "new batches are added and merged")
	require.EqualValues(t, 3+ingest.TailMaxDeltas+2, aggregateTotal(t, store, projectID), "charts count them too")

	logsWriter, err := store.NewDurableWriter()
	require.NoError(t, err)
	aiStore, err := logs_parquetstore.New(dir, logs_parquetstore.WithStream(logs_core.StreamAILogs))
	require.NoError(t, err)
	aiWriter, err := aiStore.NewDurableWriter()
	require.NoError(t, err)
	ingesttest.StartConsumer(t, client, broker.SignalLogs,
		NewSink(logsWriter, aiWriter, 64<<20, ingesttest.MaxAge, discardLogger()))

	want := int64(3 + ingest.TailMaxDeltas + 2)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		require.LessOrEqual(t, searchTotal(t, store, projectID), want, "written rows are never counted twice")
		time.Sleep(50 * time.Millisecond)
	}
	require.Zero(t, ingesttest.StreamMessages(t, client, broker.SignalLogs))
	require.Equal(t, want, searchTotal(t, store, projectID))
}

// With lanes a project's shards ack on their own: once one lane writes its
// batches the tail stops serving them, even while another lane still has
// older batches pending.
func Test_Tail_LaneAcksItsBatches_WhileAnotherLaneHasOlderOnesPending_CountsThemOnce(t *testing.T) {
	test_utils.UseIsolatedSegmentWriter(t)
	client := ingesttest.StartBroker(t, 64<<20)
	dir := t.TempDir()
	tail, err := NewTail(filepath.Join(dir, "tail"), discardLogger())
	require.NoError(t, err)
	store, err := logs_parquetstore.New(dir, logs_parquetstore.WithTail(tail))
	require.NoError(t, err)
	projectID := uuid.New()
	laneA := fmt.Sprintf("ingest.logs.%d.%s", broker.LaneShard(projectID, 0), projectID)
	laneB := fmt.Sprintf("ingest.logs.%d.%s", broker.LaneShard(projectID, 1), projectID)

	publishLogsOn(t, client, laneB, projectID, 1)
	requireEventuallyTotal(t, store, projectID, 1, "lane B's first batch")
	publishLogsOn(t, client, laneA, projectID, 2)
	publishLogsOn(t, client, laneB, projectID, 1)
	publishLogsOn(t, client, laneA, projectID, 2)
	requireEventuallyTotal(t, store, projectID, 6, "every lane's batches")

	// What lane A's worker acking its batches does on a work-queue stream.
	ctx := context.Background()
	stream, err := client.JetStream.Stream(ctx, "INGEST_LOGS")
	require.NoError(t, err)
	pending, err := client.PendingIngest(ctx, broker.SignalLogs, projectID, 64<<20)
	require.NoError(t, err)
	for _, message := range pending {
		if message.Subject == laneA {
			require.NoError(t, stream.DeleteMsg(ctx, message.Seq))
		}
	}

	requireEventuallyTotal(t, store, projectID, 2, "only lane B's batches are still pending")
}

// A segment that became readable after the tail's last refresh doesn't hide
// the pending rows from the query: it waits for a newer tail instead.
func Test_Tail_SegmentNewerThanTheTail_QueryStillGetsThePendingRows(t *testing.T) {
	client := ingesttest.StartBroker(t, 64<<20)
	tail, err := NewTail(filepath.Join(t.TempDir(), "tail"), discardLogger())
	require.NoError(t, err)
	ctx := context.Background()
	projectID := uuid.New()
	publishLogs(t, client, projectID, 3)
	require.Eventually(t, func() bool {
		return len(tail.TailFiles(ctx, projectID, logs_core.StreamLogs, time.Time{})) > 0
	}, 15*time.Second, 50*time.Millisecond)

	uploadedJustNow := time.Now().UTC()
	files := tail.TailFiles(ctx, projectID, logs_core.StreamLogs, uploadedJustNow)

	require.NotEmpty(t, files)
}

func publishLogs(t *testing.T, client *broker.Client, projectID uuid.UUID, rows int) {
	t.Helper()
	var items []*logs_core.LogItem
	for range rows {
		items = append(items, &logs_core.LogItem{ID: uuid.New(), ProjectID: projectID,
			Timestamp: time.Now().UTC(), Level: "error", Message: "pending"})
	}
	require.NoError(t, ingest.Publish(client, broker.SignalLogs, projectID, items))
}

func requireEventuallyTotal(
	t *testing.T,
	store *logs_parquetstore.LogParquetStore,
	projectID uuid.UUID,
	want int64,
	message string,
) {
	t.Helper()
	require.Eventually(t, func() bool { return searchTotal(t, store, projectID) == want },
		15*time.Second, 100*time.Millisecond, message)
}

func searchTotal(t *testing.T, store *logs_parquetstore.LogParquetStore, projectID uuid.UUID) int64 {
	t.Helper()
	from, to := time.Now().UTC().Add(-time.Hour), time.Now().UTC().Add(time.Hour)
	response, err := store.ExecuteQueryForProject(projectID, &logs_core.LogQueryRequestDTO{
		TimeRange: &logs_core.TimeRangeDTO{From: &from, To: &to},
	})
	require.NoError(t, err)
	return response.Total
}

func aggregateTotal(t *testing.T, store *logs_parquetstore.LogParquetStore, projectID uuid.UUID) int64 {
	t.Helper()
	from, to := time.Now().UTC().Add(-time.Hour), time.Now().UTC().Add(time.Hour)
	response, err := store.ExecuteAggregation(context.Background(), projectID, &logs_core.AggregationRequestDTO{
		TimeRange:  &logs_core.TimeRangeDTO{From: &from, To: &to},
		GroupField: "level",
	})
	require.NoError(t, err)
	var total int64
	for _, bucket := range response.Buckets {
		total += bucket.Count
	}
	return total
}

func publishLogsOn(t *testing.T, client *broker.Client, subject string, projectID uuid.UUID, rows int) {
	t.Helper()
	var items []*logs_core.LogItem
	for range rows {
		items = append(items, &logs_core.LogItem{ID: uuid.New(), ProjectID: projectID,
			Timestamp: time.Now().UTC(), Level: "error", Message: "pending"})
	}
	payloads, err := ingest.EncodePayloads(items)
	require.NoError(t, err)
	for _, payload := range payloads {
		_, err := client.JetStream.Publish(context.Background(), subject, payload)
		require.NoError(t, err)
	}
}
