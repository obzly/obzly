package servicemap_tests

import (
	"fmt"
	"testing"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	projects_controllers "obzly/internal/features/projects/controllers"
	projects_models "obzly/internal/features/projects/models"
	projects_testing "obzly/internal/features/projects/testing"
	"obzly/internal/features/servicemap"
	spans_core "obzly/internal/features/spans/core"
	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_middleware "obzly/internal/features/users/middleware"
	users_services "obzly/internal/features/users/services"
	users_testing "obzly/internal/features/users/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// CreateServiceMapTestRouter uses real auth and project controllers, so
// access checks run against real membership rows.
func CreateServiceMapTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	v1 := router.Group("/api/v1")
	protected := v1.Group("").Use(users_middleware.AuthMiddleware(users_services.GetUserService()))
	if routerGroup, ok := protected.(*gin.RouterGroup); ok {
		projects_controllers.GetProjectController().RegisterRoutes(routerGroup)
		projects_controllers.GetMembershipController().RegisterRoutes(routerGroup)
		servicemap.GetServiceMapController().RegisterRoutes(routerGroup)
	}

	return router
}

// SetUpProjectWithSpansAndNetworkMetrics seeds a spans-only edge (gateway ->
// payments) and a network-only edge (gateway -> postgres-db, untraced).
func SetUpProjectWithSpansAndNetworkMetrics(
	t *testing.T,
	router *gin.Engine,
	testName string,
) (owner *users_dto.SignInResponseDTO, project *projects_models.Project) {
	t.Helper()

	owner = users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ = projects_testing.CreateTestProjectWithToken(testName, owner.Token, router)

	base := time.Now().UTC().Truncate(time.Millisecond)

	spanList := []*spans_core.Span{
		{
			ID: uuid.New(), ProjectID: project.ID, TraceID: uuid.NewString(), SpanID: "s1",
			Name: "GET /checkout", ServiceName: "gateway",
			StartTime: base, EndTime: base.Add(50 * time.Millisecond),
			StatusCode: spans_core.StatusCodeOK,
			// The k8sattributes processor's real keys.
			ResourceAttributes: map[string]any{
				"k8s.namespace.name":  "prod",
				"k8s.deployment.name": "gateway-deploy",
				"k8s.node.name":       "node-1",
			},
		},
		{
			ID: uuid.New(), ProjectID: project.ID, TraceID: uuid.NewString(), SpanID: "s2", ParentSpanID: "s1",
			Name: "POST /charge", ServiceName: "payments",
			StartTime: base.Add(5 * time.Millisecond), EndTime: base.Add(20 * time.Millisecond),
			StatusCode: spans_core.StatusCodeOK,
		},
	}
	// The self-join links spans only within one trace.
	spanList[1].TraceID = spanList[0].TraceID

	require.NoError(t, spans_core.GetSpanCoreRepository().StoreSpansBatch(
		map[uuid.UUID][]*spans_core.Span{project.ID: spanList},
	))
	require.NoError(t, spans_core.GetSpanCoreRepository().ForceFlush())

	// Two counter samples, both in the past: the default window ends at
	// request-time now, which would exclude a future-dated sample.
	networkSamples := []*metrics_core.MetricSample{
		{
			ID: uuid.New(), ProjectID: project.ID, MetricName: "obi.network.flow.bytes",
			Type: metrics_core.MetricTypeCounter, Timestamp: base.Add(-2 * time.Second), Value: 1024,
			Labels: map[string]string{
				"k8s.src.owner.name": "gateway", "k8s.dst.owner.name": "postgres-db",
				"k8s.dst.namespace": "prod",
			},
		},
		{
			ID: uuid.New(), ProjectID: project.ID, MetricName: "obi.network.flow.bytes",
			Type: metrics_core.MetricTypeCounter, Timestamp: base.Add(-time.Second), Value: 3072,
			Labels: map[string]string{
				"k8s.src.owner.name": "gateway", "k8s.dst.owner.name": "postgres-db",
				"k8s.dst.namespace": "prod",
			},
		},
	}
	require.NoError(t, metrics_core.GetMetricCoreRepository().StoreSamplesBatch(
		map[uuid.UUID][]*metrics_core.MetricSample{project.ID: networkSamples},
	))
	require.NoError(t, metrics_core.GetMetricCoreRepository().ForceFlush())

	return owner, project
}

