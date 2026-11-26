package dashboards_controllers_tests

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	dashboards_dto "obzly/internal/features/dashboards/dto"
	dashboards_models "obzly/internal/features/dashboards/models"
	logs_core "obzly/internal/features/logs/core"
	metrics_testsupport "obzly/internal/features/metrics/testsupport"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func timeRangeCoveringNow() *logs_core.TimeRangeDTO {
	from := time.Now().UTC().Add(-time.Hour)
	to := time.Now().UTC().Add(time.Hour)
	return &logs_core.TimeRangeDTO{From: &from, To: &to}
}

func Test_ExecutePanelQuery_NoGrouping_ReturnsTotalLogCount(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner, project, _ := SetUpProjectWithLogs(t, router, "Panel Total")

	request := dashboards_dto.PanelQueryRequestDTO{
		Panel:     dashboards_models.Panel{Type: dashboards_models.PanelTypeStat},
		TimeRange: timeRangeCoveringNow(),
	}

	var response dashboards_dto.PanelQueryResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/query", project.ID.String()),
		"Bearer "+owner.Token, request, http.StatusOK, &response,
	)

	require.Len(t, response.Buckets, 1)
	// 3 info + 2 error seeded by SetUpProjectWithLogs.
	assert.Equal(t, float64(5), response.Buckets[0].Value)
}

func Test_ExecutePanelQuery_GroupedByLevel_ReturnsBreakdown(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner, project, _ := SetUpProjectWithLogs(t, router, "Panel Breakdown")

	request := dashboards_dto.PanelQueryRequestDTO{
		Panel:     dashboards_models.Panel{Type: dashboards_models.PanelTypePie, GroupField: "level"},
		TimeRange: timeRangeCoveringNow(),
	}

	var response dashboards_dto.PanelQueryResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/query", project.ID.String()),
		"Bearer "+owner.Token, request, http.StatusOK, &response,
	)

	counts := map[string]float64{}
	for _, bucket := range response.Buckets {
		counts[bucket.GroupKey] = bucket.Value
	}
	assert.Equal(t, float64(3), counts["info"], "levels are stored lowercase")
	assert.Equal(t, float64(2), counts["error"])
}

func Test_ExecutePanelQuery_WithQueryFilter_OnlyMatchesFiltered(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner, project, _ := SetUpProjectWithLogs(t, router, "Panel Filtered")

	request := dashboards_dto.PanelQueryRequestDTO{
		Panel: dashboards_models.Panel{
			Type: dashboards_models.PanelTypeStat,
			Query: &logs_core.QueryNode{
				Type: logs_core.QueryNodeTypeCondition,
				Condition: &logs_core.ConditionNode{
					Field:    "level",
					Operator: logs_core.ConditionOperatorEquals,
					Value:    "ERROR",
				},
			},
		},
		TimeRange: timeRangeCoveringNow(),
	}

	var response dashboards_dto.PanelQueryResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/query", project.ID.String()),
		"Bearer "+owner.Token, request, http.StatusOK, &response,
	)

	require.Len(t, response.Buckets, 1)
	assert.Equal(t, float64(2), response.Buckets[0].Value)
}

func Test_ExecutePanelQuery_SpansSource_GroupedByService_ReturnsBreakdown(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner, project, _ := SetUpProjectWithLogs(t, router, "Panel Spans")
	SeedSpans(t, project.ID)

	request := dashboards_dto.PanelQueryRequestDTO{
		Panel: dashboards_models.Panel{
			Type:       dashboards_models.PanelTypeBar,
			Source:     dashboards_models.PanelSourceSpans,
			GroupField: "service_name",
		},
		TimeRange: timeRangeCoveringNow(),
	}

	var response dashboards_dto.PanelQueryResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/query", project.ID.String()),
		"Bearer "+owner.Token, request, http.StatusOK, &response,
	)

	counts := map[string]float64{}
	for _, bucket := range response.Buckets {
		counts[bucket.GroupKey] = bucket.Value
	}
	assert.Equal(t, float64(1), counts["gateway"])
	assert.Equal(t, float64(1), counts["payments"])
}

