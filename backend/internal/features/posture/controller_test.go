package posture

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"obzly/internal/features/alerting"
	"obzly/internal/features/api_keys"
	projects_controllers "obzly/internal/features/projects/controllers"
	projects_testing "obzly/internal/features/projects/testing"
	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_middleware "obzly/internal/features/users/middleware"
	users_services "obzly/internal/features/users/services"
	users_testing "obzly/internal/features/users/testing"
	"obzly/internal/synthetics/spec"
	test_utils "obzly/internal/util/testing"
)

var testNow = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

const (
	bucketA  = "arn:aws:s3:::contoso-assets"
	instance = "arn:aws:ec2:eu-west-1:111122223333:instance/i-0a1b2c3d4e5f60001"
)

type fixture struct {
	router     *gin.Engine
	user       *users_dto.SignInResponseDTO
	auth       string
	project    uuid.UUID
	apiKey     string
	service    *Service
	connection uuid.UUID
}

func Test_PostureBatches_OpenResolveAndReopenFindings_OnlyWhereAScopeWasReadAgain(t *testing.T) {
	f := newFixture(t, "Posture lifecycle")
	first := f.batch(testNow.Add(-10*time.Hour),
		[]spec.PostureRun{nativeRun("eu-west-1", []string{"IAM.9"}, []string{"S3.8", "EC2.8"})},
		finding("S3.8", bucketA, spec.SeverityHigh), finding("EC2.8", instance, spec.SeverityHigh))
	f.sendChunked(t, first)

	summary := f.summary(t)
	assert.Equal(t, map[string]int{"high": 2}, summary.Open)
	fsbp := score(summary, "aws-fsbp")
	assert.Equal(t, 3, fsbp.Evaluated)
	assert.Equal(t, 1, fsbp.Passed)
	assert.InDelta(t, 33.3, fsbp.Percent, 0.05)
	assert.Equal(t, []string{"native"}, fsbp.Sources)

	f.send(t, f.batch(testNow.Add(-8*time.Hour),
		[]spec.PostureRun{nativeRun("eu-west-1", []string{"IAM.9", "EC2.8"}, []string{"S3.8"})},
		finding("S3.8", bucketA, spec.SeverityHigh)))
	ec2 := f.find(t, "EC2.8")
	assert.Equal(t, StatusResolved, ec2.Status, "read again without it")

	f.send(t, f.batch(testNow.Add(-6*time.Hour),
		[]spec.PostureRun{nativeRun("eu-west-1", []string{"IAM.9"}, []string{"S3.8", "EC2.8"})},
		finding("S3.8", bucketA, spec.SeverityHigh), finding("EC2.8", instance, spec.SeverityHigh)))
	detail := f.detail(t, f.find(t, "EC2.8").ID)
	assert.Equal(t, StatusOpen, detail.Status)
	assert.Equal(t, []string{EventReopened, EventResolved, EventOpened}, kinds(detail.Events))

	notRead := spec.PostureRun{
		Account:  "111122223333",
		Region:   "eu-west-1",
		Standard: "aws-fsbp",
		Source:   spec.PostureSourceNative,
		Result:   spec.PostureRunNeedsPermission,
		Reason:   "securityhub:GetFindings denied",
	}
	f.send(t, f.batch(testNow.Add(-4*time.Hour), []spec.PostureRun{notRead}))
	assert.Equal(t, StatusOpen, f.find(t, "S3.8").Status, "a scope that wasn't read resolves nothing")

	notEnabled := notRead
	notEnabled.Result, notEnabled.Reason = spec.PostureRunNotEnabled, "Security Hub not enabled"
	fallback := spec.PostureRun{Account: "111122223333", Region: "eu-west-1", Standard: "aws-fsbp",
		Source: spec.PostureSourceBaseline, Result: spec.PostureRunOK, Passed: []string{"S3.8"}}
	f.send(t, f.batch(testNow.Add(-2*time.Hour), []spec.PostureRun{notEnabled, fallback}))
	assert.Equal(t, StatusResolved, f.find(t, "S3.8").Status, "the baseline evaluated S3.8 and found it fixed")
	assert.Equal(t, StatusOpen, f.find(t, "EC2.8").Status, "the baseline didn't evaluate EC2.8: unknown, kept")

	var coverage []CoverageRow
	test_utils.MakeGetRequestAndUnmarshal(t, f.router, "/api/v1/posture/coverage", f.auth, http.StatusOK, &coverage)
	require.Len(t, coverage, 1)
	assert.Equal(t, CoverageFallback, coverage[0].Status)
	assert.Equal(t, "Security Hub not enabled", coverage[0].Reason)

	var trends TrendsResponse
	test_utils.MakePostRequestAndUnmarshal(t, f.router, "/api/v1/posture/trends", f.auth,
		TrendsRequest{From: "2026-11-08", To: "2026-11-10", Granularity: "daily"}, http.StatusOK, &trends)
	require.Len(t, trends.Periods, 3)
	today := trends.Periods[2]
	assert.Equal(t, map[string]int{"high": 1}, today.Open)
	assert.Equal(t, 3, today.Opened, "two opened and one reopened")
	assert.Equal(t, 2, today.Resolved)
	assert.Empty(t, trends.Periods[0].Open)
	assert.Contains(t, today.Scores, "aws-fsbp")

	var resource []Finding
	test_utils.MakeGetRequestAndUnmarshal(t, f.router, "/api/v1/posture/resource?key="+instance, f.auth,
		http.StatusOK, &resource)
	require.Len(t, resource, 1)
	assert.Equal(t, "EC2.8", resource[0].Control)
}

