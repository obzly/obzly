package ingestion

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	metrics_core "obzly/internal/features/metrics/core"
	projects_controllers "obzly/internal/features/projects/controllers"
	projects_models "obzly/internal/features/projects/models"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
)

const hostMetricsScope = "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/" +
	"hostmetricsreceiver/internal/scraper/cpuscraper"

func Test_GetIngestionStatus_WithSeededLogsSpansAndMetrics_IdentifiesEachSource(t *testing.T) {
	now := time.Now().UTC()
	router, project, ownerToken := newStatusRouter(t, func() time.Time { return now })
	// Two receiving replicas: their counts for the same source add up.
	replicaA, replicaB := newTestTracker(
		func() time.Time { return now },
	), newTestTracker(
		func() time.Time { return now },
	)

	replicaA.ObserveLogs(project.ID, []*logs_core.LogItem{
		logItem(map[string]any{"obzly_source": "edge-proxy", "service_name": "nginx", "host_name": "web-1",
			"k8s_namespace_name": "edge", "k8s_deployment_name": "ingress"}),
		logItem(map[string]any{"k8s_cluster_name": "prd", "k8s_namespace_name": "shop",
			"k8s_deployment_name": "checkout", "k8s_pod_name": "checkout-1", "host_name": "node-1"}),
	})
	replicaB.ObserveLogs(project.ID, []*logs_core.LogItem{
		logItem(map[string]any{"k8s_cluster_name": "prd", "k8s_namespace_name": "shop",
			"k8s_deployment_name": "checkout", "k8s_pod_name": "checkout-2", "host_name": "node-2"}),
	})
	replicaA.ObserveResourceSpans(project.ID, []*tracev1.ResourceSpans{{
		Resource: resource(map[string]string{"service.name": "checkout", "k8s.cluster.name": "prd",
			"k8s.namespace.name": "shop", "k8s.deployment.name": "checkout", "telemetry.sdk.language": "java"}),
		ScopeSpans: []*tracev1.ScopeSpans{{Spans: []*tracev1.Span{{Name: "GET /cart"}, {Name: "SELECT"}}}},
	}})
	replicaA.ObserveResourceMetrics(project.ID, []*metricsv1.ResourceMetrics{
		{
			Resource: resource(map[string]string{"service.name": "billing", "service.namespace": "payments",
				"host.name": "vm-7", "telemetry.sdk.language": "python"}),
			ScopeMetrics: []*metricsv1.ScopeMetrics{
				{Metrics: []*metricsv1.Metric{gauge("http.server.active_requests", 3)}},
			},
		},
		{
			Resource: resource(map[string]string{"host.name": "vm-7", "os.type": "linux"}),
			ScopeMetrics: []*metricsv1.ScopeMetrics{{
				Scope:   &commonv1.InstrumentationScope{Name: hostMetricsScope},
				Metrics: []*metricsv1.Metric{gauge("system.cpu.time", 4)},
			}},
		},
	})
	replicaB.ObserveSamples(project.ID, []*metrics_core.MetricSample{
		{MetricName: "node_cpu_seconds_total", Labels: map[string]string{"job": "node", "instance": "10.0.0.5:9100"}},
		{MetricName: "node_load1", Labels: map[string]string{"job": "node", "instance": "10.0.0.5:9100"}},
	})
	require.NoError(t, replicaA.Flush(context.Background()))
	require.NoError(t, replicaB.Flush(context.Background()))

	var status IngestionStatusDTO
	test_utils.MakeGetRequestAndUnmarshal(t, router, statusURL(project.ID), "Bearer "+ownerToken,
		http.StatusOK, &status)
	byName := map[string]SourceStatusDTO{}
	for _, source := range status.Sources {
		byName[source.Name] = source
		assert.Equal(t, HealthSending, source.Health, source.Name)
	}
	require.Len(t, byName, 5)

	edge := byName["edge-proxy"]
	assert.Equal(t, IdentifiedByExplicit, edge.IdentifiedBy, "obzly_source wins over the workload")
	assert.Equal(t, "nginx", edge.DataSource)
	assert.Equal(t, "web-1", edge.Location.Host)

	checkout := byName["checkout"]
	assert.Equal(t, IdentifiedByWorkload, checkout.IdentifiedBy)
	assert.Equal(t, "lang-java", checkout.DataSource, "the SDK is more specific than the platform")
	assert.Equal(t, SourceLocationDTO{Cluster: "prd", Namespace: "shop", WorkloadKind: "deployment",
		Workload: "checkout", ServiceName: "checkout", SDKLanguage: "java"}, checkout.Location)
	require.Len(t, checkout.Signals, 2)
	assert.Equal(t, SignalLogs, checkout.Signals[0].Signal)
	assert.Equal(t, int64(2), checkout.Signals[0].RecentCount, "both pods, from both replicas")
	assert.InDelta(t, 2.0, checkout.Signals[0].PerMinute, 0.01, "a source first seen now averages over one minute")
	assert.Equal(t, SignalTraces, checkout.Signals[1].Signal)
	assert.Equal(t, int64(2), checkout.Signals[1].RecentCount)

	billing := byName["billing"]
	assert.Equal(t, IdentifiedByService, billing.IdentifiedBy)
	assert.Equal(t, "lang-python", billing.DataSource)
	assert.Equal(t, "vm-7", billing.Location.Host)
	assert.Equal(t, "payments", billing.Location.ServiceNamespace)
	require.Len(t, billing.Signals, 1)
	assert.Equal(t, int64(3), billing.Signals[0].RecentCount, "metric data points")

	host := byName["vm-7"]
	assert.Equal(t, IdentifiedByHost, host.IdentifiedBy)
	assert.Equal(t, "linux", host.DataSource)
	assert.Equal(t, int64(4), host.Signals[0].RecentCount)

	node := byName["node"]
	assert.Equal(t, IdentifiedByService, node.IdentifiedBy, "job and instance stand in for service and host")
	assert.Equal(t, "prometheus", node.DataSource)
	assert.Equal(t, "10.0.0.5:9100", node.Location.Host)
	assert.Equal(t, int64(2), node.Signals[0].RecentCount)

	assert.Equal(t, 15, status.RateWindowMinutes)
	assert.False(t, status.Truncated)
}

