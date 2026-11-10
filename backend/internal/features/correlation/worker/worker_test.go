package correlation_worker_test

import (
	"os"
	"testing"
	"time"

	correlation_core "obzly/internal/features/correlation/core"
	correlation_repository "obzly/internal/features/correlation/repository"
	correlation_worker "obzly/internal/features/correlation/worker"
	logs_core "obzly/internal/features/logs/core"
	logs_testsupport "obzly/internal/features/logs/testsupport"
	projects_models "obzly/internal/features/projects/models"
	spans_core "obzly/internal/features/spans/core"
	spans_testsupport "obzly/internal/features/spans/testsupport"
	"obzly/internal/storage"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestMain wires real temp-dir Parquet stores and the Postgres repository;
// the tests drive the real worker, not mocks.
func TestMain(m *testing.M) {
	logs_testsupport.SetupRepository()
	spans_testsupport.SetupRepository()
	correlation_core.SetRepository(correlation_repository.New())
	os.Exit(m.Run())
}

func createTestProject(t *testing.T) uuid.UUID {
	t.Helper()
	project := &projects_models.Project{ID: uuid.New(), Name: "correlation-worker-test", CreatedAt: time.Now().UTC()}
	require.NoError(t, storage.GetDb().Create(project).Error)
	t.Cleanup(func() {
		storage.GetDb().Where("id = ?", project.ID).Delete(&projects_models.Project{})
	})
	return project.ID
}

func storeErrorLog(t *testing.T, projectID uuid.UUID, service string, at time.Time, traceID string) {
	t.Helper()
	require.NoError(t, logs_core.GetLogCoreRepository().StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{
		projectID: {{
			ID: uuid.New(), ProjectID: projectID, Timestamp: at, Level: logs_core.LogLevelError,
			Message: service + " request failed", TraceID: traceID,
			Fields: map[string]any{"service.name": service},
		}},
	}))
	require.NoError(t, logs_core.GetLogCoreRepository().ForceFlush())
}

func storeInfoLog(t *testing.T, projectID uuid.UUID, service string, at time.Time) {
	t.Helper()
	require.NoError(t, logs_core.GetLogCoreRepository().StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{
		projectID: {{
			ID: uuid.New(), ProjectID: projectID, Timestamp: at, Level: logs_core.LogLevelInfo,
			Message: service + " request ok", Fields: map[string]any{"service.name": service},
		}},
	}))
	require.NoError(t, logs_core.GetLogCoreRepository().ForceFlush())
}

func storeSpan(
	t *testing.T,
	projectID uuid.UUID,
	service, traceID, spanID, parentSpanID string,
	status spans_core.StatusCode,
	at time.Time,
) {
	t.Helper()
	require.NoError(t, spans_core.GetSpanCoreRepository().StoreSpansBatch(map[uuid.UUID][]*spans_core.Span{
		projectID: {{
			ID: uuid.New(), ProjectID: projectID, TraceID: traceID, SpanID: spanID, ParentSpanID: parentSpanID,
			Name: "handle", ServiceName: service, Kind: spans_core.SpanKindServer,
			StartTime: at, EndTime: at.Add(5 * time.Millisecond), StatusCode: status,
		}},
	}))
	require.NoError(t, spans_core.GetSpanCoreRepository().ForceFlush())
}

// runWorker correlates the test's own project only: other packages' tests share the database
// and delete their projects while a scan of every project runs, which failed it at random.
func runWorker(t *testing.T, projectID uuid.UUID) {
	t.Helper()
	require.NoError(t, correlation_worker.GetCorrelationBackgroundService().ExecuteTaskForProjectForTest(projectID))
}

func listSessions(t *testing.T, projectID uuid.UUID) []correlation_core.SessionSummaryDTO {
	t.Helper()
	response, err := correlation_core.GetIncidentSessionRepository().
		ListSessions(projectID, &correlation_core.ListSessionsRequestDTO{Limit: 500})
	require.NoError(t, err)
	return response.Sessions
}

