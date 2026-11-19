package logs_querying_tests

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	logs_receiving "obzly/internal/features/logs/receiving"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// insightLogs builds test logs for the Logs insight tabs.
type insightLogs struct {
	uniqueID string
	items    []logs_receiving.LogItemRequestDTO
	recent   int
}

// The Patterns tab: per-pattern counts and series, each pattern's level,
// services and sample, and new / spiking / dropping against 24 h earlier.
func Test_PatternTrends_ReturnsSeriesAndChangesAgainstYesterday(t *testing.T) {
	router, owner, project, uniqueID := SetupBasicQueryTest(t, "Pattern trends")
	// Aligned to the buckets, so the hour is exactly six of them.
	now := time.Now().UTC().Truncate(10 * time.Minute)
	from := now.Add(-time.Hour)
	logs := &insightLogs{uniqueID: uniqueID}
	logs.add(from, 8, logs_core.LogLevelError, "payment %d failed for order", map[string]any{"service_name": "checkout"})
	logs.add(from, 6, logs_core.LogLevelInfo, "request %d served", map[string]any{"service_name": "web"})
	logs.add(from.Add(-24*time.Hour), 6, logs_core.LogLevelInfo, "request %d served", map[string]any{"service_name": "web"})
	logs.add(from.Add(-24*time.Hour), 10, logs_core.LogLevelInfo, "cache %d warmed", map[string]any{"service_name": "catalog"})
	logs.add(from.Add(-24*time.Hour), 1, logs_core.LogLevelWarn, "retry %d scheduled", map[string]any{"service_name": "payments"})
	logs.add(from, 6, logs_core.LogLevelWarn, "retry %d scheduled", map[string]any{"service_name": "payments"})
	logs.submit(t, router, project.ID, owner.Token)

	var response logs_core.PatternTrendsResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, insightURL("pattern-trends", project.ID), "Bearer "+owner.Token,
		logs_core.PatternTrendsRequestDTO{
			Query:     BuildCondition("test_id", "equals", uniqueID),
			TimeRange: &logs_core.TimeRangeDTO{From: &from, To: &now},
			BucketMs:  (10 * time.Minute).Milliseconds(),
		}, http.StatusOK, &response)

	assert.Equal(t, int64(20), response.Total)
	assert.Equal(t, 3, response.Distinct)
	assert.Equal(t, 1, response.NewCount, "payment")
	assert.Equal(t, 1, response.SpikeCount, "retry")
	assert.Equal(t, 1, response.DropCount, "cache")
	assert.Len(t, response.Buckets, 6)
	require.Len(t, response.Patterns, 4)
	byHash := map[string]logs_core.PatternTrendDTO{}
	for _, pattern := range response.Patterns {
		byHash[pattern.Hash] = pattern
		assert.Len(t, pattern.Series, len(response.Buckets))
	}
	payment := byHash[logs_core.PatternHash("payment 1 failed for order")]
	assert.Equal(t, logs_core.PatternChangeNew, payment.Change)
	assert.Equal(t, int64(8), payment.Count)
	assert.Equal(t, int64(8), sum(payment.Series))
	assert.Equal(t, "error", payment.Level)
	assert.Equal(t, []string{"checkout"}, payment.Services)
	assert.Contains(t, payment.Sample, "failed for order")
	assert.NotEmpty(t, payment.Pattern)
	cache := byHash[logs_core.PatternHash("cache 1 warmed")]
	assert.Equal(t, logs_core.PatternChangeDrop, cache.Change)
	assert.Equal(t, int64(10), cache.Yesterday)
	assert.Contains(t, cache.Sample, "warmed", "a dropped pattern is described from 24 h earlier")
	assert.Equal(t, logs_core.PatternChangeSpike, byHash[logs_core.PatternHash("retry 1 scheduled")].Change)
	assert.Equal(t, "", byHash[logs_core.PatternHash("request 1 served")].Change)
	assert.Equal(t, int64(0), sum(response.Other), "every pattern is listed")
}

