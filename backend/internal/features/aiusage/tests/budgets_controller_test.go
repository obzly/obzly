package aiusage_tests

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"testing"
	"time"

	"obzly/internal/features/aiusage"
	"obzly/internal/features/alerting"
	logs_core "obzly/internal/features/logs/core"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	"obzly/internal/storage"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_BudgetsAPI_OwnersAndAdminsManage_MembersAndViewersRead(t *testing.T) {
	router := createRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	admin := users_testing.CreateTestUser(users_enums.UserRoleMember)
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)
	viewer := users_testing.CreateTestUser(users_enums.UserRoleMember)
	outsider := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Budgets "+uuid.NewString()[:8], owner.Token, router)
	projects_testing.AddMemberToProject(project, admin, users_enums.ProjectRoleAdmin, owner.Token, router)
	projects_testing.AddMemberToProject(project, member, users_enums.ProjectRoleMember, owner.Token, router)
	projects_testing.AddMemberToProject(project, viewer, users_enums.ProjectRoleViewer, owner.Token, router)
	base := "/api/v1/projects/" + project.ID.String()
	request := aiusage.BudgetRequest{Name: "Monthly team budget", AmountUSD: 12000, Period: "month", Scope: "project"}

	test_utils.MakePostRequest(t, router, base+"/ai-budgets", "Bearer "+member.Token, request, http.StatusForbidden)
	test_utils.MakePostRequest(t, router, base+"/ai-budgets", "Bearer "+viewer.Token, request, http.StatusForbidden)
	var budget aiusage.Budget
	test_utils.MakePostRequestAndUnmarshal(t, router, base+"/ai-budgets", "Bearer "+admin.Token, request,
		http.StatusOK, &budget)
	assert.Equal(t, aiusage.Thresholds{80, 100}, budget.Thresholds)
	assert.Equal(t, "UTC", budget.TimeZone)

	var budgets []aiusage.Budget
	test_utils.MakeGetRequestAndUnmarshal(t, router, base+"/ai-budgets", "Bearer "+viewer.Token,
		http.StatusOK, &budgets)
	require.Len(t, budgets, 1)
	test_utils.MakeGetRequest(t, router, base+"/ai-budgets/status", "Bearer "+member.Token, http.StatusOK)
	test_utils.MakeGetRequest(t, router, base+"/ai-budgets", "Bearer "+outsider.Token, http.StatusForbidden)

	request.AmountUSD = 15000
	test_utils.MakePutRequest(t, router, base+"/ai-budgets/"+budget.ID.String(), "Bearer "+member.Token,
		request, http.StatusForbidden)
	test_utils.MakePutRequest(t, router, base+"/ai-budgets/"+budget.ID.String(), "Bearer "+owner.Token,
		request, http.StatusOK)
	request.Period = "year"
	test_utils.MakePutRequest(t, router, base+"/ai-budgets/"+budget.ID.String(), "Bearer "+owner.Token,
		request, http.StatusBadRequest)

	test_utils.MakeDeleteRequest(t, router, base+"/ai-budgets/"+budget.ID.String(), "Bearer "+member.Token,
		http.StatusForbidden)
	test_utils.MakeDeleteRequest(t, router, base+"/ai-budgets/"+budget.ID.String(), "Bearer "+owner.Token,
		http.StatusOK)
	test_utils.MakeDeleteRequest(t, router, base+"/ai-budgets/"+budget.ID.String(), "Bearer "+owner.Token,
		http.StatusNotFound)
}