// SetUpProjectWithRedSpans seeds two traces: gateway (root SERVER, 900 ms)
// calls payments over a traced client span (server.address
// payments.prod.svc), orders-db (postgresql) and cache-1 (redis); payments
// produces to kafka. Trace 2's payments span fails, and trace 1's call from
// gateway to an untraced external API fails too.
func SetUpProjectWithRedSpans(
	t *testing.T,
	router *gin.Engine,
	testName string,
) (owner *users_dto.SignInResponseDTO, project *projects_models.Project) {
	t.Helper()

	owner = users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ = projects_testing.CreateTestProjectWithToken(testName, owner.Token, router)

	base := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	var spanList []*spans_core.Span
	add := func(
		traceID, spanID, parentID, service string,
		kind spans_core.SpanKind,
		status spans_core.StatusCode,
		offset, duration time.Duration,
		attributes map[string]any,
	) {
		spanList = append(spanList, &spans_core.Span{
			ID: uuid.New(), ProjectID: project.ID, TraceID: traceID, SpanID: spanID, ParentSpanID: parentID,
			Name: "op-" + spanID, ServiceName: service, Kind: kind,
			StartTime: base.Add(offset), EndTime: base.Add(offset + duration),
			StatusCode: status, Attributes: attributes,
		})
	}

	ok, failed := spans_core.StatusCodeOK, spans_core.StatusCodeError
	for i, paymentsStatus := range []spans_core.StatusCode{ok, failed} {
		traceID := uuid.NewString()
		p := fmt.Sprintf("t%d-", i)
		at := time.Duration(i) * time.Second
		add(traceID, p+"root", "", "gateway", spans_core.SpanKindServer, ok, at, 900*time.Millisecond, nil)
		add(traceID, p+"call-pay", p+"root", "gateway", spans_core.SpanKindClient, ok,
			at+10*time.Millisecond, 50*time.Millisecond, map[string]any{"server.address": "payments.prod.svc"})
		add(traceID, p+"pay", p+"call-pay", "payments", spans_core.SpanKindServer, paymentsStatus,
			at+15*time.Millisecond, 40*time.Millisecond, nil)
		add(traceID, p+"pay-work", p+"pay", "payments", spans_core.SpanKindInternal, ok,
			at+16*time.Millisecond, 5*time.Millisecond, nil)
		add(traceID, p+"produce", p+"pay", "payments", spans_core.SpanKindProducer, ok,
			at+20*time.Millisecond, 2*time.Millisecond, map[string]any{"messaging.system": "kafka"})
		add(traceID, p+"db", p+"root", "gateway", spans_core.SpanKindClient, ok,
			at+100*time.Millisecond, 10*time.Millisecond,
			map[string]any{"db.system": "postgresql", "server.address": "orders-db"})
		add(traceID, p+"cache", p+"root", "gateway", spans_core.SpanKindClient, ok,
			at+200*time.Millisecond, time.Millisecond,
			map[string]any{"db.system": "redis", "server.address": "cache-1"})
		if i == 0 {
			add(traceID, p+"ext", p+"root", "gateway", spans_core.SpanKindClient, failed,
				at+300*time.Millisecond, 300*time.Millisecond, map[string]any{"server.address": "api.fabrikam.example"})
		}
	}

	require.NoError(t, spans_core.GetSpanCoreRepository().StoreSpansBatch(
		map[uuid.UUID][]*spans_core.Span{project.ID: spanList},
	))
	require.NoError(t, spans_core.GetSpanCoreRepository().ForceFlush())

	return owner, project
}