func Test_PatternTrends_WithoutFrom_ReturnsBadRequest(t *testing.T) {
	router, owner, project, _ := SetupBasicQueryTest(t, "Pattern trends without from")
	now := time.Now().UTC()
	test_utils.MakePostRequest(t, router, insightURL("pattern-trends", project.ID), "Bearer "+owner.Token,
		logs_core.PatternTrendsRequestDTO{TimeRange: &logs_core.TimeRangeDTO{To: &now}}, http.StatusBadRequest)
}

// Explain this spike: the values whose share of the lines grew, with the
// ReplicaSet derived from the pod name; values whose share fell are left out.
func Test_Explain_ListsValuesOverRepresentedInTheRange(t *testing.T) {
	router, owner, project, uniqueID := SetupBasicQueryTest(t, "Explain spike")
	now := time.Now().UTC().Truncate(time.Minute)
	baselineFrom, currentFrom := now.Add(-time.Hour), now.Add(-30*time.Minute)
	logs := &insightLogs{uniqueID: uniqueID}
	logs.add(baselineFrom, 25, logs_core.LogLevelInfo, "request %d served", map[string]any{"service_name": "web"})
	logs.add(currentFrom, 25, logs_core.LogLevelInfo, "request %d served", map[string]any{"service_name": "web"})
	logs.add(currentFrom, 25, logs_core.LogLevelError, "upstream timeout after %d ms", map[string]any{
		"service_name": "payments-api", "k8s_namespace_name": "payments",
		"k8s_pod_name": "payments-api-5c8b6d7f9-m4tqz",
	})
	logs.submit(t, router, project.ID, owner.Token)

	var response logs_core.ExplainResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, insightURL("explain", project.ID), "Bearer "+owner.Token,
		logs_core.ExplainRequestDTO{
			Query:    BuildCondition("test_id", "equals", uniqueID),
			Current:  &logs_core.TimeRangeDTO{From: &currentFrom, To: new(now.Add(-time.Nanosecond))},
			Baseline: &logs_core.TimeRangeDTO{From: &baselineFrom, To: new(currentFrom.Add(-time.Nanosecond))},
		}, http.StatusOK, &response)

	assert.Equal(t, int64(50), response.CurrentTotal)
	assert.Equal(t, int64(25), response.BaselineTotal)
	found := map[string]logs_core.ExplainFieldDTO{}
	for _, field := range response.Fields {
		found[field.Field+"="+field.Value] = field
	}
	for _, key := range []string{
		"service_name=payments-api", "level=error", "k8s_namespace_name=payments",
		"k8s_pod_name=payments-api-5c8b6d7f9-m4tqz", "k8s_replicaset=payments-api-5c8b6d7f9",
	} {
		require.Contains(t, found, key)
		assert.Equal(t, int64(25), found[key].Current, key)
		assert.Equal(t, int64(0), found[key].Baseline, key)
		assert.InDelta(t, 0.5, found[key].CurrentShare, 0.001, key)
	}
	assert.NotContains(t, found, "service_name=web", "its share fell")
}

func Test_Explain_WhenUserIsNotProjectMember_ReturnsForbidden(t *testing.T) {
	router, _, project, _ := SetupBasicQueryTest(t, "Explain access")
	nonMember := users_testing.CreateTestUser(users_enums.UserRoleMember)
	now := time.Now().UTC()
	test_utils.MakePostRequest(t, router, insightURL("explain", project.ID), "Bearer "+nonMember.Token,
		logs_core.ExplainRequestDTO{
			Current:  &logs_core.TimeRangeDTO{From: new(now.Add(-time.Hour)), To: &now},
			Baseline: &logs_core.TimeRangeDTO{From: new(now.Add(-2 * time.Hour)), To: new(now.Add(-time.Hour))},
		}, http.StatusForbidden)
}

