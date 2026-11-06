package alerting_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"obzly/internal/features/alerting"
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

func Test_AlertingAPI_PermissionsValidationAndSecrets(t *testing.T) {
	router := projects_testing.CreateTestRouter(projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController(), alerting.GetController(), alerting.GetSettingsController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	projectAdmin := users_testing.CreateTestUser(users_enums.UserRoleMember)
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)
	viewer := users_testing.CreateTestUser(users_enums.UserRoleMember)
	globalAdmin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	project, _ := projects_testing.CreateTestProjectWithToken("Alerting API", owner.Token, router)
	projects_testing.AddMemberToProject(project, projectAdmin, users_enums.ProjectRoleAdmin, owner.Token, router)
	projects_testing.AddMemberToProject(project, member, users_enums.ProjectRoleMember, owner.Token, router)
	projects_testing.AddMemberToProject(project, viewer, users_enums.ProjectRoleViewer, owner.Token, router)
	base := "/api/v1/projects/" + project.ID.String()

	// Channels live in Obzly settings: global admins only, scoped to
	// projects; a project lists those available to it and never sees the URL.
	secretURL := "https://hooks.example.com/services/T000/B000/very-secret-token"
	channelRequest := alerting.SettingsChannelRequest{Name: "ops " + project.ID.String()[:8], Kind: "slack",
		Config: &alerting.ChannelConfig{URL: secretURL}, ProjectIDs: []string{project.ID.String()}}
	test_utils.MakePostRequest(t, router, "/api/v1/settings/channels", "Bearer "+owner.Token,
		channelRequest, http.StatusForbidden)
	var channel alerting.SettingsChannel
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/settings/channels", "Bearer "+globalAdmin.Token,
		channelRequest, http.StatusOK, &channel)
	assert.Equal(t, "https://hooks.example.com", channel.Target)
	assert.Equal(t, "local", channel.Source)
	test_utils.MakePostRequest(t, router, "/api/v1/settings/channels", "Bearer "+globalAdmin.Token,
		alerting.SettingsChannelRequest{Name: "bad", Kind: "webhook", Config: &alerting.ChannelConfig{URL: "ftp://x"},
			AllProjects: true}, http.StatusBadRequest)
	test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/settings/channels",
		"Bearer "+globalAdmin.Token,
		alerting.SettingsChannelRequest{
			Name:   "nowhere",
			Kind:   "slack",
			Config: &alerting.ChannelConfig{URL: secretURL},
		},
		http.StatusBadRequest,
	)
	listed := test_utils.MakeGetRequest(t, router, base+"/notification-channels", "Bearer "+viewer.Token, http.StatusOK)
	assert.NotContains(t, string(listed.Body), "very-secret-token")
	assert.Contains(t, string(listed.Body), channel.ID.String())
	var stored string
	require.NoError(t, storage.GetDb().Raw("SELECT config_encrypted FROM notification_channels WHERE id = ?",
		channel.ID).Scan(&stored).Error)
	assert.NotContains(t, stored, "hooks.example.com", "config is encrypted at rest")
	// Renaming keeps the stored config.
	var renamed alerting.SettingsChannel
	test_utils.MakePutRequestAndUnmarshal(t, router, "/api/v1/settings/channels/"+channel.ID.String(),
		"Bearer "+globalAdmin.Token, alerting.SettingsChannelRequest{Name: "ops-renamed " + project.ID.String()[:8],
			Kind: "slack", ProjectIDs: []string{project.ID.String()}}, http.StatusOK, &renamed)
	assert.Equal(t, "https://hooks.example.com", renamed.Target)

	// Rules: project owners and admins; members read.
	rule := alerting.RuleRequest{Name: "High error rate", Source: "metrics",
		Query: alerting.RuleQuery{PromQL: "vector(1)"}, Comparison: ">", Threshold: 0.5,
		ChannelIDs: []uuid.UUID{channel.ID}}
	test_utils.MakePostRequest(t, router, base+"/alert-rules", "Bearer "+member.Token, rule, http.StatusForbidden)
	var created alerting.AlertRule
	test_utils.MakePostRequestAndUnmarshal(t, router, base+"/alert-rules", "Bearer "+projectAdmin.Token, rule,
		http.StatusOK, &created)
	assert.Equal(t, 60, created.IntervalSeconds)
	assert.Equal(t, 4*3600, created.RepeatSeconds)
	assert.Equal(t, []uuid.UUID{channel.ID}, created.ChannelIDs)
	for name, bad := range map[string]alerting.RuleRequest{
		"bad promql":      {Name: "x", Source: "metrics", Query: alerting.RuleQuery{PromQL: "rate("}, Comparison: ">"},
		"bad source":      {Name: "x", Source: "events", Comparison: ">"},
		"bad compare":     {Name: "x", Source: "logs", Comparison: "~"},
		"bad metric":      {Name: "x", Source: "traces", Query: alerting.RuleQuery{Metric: "p42"}, Comparison: ">"},
		"short interval":  {Name: "x", Source: "logs", Comparison: ">", IntervalSeconds: 5},
		"foreign channel": {Name: "x", Source: "logs", Comparison: ">", ChannelIDs: []uuid.UUID{uuid.New()}},
		"reserved label":  {Name: "x", Source: "logs", Comparison: ">", Labels: alerting.Labels{"alertname": "y"}},
	} {
		response := test_utils.MakePostRequest(t, router, base+"/alert-rules", "Bearer "+owner.Token, bad,
			http.StatusBadRequest)
		assert.NotEmpty(t, string(response.Body), name)
	}
	var rules []alerting.AlertRule
	test_utils.MakeGetRequestAndUnmarshal(t, router, base+"/alert-rules", "Bearer "+viewer.Token, http.StatusOK, &rules)
	require.Len(t, rules, 1)

	// Preview runs a full query: rule editors only; vector(1) > 0.5 fires.
	test_utils.MakePostRequest(t, router, base+"/alert-rules/preview", "Bearer "+viewer.Token, rule,
		http.StatusForbidden)
	test_utils.MakePostRequest(t, router, base+"/alert-rules/preview", "Bearer "+member.Token, rule,
		http.StatusForbidden)
	var preview []alerting.PreviewSeries
	test_utils.MakePostRequestAndUnmarshal(t, router, base+"/alert-rules/preview", "Bearer "+projectAdmin.Token, rule,
		http.StatusOK, &preview)
	require.Len(t, preview, 1)
	assert.True(t, preview[0].Firing)

	// Silences: members who can write; viewers can't.
	silence := alerting.SilenceRequest{Matchers: alerting.Matchers{{Name: "alertname", Value: "High.*", Regex: true}},
		EndsAt: time.Now().Add(time.Hour), Comment: "deploy"}
	test_utils.MakePostRequest(t, router, base+"/silences", "Bearer "+viewer.Token, silence, http.StatusForbidden)
	var createdSilence alerting.Silence
	test_utils.MakePostRequestAndUnmarshal(t, router, base+"/silences", "Bearer "+member.Token, silence,
		http.StatusOK, &createdSilence)
	test_utils.MakePostRequest(
		t,
		router,
		base+"/silences",
		"Bearer "+member.Token,
		alerting.SilenceRequest{
			Matchers: alerting.Matchers{{Name: "x", Value: "("}},
			EndsAt:   time.Now().Add(-time.Hour),
		},
		http.StatusBadRequest,
	)
	test_utils.MakeDeleteRequest(t, router, base+"/silences/"+createdSilence.ID.String(), "Bearer "+member.Token,
		http.StatusOK)

	// Deleting the channel detaches it from the rule.
	test_utils.MakeDeleteRequest(
		t,
		router,
		"/api/v1/settings/channels/"+channel.ID.String(),
		"Bearer "+globalAdmin.Token,
		http.StatusOK,
	)
	var after alerting.AlertRule
	body := test_utils.MakeGetRequest(t, router, base+"/alert-rules", "Bearer "+owner.Token, http.StatusOK).Body
	var afterRules []alerting.AlertRule
	require.NoError(t, json.Unmarshal(body, &afterRules))
	after = afterRules[0]
	assert.Empty(t, after.ChannelIDs)
	test_utils.MakeDeleteRequest(t, router, base+"/alert-rules/"+created.ID.String(), "Bearer "+member.Token,
		http.StatusForbidden)
	test_utils.MakeDeleteRequest(t, router, base+"/alert-rules/"+created.ID.String(), "Bearer "+owner.Token,
		http.StatusOK)

	// Another project's members see nothing.
	stranger := users_testing.CreateTestUser(users_enums.UserRoleMember)
	response := test_utils.MakeGetRequest(t, router, base+"/alerts", "Bearer "+stranger.Token, http.StatusForbidden)
	assert.True(t, strings.Contains(string(response.Body), "insufficient"))
}