func Test_ModelPricesAPI_OwnersAndAdminsManage_FillCommonAddsDatedList(t *testing.T) {
	router := createRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Prices "+uuid.NewString()[:8], owner.Token, router)
	projects_testing.AddMemberToProject(project, member, users_enums.ProjectRoleMember, owner.Token, router)
	base := "/api/v1/projects/" + project.ID.String() + "/ai-model-prices"
	price := aiusage.ModelPriceRequest{Model: "claude-sonnet-4-5*", InputPerMillion: 2.5, OutputPerMillion: 12}

	test_utils.MakePostRequest(t, router, base, "Bearer "+member.Token, price, http.StatusForbidden)
	var created aiusage.ModelPrice
	test_utils.MakePostRequestAndUnmarshal(t, router, base, "Bearer "+owner.Token, price, http.StatusOK, &created)
	test_utils.MakePostRequest(t, router, base, "Bearer "+owner.Token, price, http.StatusBadRequest)

	test_utils.MakePostRequest(t, router, base+"/fill-common", "Bearer "+member.Token, nil, http.StatusForbidden)
	var filled aiusage.FillCommonPricesResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, base+"/fill-common", "Bearer "+owner.Token, nil,
		http.StatusOK, &filled)
	assert.NotEmpty(t, filled.AsOf)
	assert.NotContains(t, filled.Models, "claude-sonnet-4-5*", "an existing price is kept")
	assert.Positive(t, filled.Added)

	var listed aiusage.ModelPricesResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(t, router, base, "Bearer "+member.Token, http.StatusOK, &listed)
	assert.Len(t, listed.Prices, filled.Added+1)
	for _, row := range listed.Prices {
		if row.Model == "claude-sonnet-4-5*" {
			assert.Equal(t, 2.5, row.InputPerMillion)
		}
	}

	price.InputPerMillion = 3
	test_utils.MakePutRequest(t, router, base+"/"+created.ID.String(), "Bearer "+member.Token, price,
		http.StatusForbidden)
	test_utils.MakePutRequest(t, router, base+"/"+created.ID.String(), "Bearer "+owner.Token, price, http.StatusOK)
	test_utils.MakeDeleteRequest(t, router, base+"/"+created.ID.String(), "Bearer "+owner.Token, http.StatusOK)
}

func Test_CreateBudget_KeepsReadOnlyManagedAlertRule_DeletedWithTheBudget(t *testing.T) {
	router := createRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Budget rule "+uuid.NewString()[:8], owner.Token, router)
	channel := createChannel(t, project.ID)
	base := "/api/v1/projects/" + project.ID.String()

	var budget aiusage.Budget
	test_utils.MakePostRequestAndUnmarshal(t, router, base+"/ai-budgets", "Bearer "+owner.Token,
		aiusage.BudgetRequest{Name: "Daily per engineer", AmountUSD: 40, Period: "day", Scope: "each_user",
			Thresholds: []float64{100, 80}, ChannelIDs: []uuid.UUID{channel}}, http.StatusOK, &budget)
	assert.Equal(t, aiusage.Thresholds{80, 100}, budget.Thresholds)

	var rules []alerting.AlertRule
	test_utils.MakeGetRequestAndUnmarshal(t, router, base+"/alert-rules", "Bearer "+owner.Token, http.StatusOK, &rules)
	require.Len(t, rules, 1)
	rule := rules[0]
	assert.Equal(t, "ai-budget:"+budget.ID.String(), rule.ManagedBy)
	assert.Equal(t, alerting.SourceAICost, rule.Source)
	assert.Equal(t, 300, rule.IntervalSeconds)
	assert.Equal(t, []uuid.UUID{channel}, rule.ChannelIDs)
	test_utils.MakeDeleteRequest(t, router, base+"/alert-rules/"+rule.ID.String(), "Bearer "+owner.Token,
		http.StatusConflict)

	test_utils.MakePostRequest(t, router, base+"/ai-budgets", "Bearer "+owner.Token,
		aiusage.BudgetRequest{Name: "Bad channel", AmountUSD: 1, Period: "day", Scope: "project",
			ChannelIDs: []uuid.UUID{uuid.New()}}, http.StatusBadRequest)

	test_utils.MakeDeleteRequest(t, router, base+"/ai-budgets/"+budget.ID.String(), "Bearer "+owner.Token,
		http.StatusOK)
	test_utils.MakeGetRequestAndUnmarshal(t, router, base+"/alert-rules", "Bearer "+owner.Token, http.StatusOK, &rules)
	assert.Empty(t, rules)
}