// Volume & noise: lines per day by service, by level, and the biggest
// patterns per service and level with their template.
func Test_Volume_ReturnsServicesLevelsAndNoisiestPatterns(t *testing.T) {
	router, owner, project, uniqueID := SetupBasicQueryTest(t, "Log volume")
	now := time.Now().UTC().Truncate(time.Minute)
	logs := &insightLogs{uniqueID: uniqueID}
	logs.add(now.Add(-40*time.Minute), 30, logs_core.LogLevelInfo, "GET /static/app-%d.js 200", map[string]any{"service_name": "web"})
	logs.add(now.Add(-40*time.Minute), 6, logs_core.LogLevelError, "payment %d failed", map[string]any{"service_name": "checkout"})
	logs.submit(t, router, project.ID, owner.Token)

	var response logs_core.VolumeResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, insightURL("volume", project.ID), "Bearer "+owner.Token,
		logs_core.VolumeRequestDTO{Days: 7}, http.StatusOK, &response)

	assert.Len(t, response.Days, 7)
	totals := map[string]int64{}
	for _, service := range response.Services {
		totals[service.Name] = service.Total
		assert.Len(t, service.Counts, 7)
		assert.Equal(t, service.Total, sum(service.Counts))
	}
	assert.Equal(t, map[string]int64{"web": 30, "checkout": 6}, totals)
	assert.Equal(t, []logs_core.VolumeCountDTO{{Name: "info", Count: 30}, {Name: "error", Count: 6}}, response.Levels)
	require.Len(t, response.Noisy, 2)
	assert.Equal(t, "web", response.Noisy[0].Service)
	assert.Equal(t, "info", response.Noisy[0].Level)
	assert.Equal(t, int64(30), response.Noisy[0].Count)
	assert.Equal(t, logs_core.PatternHash("GET /static/app-1.js 200"), response.Noisy[0].Hash)
	assert.Contains(t, response.Noisy[0].Sample, "/static/")
	assert.Equal(t, "checkout", response.Noisy[1].Service)
}

func Test_Volume_WhenUserIsNotProjectMember_ReturnsForbidden(t *testing.T) {
	router, _, project, _ := SetupBasicQueryTest(t, "Volume access")
	nonMember := users_testing.CreateTestUser(users_enums.UserRoleMember)
	test_utils.MakePostRequest(t, router, insightURL("volume", project.ID), "Bearer "+nonMember.Token,
		logs_core.VolumeRequestDTO{}, http.StatusForbidden)
}

// Health map: workloads from pod names under their namespace with their
// error lines; by service, one group; by host, the host_name field.
func Test_HealthMap_GroupsWorkloadsByNamespaceWithErrorLines(t *testing.T) {
	router, owner, project, uniqueID := SetupBasicQueryTest(t, "Health map")
	now := time.Now().UTC().Truncate(time.Minute)
	from := now.Add(-time.Hour)
	logs := &insightLogs{uniqueID: uniqueID}
	checkout := map[string]any{
		"service_name": "checkout", "k8s_namespace_name": "shop", "k8s_pod_name": "checkout-7d4b9c8f6-x2kqz",
		"host_name": "node-a",
	}
	logs.add(from, 6, logs_core.LogLevelInfo, "order %d confirmed", checkout)
	logs.add(from, 4, logs_core.LogLevelError, "order %d failed", checkout)
	logs.add(from, 2, logs_core.LogLevelWarn, "order %d slow", checkout)
	logs.add(from, 5, logs_core.LogLevelError, "upstream timeout after %d ms", map[string]any{
		"service_name": "payments-api", "k8s_namespace_name": "payments",
		"k8s_pod_name": "payments-api-5c8b6d7f9-m4tqz", "host_name": "node-b",
	})
	logs.submit(t, router, project.ID, owner.Token)
	request := logs_core.HealthMapRequestDTO{
		Query:     BuildCondition("test_id", "equals", uniqueID),
		TimeRange: &logs_core.TimeRangeDTO{From: &from, To: &now},
		GroupBy:   logs_core.HealthMapByKubernetes,
	}

	var byNamespace logs_core.HealthMapResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, insightURL("health-map", project.ID), "Bearer "+owner.Token,
		request, http.StatusOK, &byNamespace)
	require.Len(t, byNamespace.Groups, 2)
	assert.Equal(t, "shop", byNamespace.Groups[0].Name)
	assert.Equal(t, []logs_core.HealthTileDTO{{Name: "checkout", Total: 12, Errors: 4, Warnings: 2}},
		byNamespace.Groups[0].Tiles)
	assert.Equal(t, "payments", byNamespace.Groups[1].Name)
	assert.Equal(t, []logs_core.HealthTileDTO{{Name: "payments-api", Total: 5, Errors: 5}},
		byNamespace.Groups[1].Tiles)

	request.GroupBy = logs_core.HealthMapByService
	var byService logs_core.HealthMapResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, insightURL("health-map", project.ID), "Bearer "+owner.Token,
		request, http.StatusOK, &byService)
	require.Len(t, byService.Groups, 1)
	assert.Len(t, byService.Groups[0].Tiles, 2)

	request.GroupBy = logs_core.HealthMapByHost
	var byHost logs_core.HealthMapResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, insightURL("health-map", project.ID), "Bearer "+owner.Token,
		request, http.StatusOK, &byHost)
	require.Len(t, byHost.Groups, 2)
	assert.Equal(t, "node-a", byHost.Groups[0].Name)
	assert.Equal(t, "checkout", byHost.Groups[0].Tiles[0].Name)

	request.GroupBy = "cluster"
	test_utils.MakePostRequest(t, router, insightURL("health-map", project.ID), "Bearer "+owner.Token,
		request, http.StatusBadRequest)
}

