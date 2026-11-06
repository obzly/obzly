package correlation_repository_test

import (
	"os"
	"testing"
	"time"

	correlation_core "obzly/internal/features/correlation/core"
	correlation_repository "obzly/internal/features/correlation/repository"
	logs_core "obzly/internal/features/logs/core"
	logs_testsupport "obzly/internal/features/logs/testsupport"
	projects_models "obzly/internal/features/projects/models"
	spans_core "obzly/internal/features/spans/core"
	spans_testsupport "obzly/internal/features/spans/testsupport"
	"obzly/internal/storage"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestMain wires real temp-dir Parquet stores and the Postgres repository.
func TestMain(m *testing.M) {
	logs_testsupport.SetupRepository()
	spans_testsupport.SetupRepository()
	correlation_core.SetRepository(correlation_repository.New())
	os.Exit(m.Run())
}

func createTestProject(t *testing.T) uuid.UUID {
	t.Helper()
	project := &projects_models.Project{ID: uuid.New(), Name: "correlation-repo-test", CreatedAt: time.Now().UTC()}
	require.NoError(t, storage.GetDb().Create(project).Error)
	t.Cleanup(func() {
		storage.GetDb().Where("id = ?", project.ID).Delete(&projects_models.Project{})
	})
	return project.ID
}

func Test_CreateSession_PersistsSessionAndSignals(t *testing.T) {
	projectID := createTestProject(t)
	repo := correlation_repository.New()

	now := time.Now().UTC()
	session := &correlation_core.IncidentSession{
		ProjectID:      projectID,
		ServiceName:    "checkout",
		WindowStart:    now.Add(-1 * time.Minute),
		WindowEnd:      now,
		TriggerType:    correlation_core.TriggerTypeErrorLog,
		TriggerSummary: "1 error log(s)",
	}
	signals := []*correlation_core.IncidentSessionSignal{
		{
			SignalType: correlation_core.SignalTypeLog,
			SignalRef:  correlation_core.SignalRefLog(uuid.New().String()),
			OccurredAt: now,
		},
	}

	require.NoError(t, repo.CreateSession(session, signals))
	require.NotEqual(t, uuid.Nil, session.ID)

	response, err := repo.ListSessions(projectID, &correlation_core.ListSessionsRequestDTO{})
	require.NoError(t, err)
	require.Len(t, response.Sessions, 1)
	require.Equal(t, "checkout", response.Sessions[0].ServiceName)
	require.Equal(t, string(correlation_core.TriggerTypeErrorLog), response.Sessions[0].TriggerType)
}

func Test_CreateSession_WithNoSignals_Errors(t *testing.T) {
	projectID := createTestProject(t)
	repo := correlation_repository.New()

	session := &correlation_core.IncidentSession{
		ProjectID:   projectID,
		ServiceName: "checkout",
		WindowStart: time.Now().UTC(),
		WindowEnd:   time.Now().UTC(),
		TriggerType: correlation_core.TriggerTypeErrorLog,
	}

	require.Error(t, repo.CreateSession(session, nil))
}

func Test_ListSessions_FiltersByTimeRangeAndRespectsLimit(t *testing.T) {
	projectID := createTestProject(t)
	repo := correlation_repository.New()

	base := time.Now().UTC().Add(-2 * time.Hour)
	for i := 0; i < 3; i++ {
		windowStart := base.Add(time.Duration(i) * time.Hour)
		session := &correlation_core.IncidentSession{
			ProjectID:   projectID,
			ServiceName: "svc",
			WindowStart: windowStart,
			WindowEnd:   windowStart.Add(time.Minute),
			TriggerType: correlation_core.TriggerTypeErrorLog,
		}
		signals := []*correlation_core.IncidentSessionSignal{
			{
				SignalType: correlation_core.SignalTypeLog,
				SignalRef:  correlation_core.SignalRefLog(uuid.New().String()),
				OccurredAt: windowStart,
			},
		}
		require.NoError(t, repo.CreateSession(session, signals))
	}

	// Only the middle session's window overlaps this narrow range.
	from := base.Add(1 * time.Hour).Add(-time.Second)
	to := base.Add(1 * time.Hour).Add(2 * time.Minute)
	response, err := repo.ListSessions(projectID, &correlation_core.ListSessionsRequestDTO{
		TimeRange: &correlation_core.TimeRangeDTO{From: &from, To: &to},
	})
	require.NoError(t, err)
	require.Len(t, response.Sessions, 1)

	response, err = repo.ListSessions(projectID, &correlation_core.ListSessionsRequestDTO{Limit: 1})
	require.NoError(t, err)
	require.Len(t, response.Sessions, 1)
}

func Test_GetSessionDetail_RehydratesLogAndSpanSignals(t *testing.T) {
	projectID := createTestProject(t)
	repo := correlation_repository.New()

	now := time.Now().UTC()
	logID := uuid.New()
	require.NoError(t, logs_core.GetLogCoreRepository().StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{
		projectID: {{
			ID: logID, ProjectID: projectID, Timestamp: now, Level: logs_core.LogLevelError,
			Message: "checkout failed", Fields: map[string]any{"service.name": "checkout"},
		}},
	}))
	require.NoError(t, logs_core.GetLogCoreRepository().ForceFlush())

	traceID := "trace-detail-1"
	spanID := "span-detail-1"
	require.NoError(t, spans_core.GetSpanCoreRepository().StoreSpansBatch(map[uuid.UUID][]*spans_core.Span{
		projectID: {{
			ID: uuid.New(), ProjectID: projectID, TraceID: traceID, SpanID: spanID,
			Name: "POST /charge", ServiceName: "payments", Kind: spans_core.SpanKindServer,
			StartTime: now, EndTime: now.Add(10 * time.Millisecond), StatusCode: spans_core.StatusCodeError,
		}},
	}))
	require.NoError(t, spans_core.GetSpanCoreRepository().ForceFlush())

	session := &correlation_core.IncidentSession{
		ProjectID: projectID, ServiceName: "checkout", WindowStart: now, WindowEnd: now,
		TriggerType: correlation_core.TriggerTypeTraceIDLink,
	}
	signals := []*correlation_core.IncidentSessionSignal{
		{
			SignalType: correlation_core.SignalTypeLog,
			SignalRef:  correlation_core.SignalRefLog(logID.String()),
			OccurredAt: now,
		},
		{
			SignalType: correlation_core.SignalTypeSpan,
			SignalRef:  correlation_core.SignalRefSpan(traceID, spanID),
			OccurredAt: now,
		},
		{
			SignalType: correlation_core.SignalTypeLog,
			SignalRef:  correlation_core.SignalRefLog(uuid.New().String()),
			OccurredAt: now,
		},
	}
	require.NoError(t, repo.CreateSession(session, signals))

	detail, err := repo.GetSessionDetail(projectID, session.ID)
	require.NoError(t, err)
	require.Len(t, detail.Signals, 3)

	var logFound, spanFound, missingFound bool
	for _, sig := range detail.Signals {
		switch sig.SignalType {
		case correlation_core.SignalTypeLog:
			if sig.Available {
				require.Equal(t, "checkout failed", sig.Log.Message)
				logFound = true
			} else {
				missingFound = true
			}
		case correlation_core.SignalTypeSpan:
			require.True(t, sig.Available)
			require.Equal(t, "payments", sig.Span.ServiceName)
			require.Equal(t, "ERROR", sig.Span.StatusCode)
			spanFound = true
		}
	}
	require.True(t, logFound, "expected the real log signal to be rehydrated")
	require.True(t, spanFound, "expected the span signal to be rehydrated")
	require.True(
		t,
		missingFound,
		"expected the fabricated, nonexistent logId signal to gracefully come back Available: false",
	)
}

func Test_Watermark_GetSetRoundTrip(t *testing.T) {
	projectID := createTestProject(t)
	repo := correlation_repository.New()

	watermark, err := repo.GetWatermark(projectID)
	require.NoError(t, err)
	require.True(t, watermark.IsZero(), "a never-scanned project should report a zero watermark")

	t1 := time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, repo.SetWatermark(projectID, t1))

	got, err := repo.GetWatermark(projectID)
	require.NoError(t, err)
	require.WithinDuration(t, t1, got, time.Millisecond)

	t2 := t1.Add(time.Hour)
	require.NoError(t, repo.SetWatermark(projectID, t2))

	got, err = repo.GetWatermark(projectID)
	require.NoError(t, err)
	require.WithinDuration(t, t2, got, time.Millisecond)
}
