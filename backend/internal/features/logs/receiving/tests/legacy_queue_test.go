package logs_receiving_tests

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	logs_receiving "obzly/internal/features/logs/receiving"
	cache_utils "obzly/internal/util/cache"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Logs a hard-killed older release left in the Valkey queue are stored on
// the next start.
func Test_DrainLegacyQueue_StoresLeftoverLogsAndEmptiesTheList(t *testing.T) {
	test_utils.UseIsolatedSegmentWriter(t)
	projectID := uuid.New()
	queue := cache_utils.NewValkeyQueueService()
	var serialized [][]byte
	for i := range 3 {
		data, err := json.Marshal(&logs_core.LogItem{
			ID: uuid.New(), ProjectID: projectID, Timestamp: time.Now().UTC().Add(time.Duration(i) * time.Second),
			Level: logs_core.LogLevelInfo, Message: "left in the queue",
		})
		require.NoError(t, err)
		serialized = append(serialized, data)
	}
	require.NoError(t, queue.EnqueueBatch("obzly:logs:queue", serialized))

	stored, err := logs_receiving.DrainLegacyQueue(slog.New(slog.NewTextHandler(io.Discard, nil)))

	require.NoError(t, err)
	require.GreaterOrEqual(t, stored, 3)
	length, err := queue.QueueLength("obzly:logs:queue")
	require.NoError(t, err)
	require.Zero(t, length)
	require.NoError(t, logs_core.GetLogCoreRepository().ForceFlush())
	stats, err := logs_core.GetLogCoreRepository().GetProjectLogStats(projectID)
	require.NoError(t, err)
	require.EqualValues(t, 3, stats.TotalLogs)
}