func Test_HealthMap_WhenUserIsNotProjectMember_ReturnsForbidden(t *testing.T) {
	router, _, project, _ := SetupBasicQueryTest(t, "Health map access")
	nonMember := users_testing.CreateTestUser(users_enums.UserRoleMember)
	now := time.Now().UTC()
	test_utils.MakePostRequest(t, router, insightURL("health-map", project.ID), "Bearer "+nonMember.Token,
		logs_core.HealthMapRequestDTO{
			TimeRange: &logs_core.TimeRangeDTO{From: new(now.Add(-time.Hour)), To: &now},
			GroupBy:   logs_core.HealthMapByService,
		}, http.StatusForbidden)
}

// add appends n logs one minute apart from from+1m, message formatted with
// their index.
func (l *insightLogs) add(
	from time.Time, n int, level logs_core.LogLevel, format string, fields map[string]any,
) {
	for i := range n {
		at := from.Add(time.Duration(i%25+1) * time.Minute)
		withID := map[string]any{"test_id": l.uniqueID}
		for key, value := range fields {
			withID[key] = value
		}
		l.items = append(l.items, logs_receiving.LogItemRequestDTO{
			Level: level, Message: fmt.Sprintf(format, i+1), Timestamp: at.Format(time.RFC3339Nano), Fields: withID,
		})
		if time.Since(at) < 2*time.Hour {
			l.recent++
		}
	}
}

// submit sends the logs in batches the receiver accepts and waits until the
// recent ones can be searched (the older ones are flushed with them).
func (l *insightLogs) submit(t *testing.T, router *gin.Engine, projectID uuid.UUID, token string) {
	for start := 0; start < len(l.items); start += logs_receiving.MaxBatchSize {
		batch := l.items[start:min(start+logs_receiving.MaxBatchSize, len(l.items))]
		test_utils.MakePostRequest(t, router, fmt.Sprintf("/api/v1/logs/receiving/%s", projectID.String()), "",
			&logs_receiving.SubmitLogsRequestDTO{Logs: batch}, http.StatusAccepted)
	}
	WaitForLogsToBeIndexed(t, router, projectID, min(l.recent, 50), l.uniqueID, "Bearer "+token)
}

func insightURL(route string, projectID uuid.UUID) string {
	return "/api/v1/logs/query/" + route + "/" + projectID.String()
}

func sum(values []int64) int64 {
	var total int64
	for _, value := range values {
		total += value
	}
	return total
}
