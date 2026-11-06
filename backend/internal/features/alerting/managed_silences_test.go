package alerting_test

import (
	"net/http"
	"testing"
	"time"

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

func Test_ApplyManagedSilences_CreatesReplacesRemoves_AndTheAPIRefusesDeletes(t *testing.T) {
	router := projects_testing.CreateTestRouter(projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController(), alerting.GetController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Maintenance silences", owner.Token, router)
	t.Cleanup(func() { storage.GetDb().Exec("DELETE FROM silences WHERE managed_by <> ''") })
	silencesURL := "/api/v1/projects/" + project.ID.String() + "/silences"
	checkout, cart := uuid.NewString(), uuid.NewString()
	now := time.Now().UTC().Truncate(time.Second)
	window := sharedconfig.MaintenanceSilence{
		Key: "synthetics-maintenance:w1:" + project.ID.String() + ":1", ProjectID: project.ID.String(),
		Comment: "Synthetics maintenance: Weekly DB patching", CheckIDs: []string{checkout, cart},
		StartsAt: now.Add(-time.Minute), EndsAt: now.Add(time.Hour),
	}
	elsewhere := window
	elsewhere.Key, elsewhere.ProjectID = "synthetics-maintenance:w1:other:1", uuid.NewString()
	over := window
	over.Key, over.EndsAt = "synthetics-maintenance:w1:old:1", now.Add(-time.Second)

	require.NoError(t, alerting.ApplyManagedSilences([]sharedconfig.MaintenanceSilence{window, elsewhere, over}))

	var silences []*alerting.Silence
	test_utils.MakeGetRequestAndUnmarshal(t, router, silencesURL, "Bearer "+owner.Token, http.StatusOK, &silences)
	require.Len(t, silences, 1, "another Site's project and a finished occurrence are skipped")
	silence := silences[0]
	require.Equal(t, window.Key, silence.ManagedBy)
	require.Equal(t, window.Comment, silence.Comment)
	require.Len(t, silence.Matchers, 1)
	require.Equal(t, "check_id", silence.Matchers[0].Name)
	require.True(t, silence.Matchers[0].Regex)
	require.Contains(t, silence.Matchers[0].Value, checkout)
	require.Contains(t, silence.Matchers[0].Value, cart)
	test_utils.MakeDeleteRequest(t, router, silencesURL+"/"+silence.ID.String(), "Bearer "+owner.Token,
		http.StatusConflict)

	edited := window
	edited.CheckIDs, edited.EndsAt = []string{checkout}, now.Add(2*time.Hour)
	require.NoError(t, alerting.ApplyManagedSilences([]sharedconfig.MaintenanceSilence{edited}))
	var replaced []*alerting.Silence
	test_utils.MakeGetRequestAndUnmarshal(t, router, silencesURL, "Bearer "+owner.Token, http.StatusOK, &replaced)
	require.Len(t, replaced, 1, "replaced, not added")
	require.Equal(t, alerting.Matcher{Name: "check_id", Value: checkout}, replaced[0].Matchers[0])
	require.True(t, replaced[0].EndsAt.Equal(edited.EndsAt))

	require.NoError(t, alerting.ApplyManagedSilences(nil))
	var removed []*alerting.Silence
	test_utils.MakeGetRequestAndUnmarshal(t, router, silencesURL, "Bearer "+owner.Token, http.StatusOK, &removed)
	require.Empty(t, removed, "a deleted window removes its silences")
}

func Test_ApplyManagedRules_LabelsTheRuleWithItsCheckID(t *testing.T) {
	router := projects_testing.CreateTestRouter(projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController(), alerting.GetController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Managed rule labels", owner.Token, router)
	t.Cleanup(func() {
		storage.GetDb().Exec("DELETE FROM alert_rules WHERE managed_by LIKE 'synthetics:test-label-%'")
	})
	checkID := uuid.NewString()
	alert := sharedconfig.MonitorAlert{
		Key: "synthetics:test-label-1:down", ProjectID: project.ID.String(), Name: "Synthetics: Checkout is down",
		PromQL: `count(last_over_time(synthetics_up{check_id="1"}[420s]) == 0)`, Comparison: ">=", Threshold: 1,
		IntervalSeconds: 180, Severity: "critical", CheckID: checkID,
	}

	require.NoError(t, alerting.ApplyManagedRules([]sharedconfig.MonitorAlert{alert}))

	var rules []*alerting.AlertRule
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/projects/"+project.ID.String()+"/alert-rules",
		"Bearer "+owner.Token, http.StatusOK, &rules)
	require.Len(t, rules, 1)
	require.Equal(t, checkID, rules[0].Labels["check_id"])
}