func Test_CorrelateProject_ErrorLog_CreatesSession(t *testing.T) {
	projectID := createTestProject(t)
	now := time.Now().UTC()

	storeErrorLog(t, projectID, "checkout", now.Add(-time.Minute), "")

	runWorker(t, projectID)

	sessions := listSessions(t, projectID)
	require.NotEmpty(t, sessions, "an ERROR log should be a genuine anomaly and create a session")

	found := false
	for _, s := range sessions {
		if s.ServiceName == "checkout" {
			found = true
			require.Equal(t, string(correlation_core.TriggerTypeErrorLog), s.TriggerType)
		}
	}
	require.True(t, found)
}

func Test_CorrelateProject_NoAnomalies_NoSessionsCreated(t *testing.T) {
	projectID := createTestProject(t)
	now := time.Now().UTC()

	storeInfoLog(t, projectID, "quiet-service", now.Add(-time.Minute))
	storeSpan(
		t,
		projectID,
		"quiet-service",
		"trace-quiet",
		"span-quiet",
		"",
		spans_core.StatusCodeOK,
		now.Add(-time.Minute),
	)

	runWorker(t, projectID)

	sessions := listSessions(t, projectID)
	for _, s := range sessions {
		require.NotEqual(
			t,
			"quiet-service",
			s.ServiceName,
			"no genuine anomaly signal exists for this service, so no session should ever be created for it",
		)
	}
}

func Test_CorrelateProject_CrossServiceTraceLink_MergesConnectedServices(t *testing.T) {
	projectID := createTestProject(t)
	now := time.Now().UTC()
	traceID := "trace-cross-service-1"

	// The frontend -> backend parent/child span pair is the service graph
	// edge that allows the merge.
	storeErrorLog(t, projectID, "frontend", now, traceID)
	storeSpan(t, projectID, "frontend", traceID, "span-frontend-1", "", spans_core.StatusCodeOK, now)
	storeSpan(t, projectID, "backend", traceID, "span-backend-1", "span-frontend-1", spans_core.StatusCodeError, now)

	runWorker(t, projectID)

	sessions := listSessions(t, projectID)
	var merged *correlation_core.SessionSummaryDTO
	for i := range sessions {
		if sessions[i].TriggerType == string(correlation_core.TriggerTypeTraceIDLink) {
			merged = &sessions[i]
		}
	}
	require.NotNil(t, merged, "connected services sharing a trace_id should merge into one trace_id_link session")
	require.Equal(t, "backend", merged.ServiceName, "primaryService picks the lexicographically-first service name")

	detail, err := correlation_core.GetIncidentSessionRepository().
		GetSessionDetail(projectID, uuid.MustParse(merged.ID))
	require.NoError(t, err)

	var hasLog, hasSpan bool
	for _, sig := range detail.Signals {
		switch sig.SignalType {
		case correlation_core.SignalTypeLog:
			hasLog = true
		case correlation_core.SignalTypeSpan:
			hasSpan = true
		}
	}
	require.True(t, hasLog, "merged session should keep the original error log signal")
	require.True(t, hasSpan, "merged session should keep the original error span signal")
}

func Test_CorrelateProject_CrossServiceTraceLink_WithoutGraphEdge_DoesNotMerge(t *testing.T) {
	projectID := createTestProject(t)
	now := time.Now().UTC()
	traceID := "trace-cross-service-2"

	// Shared trace_id but no call edge between the services: no merge.
	storeErrorLog(t, projectID, "isolated-a", now, traceID)
	storeSpan(t, projectID, "isolated-b", traceID, "span-isolated-1", "", spans_core.StatusCodeError, now)

	runWorker(t, projectID)

	sessions := listSessions(t, projectID)
	var sawA, sawB, sawMerged bool
	for _, s := range sessions {
		switch s.ServiceName {
		case "isolated-a":
			sawA = true
		case "isolated-b":
			sawB = true
		}
		if s.TriggerType == string(correlation_core.TriggerTypeTraceIDLink) {
			sawMerged = true
		}
	}
	require.True(t, sawA, "isolated-a's own error log should still create its own session")
	require.True(t, sawB, "isolated-b's own error span should still create its own session")
	require.False(
		t,
		sawMerged,
		"unconnected services sharing a trace_id must not merge - that's the graph-pruning guarantee",
	)
}