func Test_ListChannels_AsViewer_HidesRecipientsAndOtherProjects(t *testing.T) {
	router := projects_testing.CreateTestRouter(projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController(), alerting.GetController(), alerting.GetSettingsController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	viewer := users_testing.CreateTestUser(users_enums.UserRoleMember)
	globalAdmin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	project, _ := projects_testing.CreateTestProjectWithToken("Channel list", owner.Token, router)
	otherProject, _ := projects_testing.CreateTestProjectWithToken("Channel list other", owner.Token, router)
	projects_testing.AddMemberToProject(project, viewer, users_enums.ProjectRoleViewer, owner.Token, router)
	var channel alerting.SettingsChannel
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/settings/channels", "Bearer "+globalAdmin.Token,
		alerting.SettingsChannelRequest{
			Name: "oncall " + project.ID.String()[:8], Kind: "email",
			Config:     &alerting.ChannelConfig{To: []string{"oncall@example.com"}},
			ProjectIDs: []string{project.ID.String(), otherProject.ID.String()},
		}, http.StatusOK, &channel)
	t.Cleanup(func() { storage.GetDb().Exec("DELETE FROM notification_channels WHERE id = ?", channel.ID) })
	base := "/api/v1/projects/" + project.ID.String()

	listed := test_utils.MakeGetRequest(t, router, base+"/notification-channels", "Bearer "+viewer.Token,
		http.StatusOK)
	assert.Contains(t, string(listed.Body), channel.ID.String())
	assert.NotContains(t, string(listed.Body), "oncall@example.com")
	assert.NotContains(t, string(listed.Body), otherProject.ID.String())

	var ownerView []alerting.NotificationChannel
	test_utils.MakeGetRequestAndUnmarshal(t, router, base+"/notification-channels", "Bearer "+owner.Token,
		http.StatusOK, &ownerView)
	var found *alerting.NotificationChannel
	for _, c := range ownerView {
		if c.ID == channel.ID {
			found = &c
		}
	}
	require.NotNil(t, found)
	assert.Equal(t, "oncall@example.com", found.Target, "rule editors see where it sends")
	assert.Equal(t, alerting.ProjectIDs{project.ID.String()}, found.ProjectIDs)
}