func Test_ExecutePanelQuery_SpansSource_ErrorCountMetric(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner, project, _ := SetUpProjectWithLogs(t, router, "Panel Spans Errors")
	SeedSpans(t, project.ID)

	request := dashboards_dto.PanelQueryRequestDTO{
		Panel: dashboards_models.Panel{
			Type:   dashboards_models.PanelTypeStat,
			Source: dashboards_models.PanelSourceSpans,
			Metric: "error_count",
		},
		TimeRange: timeRangeCoveringNow(),
	}

	var response dashboards_dto.PanelQueryResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/query", project.ID.String()),
		"Bearer "+owner.Token, request, http.StatusOK, &response,
	)

	require.Len(t, response.Buckets, 1)
	assert.Equal(t, float64(1), response.Buckets[0].Value)
}

func Test_ExecutePanelQuery_SpansSource_InvalidMetric_ReturnsBadRequest(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner, project, _ := SetUpProjectWithLogs(t, router, "Panel Spans Bad Metric")
	SeedSpans(t, project.ID)

	request := dashboards_dto.PanelQueryRequestDTO{
		Panel: dashboards_models.Panel{
			Type:   dashboards_models.PanelTypeStat,
			Source: dashboards_models.PanelSourceSpans,
			Metric: "not-a-real-metric",
		},
		TimeRange: timeRangeCoveringNow(),
	}

	test_utils.MakePostRequest(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/query", project.ID.String()),
		"Bearer "+owner.Token, request, http.StatusBadRequest,
	)
}

func Test_ExecutePanelQuery_MetricsSource_SumByService_ReturnsBreakdown(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner, project, _ := SetUpProjectWithLogs(t, router, "Panel Metrics")
	SeedMetrics(t, project.ID)

	request := dashboards_dto.PanelQueryRequestDTO{
		Panel: dashboards_models.Panel{
			Type:      dashboards_models.PanelTypeLine,
			Source:    dashboards_models.PanelSourceMetrics,
			PromQuery: "sum by (service) (http_requests_total)",
			BucketMs:  60_000,
		},
		TimeRange: timeRangeCoveringNow(),
	}

	var response dashboards_dto.PanelQueryResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/query", project.ID.String()),
		"Bearer "+owner.Token, request, http.StatusOK, &response,
	)

	require.NotEmpty(t, response.Buckets)
	for _, bucket := range response.Buckets {
		assert.Equal(t, "service=api", bucket.GroupKey)
	}
}

func Test_ExecutePanelQuery_MetricsSource_EmptyQuery_ReturnsNoBuckets(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner, project, _ := SetUpProjectWithLogs(t, router, "Panel Metrics Empty")
	SeedMetrics(t, project.ID)

	request := dashboards_dto.PanelQueryRequestDTO{
		Panel: dashboards_models.Panel{
			Type:   dashboards_models.PanelTypeLine,
			Source: dashboards_models.PanelSourceMetrics,
		},
		TimeRange: timeRangeCoveringNow(),
	}

	var response dashboards_dto.PanelQueryResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/query", project.ID.String()),
		"Bearer "+owner.Token, request, http.StatusOK, &response,
	)
	assert.Empty(t, response.Buckets)
}

// A wide range with a small explicit bucketMs is rejected, not handed to the
// PromQL engine.
func Test_ExecutePanelQuery_MetricsSource_ExcessiveResolution_ReturnsBadRequest(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner, project, _ := SetUpProjectWithLogs(t, router, "Panel Metrics Excessive Resolution")
	SeedMetrics(t, project.ID)

	from := time.Now().UTC().Add(-30 * 24 * time.Hour)
	to := time.Now().UTC()
	request := dashboards_dto.PanelQueryRequestDTO{
		Panel: dashboards_models.Panel{
			Type:      dashboards_models.PanelTypeLine,
			Source:    dashboards_models.PanelSourceMetrics,
			PromQuery: "sum(http_requests_total)",
			// 30 days at 1 s is ~2.6 million points; the step floor doesn't
			// catch this.
			BucketMs: 1_000,
		},
		TimeRange: &logs_core.TimeRangeDTO{From: &from, To: &to},
	}

	test_utils.MakePostRequest(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/query", project.ID.String()),
		"Bearer "+owner.Token, request, http.StatusBadRequest,
	)
}