func Test_GetIngestionStatus_WithSourcesLastSeenAtDifferentTimes_ReportsSendingQuietAndStopped(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute).Add(30 * time.Second)
	router, project, ownerToken := newStatusRouter(t, func() time.Time { return now })
	seed := func(at time.Time, service string, items int) {
		tracker := newTestTracker(func() time.Time { return at })
		logs := make([]*logs_core.LogItem, items)
		for i := range logs {
			logs[i] = logItem(map[string]any{"service_name": service, "host_name": "app-1"})
		}
		tracker.ObserveLogs(project.ID, logs)
		require.NoError(t, tracker.Flush(context.Background()))
	}
	seed(now.Add(-30*time.Hour), "batch-importer", 5)
	seed(now.Add(-2*time.Hour), "nightly-report", 5)
	seed(now.Add(-14*time.Minute), "api", 30)
	seed(now.Add(-1*time.Minute), "api", 30)

	var status IngestionStatusDTO
	test_utils.MakeGetRequestAndUnmarshal(t, router, statusURL(project.ID), "Bearer "+ownerToken,
		http.StatusOK, &status)
	require.Len(t, status.Sources, 3)
	assert.Equal(t, []string{"api", "nightly-report", "batch-importer"},
		[]string{status.Sources[0].Name, status.Sources[1].Name, status.Sources[2].Name}, "sending, quiet, stopped")

	api := status.Sources[0]
	assert.Equal(t, HealthSending, api.Health)
	assert.Equal(t, int64(60), api.Signals[0].RecentCount)
	assert.InDelta(t, 60.0/14.5, api.Signals[0].PerMinute, 0.1, "averaged since it was first seen")

	quiet := status.Sources[1]
	assert.Equal(t, HealthQuiet, quiet.Health)
	assert.Equal(t, HealthQuiet, quiet.Signals[0].Health)
	assert.Equal(t, int64(0), quiet.Signals[0].RecentCount, "nothing in the rate window")
	assert.WithinDuration(t, now.Add(-2*time.Hour), quiet.LastSeen, time.Second)

	stopped := status.Sources[2]
	assert.Equal(t, HealthStopped, stopped.Health)
	assert.Zero(t, stopped.Signals[0].PerMinute)
}

func Test_GetIngestionStatus_WhenNothingWasSent_ReturnsNoSources(t *testing.T) {
	router, project, ownerToken := newStatusRouter(t, func() time.Time { return time.Now().UTC() })

	var status IngestionStatusDTO
	test_utils.MakeGetRequestAndUnmarshal(t, router, statusURL(project.ID), "Bearer "+ownerToken,
		http.StatusOK, &status)
	assert.NotNil(t, status.Sources)
	assert.Empty(t, status.Sources)
}

