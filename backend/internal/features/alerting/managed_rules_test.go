package alerting_test

import (
	"net/http"
	"testing"

	"obzly/internal/features/alerting"
	projects_controllers "obzly/internal/features/projects/controllers"
	projects_testing "obzly/internal/features/projects/testing"
	"obzly/internal/features/sharedconfig"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	"obzly/internal/storage"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func Test_ApplyManagedRules_CreatesUpdatesRemoves_AndTheAPIRefusesEdits(t *testing.T) {
	router := projects_testing.CreateTestRouter(projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController(), alerting.GetController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Managed rules", owner.Token, router)
	channel := uuid.New()
	require.NoError(t, storage.GetDb().Exec(`INSERT INTO notification_channels
		(id, project_id, name, kind, config_encrypted, source, all_projects, project_ids, created_at, updated_at)
		VALUES (?, NULL, ?, 'webhook', 'x', 'central', TRUE, '[]', NOW(), NOW())`, channel, "ops "+channel.String()[:6]).Error)
	t.Cleanup(func() {
		storage.GetDb().Exec("DELETE FROM alert_rules WHERE managed_by LIKE 'synthetics:test-%'")
		storage.GetDb().Exec("DELETE FROM notification_channels WHERE id = ?", channel)
	})
	down := sharedconfig.MonitorAlert{
		Key: "synthetics:test-1:down", ProjectID: project.ID.String(), Name: "Synthetics: Checkout is down",
		PromQL: `count(last_over_time(synthetics_up{check_id="1"}[420s]) == 0)`, Comparison: ">=", Threshold: 2,
		IntervalSeconds: 180, Severity: "critical", ChannelIDs: []uuid.UUID{channel},
	}
	elsewhere := down
	elsewhere.Key, elsewhere.ProjectID = "synthetics:test-2:down", uuid.NewString()

	require.NoError(t, alerting.ApplyManagedRules([]sharedconfig.MonitorAlert{down, elsewhere}))

	var rules []*alerting.AlertRule
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/projects/"+project.ID.String()+"/alert-rules",
		"Bearer "+owner.Token, http.StatusOK, &rules)
	require.Len(t, rules, 1, "a rule for a project this Site doesn't have is skipped")
	rule := rules[0]
	require.Equal(t, "synthetics:test-1:down", rule.ManagedBy)
	require.Equal(t, []uuid.UUID{channel}, rule.ChannelIDs)
	require.Equal(t, "critical", rule.Labels["severity"])

	ruleURL := "/api/v1/projects/" + project.ID.String() + "/alert-rules/" + rule.ID.String()
	test_utils.MakeDeleteRequest(t, router, ruleURL, "Bearer "+owner.Token, http.StatusConflict)

	down.Threshold = 3
	require.NoError(t, alerting.ApplyManagedRules([]sharedconfig.MonitorAlert{down}))
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/projects/"+project.ID.String()+"/alert-rules",
		"Bearer "+owner.Token, http.StatusOK, &rules)
	require.Len(t, rules, 1)
	require.Equal(t, rule.ID, rules[0].ID, "an update keeps the rule and its state")
	require.Equal(t, 3.0, rules[0].Threshold)

	budgetKey := alerting.ManagedByBudgetPrefix + uuid.NewString()
	require.NoError(t, alerting.SaveManagedRule(&alerting.ManagedRule{
		Key: budgetKey, ProjectID: project.ID, Name: "AI budget: Monthly", Source: alerting.SourceAICost,
		Comparison: ">=", Threshold: 80, IntervalSeconds: 300,
	}))
	t.Cleanup(func() { _ = alerting.DeleteManagedRule(budgetKey) })

	require.NoError(t, alerting.ApplyManagedRules(nil))
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/projects/"+project.ID.String()+"/alert-rules",
		"Bearer "+owner.Token, http.StatusOK, &rules)
	require.Len(t, rules, 1, "a monitor without alerts any more removes its rule")
	require.Equal(t, budgetKey, rules[0].ManagedBy, "the control plane's sync leaves budget rules alone")
}

func Test_ApplyManagedRules_UnknownChannel_IsLeftOutAndTheRestStillSyncs(t *testing.T) {
	router := projects_testing.CreateTestRouter(projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController(), alerting.GetController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Managed rules channels", owner.Token, router)
	channel := uuid.New()
	require.NoError(t, storage.GetDb().Exec(`INSERT INTO notification_channels
		(id, project_id, name, kind, config_encrypted, source, all_projects, project_ids, created_at, updated_at)
		VALUES (?, NULL, ?, 'webhook', 'x', 'central', TRUE, '[]', NOW(), NOW())`, channel, "ops "+channel.String()[:6]).Error)
	t.Cleanup(func() {
		storage.GetDb().Exec("DELETE FROM alert_rules WHERE managed_by LIKE 'synthetics:test-ch-%'")
		storage.GetDb().Exec("DELETE FROM notification_channels WHERE id = ?", channel)
	})
	missing := uuid.New()
	alert := sharedconfig.MonitorAlert{
		Key: "synthetics:test-ch-1:down", ProjectID: project.ID.String(), Name: "Synthetics: API is down",
		PromQL: `count(last_over_time(synthetics_up{check_id="2"}[420s]) == 0)`, Comparison: ">=", Threshold: 2,
		IntervalSeconds: 180, Severity: "critical", ChannelIDs: []uuid.UUID{missing, channel},
	}

	require.NoError(t, alerting.ApplyManagedRules([]sharedconfig.MonitorAlert{alert}))

	var rules []*alerting.AlertRule
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/projects/"+project.ID.String()+"/alert-rules",
		"Bearer "+owner.Token, http.StatusOK, &rules)
	require.Len(t, rules, 1)
	require.Equal(t, []uuid.UUID{channel}, rules[0].ChannelIDs, "only the channel this Site has")
}