func Test_ExecutePanelQuery_MetricsSource_InvalidSyntax_ReturnsBadRequest(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner, project, _ := SetUpProjectWithLogs(t, router, "Panel Metrics Bad Syntax")

	request := dashboards_dto.PanelQueryRequestDTO{
		Panel: dashboards_models.Panel{
			Type:      dashboards_models.PanelTypeLine,
			Source:    dashboards_models.PanelSourceMetrics,
			PromQuery: "sum(",
		},
		TimeRange: timeRangeCoveringNow(),
	}

	test_utils.MakePostRequest(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/query", project.ID.String()),
		"Bearer "+owner.Token, request, http.StatusBadRequest,
	)
}

func Test_ExecutePanelQuery_NonMember_ReturnsForbidden(t *testing.T) {
	router := CreateDashboardTestRouter()
	_, project, _ := SetUpProjectWithLogs(t, router, "Panel Forbidden")
	outsider := users_testing.CreateTestUser(users_enums.UserRoleMember)

	request := dashboards_dto.PanelQueryRequestDTO{
		Panel:     dashboards_models.Panel{Type: dashboards_models.PanelTypeStat},
		TimeRange: timeRangeCoveringNow(),
	}
	test_utils.MakePostRequest(
		t, router,
		fmt.Sprintf("/api/v1/projects/dashboards/%s/query", project.ID.String()),
		"Bearer "+outsider.Token, request, http.StatusForbidden,
	)
}

func Test_ExecutePanelQuery_LogsPanelWithoutTimeRange_ReturnsBadRequest(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner, project, _ := SetUpProjectWithLogs(t, router, "Panel No Range")
	to := time.Now().UTC()

	for _, timeRange := range []*logs_core.TimeRangeDTO{nil, {To: &to}} {
		request := dashboards_dto.PanelQueryRequestDTO{
			Panel:     dashboards_models.Panel{Type: dashboards_models.PanelTypeBar, BucketMs: 1},
			TimeRange: timeRange,
		}
		test_utils.MakePostRequest(
			t, router,
			fmt.Sprintf("/api/v1/projects/dashboards/%s/query", project.ID.String()),
			"Bearer "+owner.Token, request, http.StatusBadRequest,
		)
	}
}

// A metrics panel whose query fails in storage answers 500 with a fixed
// message: it was a 400 "invalid PromQL query: query fragment failed: ...",
// naming the fragment, the executor and the segment file. A mistake in the
// expression still comes back with its text.
func Test_ExecutePanelQuery_MetricsSource_WhenStorageFails_Returns500WithoutTheCause(t *testing.T) {
	router := CreateDashboardTestRouter()
	owner, project, _ := SetUpProjectWithLogs(t, router, "Panel Metrics Storage Failure")
	url := fmt.Sprintf("/api/v1/projects/dashboards/%s/query", project.ID.String())
	panel := func(promQuery string) dashboards_dto.PanelQueryRequestDTO {
		return dashboards_dto.PanelQueryRequestDTO{
			Panel: dashboards_models.Panel{
				Type: dashboards_models.PanelTypeLine, Source: dashboards_models.PanelSourceMetrics,
				PromQuery: promQuery, BucketMs: 60_000,
			},
			TimeRange: timeRangeCoveringNow(),
		}
	}

	invalid := test_utils.MakePostRequest(t, router, url, "Bearer "+owner.Token,
		panel("sum(rate(http_requests_total[5m"), http.StatusBadRequest)
	assert.Contains(t, string(invalid.Body), "invalid PromQL query")
	assert.Contains(t, string(invalid.Body), "parse error")

	metrics_testsupport.FailSampleReads(t, nil)
	for _, promQuery := range []string{"sum by (service) (rate(http_requests_total[5m]))", "http_requests_total"} {
		failed := test_utils.MakePostRequest(t, router, url, "Bearer "+owner.Token,
			panel(promQuery), http.StatusInternalServerError)

		assert.JSONEq(t, `{"error":"failed to execute panel query"}`, string(failed.Body), promQuery)
	}
}