func Test_CorrelateProject_NonOverlappingBuckets_CreatesSeparateSessions(t *testing.T) {
	projectID := createTestProject(t)
	now := time.Now().UTC()

	storeErrorLog(t, projectID, "bursty", now.Add(-30*time.Minute), "")
	storeErrorLog(t, projectID, "bursty", now.Add(-5*time.Minute), "")

	runWorker(t, projectID)

	sessions := listSessions(t, projectID)
	var bursty []correlation_core.SessionSummaryDTO
	for _, s := range sessions {
		if s.ServiceName == "bursty" {
			bursty = append(bursty, s)
		}
	}
	require.Len(
		t,
		bursty,
		2,
		"two error logs 25 minutes apart fall in different 5-minute grouping windows and must not be merged into one session",
	)
	for _, s := range bursty {
		require.WithinDuration(
			t,
			s.WindowStart,
			s.WindowEnd,
			0,
			"a session built from a single signal has a zero-width window",
		)
	}
}

// Test_CorrelateProject_Watermark_BoundsScanCost: a second run scans only
// (watermark, now], so old signals are neither rescanned nor re-grouped.
func Test_CorrelateProject_Watermark_BoundsScanCost(t *testing.T) {
	projectID := createTestProject(t)
	now := time.Now().UTC()

	// Inside the 24h cold-start lookback.
	for i := 0; i < 200; i++ {
		storeErrorLog(t, projectID, "legacy", now.Add(-20*time.Hour).Add(time.Duration(i)*time.Millisecond), "")
	}

	// One project only: the shared test database holds many other projects.
	require.NoError(t, correlation_worker.GetCorrelationBackgroundService().ExecuteTaskForProjectForTest(projectID))

	watermarkAfterFirstRun, err := correlation_core.GetIncidentSessionRepository().GetWatermark(projectID)
	require.NoError(t, err)
	require.True(
		t,
		watermarkAfterFirstRun.After(now.Add(-time.Minute)),
		"watermark should advance to ~now after a successful run",
	)

	sessionsAfterFirstRun := listSessions(t, projectID)
	require.NotEmpty(t, sessionsAfterFirstRun)

	storeErrorLog(t, projectID, "fresh", time.Now().UTC(), "")

	start := time.Now()
	require.NoError(t, correlation_worker.GetCorrelationBackgroundService().ExecuteTaskForProjectForTest(projectID))
	elapsed := time.Since(start)

	require.Less(
		t,
		elapsed,
		3*time.Second,
		"a second run must stay fast regardless of the 200 already-scanned historical rows - the watermark, not full-history rescanning, is what bounds cost",
	)

	sessionsAfterSecondRun := listSessions(t, projectID)
	freshFound := false
	for _, s := range sessionsAfterSecondRun {
		if s.ServiceName == "fresh" {
			freshFound = true
		}
	}
	require.True(t, freshFound, "the new small batch should still be picked up by the next incremental run")

	legacyCountAfterFirst := 0
	for _, s := range sessionsAfterFirstRun {
		if s.ServiceName == "legacy" {
			legacyCountAfterFirst++
		}
	}
	legacyCountAfterSecond := 0
	for _, s := range sessionsAfterSecondRun {
		if s.ServiceName == "legacy" {
			legacyCountAfterSecond++
		}
	}
	require.Equal(t, legacyCountAfterFirst, legacyCountAfterSecond,
		"the second run must not rescan/re-create sessions for signals already covered by the advanced watermark")
}
