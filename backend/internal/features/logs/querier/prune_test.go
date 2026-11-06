package logs_querier_test

import (
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	logs_ingester "obzly/internal/features/logs/ingester"
	logs_querier "obzly/internal/features/logs/querier"
	"obzly/internal/storage/segments"
	"obzly/internal/telemetry"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func Test_ExecuteQuery_ServiceFilter_SkipsOtherServicesSegmentsWithSameResult(t *testing.T) {
	now := time.Now().UTC()
	projectID, querier := seedSegmentPerService(t, now)
	before := prunedLogSegments()

	response, err := querier.ExecuteQuery(projectID, queryRequest(
		condition("service_name", logs_core.ConditionOperatorEquals, "svc-a"),
		now.Add(-time.Hour), now.Add(time.Hour),
	))

	require.NoError(t, err)
	require.ElementsMatch(t, []string{"a-info", "a-error"}, messagesOf(response.Logs))
	require.EqualValues(t, 2, response.Total)
	require.Equal(t, before+1, prunedLogSegments(), "svc-b's segment is never opened")
}

func Test_ExecuteQuery_LevelFilterInsideAnd_PrunesCaseInsensitively(t *testing.T) {
	now := time.Now().UTC()
	projectID, querier := seedSegmentPerService(t, now)
	before := prunedLogSegments()

	response, err := querier.ExecuteQuery(projectID, queryRequest(
		logical(logs_core.LogicalOperatorAnd,
			condition("level", logs_core.ConditionOperatorEquals, "ERROR"),
			condition("message", logs_core.ConditionOperatorContains, "a-"),
		),
		now.Add(-time.Hour), now.Add(time.Hour),
	))

	require.NoError(t, err)
	require.Equal(t, []string{"a-error"}, messagesOf(response.Logs))
	require.Equal(t, before+1, prunedLogSegments(), "svc-b's segment holds only info")
}

func Test_ExecuteQuery_ServiceFilterUnderOr_DoesNotPrune(t *testing.T) {
	now := time.Now().UTC()
	projectID, querier := seedSegmentPerService(t, now)
	before := prunedLogSegments()

	response, err := querier.ExecuteQuery(projectID, queryRequest(
		logical(logs_core.LogicalOperatorOr,
			condition("service_name", logs_core.ConditionOperatorEquals, "svc-a"),
			condition("message", logs_core.ConditionOperatorEquals, "b-info"),
		),
		now.Add(-time.Hour), now.Add(time.Hour),
	))

	require.NoError(t, err)
	require.ElementsMatch(t, []string{"a-info", "a-error", "b-info"}, messagesOf(response.Logs))
	require.Equal(t, before, prunedLogSegments())
}

func Test_ExecuteQuery_InOnService_KeepsSegmentsHoldingAnyValue(t *testing.T) {
	now := time.Now().UTC()
	projectID, querier := seedSegmentPerService(t, now)
	before := prunedLogSegments()

	response, err := querier.ExecuteQuery(projectID, queryRequest(
		condition("service_name", logs_core.ConditionOperatorIn, []any{"svc-b", "svc-z"}),
		now.Add(-time.Hour), now.Add(time.Hour),
	))

	require.NoError(t, err)
	require.Equal(t, []string{"b-info"}, messagesOf(response.Logs))
	require.Equal(t, before+1, prunedLogSegments())
}

// seedSegmentPerService writes one segment for svc-a (info and error) and
// one for svc-b (info only) in the same project.
func seedSegmentPerService(t *testing.T, now time.Time) (uuid.UUID, *logs_querier.LogQuerier) {
	t.Helper()
	ingester, err := logs_ingester.NewLogIngester(t.TempDir(), segments.WithMaxRows(1_000_000))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })

	projectID := uuid.New()
	logItem := func(service, level, message string) *logs_core.LogItem {
		return &logs_core.LogItem{
			ID: uuid.New(), ProjectID: projectID, Timestamp: now, Message: message,
			Level: logs_core.LogLevel(level), Fields: map[string]any{"service_name": service},
		}
	}
	for _, batch := range [][]*logs_core.LogItem{
		{logItem("svc-a", "info", "a-info"), logItem("svc-a", "error", "a-error")},
		{logItem("svc-b", "info", "b-info")},
	} {
		require.NoError(t, ingester.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: batch}))
		_, err := ingester.FlushAll()
		require.NoError(t, err)
	}

	querier, err := logs_querier.NewLogQuerier()
	require.NoError(t, err)
	t.Cleanup(func() { _ = querier.Close() })
	return projectID, querier
}

func prunedLogSegments() float64 {
	return testutil.ToFloat64(telemetry.QuerySegmentsPruned.WithLabelValues(logs_core.StreamLogs))
}
