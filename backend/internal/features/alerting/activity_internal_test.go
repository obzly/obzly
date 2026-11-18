package alerting

import (
	"context"
	"net/http"
	"testing"
	"time"

	projects_controllers "obzly/internal/features/projects/controllers"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	"obzly/internal/storage"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Firings (pending to firing) and delivered notifications are counted per rule and UTC
// day: a silenced firing counts as fired but sends nothing, a resolved notification counts,
// days older than the week are left out, and every rule is listed, quiet ones with zeros.
func Test_GetRuleActivity_AfterFiresAndNotifications_ReturnsDailyCounts(t *testing.T) {
	router := projects_testing.CreateTestRouter(projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController(), GetController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	viewer := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Alert activity", owner.Token, router)
	projects_testing.AddMemberToProject(project, viewer, users_enums.ProjectRoleViewer, owner.Token, router)
	rec := newReceiver(t)
	f := newFixtureForProject(t, project.ID, rec.server.URL, loopbackSender(t))
	dispatch := func() {
		t.Helper()
		_, err := f.dispatcher.dispatchOnce(context.Background())
		require.NoError(t, err)
	}
	noon := time.Now().UTC().Truncate(24 * time.Hour).Add(12 * time.Hour)

	noisy := f.rule(t, "vector(1)", 0, 0)
	older := f.rule(t, "vector(1)", 0, 0)
	quiet := f.rule(t, "vector(1)", 0, 0)

	// older: fired and resolved 10 days ago (outside the week), fired again 2 days ago.
	f.clock = noon.AddDate(0, 0, -10)
	f.advanceWith(t, older, sample{labels: Labels{"x": "1"}, value: 1})
	dispatch()
	f.clock = f.clock.Add(time.Minute)
	f.advanceWith(t, older, sample{labels: Labels{"x": "1"}, value: -1})
	dispatch()
	f.clock = noon.AddDate(0, 0, -2)
	f.advanceWith(t, older, sample{labels: Labels{"x": "1"}, value: 1})
	dispatch()

	// noisy, today: web fires and resolves (2 notifications), api fires (1), db fires
	// under a silence (none).
	f.clock = noon
	f.advanceWith(t, noisy, sample{labels: Labels{"service": "web"}, value: 1})
	dispatch()
	f.clock = f.clock.Add(time.Minute)
	f.advanceWith(t, noisy, sample{labels: Labels{"service": "web"}, value: -1})
	dispatch()
	f.advanceWith(t, noisy, sample{labels: Labels{"service": "api"}, value: 1})
	dispatch()
	require.NoError(t, f.repo.saveSilence(&Silence{
		ID: uuid.New(), ProjectID: project.ID, Matchers: Matchers{{Name: "service", Value: "db"}},
		StartsAt: f.clock.Add(-time.Minute), EndsAt: time.Now().UTC().Add(48 * time.Hour), CreatedAt: f.clock,
	}))
	f.clock = f.clock.Add(time.Minute)
	f.advanceWith(t, noisy,
		sample{labels: Labels{"service": "api"}, value: 1}, sample{labels: Labels{"service": "db"}, value: 1})
	dispatch()
	require.Len(t, rec.payloads(), 6)

	var activity RuleActivityResponse
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/projects/"+project.ID.String()+"/alert-rules/activity",
		"Bearer "+viewer.Token, http.StatusOK, &activity)
	require.Len(t, activity.Days, activityDays)
	assert.Equal(t, noon.Format(dayLayout), activity.Days[6])
	assert.Equal(t, noon.AddDate(0, 0, -6).Format(dayLayout), activity.Days[0])
	assert.NotNil(t, activity.TrackedSince)
	byRule := map[uuid.UUID]RuleActivity{}
	for _, rule := range activity.Rules {
		byRule[rule.RuleID] = rule
	}
	require.Len(t, byRule, 3)
	assert.Equal(t, []int{0, 0, 0, 0, 0, 0, 3}, byRule[noisy.ID].Fired)
	assert.Equal(t, []int{0, 0, 0, 0, 0, 0, 3}, byRule[noisy.ID].Notifications)
	assert.Equal(t, 3, byRule[noisy.ID].FiredTotal)
	assert.Equal(t, 3, byRule[noisy.ID].NotificationsTotal)
	assert.Equal(t, []int{0, 0, 0, 0, 1, 0, 0}, byRule[older.ID].Fired)
	assert.Equal(t, []int{0, 0, 0, 0, 1, 0, 0}, byRule[older.ID].Notifications)
	assert.Equal(t, 0, byRule[quiet.ID].FiredTotal)
	assert.Equal(t, make([]int, activityDays), byRule[quiet.ID].Notifications)

	// The header: noisy's api and db plus older's x firing, three rules, one silence.
	var summary AlertsSummary
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/projects/"+project.ID.String()+"/alerts/summary",
		"Bearer "+viewer.Token, http.StatusOK, &summary)
	assert.Equal(t, AlertsSummary{Firing: 3, Pending: 0, Rules: 3, Silences: 1}, summary)

	// Deleting a rule deletes its counters (10 and 2 days ago).
	countRows := func() int64 {
		var rows int64
		require.NoError(t, storage.GetDb().Raw("SELECT count(*) FROM alert_rule_activity WHERE rule_id = ?",
			older.ID).Scan(&rows).Error)
		return rows
	}
	require.Equal(t, int64(2), countRows())
	require.NoError(t, storage.GetDb().Exec("DELETE FROM alert_rules WHERE id = ?", older.ID).Error)
	assert.Zero(t, countRows())
}

func Test_GetRuleActivityAndSummary_WhenUserIsNotProjectMember_ReturnsForbidden(t *testing.T) {
	router := projects_testing.CreateTestRouter(projects_controllers.GetProjectController(), GetController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	stranger := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectWithToken("Alert activity access", owner.Token, router)
	base := "/api/v1/projects/" + project.ID.String()

	test_utils.MakeGetRequest(t, router, base+"/alert-rules/activity", "Bearer "+stranger.Token, http.StatusForbidden)
	test_utils.MakeGetRequest(t, router, base+"/alerts/summary", "Bearer "+stranger.Token, http.StatusForbidden)
	test_utils.MakeGetRequest(t, router, base+"/alert-rules/activity", "Bearer "+owner.Token, http.StatusOK)
}

// Counters older than keepActivity are pruned; newer ones stay.
func Test_PruneActivity_DeletesCountersOlderThanTheRetention(t *testing.T) {
	f := newFixture(t, "https://example.com/hook", newSender())
	rule := f.rule(t, "vector(1)", 0, 0)
	now := time.Now().UTC()
	for _, at := range []time.Time{now.Add(-keepActivity - 48*time.Hour), now.AddDate(0, 0, -3)} {
		require.NoError(t, countActivity(storage.GetDb(), rule.ID, at, 1, 2))
	}
	require.NoError(t, f.repo.pruneActivity(context.Background(), now.Add(-keepActivity)))
	var days []string
	require.NoError(t, storage.GetDb().Raw(
		"SELECT to_char(day, 'YYYY-MM-DD') FROM alert_rule_activity WHERE rule_id = ?", rule.ID).Scan(&days).Error)
	assert.Equal(t, []string{now.AddDate(0, 0, -3).Format(dayLayout)}, days)
}
