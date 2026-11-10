package cloudcost

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
	"obzly/internal/storage"
	"obzly/internal/synthetics/spec"
	test_utils "obzly/internal/util/testing"
)

var testNow = time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)

type fixture struct {
	router  *gin.Engine
	user    *users_dto.SignInResponseDTO
	auth    string
	project uuid.UUID
	apiKey  string
	service *Service
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
	projects_controllers.GetMembershipController().RegisterRoutes(group)
	api_keys.GetApiKeyController().RegisterRoutes(group)
	controller.RegisterRoutes(group)
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project := projects_testing.CreateTestProject(name, user, router)
	key := api_keys.CreateTestApiKey("Cloud cost", project.ID, user.Token, router)
	return &fixture{router: router, user: user, auth: "Bearer " + user.Token, project: project.ID, apiKey: key.Token,
		service: service}
}

func (f *fixture) send(t *testing.T, apiKey string, batch spec.CostBatch, status int) map[string]any {
	t.Helper()
	var body bytes.Buffer
	writer := gzip.NewWriter(&body)
	require.NoError(t, json.NewEncoder(writer).Encode(batch))
	require.NoError(t, writer.Close())
	request := httptest.NewRequest(http.MethodPost, "/v1/cloud/cost", &body)
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

func (f *fixture) explore(t *testing.T, request ExploreRequest) ExploreResponse {
	t.Helper()
	var response ExploreResponse
	test_utils.MakePostRequestAndUnmarshal(t, f.router, "/api/v1/cloud-cost/explore", f.auth, request,
		http.StatusOK, &response)
	return response
}

func row(day, account, service, region, currency string, billed, effective int64) spec.CostRow {
	return spec.CostRow{Day: day, SubAccount: account, SubAccountName: "Contoso-" + account[:4], Service: service,
		Region: region, Currency: currency, BilledMicros: billed, EffectiveMicros: effective}
}

func batch(connection uuid.UUID, source, from, to string, rows ...spec.CostRow) spec.CostBatch {
	return spec.CostBatch{
		BatchID: uuid.NewString(), Seq: 1, Total: 1, ConnectionID: connection.String(), ConnectionName: "Contoso-Payer",
		Provider: spec.ProviderAWS, Source: source, From: from, To: to, Rows: rows,
	}
}

func total(response ExploreResponse, currency string) float64 {
	for _, block := range response.Currencies {
		if block.Currency == currency {
			return block.Total
		}
	}
	return 0
}

func Test_ReceiveCost_ChunksApplyOnceComplete_AndExportRowsReplaceAPIDays(t *testing.T) {
	f := newFixture(t, "Cost receive")
	connection := uuid.New()
	daily := ExploreRequest{From: "2026-11-01", To: "2026-11-08", Granularity: GranularityDaily, GroupBy: DimService}

	answer := f.send(t, f.apiKey, batch(connection, spec.CostSourceAPI, "2026-11-01", "2026-11-08",
		row("2026-11-07", "111111111111", "Amazon EC2", "us-east-1", "USD", 10_000_000, 8_000_000),
		row("2026-11-08", "111111111111", "Amazon EC2", "us-east-1", "USD", 4_000_000, 3_000_000)), http.StatusOK)
	assert.Equal(t, true, answer["applied"])
	assert.InDelta(t, 11.0, total(f.explore(t, daily), "USD"), 1e-9)

	// The export for Nov 1-7 arrives in two chunks: nothing changes until both are here.
	export := batch(connection, spec.CostSourceExport, "2026-11-01", "2026-11-07",
		row("2026-11-06", "111111111111", "Amazon EC2", "us-east-1", "USD", 5_000_000, 5_000_000),
		row("2026-11-07", "111111111111", "Amazon S3", "us-east-1", "USD", 1_250_000, 1_250_000))
	first, second := export, export
	first.Total, second.Total, second.Seq = 2, 2, 2
	first.Rows, second.Rows = export.Rows[:1], export.Rows[1:]
	assert.Equal(t, false, f.send(t, f.apiKey, first, http.StatusOK)["applied"])
	assert.InDelta(t, 11.0, total(f.explore(t, daily), "USD"), 1e-9)
	assert.Equal(t, true, f.send(t, f.apiKey, second, http.StatusOK)["applied"])

	// Nov 7 is the export's now (EC2 from the API is gone); Nov 8 is still the API's.
	response := f.explore(t, daily)
	assert.InDelta(t, 5+1.25+3, total(response, "USD"), 1e-9)
	byService := map[string]ExploreRow{}
	for _, r := range response.Currencies[0].Rows {
		byService[r.Key] = r
	}
	assert.InDelta(t, 5+3, byService["Amazon EC2"].Total, 1e-9)
	assert.InDelta(t, 1.25, byService["Amazon S3"].Total, 1e-9)

	// Billed is the invoice's figure.
	billed := daily
	billed.Metric = MetricBilled
	assert.InDelta(t, 5+1.25+4, total(f.explore(t, billed), "USD"), 1e-9)

	f.send(t, "obz_not-a-key", batch(connection, spec.CostSourceAPI, "2026-11-01", "2026-11-01"),
		http.StatusUnauthorized)
	bad := batch(connection, "cache", "2026-11-01", "2026-11-01")
	f.send(t, f.apiKey, bad, http.StatusBadRequest)
}

func Test_ExploreCost_SplitsCurrencies_GroupsByTag_AndFacetsFollowTheOtherFilters(t *testing.T) {
	f := newFixture(t, "Cost explore")
	other := newFixture(t, "Cost explore elsewhere")
	aws, azure := uuid.New(), uuid.New()
	tagged := row("2026-11-03", "111111111111", "Amazon EC2", "us-east-1", "USD", 7_000_000, 7_000_000)
	tagged.Tags = map[string]string{"team": "checkout"}
	f.send(t, f.apiKey, batch(aws, spec.CostSourceExport, "2026-11-01", "2026-11-30", tagged,
		row("2026-10-20", "111111111111", "Amazon S3", "us-west-2", "USD", 2_000_000, 2_000_000)), http.StatusOK)
	f.send(t, f.apiKey, batch(aws, spec.CostSourceExport, "2026-10-01", "2026-10-31",
		row("2026-10-20", "111111111111", "Amazon S3", "us-west-2", "USD", 2_000_000, 2_000_000)), http.StatusOK)
	euro := batch(azure, spec.CostSourceExport, "2026-11-01", "2026-11-30",
		row("2026-11-04", "aaaaaaaa-sub", "Virtual Machines", "westeurope", "EUR", 9_000_000, 9_000_000))
	euro.Provider = spec.ProviderAzure
	f.send(t, f.apiKey, euro, http.StatusOK)
	other.send(t, other.apiKey, batch(uuid.New(), spec.CostSourceExport, "2026-11-01", "2026-11-30",
		row("2026-11-03", "999999999999", "Secret", "us-east-1", "USD", 1_000_000_000, 1_000_000_000)), http.StatusOK)

	monthly := ExploreRequest{
		From:        "2026-10-01",
		To:          "2026-11-08",
		Granularity: GranularityMonthly,
		GroupBy:     "tag:team",
	}
	response := f.explore(t, monthly)
	require.Len(t, response.Currencies, 2, "never added across currencies")
	assert.Equal(t, "EUR", response.Currencies[0].Currency)
	assert.InDelta(t, 9, response.Currencies[0].Total, 1e-9)
	usd := response.Currencies[1]
	assert.InDelta(t, 9, usd.Total, 1e-9, "only this user's projects")
	require.Len(t, response.Periods, 2)
	assert.Equal(t, "2026-10", response.Periods[0].Key)
	assert.Equal(t, CoveragePartial, response.Periods[0].Coverage, "the Azure connection delivered nothing in October")
	byTag := map[string]ExploreRow{}
	for _, r := range usd.Rows {
		byTag[r.Key] = r
	}
	assert.InDelta(t, 7, byTag["checkout"].Values[1], 1e-9)
	assert.InDelta(t, 2, byTag[""].Values[0], 1e-9, "untagged is its own row")

	var facets FacetsResponse
	test_utils.MakePostRequestAndUnmarshal(t, f.router, "/api/v1/cloud-cost/facets", f.auth,
		ExploreRequest{From: "2026-10-01", To: "2026-11-08", Filters: Filters{Provider: []string{spec.ProviderAzure}}},
		http.StatusOK, &facets)
	assert.Equal(t, []string{"westeurope"}, values(facets.Dimensions[DimRegion]), "Azure leaves only Azure regions")
	assert.ElementsMatch(t, []string{"aws", "azure"}, values(facets.Dimensions[DimProvider]),
		"a filter's own values aren't narrowed by it")
	providers := map[string]map[string]float64{}
	for _, value := range facets.Dimensions[DimProvider] {
		providers[value.Value] = value.Amounts
	}
	assert.Equal(t, map[string]float64{"EUR": 9}, providers["azure"], "a facet's amounts stay in their currency")
	assert.Equal(t, map[string]float64{"USD": 9}, providers["aws"])
	assert.Equal(t, "Contoso-aaaa", facets.Dimensions[DimAccount][0].Label)
	assert.Contains(t, facets.Dimensions, "tag:team")

	test_utils.MakePostRequest(t, f.router, "/api/v1/cloud-cost/explore", f.auth,
		ExploreRequest{From: "2026-01-01", To: "2026-11-08", Granularity: GranularityDaily, GroupBy: DimService},
		http.StatusBadRequest)
	test_utils.MakePostRequest(t, f.router, "/api/v1/cloud-cost/explore", f.auth,
		ExploreRequest{From: "2026-11-01", To: "2026-11-08", Granularity: GranularityDaily, GroupBy: "tag:x' OR 1=1"},
		http.StatusBadRequest)
	test_utils.MakePostRequest(t, f.router, "/api/v1/cloud-cost/explore", f.auth,
		ExploreRequest{From: "2026-11-01", To: "2026-11-08", Granularity: GranularityDaily, GroupBy: DimService,
			ProjectID: other.project.String()}, http.StatusForbidden)
}

func Test_CostSummary_MonthToDateForecastAndSources(t *testing.T) {
	f := newFixture(t, "Cost summary")
	connection := uuid.New()
	payload := batch(connection, spec.CostSourceAPI, "2026-10-01", "2026-11-08",
		row("2026-10-03", "111111111111", "Amazon EC2", "", "USD", 30_000_000, 30_000_000),
		row("2026-10-20", "111111111111", "Amazon EC2", "", "USD", 50_000_000, 50_000_000),
		row("2026-11-02", "111111111111", "Amazon EC2", "", "USD", 40_000_000, 40_000_000))
	payload.Forecast = []spec.CostForecast{{Month: "2026-11", Currency: "USD", RemainingMicros: 100_000_000}}
	f.send(t, f.apiKey, payload, http.StatusOK)

	var summary SummaryDTO
	test_utils.MakeGetRequestAndUnmarshal(t, f.router, "/api/v1/cloud-cost/summary", f.auth, http.StatusOK, &summary)
	require.Len(t, summary.Currencies, 1)
	usd := summary.Currencies[0]
	assert.InDelta(t, 40, usd.MonthToDate, 1e-9)
	assert.InDelta(t, 80, usd.LastMonth, 1e-9)
	assert.InDelta(t, 30, usd.LastMonthSameDays, 1e-9, "October 1-9 only")
	require.NotNil(t, usd.Forecast)
	assert.InDelta(t, 140, *usd.Forecast, 1e-9)
	require.Len(t, summary.Sources, 1)
	assert.Equal(t, spec.CostSourceAPI, summary.Sources[0].Source)
	assert.Equal(t, "2026-10-01", *summary.Sources[0].CoveredFrom)
}

func Test_CostBudget_KeepsAManagedRule_AndAlertsOnTheProjection(t *testing.T) {
	f := newFixture(t, "Cost budget")
	connection := uuid.New()
	payload := batch(connection, spec.CostSourceExport, "2026-11-01", "2026-11-08",
		row("2026-11-02", "111111111111", "Amazon EC2", "", "USD", 60_000_000, 60_000_000))
	payload.Forecast = []spec.CostForecast{{Month: "2026-11", Currency: "USD", RemainingMicros: 40_000_000}}
	f.send(t, f.apiKey, payload, http.StatusOK)

	var budget BudgetDTO
	test_utils.MakePostRequestAndUnmarshal(t, f.router, fmt.Sprintf("/api/v1/projects/%s/cost-budgets", f.project),
		f.auth, BudgetRequest{Name: "Contoso monthly", Currency: "usd", Amount: 120, ThresholdPercent: 80},
		http.StatusOK, &budget)
	assert.Equal(t, "USD", budget.Currency)
	assert.InDelta(t, 60, budget.Spent, 1e-9)
	assert.InDelta(t, 100, budget.Projected, 1e-9)
	assert.Equal(t, "forecast", budget.Method)
	assert.Equal(t, BudgetNear, budget.Status)

	var rule alerting.AlertRule
	require.NoError(t, storage.GetDb().Where("managed_by = ?", budgetRuleKey(budget.ID)).First(&rule).Error)
	assert.Equal(t, alerting.SourceCloudCost, rule.Source)
	samples, err := f.service.EvaluateRule(context.Background(), &rule, testNow)
	require.NoError(t, err)
	require.Len(t, samples, 1)
	assert.InDelta(t, 83.3, samples[0].Value, 0.05)

	filtered := BudgetRequest{Name: "EC2 only", Currency: "USD", Amount: 50,
		Filters: Filters{Service: []string{"Amazon EC2"}}}
	test_utils.MakePostRequestAndUnmarshal(t, f.router, fmt.Sprintf("/api/v1/projects/%s/cost-budgets", f.project),
		f.auth, filtered, http.StatusOK, &budget)
	assert.Equal(t, "run-rate", budget.Method)
	assert.Equal(t, BudgetOver, budget.Status)

	test_utils.MakePostRequest(t, f.router, fmt.Sprintf("/api/v1/projects/%s/cost-budgets", f.project), f.auth,
		BudgetRequest{Name: "Bad", Currency: "dollars", Amount: 10}, http.StatusBadRequest)
	test_utils.MakeDeleteRequest(t, f.router, fmt.Sprintf("/api/v1/projects/%s/cost-budgets/%s", f.project, budget.ID),
		f.auth, http.StatusOK)
	var count int64
	storage.GetDb().Model(&alerting.AlertRule{}).Where("managed_by = ?", budgetRuleKey(budget.ID)).Count(&count)
	assert.Zero(t, count, "the budget's rule goes with it")

	viewer := users_testing.CreateTestUser(users_enums.UserRoleMember)
	test_utils.MakePostRequest(t, f.router, fmt.Sprintf("/api/v1/projects/%s/cost-budgets", f.project),
		"Bearer "+viewer.Token, BudgetRequest{Name: "Not mine", Currency: "USD", Amount: 10}, http.StatusForbidden)
}

func Test_CostAnomalies_FlagASpikeAgainstTheSameWeekday_AndCanBeAcknowledged(t *testing.T) {
	f := newFixture(t, "Cost anomalies")
	connection := uuid.New()
	var rows []spec.CostRow
	yesterday := testNow.AddDate(0, 0, -1).Truncate(24 * time.Hour)
	for back := 0; back < 7*9; back++ {
		day := yesterday.AddDate(0, 0, -back)
		amount := int64(100_000_000 + back%3*1_000_000)
		if back == 0 {
			amount = 450_000_000
		}
		rows = append(
			rows,
			row(day.Format(dayLayout), "111111111111", "Data Transfer", "us-east-1", "USD", amount, amount),
		)
		// Steady, and a dip on an estimated day, which isn't flagged.
		steady := row(day.Format(dayLayout), "111111111111", "Amazon RDS", "us-east-1", "USD", 200_000_000, 200_000_000)
		if back == 0 {
			steady.EffectiveMicros, steady.BilledMicros, steady.Estimated = 20_000_000, 20_000_000, true
		}
		rows = append(rows, steady)
	}
	f.send(t, f.apiKey, batch(connection, spec.CostSourceExport, yesterday.AddDate(0, 0, -7*9).Format(dayLayout),
		yesterday.Format(dayLayout), rows...), http.StatusOK)

	var anomalies []AnomalyDTO
	test_utils.MakeGetRequestAndUnmarshal(t, f.router, "/api/v1/cloud-cost/anomalies", f.auth, http.StatusOK,
		&anomalies)
	services := map[string]AnomalyDTO{}
	for _, anomaly := range anomalies {
		services[anomaly.Service] = anomaly
	}
	require.Contains(t, services, "Data Transfer")
	spike := services["Data Transfer"]
	assert.Equal(t, yesterday.Format(dayLayout), spike.Day)
	assert.InDelta(t, 450, spike.Actual, 1e-9)
	assert.InDelta(t, 101, spike.Expected, 1)
	assert.Equal(t, "Contoso-Payer", spike.ConnectionName)
	assert.NotContains(t, services, "Amazon RDS", "a drop on an estimated day isn't flagged")

	var updated Anomaly
	answer := test_utils.MakeRequest(t, f.router, test_utils.RequestOptions{Method: http.MethodPatch,
		URL: "/api/v1/cloud-cost/anomalies/" + spike.ID.String(), AuthToken: f.auth,
		Body: AnomalyUpdate{Status: "acknowledged", Note: "load test"}, ExpectedStatus: http.StatusOK})
	require.NoError(t, json.Unmarshal(answer.Body, &updated))
	assert.Equal(t, "acknowledged", updated.Status)
	stranger := users_testing.CreateTestUser(users_enums.UserRoleMember)
	test_utils.MakeRequest(t, f.router, test_utils.RequestOptions{Method: http.MethodPatch,
		URL: "/api/v1/cloud-cost/anomalies/" + spike.ID.String(), AuthToken: "Bearer " + stranger.Token,
		Body: AnomalyUpdate{Status: "open"}, ExpectedStatus: http.StatusForbidden})
}

func Test_ResourceCost_FindsAnInstanceByItsARN_FromExportsOnly(t *testing.T) {
	f := newFixture(t, "Cost resource")
	connection := uuid.New()
	instance := row("2026-11-05", "111111111111", "Amazon EC2", "us-east-1", "USD", 3_000_000, 2_500_000)
	instance.Resource, instance.ChargeCategory = "i-0abc123", "Usage"
	f.send(t, f.apiKey, batch(connection, spec.CostSourceExport, "2026-11-01", "2026-11-08", instance), http.StatusOK)

	var cost ResourceCostDTO
	test_utils.MakeGetRequestAndUnmarshal(t, f.router,
		"/api/v1/cloud-cost/resource?key=arn:aws:ec2:us-east-1:111111111111:instance/i-0abc123", f.auth,
		http.StatusOK, &cost)
	require.Len(t, cost.Currencies, 1)
	assert.InDelta(t, 2.5, cost.Currencies[0].Total, 1e-9)
	assert.InDelta(t, 2.5, cost.Currencies[0].ByCharge["Usage"], 1e-9)
	assert.True(t, cost.Covered)

	var azure ResourceCostDTO
	test_utils.MakeGetRequestAndUnmarshal(t, f.router,
		"/api/v1/cloud-cost/resource?key=/subscriptions/33333333-3333-3333-3333-333333333333/resourceGroups/web/"+
			"providers/Microsoft.Compute/virtualMachines/vm1", f.auth, http.StatusOK, &azure)
	assert.Empty(t, azure.Currencies)
	assert.False(t, azure.Covered, "an AWS export doesn't cover an Azure resource")
}

func Test_Periods_CutTheFirstAndLastShort(t *testing.T) {
	from := time.Date(2025, 3, 15, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	quarters := periods(from, to, GranularityQuarterly)
	assert.Equal(t, period{Key: "2025-Q1", From: "2025-03-15", To: "2025-03-31"}, quarters[0])
	assert.Equal(t, period{Key: "2026-Q4", From: "2026-10-01", To: "2026-11-08"}, quarters[len(quarters)-1])
	assert.Len(t, quarters, 8)
	years := periods(from, to, GranularityYearly)
	assert.Equal(t, []string{"2025", "2026"}, []string{years[0].Key, years[1].Key})
	assert.Equal(t, "-1.500000", microsText(-1_500_000))
}

func values(list []FacetValue) []string {
	out := make([]string, 0, len(list))
	for _, value := range list {
		out = append(out, value.Value)
	}
	return out
}