func Test_SyncBudgetRules_WhenRuleWasRemoved_RecreatesIt(t *testing.T) {
	router := createRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Budget sync "+uuid.NewString()[:8], owner.Token, router)
	var budget aiusage.Budget
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/projects/"+project.ID.String()+"/ai-budgets",
		"Bearer "+owner.Token, aiusage.BudgetRequest{Name: "Weekly", AmountUSD: 10, Period: "week", Scope: "project"},
		http.StatusOK, &budget)
	key := "ai-budget:" + budget.ID.String()
	// What an older replica's synthetics sync did to every managed rule.
	require.NoError(t, alerting.DeleteManagedRule(key))

	require.NoError(t, aiusage.GetAIUsageService().SyncBudgetRules())

	var count int64
	require.NoError(t, storage.GetDb().Model(&alerting.AlertRule{}).Where("managed_by = ?", key).Count(&count).Error)
	assert.Equal(t, int64(1), count)
	require.NoError(t, aiusage.GetAIUsageService().SyncBudgetRules())
	require.NoError(t, storage.GetDb().Model(&alerting.AlertRule{}).Where("managed_by = ?", key).Count(&count).Error)
	assert.Equal(t, int64(1), count, "an existing rule isn't duplicated")
}

func Test_GetBudgetStatus_EachUser_SumsClaudeCodeAndPricedLLMAppSpend(t *testing.T) {
	router, project, token := setupProjectWithLLMSpans(t)
	now := time.Now().UTC()
	at := midpointToday(now)
	storeClaudeCodeCosts(t, project.ID, at, map[string]int64{"a.rivera@example.com": 30_000_000,
		"j.chen@example.com": 5_000_000})
	base := "/api/v1/projects/" + project.ID.String()

	var budget aiusage.Budget
	test_utils.MakePostRequestAndUnmarshal(t, router, base+"/ai-budgets", "Bearer "+token,
		aiusage.BudgetRequest{Name: "Daily per engineer", AmountUSD: 40, Period: "day", Scope: "each_user",
			IncludeLLMApps: true}, http.StatusOK, &budget)

	var statuses []aiusage.BudgetStatusDTO
	test_utils.MakeGetRequestAndUnmarshal(t, router, base+"/ai-budgets/status", "Bearer "+token,
		http.StatusOK, &statuses)
	require.Len(t, statuses, 1)
	status := statuses[0]
	require.Empty(t, status.Error)
	// The LLM spans were stored 10 minutes ago: count them only if that is today.
	llm := map[string]float64{}
	if !time.Now().UTC().Add(-10 * time.Minute).Before(startOfDay(now)) {
		llm["a.rivera@example.com"], llm["j.chen@example.com"] = 0.00492, 0.00028
	}
	require.Len(t, status.Top, 2)
	assert.Equal(t, "a.rivera@example.com", status.Top[0].Key)
	assert.InDelta(t, 30+llm["a.rivera@example.com"], status.Top[0].SpentUSD, 1e-9)
	assert.InDelta(t, 5+llm["j.chen@example.com"], status.Top[1].SpentUSD, 1e-9)
	assert.InDelta(t, (30+llm["a.rivera@example.com"])/40*100, status.Percent, 1e-6)
	assert.Equal(t, 2, status.Groups)
	assert.Equal(t, startOfDay(now).UnixMilli(), status.PeriodStart)
}

