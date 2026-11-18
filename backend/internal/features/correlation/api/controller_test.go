package correlation_api_test

import (
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	correlation_api "obzly/internal/features/correlation/api"
	correlation_core "obzly/internal/features/correlation/core"
	correlation_repository "obzly/internal/features/correlation/repository"
	logs_core "obzly/internal/features/logs/core"
	logs_testsupport "obzly/internal/features/logs/testsupport"
	projects_controllers "obzly/internal/features/projects/controllers"
	projects_models "obzly/internal/features/projects/models"
	projects_testing "obzly/internal/features/projects/testing"
	spans_core "obzly/internal/features/spans/core"
	spans_testsupport "obzly/internal/features/spans/testsupport"
	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_middleware "obzly/internal/features/users/middleware"
	users_services "obzly/internal/features/users/services"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	logs_testsupport.SetupRepository()
	spans_testsupport.SetupRepository()
	correlation_core.SetRepository(correlation_repository.New())
	os.Exit(m.Run())
}

func Test_GetSessionImpact_RanksSessionServicesByErrorSignalsInWindow(t *testing.T) {
	router := createCorrelationTestRouter()
	owner, project, sessionID := setUpSessionWithSignals(t, router, "Incident Impact")

	var response correlation_core.SessionImpactDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/correlation/sessions/%s/%s/impact", project.ID, sessionID),
		"Bearer "+owner.Token, http.StatusOK, &response,
	)

	assert.Equal(t, sessionID.String(), response.SessionID)
	require.Len(t, response.Services, 3, "search errored in the window but is not in the session")

	// checkout: the session's service, 3 error logs + 1 error span in the
	// window (its log 10 minutes later doesn't count).
	assert.Equal(t, correlation_core.ServiceImpactDTO{
		ServiceName: "checkout", ErrorLogs: 3, ErrorSpans: 1, Total: 4,
	}, response.Services[0])
	// payments: owns a span signal; its OK span isn't an error.
	assert.Equal(t, correlation_core.ServiceImpactDTO{
		ServiceName: "payments", ErrorLogs: 0, ErrorSpans: 2, Total: 2,
	}, response.Services[1])
	// web: owns a log signal and has no spans.
	assert.Equal(t, correlation_core.ServiceImpactDTO{
		ServiceName: "web", ErrorLogs: 1, ErrorSpans: 0, Total: 1,
	}, response.Services[2])
}

func Test_GetSessionImpact_NonMember_ReturnsForbidden(t *testing.T) {
	router := createCorrelationTestRouter()
	_, project, sessionID := setUpSessionWithSignals(t, router, "Incident Impact Forbidden")
	outsider := users_testing.CreateTestUser(users_enums.UserRoleMember)

	test_utils.MakeGetRequest(
		t, router,
		fmt.Sprintf("/api/v1/correlation/sessions/%s/%s/impact", project.ID, sessionID),
		"Bearer "+outsider.Token, http.StatusForbidden,
	)
}

func Test_GetSessionImpact_UnknownSession_ReturnsNotFound(t *testing.T) {
	router := createCorrelationTestRouter()
	owner, project, _ := setUpSessionWithSignals(t, router, "Incident Impact Not Found")

	test_utils.MakeGetRequest(
		t, router,
		fmt.Sprintf("/api/v1/correlation/sessions/%s/%s/impact", project.ID, uuid.New()),
		"Bearer "+owner.Token, http.StatusNotFound,
	)
}

func createCorrelationTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	v1 := router.Group("/api/v1")
	protected := v1.Group("").Use(users_middleware.AuthMiddleware(users_services.GetUserService()))
	if routerGroup, ok := protected.(*gin.RouterGroup); ok {
		projects_controllers.GetProjectController().RegisterRoutes(routerGroup)
		projects_controllers.GetMembershipController().RegisterRoutes(routerGroup)
		correlation_api.GetCorrelationController().RegisterRoutes(routerGroup)
	}
	return router
}

// setUpSessionWithSignals stores a checkout session over a 2-minute window
// with a span signal owned by payments and a log signal owned by web, plus
// error logs and spans in and around the window, including an unrelated
// service (search).
func setUpSessionWithSignals(
	t *testing.T,
	router *gin.Engine,
	testName string,
) (*users_dto.SignInResponseDTO, *projects_models.Project, uuid.UUID) {
	t.Helper()

	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken(testName, owner.Token, router)

	start := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Minute)
	end := start.Add(2 * time.Minute)

	var logs []*logs_core.LogItem
	addLog := func(service string, at time.Time, level logs_core.LogLevel) *logs_core.LogItem {
		item := &logs_core.LogItem{
			ID: uuid.New(), ProjectID: project.ID, Timestamp: at, Level: level,
			Message: service + " failed", Fields: map[string]any{"service.name": service},
		}
		logs = append(logs, item)
		return item
	}
	for i := range 3 {
		addLog("checkout", start.Add(time.Duration(i)*time.Second), logs_core.LogLevelError)
	}
	addLog("checkout", start.Add(30*time.Second), logs_core.LogLevelInfo)
	addLog("checkout", end.Add(10*time.Minute), logs_core.LogLevelError)
	webLog := addLog("web", start.Add(time.Minute), logs_core.LogLevelError)
	for i := range 5 {
		addLog("search", start.Add(time.Duration(i)*time.Second), logs_core.LogLevelError)
	}
	require.NoError(t, logs_core.GetLogCoreRepository().StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{
		project.ID: logs,
	}))
	require.NoError(t, logs_core.GetLogCoreRepository().ForceFlush())

	traceID := uuid.NewString()
	var spans []*spans_core.Span
	addSpan := func(service, spanID string, status spans_core.StatusCode, at time.Time) {
		spans = append(spans, &spans_core.Span{
			ID: uuid.New(), ProjectID: project.ID, TraceID: traceID, SpanID: spanID,
			Name: "handle", ServiceName: service, Kind: spans_core.SpanKindServer,
			StartTime: at, EndTime: at.Add(5 * time.Millisecond), StatusCode: status,
		})
	}
	addSpan("checkout", "c1", spans_core.StatusCodeError, start.Add(10*time.Second))
	addSpan("payments", "p1", spans_core.StatusCodeError, start.Add(11*time.Second))
	addSpan("payments", "p2", spans_core.StatusCodeError, end)
	addSpan("payments", "p3", spans_core.StatusCodeOK, start.Add(12*time.Second))
	addSpan("search", "s1", spans_core.StatusCodeError, start.Add(13*time.Second))
	require.NoError(t, spans_core.GetSpanCoreRepository().StoreSpansBatch(map[uuid.UUID][]*spans_core.Span{
		project.ID: spans,
	}))
	require.NoError(t, spans_core.GetSpanCoreRepository().ForceFlush())

	session := &correlation_core.IncidentSession{
		ProjectID: project.ID, ServiceName: "checkout", WindowStart: start, WindowEnd: end,
		TriggerType: correlation_core.TriggerTypeTraceIDLink, TriggerSummary: "test session",
	}
	require.NoError(t, correlation_core.GetIncidentSessionRepository().CreateSession(session,
		[]*correlation_core.IncidentSessionSignal{
			{
				SignalType: correlation_core.SignalTypeSpan,
				SignalRef:  correlation_core.SignalRefSpan(traceID, "p1"),
				OccurredAt: start.Add(11 * time.Second),
			},
			{
				SignalType: correlation_core.SignalTypeLog,
				SignalRef:  correlation_core.SignalRefLog(webLog.ID.String()),
				OccurredAt: webLog.Timestamp,
			},
		}))

	return owner, project, session.ID
}