func Test_SuppressingAFinding_NeedsAReason_LeavesScoresAndAlerts_AndExpires(t *testing.T) {
	f := newFixture(t, "Posture suppression")
	f.send(t, f.batch(testNow.Add(-time.Hour),
		[]spec.PostureRun{nativeRun("eu-west-1", nil, []string{"S3.8"})},
		finding("S3.8", bucketA, spec.SeverityCritical)))
	open := f.find(t, "S3.8")

	rule := &alerting.AlertRule{ProjectID: f.project, Source: alerting.SourceCloudPosture,
		Query: alerting.RuleQuery{MinSeverity: spec.SeverityHigh}}
	samples, err := f.service.EvaluateRule(context.Background(), rule, testNow)
	require.NoError(t, err)
	require.Len(t, samples, 1)
	assert.Equal(t, bucketA, samples[0].Labels["resource"])
	later, err := f.service.EvaluateRule(context.Background(), rule, testNow.Add(3*time.Hour))
	require.NoError(t, err)
	assert.Empty(t, later, "a finding alerts when it opens, not forever")

	path := "/api/v1/posture/findings/" + open.ID.String()
	f.patch(t, path, f.auth, FindingUpdate{Suppressed: true}, http.StatusBadRequest)
	outsider := users_testing.CreateTestUser(users_enums.UserRoleMember)
	f.patch(t, path, "Bearer "+outsider.Token, FindingUpdate{Suppressed: true, Reason: "mine now"},
		http.StatusForbidden)
	test_utils.MakeGetRequest(t, f.router, path, "Bearer "+outsider.Token, http.StatusNotFound)

	until := testNow.Add(24 * time.Hour).Format(time.RFC3339)
	var suppressed Finding
	answer := f.patch(t, path, f.auth,
		FindingUpdate{Suppressed: true, Reason: "Public website bucket, approved by security", Until: until},
		http.StatusOK)
	require.NoError(t, json.Unmarshal(answer.Body, &suppressed))
	assert.True(t, suppressed.Suppressed)

	summary := f.summary(t)
	assert.Empty(t, summary.Open)
	assert.Equal(t, 1, summary.Suppressed)
	assert.Equal(t, 100.0, score(summary, "aws-fsbp").Percent, "a suppressed finding's control counts as passed")
	samples, err = f.service.EvaluateRule(context.Background(), rule, testNow)
	require.NoError(t, err)
	assert.Empty(t, samples)
	var listed FindingsResponse
	test_utils.MakePostRequestAndUnmarshal(t, f.router, "/api/v1/posture/findings", f.auth,
		FindingsRequest{Filters: Filters{Status: "suppressed"}}, http.StatusOK, &listed)
	assert.Equal(t, int64(1), listed.Total)

	require.NoError(t, expireSuppressions(context.Background(), testNow.Add(25*time.Hour)))
	detail := f.detail(t, open.ID)
	assert.False(t, detail.Suppressed)
	assert.Equal(t, "suppression expired", detail.Events[0].Note)

	test_utils.MakePutRequest(t, f.router, fmt.Sprintf("/api/v1/projects/%s/posture-alerts", f.project), f.auth,
		AlertSettingsDTO{MinSeverity: "informational"}, http.StatusBadRequest)
	var settings AlertSettingsDTO
	test_utils.MakePutRequestAndUnmarshal(t, f.router, fmt.Sprintf("/api/v1/projects/%s/posture-alerts", f.project),
		f.auth, AlertSettingsDTO{MinSeverity: spec.SeverityCritical}, http.StatusOK, &settings)
	assert.Equal(t, spec.SeverityCritical, settings.MinSeverity)
}