func Test_EvaluateBudgetRule_EachThresholdFiresOncePerPeriodAndResolvesAtRollover(t *testing.T) {
	router := createRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Budget eval "+uuid.NewString()[:8], owner.Token, router)
	channel := createChannel(t, project.ID)
	now := time.Now().UTC()
	at := midpointToday(now)
	storeClaudeCodeCosts(t, project.ID, at, map[string]int64{"a.rivera@example.com": 900_000,
		"j.chen@example.com": 500_000})

	var budget aiusage.Budget
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/projects/"+project.ID.String()+"/ai-budgets",
		"Bearer "+owner.Token, aiusage.BudgetRequest{Name: "Daily per engineer", AmountUSD: 1, Period: "day",
			Scope: "each_user", ChannelIDs: []uuid.UUID{channel}}, http.StatusOK, &budget)
	var rule alerting.AlertRule
	require.NoError(t, storage.GetDb().Where("managed_by = ?", "ai-budget:"+budget.ID.String()).First(&rule).Error)
	rule.ChannelIDs = []uuid.UUID{channel}
	evaluate := func(at time.Time) {
		t.Helper()
		evaluator := alerting.GetEvaluator().WithClock(func() time.Time { return at })
		require.NoError(t, evaluator.Evaluate(context.Background(), &rule))
		var lastError string
		require.NoError(t, storage.GetDb().Raw("SELECT last_error FROM alert_rules WHERE id = ?", rule.ID).
			Scan(&lastError).Error)
		require.Empty(t, lastError)
	}

	evaluate(now.Add(time.Second))
	assert.Equal(t, []string{"firing:80:warning"}, notifications(t, rule.ID))
	evaluate(now.Add(2 * time.Second))
	assert.Len(t, notifications(t, rule.ID), 1, "a threshold notifies once per period")

	storeClaudeCodeCosts(t, project.ID, at, map[string]int64{"a.rivera@example.com": 200_000})
	evaluate(now.Add(3 * time.Second))
	assert.Equal(t, []string{"firing:100:critical", "firing:80:warning"}, notifications(t, rule.ID))

	nextPeriod := startOfDay(now).Add(24*time.Hour + time.Minute)
	evaluate(nextPeriod)
	assert.Equal(t, []string{"firing:100:critical", "firing:80:warning", "resolved:100:critical",
		"resolved:80:warning"}, notifications(t, rule.ID))
	evaluate(nextPeriod.Add(5 * time.Minute))
	assert.Len(t, notifications(t, rule.ID), 4)
}

func Test_EvaluateBudgetRule_OverTheScanLimit_RecordsARuleErrorAndDoesNotAlert(t *testing.T) {
	router := createRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Budget limit "+uuid.NewString()[:8], owner.Token, router)
	now := time.Now().UTC()
	storeClaudeCodeCosts(t, project.ID, midpointToday(now), map[string]int64{"a.rivera@example.com": 5_000_000})
	var budget aiusage.Budget
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/projects/"+project.ID.String()+"/ai-budgets",
		"Bearer "+owner.Token, aiusage.BudgetRequest{Name: "Tiny", AmountUSD: 1, Period: "day", Scope: "project"},
		http.StatusOK, &budget)
	var rule alerting.AlertRule
	require.NoError(t, storage.GetDb().Where("managed_by = ?", "ai-budget:"+budget.ID.String()).First(&rule).Error)

	alerting.SetAICostSource(aiusage.GetAIUsageService().WithScanBudget(1).EvaluateBudgetRule)
	t.Cleanup(func() { alerting.SetAICostSource(aiusage.GetAIUsageService().EvaluateBudgetRule) })
	require.NoError(t, alerting.GetEvaluator().WithClock(func() time.Time { return now.Add(time.Second) }).
		Evaluate(context.Background(), &rule))

	var lastError string
	require.NoError(t, storage.GetDb().Raw("SELECT last_error FROM alert_rules WHERE id = ?", rule.ID).
		Scan(&lastError).Error)
	assert.Contains(t, lastError, "over the")
	var states int64
	require.NoError(t, storage.GetDb().Model(&alerting.AlertState{}).Where("rule_id = ?", rule.ID).Count(&states).Error)
	assert.Zero(t, states, "no alert from partial data")
}

