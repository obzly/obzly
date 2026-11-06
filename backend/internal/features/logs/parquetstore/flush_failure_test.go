package logs_parquetstore_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	logs_parquetstore "obzly/internal/features/logs/parquetstore"
	"obzly/internal/storage/segments"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Direct writes are the only copy of their rows: a failed flush keeps them.
func Test_ForceFlush_WhenSegmentWriteFails_WritesTheRowsOnTheNextFlush(t *testing.T) {
	test_utils.UseIsolatedSegmentWriter(t)
	dataDir := t.TempDir()
	store, err := logs_parquetstore.New(dataDir)
	require.NoError(t, err)
	projectID := uuid.New()
	require.NoError(t, store.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: testLogItems(projectID, 3)}))
	unblock := blockSegmentDir(t, dataDir, projectID)

	require.Error(t, store.ForceFlush())

	unblock()
	require.NoError(t, store.ForceFlush())
	written, err := (&segments.SegmentRepository{}).ListByProject(projectID, logs_core.StreamLogs)
	require.NoError(t, err)
	require.Len(t, written, 1)
	require.Equal(t, int64(3), written[0].RowCount)
}

// JetStream redelivers a batch whose flush failed, so the durable writer
// must not keep its rows too.
func Test_DurableWriterFlush_WhenSegmentWriteFails_DoesNotKeepTheRows(t *testing.T) {
	test_utils.UseIsolatedSegmentWriter(t)
	dataDir := t.TempDir()
	store, err := logs_parquetstore.New(dataDir)
	require.NoError(t, err)
	durable, err := store.NewDurableWriter()
	require.NoError(t, err)
	projectID := uuid.New()
	require.NoError(t, durable.Append(map[uuid.UUID][]*logs_core.LogItem{projectID: testLogItems(projectID, 3)}))
	unblock := blockSegmentDir(t, dataDir, projectID)

	_, err = durable.Flush(projectID)
	require.Error(t, err)

	unblock()
	record, err := durable.Flush(projectID)
	require.NoError(t, err)
	require.Nil(t, record, "the redelivered batch is the retry")
}

// blockSegmentDir puts a file where the project's logs segment directory
// goes, so the flush fails.
func blockSegmentDir(t *testing.T, dataDir string, projectID uuid.UUID) (unblock func()) {
	t.Helper()

	streamDir := filepath.Join(dataDir, logs_core.StreamLogs)
	require.NoError(t, os.MkdirAll(streamDir, 0o755))
	blocker := filepath.Join(streamDir, projectID.String())
	require.NoError(t, os.WriteFile(blocker, nil, 0o644))
	return func() { require.NoError(t, os.Remove(blocker)) }
}

func testLogItems(projectID uuid.UUID, count int) []*logs_core.LogItem {
	start := time.Now().UTC()
	items := make([]*logs_core.LogItem, count)
	for i := range items {
		items[i] = &logs_core.LogItem{
			ID: uuid.New(), ProjectID: projectID, Timestamp: start.Add(time.Duration(i) * time.Second),
			Level: logs_core.LogLevelInfo, Message: "payment accepted",
		}
	}
	return items
}