func Test_PostureFacets_FollowTheOtherFilters_AndBadBatchesAreRefused(t *testing.T) {
	f := newFixture(t, "Posture facets")
	azure := finding("azure-storage-min-tls", "/subscriptions/sub/resourcegroups/web/providers/microsoft.storage/x",
		spec.SeverityMedium)
	azure.Standards = []spec.StandardRef{{Standard: "mcsb", Control: "DP-3"}}
	f.send(t, f.batch(testNow.Add(-time.Hour), []spec.PostureRun{nativeRun("eu-west-1", nil, []string{"S3.8"})},
		finding("S3.8", bucketA, spec.SeverityHigh), azure))

	var facets FacetsResponse
	test_utils.MakePostRequestAndUnmarshal(t, f.router, "/api/v1/posture/facets", f.auth,
		Filters{Standard: []string{"mcsb"}}, http.StatusOK, &facets)
	assert.Equal(t, []FacetValue{{Value: "medium", Count: 1}}, facets.Dimensions["severity"])
	assert.Len(t, facets.Dimensions["standard"], 2, "a filter's own values aren't narrowed by it")

	bad := f.batch(testNow, nil, spec.PostureFinding{Control: "", ResourceKey: bucketA})
	f.post(t, f.apiKey, bad, http.StatusBadRequest)
	f.post(t, "not-a-key", f.batch(testNow, nil), http.StatusUnauthorized)
}

func newFixture(t *testing.T, name string) *fixture {
	t.Helper()
	service := &Service{slog.Default(), func() time.Time { return testNow }}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	controller := &Controller{service}
	controller.RegisterReceiver(router)
	protected := router.Group("/api/v1").Use(users_middleware.AuthMiddleware(users_services.GetUserService()))
	group := protected.(*gin.RouterGroup)
	projects_controllers.GetProjectController().RegisterRoutes(group)
	api_keys.GetApiKeyController().RegisterRoutes(group)
	controller.RegisterRoutes(group)
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project := projects_testing.CreateTestProject(name, user, router)
	key := api_keys.CreateTestApiKey("Cloud posture", project.ID, user.Token, router)
	return &fixture{router: router, user: user, auth: "Bearer " + user.Token, project: project.ID, apiKey: key.Token,
		service: service, connection: uuid.New()}
}

func (f *fixture) batch(at time.Time, runs []spec.PostureRun, findings ...spec.PostureFinding) spec.PostureBatch {
	return spec.PostureBatch{BatchID: uuid.NewString(), Seq: 1, Total: 1, ConnectionID: f.connection.String(),
		ConnectionName: "Contoso-Prod", Provider: spec.ProviderAWS, ScannedAt: at, Runs: runs, Findings: findings}
}