func Test_EvaluateBudgetRule_MonthlyAcrossDays_SameSpendFromCacheAndLive(t *testing.T) {
	router := createRouter()
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Budget days "+uuid.NewString()[:8], owner.Token, router)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	storeClaudeCodeCosts(t, project.ID, time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC),
		map[string]int64{"a.rivera@example.com": 500_000})
	storeClaudeCodeCosts(t, project.ID, time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC),
		map[string]int64{"a.rivera@example.com": 600_000, "j.chen@example.com": 100_000})
	var budget aiusage.Budget
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/projects/"+project.ID.String()+"/ai-budgets",
		"Bearer "+owner.Token, aiusage.BudgetRequest{Name: "Monthly", AmountUSD: 1, Period: "month", Scope: "each_user"},
		http.StatusOK, &budget)
	var rule alerting.AlertRule
	require.NoError(t, storage.GetDb().Where("managed_by = ?", "ai-budget:"+budget.ID.String()).First(&rule).Error)

	for range 2 {
		samples, err := aiusage.GetAIUsageService().EvaluateBudgetRule(context.Background(), &rule, now)
		require.NoError(t, err)
		require.Len(t, samples, 2, "a.rivera is past 80% and 100%; j.chen at 10% is not")
		for _, sample := range samples {
			assert.Equal(t, "a.rivera@example.com", sample.Labels["user"])
			assert.InDelta(t, 110, sample.Value, 1e-6)
			assert.Equal(t, "2026-10-01", sample.Labels["period"])
		}
	}
}

// notifications are the rule's queued notifications as status:threshold:severity, sorted.
func notifications(t *testing.T, ruleID uuid.UUID) []string {
	t.Helper()
	var items []alerting.OutboxItem
	require.NoError(t, storage.GetDb().Where("rule_id = ?", ruleID).Order("status, created_at").Find(&items).Error)
	result := []string{}
	for _, item := range items {
		assert.Equal(t, "a.rivera@example.com", item.Payload.Labels["user"])
		result = append(result, fmt.Sprintf("%s:%s:%s", item.Status, item.Payload.Labels["threshold"],
			item.Payload.Labels["severity"]))
	}
	sort.Strings(result)
	return result
}

func storeClaudeCodeCosts(t *testing.T, projectID uuid.UUID, at time.Time, micros map[string]int64) {
	t.Helper()
	items := []*logs_core.LogItem{}
	for user, cost := range micros {
		items = append(items, &logs_core.LogItem{
			ID: uuid.New(), ProjectID: projectID, Timestamp: at, Level: logs_core.LogLevelInfo,
			Message: "claude_code.api_request",
			Fields: map[string]any{"user_email": user, "model": "claude-opus-4-1", "session_id": "session-" + user,
				"cost_usd_micros": fmt.Sprint(cost)},
		})
	}
	repository := logs_core.GetAIRepository()
	require.NoError(t, repository.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: items}))
	require.NoError(t, repository.ForceFlush())
}

func createChannel(t *testing.T, projectID uuid.UUID) uuid.UUID {
	t.Helper()
	channel := uuid.New()
	require.NoError(t, storage.GetDb().Exec(`INSERT INTO notification_channels
		(id, project_id, name, kind, config_encrypted, source, all_projects, project_ids, created_at, updated_at)
		VALUES (?, NULL, ?, 'webhook', 'x', 'local', FALSE, ?::jsonb, NOW(), NOW())`,
		channel, "budgets "+channel.String()[:6], `["`+projectID.String()+`"]`).Error)
	t.Cleanup(func() {
		storage.GetDb().Exec("DELETE FROM notification_outbox WHERE channel_id = ?", channel)
		storage.GetDb().Exec("DELETE FROM notification_channels WHERE id = ?", channel)
	})
	return channel
}

func startOfDay(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

// midpointToday is a time between today's start and now, so events at it
// count toward a daily budget evaluated now.
func midpointToday(now time.Time) time.Time {
	return now.Add(-now.Sub(startOfDay(now)) / 2)
}