func Test_GetIngestionStatus_WithRefusedData_ReportsCountsByReasonAndWhetherRetried(t *testing.T) {
	now := time.Now().UTC()
	router, project, ownerToken := newStatusRouter(t, func() time.Time { return now })
	replicaA, replicaB := newTestTracker(
		func() time.Time { return now },
	), newTestTracker(
		func() time.Time { return now },
	)

	replicaA.Refused(project.ID, SignalLogs, logs_core.ErrorRateLimitExceeded, 1000)
	replicaB.Refused(project.ID, SignalLogs, logs_core.ErrorRateLimitExceeded, 500)
	replicaA.Refused(project.ID, SignalLogs, logs_core.ErrorLogTooLarge, 3)
	replicaA.Refused(project.ID, SignalLogs, "invalid timestamp: 2026-13-45", 2)
	replicaB.Refused(project.ID, SignalTraces, logs_core.ErrorIngestBufferFull, 40)
	require.NoError(t, replicaA.Flush(context.Background()))
	require.NoError(t, replicaB.Flush(context.Background()))

	var status IngestionStatusDTO
	test_utils.MakeGetRequestAndUnmarshal(t, router, statusURL(project.ID), "Bearer "+ownerToken,
		http.StatusOK, &status)

	assert.Equal(t, 24, status.RefusedWindowHours)
	byReason := map[string]RefusedDTO{}
	for _, refused := range status.Refused {
		byReason[refused.Signal+"/"+refused.Reason] = refused
	}
	require.Len(t, byReason, 4)
	assert.Equal(t, status.Refused[0].Reason, logs_core.ErrorRateLimitExceeded, "most refused first")
	assert.EqualValues(t, 1500, byReason["logs/"+logs_core.ErrorRateLimitExceeded].Count, "replicas add up")
	assert.True(t, byReason["logs/"+logs_core.ErrorRateLimitExceeded].Retried)
	assert.False(t, byReason["logs/"+logs_core.ErrorLogTooLarge].Retried, "the sender drops invalid logs")
	assert.EqualValues(t, 2, byReason["logs/"+RefusedOther].Count, "free text becomes OTHER")
	assert.True(t, byReason["traces/"+logs_core.ErrorIngestBufferFull].Retried)
}

func Test_GetIngestionStatus_ByRole_OnlyProjectMembersMayRead(t *testing.T) {
	router, project, ownerToken := newStatusRouter(t, func() time.Time { return time.Now().UTC() })
	viewer := users_testing.CreateTestUser(users_enums.UserRoleMember)
	outsider := users_testing.CreateTestUser(users_enums.UserRoleMember)
	projects_testing.AddMemberToProject(project, viewer, users_enums.ProjectRoleViewer, ownerToken, router)

	tracker := newTestTracker(func() time.Time { return time.Now().UTC() })
	tracker.ObserveLogs(project.ID, []*logs_core.LogItem{logItem(map[string]any{"service_name": "secret-svc"})})
	require.NoError(t, tracker.Flush(context.Background()))

	var status IngestionStatusDTO
	test_utils.MakeGetRequestAndUnmarshal(t, router, statusURL(project.ID), "Bearer "+viewer.Token,
		http.StatusOK, &status)
	require.Len(t, status.Sources, 1, "a viewer may read it")

	response := test_utils.MakeGetRequest(t, router, statusURL(project.ID), "Bearer "+outsider.Token,
		http.StatusForbidden)
	assert.NotContains(t, string(response.Body), "secret-svc")
	test_utils.MakeGetRequest(t, router, statusURL(project.ID), "", http.StatusUnauthorized)
	test_utils.MakeGetRequest(t, router, "/api/v1/projects/not-a-uuid/ingestion-status", "Bearer "+ownerToken,
		http.StatusBadRequest)
}

func newStatusRouter(t *testing.T, now func() time.Time) (*gin.Engine, *projects_models.Project, string) {
	t.Helper()
	testService := &Service{repo, now, &statusCache{entries: map[uuid.UUID]*IngestionStatusDTO{}}}
	router := projects_testing.CreateTestRouter(projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController(), &Controller{testService})
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, token := projects_testing.CreateTestProjectWithToken("Ingestion "+uuid.NewString()[:8], owner.Token,
		router)
	return router, project, token
}

func newTestTracker(now func() time.Time) *Tracker {
	return &Tracker{repo: repo, logger: slog.Default(), now: now}
}

func statusURL(projectID uuid.UUID) string {
	return "/api/v1/projects/" + projectID.String() + "/ingestion-status"
}

func logItem(fields map[string]any) *logs_core.LogItem {
	return &logs_core.LogItem{ID: uuid.New(), Timestamp: time.Now().UTC(), Message: "hello", Fields: fields}
}

func resource(attributes map[string]string) *resourcev1.Resource {
	result := &resourcev1.Resource{}
	for key, value := range attributes {
		result.Attributes = append(result.Attributes, &commonv1.KeyValue{
			Key: key, Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: value}},
		})
	}
	return result
}

func gauge(name string, points int) *metricsv1.Metric {
	dataPoints := make([]*metricsv1.NumberDataPoint, points)
	for i := range dataPoints {
		dataPoints[i] = &metricsv1.NumberDataPoint{Value: &metricsv1.NumberDataPoint_AsDouble{AsDouble: 1}}
	}
	return &metricsv1.Metric{Name: name, Data: &metricsv1.Metric_Gauge{Gauge: &metricsv1.Gauge{DataPoints: dataPoints}}}
}