func (f *fixture) send(t *testing.T, batch spec.PostureBatch) {
	t.Helper()
	answer := f.post(t, f.apiKey, batch, http.StatusOK)
	require.Equal(t, true, answer["applied"])
}

// sendChunked sends the runs with the first chunk and each finding in a chunk of its own.
func (f *fixture) sendChunked(t *testing.T, batch spec.PostureBatch) {
	t.Helper()
	total := len(batch.Findings)
	for i, item := range batch.Findings {
		chunk := batch
		chunk.Seq, chunk.Total, chunk.Findings = i+1, total, []spec.PostureFinding{item}
		if i > 0 {
			chunk.Runs = nil
		}
		answer := f.post(t, f.apiKey, chunk, http.StatusOK)
		assert.Equal(t, i == total-1, answer["applied"], "applies once complete")
	}
}

func (f *fixture) post(t *testing.T, apiKey string, batch spec.PostureBatch, status int) map[string]any {
	t.Helper()
	var body bytes.Buffer
	writer := gzip.NewWriter(&body)
	require.NoError(t, json.NewEncoder(writer).Encode(batch))
	require.NoError(t, writer.Close())
	request := httptest.NewRequest(http.MethodPost, "/v1/cloud/posture", &body)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Encoding", "gzip")
	request.Header.Set("X-API-Key", apiKey)
	recorder := httptest.NewRecorder()
	f.router.ServeHTTP(recorder, request)
	require.Equal(t, status, recorder.Code, recorder.Body.String())
	var answer map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &answer)
	return answer
}

func (f *fixture) patch(t *testing.T, path, auth string, body any, status int) *test_utils.TestResponse {
	t.Helper()
	return test_utils.MakeRequest(t, f.router, test_utils.RequestOptions{Method: http.MethodPatch, URL: path,
		Body: body, AuthToken: auth, ExpectedStatus: status})
}

func (f *fixture) summary(t *testing.T) SummaryDTO {
	t.Helper()
	var summary SummaryDTO
	test_utils.MakeGetRequestAndUnmarshal(t, f.router, "/api/v1/posture/summary", f.auth, http.StatusOK, &summary)
	return summary
}

func (f *fixture) find(t *testing.T, control string) Finding {
	t.Helper()
	var listed FindingsResponse
	test_utils.MakePostRequestAndUnmarshal(t, f.router, "/api/v1/posture/findings", f.auth,
		FindingsRequest{Filters: Filters{Status: "all", Control: []string{control}}}, http.StatusOK, &listed)
	require.Len(t, listed.Findings, 1)
	return listed.Findings[0]
}

func (f *fixture) detail(t *testing.T, id uuid.UUID) FindingDetail {
	t.Helper()
	var detail FindingDetail
	test_utils.MakeGetRequestAndUnmarshal(t, f.router, "/api/v1/posture/findings/"+id.String(), f.auth,
		http.StatusOK, &detail)
	return detail
}

func nativeRun(region string, passed, failed []string) spec.PostureRun {
	return spec.PostureRun{Account: "111122223333", Region: region, Standard: "aws-fsbp",
		Source: spec.PostureSourceNative, Result: spec.PostureRunOK, Passed: passed, Failed: failed}
}

func finding(control, resource, severity string) spec.PostureFinding {
	return spec.PostureFinding{Source: spec.PostureSourceNative, Account: "111122223333", Region: "eu-west-1",
		Control: control, Title: control + " fails", Severity: severity, ResourceKey: resource,
		Standards: []spec.StandardRef{{Standard: "aws-fsbp", Control: control}}}
}

func score(summary SummaryDTO, standard string) StandardScore {
	for _, score := range summary.Standards {
		if score.Standard == standard {
			return score
		}
	}
	return StandardScore{}
}

func kinds(events []Event) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, event.Kind)
	}
	return out
}
