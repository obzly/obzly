package otlp_metrics_tests

import (
	"time"

	api_keys "obzly/internal/features/api_keys"
	otlp_metrics "obzly/internal/features/otlp/metrics"
	projects_controllers "obzly/internal/features/projects/controllers"
	projects_testing "obzly/internal/features/projects/testing"
	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_middleware "obzly/internal/features/users/middleware"
	users_services "obzly/internal/features/users/services"
	users_testing "obzly/internal/features/users/testing"

	"github.com/gin-gonic/gin"

	collectormetricsv1 "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
)

// CreateOtlpTestRouter mounts the OTLP HTTP receiver and the controllers
// fixtures need.
func CreateOtlpTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	otlp_metrics.GetHTTPController().RegisterRoutes(router)

	v1 := router.Group("/api/v1")
	protected := v1.Group("").Use(users_middleware.AuthMiddleware(users_services.GetUserService()))

	if routerGroup, ok := protected.(*gin.RouterGroup); ok {
		projects_controllers.GetProjectController().RegisterRoutes(routerGroup)
		projects_controllers.GetMembershipController().RegisterRoutes(routerGroup)
		api_keys.GetApiKeyController().RegisterRoutes(routerGroup)
	}

	return router
}

type TestFixture struct {
	Router    *gin.Engine
	User      *users_dto.SignInResponseDTO
	ApiKey    *api_keys.ApiKey
	ProjectID string
}

func SetUpFixture(name string) *TestFixture {
	router := CreateOtlpTestRouter()
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project := projects_testing.CreateTestProject(name, user, router)
	apiKey := api_keys.CreateTestApiKey("OTLP Metrics Test Key", project.ID, user.Token, router)

	return &TestFixture{
		Router:    router,
		User:      user,
		ApiKey:    apiKey,
		ProjectID: project.ID.String(),
	}
}

// BuildSumExportRequest is one monotonic Sum point with the given temporality.
func BuildSumExportRequest(
	metricName string,
	temporality metricsv1.AggregationTemporality,
	at time.Time,
	value float64,
) *collectormetricsv1.ExportMetricsServiceRequest {
	return singleMetricRequest(&metricsv1.Metric{Name: metricName, Data: &metricsv1.Metric_Sum{Sum: &metricsv1.Sum{
		AggregationTemporality: temporality,
		IsMonotonic:            true,
		DataPoints: []*metricsv1.NumberDataPoint{{
			TimeUnixNano: uint64(at.UnixNano()),
			Value:        &metricsv1.NumberDataPoint_AsDouble{AsDouble: value},
		}},
	}}})
}

// BuildHistogramExportRequest is one Histogram point with bounds 10 and 50;
// buckets holds the three per-bucket counts.
func BuildHistogramExportRequest(
	metricName string,
	temporality metricsv1.AggregationTemporality,
	at time.Time,
	count uint64,
	sum float64,
	buckets []uint64,
) *collectormetricsv1.ExportMetricsServiceRequest {
	return singleMetricRequest(&metricsv1.Metric{Name: metricName, Data: &metricsv1.Metric_Histogram{
		Histogram: &metricsv1.Histogram{
			AggregationTemporality: temporality,
			DataPoints: []*metricsv1.HistogramDataPoint{{
				TimeUnixNano:   uint64(at.UnixNano()),
				Count:          count,
				Sum:            &sum,
				ExplicitBounds: []float64{10, 50},
				BucketCounts:   buckets,
			}},
		},
	}})
}

// BuildGaugeExportRequest builds a minimal valid export with one gauge metric
// and two data points.
func BuildGaugeExportRequest(metricName string) *collectormetricsv1.ExportMetricsServiceRequest {
	now := time.Now().UTC()
	return &collectormetricsv1.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricsv1.ResourceMetrics{
			{
				Resource: &resourcev1.Resource{
					Attributes: []*commonv1.KeyValue{
						{
							Key: "service.name",
							Value: &commonv1.AnyValue{
								Value: &commonv1.AnyValue_StringValue{StringValue: "test-service"},
							},
						},
					},
				},
				ScopeMetrics: []*metricsv1.ScopeMetrics{
					{
						Metrics: []*metricsv1.Metric{
							{
								Name: metricName,
								Data: &metricsv1.Metric_Gauge{
									Gauge: &metricsv1.Gauge{
										DataPoints: []*metricsv1.NumberDataPoint{
											{
												TimeUnixNano: uint64(now.UnixNano()),
												Value:        &metricsv1.NumberDataPoint_AsDouble{AsDouble: 42.5},
												Attributes: []*commonv1.KeyValue{
													{
														Key: "region",
														Value: &commonv1.AnyValue{
															Value: &commonv1.AnyValue_StringValue{
																StringValue: "us-east",
															},
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func singleMetricRequest(metric *metricsv1.Metric) *collectormetricsv1.ExportMetricsServiceRequest {
	return &collectormetricsv1.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricsv1.ResourceMetrics{{
			ScopeMetrics: []*metricsv1.ScopeMetrics{{Metrics: []*metricsv1.Metric{metric}}},
		}},
	}
}
