package synthetics_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"obzly/internal/cache"
	"obzly/internal/features/access_tokens"
	projects_testing "obzly/internal/features/projects/testing"
	"obzly/internal/features/synthetics"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	"obzly/internal/storage"
	"obzly/internal/synthetics/spec"
	test_utils "obzly/internal/util/testing"
)

func Test_ChecksAPI_AdminsManage_MembersRead_SecretsNeverReturned(t *testing.T) {
	router, admin, member := setup(t)
	destination := createDestination(t, router, admin, "results")

	request := httpCheckRequest("Orders API", destination.ID)
	request.Secrets = map[string]string{"ORDERS_TOKEN": "very-secret-value"}
	test_utils.MakePostRequest(t, router, "/api/v1/synthetics/checks", bearer(member), request, http.StatusForbidden)
	var created synthetics.CheckView
	response := test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/synthetics/checks", bearer(admin), request,
		http.StatusOK, &created)

	require.NotContains(t, string(response.Body), "very-secret-value")
	require.Equal(t, []synthetics.SecretState{{Name: "ORDERS_TOKEN", Stored: true}}, created.Secrets)
	require.Equal(t, 1, created.Version)
	require.Equal(t, "results", created.Destination.Name)
	require.Equal(t, "site-a", created.Destination.SiteID)
	var listed []synthetics.CheckView
	test_utils.MakeGetRequestAndUnmarshal(
		t,
		router,
		"/api/v1/synthetics/checks",
		bearer(admin),
		http.StatusOK,
		&listed,
	)
	require.Len(t, listed, 1)
	require.Equal(t, "Bearer ${env:ORDERS_TOKEN}", listed[0].Definition.Request.Headers["Authorization"])

	// Members read a summary: no headers (inline secrets), body, assertions
	// or secret names.
	for _, path := range []string{"/api/v1/synthetics/checks", "/api/v1/synthetics/checks/" + created.ID.String()} {
		response := test_utils.MakeGetRequest(t, router, path, bearer(member), http.StatusOK)
		require.NotContains(t, string(response.Body), "ORDERS_TOKEN", path)
		require.NotContains(t, string(response.Body), "assertions", path)
		require.Contains(t, string(response.Body), "https://api.example.com/health", path)
		require.Contains(t, string(response.Body), created.Destination.ProjectID, path)
	}

	duplicate := httpCheckRequest("orders api", destination.ID)
	response = test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/synthetics/checks",
		bearer(admin),
		duplicate,
		http.StatusBadRequest,
	)
	require.Contains(t, string(response.Body), "already exists")

	tooOften := httpCheckRequest("Too often", destination.ID)
	tooOften.IntervalSeconds = 10
	response = test_utils.MakePostRequest(
		t,
		router,
		"/api/v1/synthetics/checks",
		bearer(admin),
		tooOften,
		http.StatusBadRequest,
	)
	require.Contains(t, string(response.Body), "at least 30s")
}

// Saves carry the version the editor loaded, so one admin can't silently
// overwrite another's change.
func Test_UpdateCheck_WithAStaleVersion_Returns409(t *testing.T) {
	router, admin, _ := setup(t)
	destination := createDestination(t, router, admin, "results")
	created := createCheck(t, router, admin, httpCheckRequest("Health", destination.ID))
	url := "/api/v1/synthetics/checks/" + created.ID.String()

	first := httpCheckRequest("Health", destination.ID)
	first.Version, first.IntervalSeconds = created.Version, 120
	var updated synthetics.CheckView
	test_utils.MakePutRequestAndUnmarshal(t, router, url, bearer(admin), first, http.StatusOK, &updated)
	second := httpCheckRequest("Health", destination.ID)
	second.Version = created.Version
	response := test_utils.MakePutRequest(t, router, url, bearer(admin), second, http.StatusConflict)

	require.Equal(t, 2, updated.Version)
	require.Equal(t, 120, updated.IntervalSeconds)
	require.Contains(t, string(response.Body), "changed by someone else")
	require.Equal(t, []synthetics.SecretState{{Name: "ORDERS_TOKEN", Stored: false}}, updated.Secrets)
}

func Test_CloneCheck_CopiesSecrets_AsADisabledCopy(t *testing.T) {
	router, admin, _ := setup(t)
	destination := createDestination(t, router, admin, "results")
	request := httpCheckRequest("Checkout", destination.ID)
	request.Secrets = map[string]string{"ORDERS_TOKEN": "s3cret"}
	original := createCheck(t, router, admin, request)

	var clone, second synthetics.CheckView
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/synthetics/checks/"+original.ID.String()+"/clone",
		bearer(admin), nil, http.StatusOK, &clone)
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/synthetics/checks/"+original.ID.String()+"/clone",
		bearer(admin), nil, http.StatusOK, &second)

	require.Equal(t, "Checkout (copy)", clone.Name)
	require.Equal(t, "Checkout (copy) 2", second.Name)
	require.False(t, clone.Enabled)
	require.Equal(t, original.Definition, clone.Definition)
	require.Equal(t, []synthetics.SecretState{{Name: "ORDERS_TOKEN", Stored: true}}, clone.Secrets)
}

func Test_ProbeConfig_ServesItsLocationsChecksWithSecrets_AndAnswers304WhenUnchanged(t *testing.T) {
	router, admin, _ := setup(t)
	destination := createDestination(t, router, admin, "results")
	everywhere := httpCheckRequest("Everywhere", destination.ID)
	everywhere.Secrets = map[string]string{"ORDERS_TOKEN": "s3cret"}
	createCheck(t, router, admin, everywhere)
	elsewhere := httpCheckRequest("Elsewhere", destination.ID)
	elsewhere.Locations = []string{"aws-us-east-1"}
	createCheck(t, router, admin, elsewhere)
	disabled := httpCheckRequest("Disabled", destination.ID)
	disabled.Enabled = false
	createCheck(t, router, admin, disabled)
	probe := createProbe(t, router, admin, "probe-euw1-01", "aws-eu-west-1")

	first := probeRequest(t, router, probe.Token, "GET", "/api/v1/synthetics/probe/config", nil, nil, http.StatusOK)
	var config spec.ProbeConfig
	require.NoError(t, json.Unmarshal(first.Body, &config))
	etag := first.Headers.Get("ETag")
	probeRequest(t, router, probe.Token, "GET", "/api/v1/synthetics/probe/config", nil,
		map[string]string{"If-None-Match": etag}, http.StatusNotModified)

	require.Equal(t, "aws-eu-west-1", config.Probe.Location)
	require.Len(t, config.Checks, 1)
	require.Equal(t, "Everywhere", config.Checks[0].Name)
	require.Equal(t, "Bearer s3cret", config.Checks[0].Definition.Request.Headers["Authorization"])
	require.Equal(
		t,
		"https://api.example.com/health",
		config.Checks[0].Target,
		"labels come from the unresolved target",
	)
	require.Equal(t, []string{"s3cret"}, config.Checks[0].Redact, "the probe keeps secret values out of results")
	require.Equal(
		t,
		[]spec.Destination{
			{ID: destination.ID.String(), OTLPURL: "https://site-a.example.com", APIKey: "lb_destination_key_1234"}, // gitleaks:allow (test fixture)
		},
		config.Destinations,
	)
	require.NotEmpty(t, etag)

	// The config is cached until something it comes from changes.
	createCheck(t, router, admin, httpCheckRequest("Added later", destination.ID))
	changed := probeRequest(t, router, probe.Token, "GET", "/api/v1/synthetics/probe/config", nil,
		map[string]string{"If-None-Match": etag}, http.StatusOK)
	require.NotEqual(t, etag, changed.Headers.Get("ETag"))
	require.NoError(t, json.Unmarshal(changed.Body, &config))
	require.Len(t, config.Checks, 2)
	var updated synthetics.Destination
	test_utils.MakePutRequestAndUnmarshal(t, router, "/api/v1/synthetics/destinations/"+destination.ID.String(),
		bearer(admin), synthetics.DestinationRequest{
			Name: "results", SiteID: "site-a", ProjectID: destination.ProjectID,
			OTLPURL: "https://site-a.example.com", APIKey: "lb_rotated_key_5678", // gitleaks:allow (test fixture)
		}, http.StatusOK, &updated)
	rotated := probeRequest(t, router, probe.Token, "GET", "/api/v1/synthetics/probe/config", nil,
		map[string]string{"If-None-Match": changed.Headers.Get("ETag")}, http.StatusOK)
	require.Contains(t, string(rotated.Body), "lb_rotated_key_5678")
}

func Test_ProbeToken_Rotated_OldTokenWorksUntilItsGraceEnds(t *testing.T) {
	router, admin, _ := setup(t)
	probe := createProbe(t, router, admin, "probe-eu-1", "aws-eu-west-1")
	var rotated synthetics.ProbeTokenResponse
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/synthetics/probes/"+probe.Probe.ID.String()+"/token",
		bearer(admin), nil, http.StatusOK, &rotated)

	probeRequest(t, router, rotated.Token, http.MethodGet, "/api/v1/synthetics/probe/config", nil, nil, http.StatusOK)
	probeRequest(t, router, probe.Token, http.MethodGet, "/api/v1/synthetics/probe/config", nil, nil, http.StatusOK)

	// A day later (the cache holds a token for 30 s; a new token isn't cached).
	require.NoError(t, storage.GetDb().Exec(
		"UPDATE synthetic_probe_tokens SET expires_at = NOW() - INTERVAL '1 second' WHERE expires_at IS NOT NULL").
		Error)
	client := cache.GetCache()
	require.NoError(t, client.Do(context.Background(),
		client.B().Del().Key("synthetics:probe-token:"+access_tokens.HashToken(probe.Token)).Build()).Error())
	probeRequest(t, router, probe.Token, http.MethodGet, "/api/v1/synthetics/probe/config", nil, nil,
		http.StatusUnauthorized)
	probeRequest(t, router, rotated.Token, http.MethodGet, "/api/v1/synthetics/probe/config", nil, nil, http.StatusOK)
}

func Test_ProbeToken_RotatedWithRevokeOld_OldTokenStopsAtOnce(t *testing.T) {
	router, admin, _ := setup(t)
	probe := createProbe(t, router, admin, "probe-leaked", "aws-eu-west-1")
	probeRequest(t, router, probe.Token, http.MethodGet, "/api/v1/synthetics/probe/config", nil, nil, http.StatusOK)

	var rotated synthetics.ProbeTokenResponse
	test_utils.MakePostRequestAndUnmarshal(t, router,
		"/api/v1/synthetics/probes/"+probe.Probe.ID.String()+"/token?revokeOld=true", bearer(admin), nil,
		http.StatusOK, &rotated)

	probeRequest(t, router, probe.Token, http.MethodGet, "/api/v1/synthetics/probe/config", nil, nil,
		http.StatusUnauthorized)
	probeRequest(t, router, rotated.Token, http.MethodGet, "/api/v1/synthetics/probe/config", nil, nil, http.StatusOK)
}

func Test_ProbeToken_Unknown_IsRefused_AndRememberedBriefly(t *testing.T) {
	router, _, _ := setup(t)
	unknown := spec.TokenPrefix + strings.Repeat("ab", 24)

	probeRequest(t, router, unknown, http.MethodGet, "/api/v1/synthetics/probe/config", nil, nil,
		http.StatusUnauthorized)

	client := cache.GetCache()
	cached, err := client.Do(context.Background(),
		client.B().Get().Key("synthetics:probe-token:"+access_tokens.HashToken(unknown)).Build()).ToString()
	require.NoError(t, err)
	require.Equal(t, "invalid", cached, "guessing doesn't cost a database query each time")
	probeRequest(t, router, unknown, http.MethodGet, "/api/v1/synthetics/probe/config", nil, nil,
		http.StatusUnauthorized)
}

func Test_ProbeGroup_TooManyInstances_NewOnesRefused_OldOnesForgotten(t *testing.T) {
	router, admin, _ := setup(t)
	probe := createProbe(t, router, admin, "probe-crowded", "aws-ap-south-1")
	db := storage.GetDb()
	require.NoError(t, db.Exec(`INSERT INTO synthetic_probe_members (probe_id, name, version, checks, first_seen,
		last_seen_at) VALUES (?, 'long-gone', 'v1', 0, NOW() - INTERVAL '3 days', NOW() - INTERVAL '61 minutes')`,
		probe.Probe.ID).Error)
	require.NoError(t, db.Exec(`INSERT INTO synthetic_probe_members (probe_id, name, version, checks, first_seen,
		last_seen_at) SELECT ?, 'member-' || n, 'v1', 0, NOW(), NOW() FROM generate_series(1, 64) AS n`,
		probe.Probe.ID).Error)
	poll := func(instance string, status int) {
		probeRequest(t, router, probe.Token, http.MethodGet, "/api/v1/synthetics/probe/config", nil,
			map[string]string{spec.HeaderInstance: instance}, status)
	}

	poll("member-1", http.StatusOK)
	poll("invented-65th", http.StatusTooManyRequests)

	var names []string
	require.NoError(t, db.Raw("SELECT name FROM synthetic_probe_members WHERE probe_id = ?", probe.Probe.ID).
		Scan(&names).Error)
	require.Len(t, names, 64)
	require.NotContains(t, names, "long-gone", "members unseen for an hour are forgotten")
	require.NotContains(t, names, "invented-65th")
}

func Test_ProbeGroups_MembersSilentForAnHour_AreNoLongerListed(t *testing.T) {
	router, admin, _ := setup(t)
	require.NoError(t, storage.GetDb().Exec("DELETE FROM synthetic_probe_members").Error)
	probe := createProbe(t, router, admin, "probe-spot", "aws-ap-south-1")
	for name, silent := range map[string]string{"spot-reclaimed": "2 hours", "restarting": "30 minutes"} {
		require.NoError(t, storage.GetDb().Exec(`INSERT INTO synthetic_probe_members (probe_id, name, version, checks,
			first_seen, last_seen_at) VALUES (?, ?, 'v1', 0, NOW() - INTERVAL '1 day', NOW() - ?::interval)`,
			probe.Probe.ID, name, silent).Error)
	}

	var groups []synthetics.ProbeView
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/synthetics/probes", bearer(admin), http.StatusOK, &groups)

	require.Len(t, groups, 1)
	require.Len(t, groups[0].Members, 1)
	require.Equal(t, "restarting", groups[0].Members[0].Name)
	require.False(t, groups[0].Members[0].Live)
}

func Test_ProbeGroup_TooManyOpenTaskPolls_TheRestAreTurnedAway(t *testing.T) {
	router, admin, _ := setup(t)
	probe := createProbe(t, router, admin, "probe-polls", "aws-eu-west-1")
	var turnedAway atomic.Int32
	var wait sync.WaitGroup
	for range 70 {
		wait.Go(func() {
			request := httptest.NewRequest(http.MethodGet, "/api/v1/synthetics/probe/tasks?wait=2", nil)
			request.Header.Set("Authorization", "Bearer "+probe.Token)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code == http.StatusTooManyRequests {
				turnedAway.Add(1)
			}
		})
	}
	wait.Wait()

	require.EqualValues(t, 6, turnedAway.Load(), "64 wait at once, per replica")
}

func Test_ProbeRoutes_BodyOverOneMB_Returns413(t *testing.T) {
	router, admin, _ := setup(t)
	probe := createProbe(t, router, admin, "probe-big", "aws-eu-west-1")
	huge := spec.RunResult{BodySnippet: strings.Repeat("x", 2<<20)}

	probeRequest(t, router, probe.Token, http.MethodPost, "/api/v1/synthetics/probe/tasks/"+uuid.NewString()+"/result",
		huge, nil, http.StatusRequestEntityTooLarge)
}

func Test_ProbeGroup_InstancesJoin_AndEachLearnsTheLiveMembers(t *testing.T) {
	router, admin, _ := setup(t)
	require.NoError(t, storage.GetDb().Exec("DELETE FROM synthetic_probe_members").Error)
	probe := createProbe(t, router, admin, "probe-ap-1", "aws-ap-south-1")
	poll := func(instance string) string {
		response := probeRequest(t, router, probe.Token, http.MethodGet, "/api/v1/synthetics/probe/tasks?wait=1", nil,
			map[string]string{spec.HeaderInstance: instance, spec.HeaderChecks: "4"}, http.StatusNoContent)
		return response.Headers.Get(spec.HeaderMembers)
	}

	poll("obzly-probe-7c9d-0")
	poll("obzly-probe-7c9d-1")
	require.Equal(t, "obzly-probe-7c9d-0,obzly-probe-7c9d-1", poll("obzly-probe-7c9d-0"))
	probeRequest(t, router, probe.Token, http.MethodGet, "/api/v1/synthetics/probe/tasks?wait=1", nil,
		map[string]string{spec.HeaderInstance: "bad name!"}, http.StatusNoContent)

	var groups []synthetics.ProbeView
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/synthetics/probes", bearer(admin), http.StatusOK, &groups)
	require.Len(t, groups, 1)
	names := []string{}
	for _, member := range groups[0].Members {
		names = append(names, member.Name)
		require.True(t, member.Live)
	}
	require.Equal(t, []string{"default", "obzly-probe-7c9d-0", "obzly-probe-7c9d-1"}, names,
		"an unusable name counts as the group's default instance")

	require.NoError(t, storage.GetDb().Exec(
		"UPDATE synthetic_probe_members SET last_seen_at = NOW() - INTERVAL '2 minutes' WHERE name = ?",
		"obzly-probe-7c9d-1").Error)
	require.NotContains(t, poll("obzly-probe-7c9d-0"), "obzly-probe-7c9d-1", "a silent member leaves the group")
}

func Test_ProbeGroup_AnInstanceLeaves_TheOthersLearnAtOnce_ItStaysListed(t *testing.T) {
	router, admin, _ := setup(t)
	require.NoError(t, storage.GetDb().Exec("DELETE FROM synthetic_probe_members").Error)
	probe := createProbe(t, router, admin, "probe-eu-1", "aws-eu-west-1")
	poll := func(instance string) string {
		response := probeRequest(t, router, probe.Token, http.MethodGet, "/api/v1/synthetics/probe/tasks?wait=1", nil,
			map[string]string{spec.HeaderInstance: instance}, http.StatusNoContent)
		return response.Headers.Get(spec.HeaderMembers)
	}
	poll("obzly-probe-0")
	require.Equal(t, "obzly-probe-0,obzly-probe-1", poll("obzly-probe-1"))

	probeRequest(t, router, probe.Token, http.MethodPost, "/api/v1/synthetics/probe/leave", nil,
		map[string]string{spec.HeaderInstance: "obzly-probe-1"}, http.StatusOK)

	require.Equal(t, "obzly-probe-0", poll("obzly-probe-0"))
	var groups []synthetics.ProbeView
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/synthetics/probes", bearer(admin), http.StatusOK, &groups)
	require.Len(t, groups, 1)
	live := map[string]bool{}
	for _, member := range groups[0].Members {
		live[member.Name] = member.Live
	}
	require.Equal(t, map[string]bool{"obzly-probe-0": true, "obzly-probe-1": false}, live)
	require.Equal(t, "obzly-probe-0,obzly-probe-1", poll("obzly-probe-1"), "it rejoins on its next poll")
	probeRequest(t, router, "obz_prb_unknown", http.MethodPost, "/api/v1/synthetics/probe/leave", nil, nil,
		http.StatusUnauthorized)
}

func Test_Test_QueuedForAProbe_ResultReachesTheEditor(t *testing.T) {
	router, admin, _ := setup(t)
	probe := createProbe(t, router, admin, "probe-blr-01", "office-blr")
	other := createProbe(t, router, admin, "probe-other", "aws-us-east-1")
	probeRequest(t, router, probe.Token, "GET", "/api/v1/synthetics/probe/config", nil, nil, http.StatusOK)

	var run synthetics.TestRun
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/synthetics/tests", bearer(admin), synthetics.TestRequest{
		Check: synthetics.CheckRequest{
			Name:       "Draft",
			Definition: spec.Definition{Type: spec.TypeTCP, Target: "db:5432"},
		},
		Locations: []string{"office-blr", "aws-ap-south-1"},
	}, http.StatusOK, &run)
	require.Len(t, run.Tasks, 2)
	require.Equal(t, synthetics.TaskQueued, run.Tasks[0].Status)
	require.Equal(t, synthetics.TaskNoProbe, run.Tasks[1].Status)

	polled := probeRequest(
		t,
		router,
		probe.Token,
		"GET",
		"/api/v1/synthetics/probe/tasks?wait=2",
		nil,
		nil,
		http.StatusOK,
	)
	var task spec.Task
	require.NoError(t, json.Unmarshal(polled.Body, &task))
	require.Equal(t, run.Tasks[0].ID, task.ID)
	require.Equal(t, "db:5432", task.Check.Definition.Target)
	result := spec.RunResult{
		Check:      "Draft",
		Type:       spec.TypeTCP,
		Status:     spec.StatusUp,
		DurationMs: 3,
		StartedAt:  time.Now().UTC(),
	}
	probeRequest(
		t,
		router,
		other.Token,
		"POST",
		"/api/v1/synthetics/probe/tasks/"+task.ID+"/result",
		result,
		nil,
		http.StatusNotFound,
	)
	probeRequest(
		t,
		router,
		probe.Token,
		"POST",
		"/api/v1/synthetics/probe/tasks/"+task.ID+"/result",
		result,
		nil,
		http.StatusOK,
	)
	probeRequest(t, router, probe.Token, "GET", "/api/v1/synthetics/probe/tasks?wait=1", nil, nil, http.StatusNoContent)

	var done synthetics.TestRun
	test_utils.MakeGetRequestAndUnmarshal(
		t,
		router,
		"/api/v1/synthetics/tests/"+run.ID,
		bearer(admin),
		http.StatusOK,
		&done,
	)
	require.Equal(t, synthetics.TaskDone, done.Tasks[0].Status)
	require.Equal(t, "office-blr", done.Tasks[0].Result.Location, "the server stamps the probe's own location")
	require.Equal(t, spec.StatusUp, done.Tasks[0].Result.Status)
}

func Test_Test_FromTheControlPlane_RefusesLoopbackPrivateAndMetadataAddresses(t *testing.T) {
	router, admin, _ := setup(t)

	for _, definition := range []spec.Definition{
		{Type: spec.TypeTCP, Target: "127.0.0.1:5432"},
		{Type: spec.TypeTCP, Target: "10.0.0.5:6379"},
		{Type: spec.TypeHTTP, Request: &spec.HTTPRequest{URL: "http://172.16.0.10:8222/varz"}},
		{Type: spec.TypeHTTP, Request: &spec.HTTPRequest{URL: "http://[64:ff9b::a9fe:a9fe]/latest/meta-data/"}},
		{Type: spec.TypeDNS, Target: "example.com", DNS: &spec.DNSOptions{RecordType: "A", Resolver: "10.96.0.10"}},
	} {
		var run synthetics.TestRun
		test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/synthetics/tests", bearer(admin),
			synthetics.TestRequest{Check: synthetics.CheckRequest{Definition: definition}, FromControlPlane: true},
			http.StatusOK, &run)

		require.Len(t, run.Tasks, 1)
		require.True(t, run.Tasks[0].Result.FromControlPlane)
		require.Equal(t, spec.StatusDown, run.Tasks[0].Result.Status)
		require.Contains(t, run.Tasks[0].Result.Error, "not public")
	}
}

func Test_Import_TheSampleFile_PreviewApplyThenUnchanged(t *testing.T) {
	router, admin, _ := setup(t)
	createDestination(t, router, admin, "synthetics")
	data, err := os.ReadFile("../../../../docs/examples/synthetics-checks.yaml")
	require.NoError(t, err)
	request := synthetics.ImportRequest{
		YAML:    string(data),
		Secrets: map[string]string{"ORDERS_API_TOKEN": "token-value"},
	}

	var preview, applied, again synthetics.ImportPreview
	test_utils.MakePostRequestAndUnmarshal(
		t,
		router,
		"/api/v1/synthetics/checks/import/preview",
		bearer(admin),
		request,
		http.StatusOK,
		&preview,
	)
	var before []synthetics.CheckView
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/synthetics/checks", bearer(admin), http.StatusOK, &before)
	test_utils.MakePostRequestAndUnmarshal(
		t,
		router,
		"/api/v1/synthetics/checks/import",
		bearer(admin),
		request,
		http.StatusOK,
		&applied,
	)
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/synthetics/checks/import/preview", bearer(admin),
		synthetics.ImportRequest{YAML: string(data)}, http.StatusOK, &again)

	require.Len(t, preview.Create, 9)
	require.Empty(t, before, "a preview changes nothing")
	require.True(t, applied.Applied)
	require.Equal(t, []synthetics.SecretState{
		{Name: "OBZLY_SECRET_CHECKOUT_PASSWORD", Stored: false}, {Name: "ORDERS_API_TOKEN", Stored: true},
	}, applied.Secrets)
	require.Empty(t, again.Create)
	require.Empty(t, again.Update)
	require.Len(t, again.Unchanged, 9)

	changed := strings.Replace(string(data), "    interval: 300s\n", "    interval: 600s\n", 1)
	var update synthetics.ImportPreview
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/synthetics/checks/import/preview", bearer(admin),
		synthetics.ImportRequest{YAML: changed}, http.StatusOK, &update)
	require.Equal(
		t,
		[]synthetics.ImportChange{{Name: "Orders API - create quote", Changes: []string{"interval 300s → 600s"}}},
		update.Update,
	)

	exported := test_utils.MakeGetRequest(t, router, "/api/v1/synthetics/checks/export", bearer(admin), http.StatusOK)
	require.Contains(t, string(exported.Body), "${env:ORDERS_API_TOKEN}")
	require.NotContains(t, string(exported.Body), "token-value")
}

func Test_Import_AnyError_AppliesNothing_AndPruneDeletesTheRest(t *testing.T) {
	router, admin, _ := setup(t)
	destination := createDestination(t, router, admin, "synthetics")
	createCheck(t, router, admin, httpCheckRequest("Not in the file", destination.ID))
	broken := "apiVersion: obzly.com/synthetics/v1\nkind: MonitorList\nmonitors:\n" +
		"  - {name: Good, type: tcp, target: 'db:5432'}\n  - {name: Bad, type: tcp, target: nope}\n"
	pruning := "apiVersion: obzly.com/synthetics/v1\nkind: MonitorList\nprune: true\nmonitors:\n" +
		"  - {name: Good, type: tcp, target: 'db:5432'}\n"

	var failed, pruned synthetics.ImportPreview
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/synthetics/checks/import", bearer(admin),
		synthetics.ImportRequest{YAML: broken}, http.StatusOK, &failed)
	var afterFailure []synthetics.CheckView
	test_utils.MakeGetRequestAndUnmarshal(
		t,
		router,
		"/api/v1/synthetics/checks",
		bearer(admin),
		http.StatusOK,
		&afterFailure,
	)
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/synthetics/checks/import", bearer(admin),
		synthetics.ImportRequest{YAML: pruning}, http.StatusOK, &pruned)
	var afterPrune []synthetics.CheckView
	test_utils.MakeGetRequestAndUnmarshal(
		t,
		router,
		"/api/v1/synthetics/checks",
		bearer(admin),
		http.StatusOK,
		&afterPrune,
	)

	require.False(t, failed.Applied)
	require.Len(t, failed.Errors, 1)
	require.Equal(t, 5, failed.Errors[0].Line)
	require.Len(t, afterFailure, 1)
	require.True(t, pruned.Applied)
	require.Equal(t, []string{"Not in the file"}, pruned.Delete)
	require.Len(t, afterPrune, 1)
	require.Equal(t, "Good", afterPrune[0].Name)
}

func Test_DeleteDestination_StillInUse_Returns409(t *testing.T) {
	router, admin, _ := setup(t)
	destination := createDestination(t, router, admin, "results")
	createCheck(t, router, admin, httpCheckRequest("Uses it", destination.ID))

	test_utils.MakeDeleteRequest(
		t,
		router,
		"/api/v1/synthetics/destinations/"+destination.ID.String(),
		bearer(admin),
		http.StatusConflict,
	)
}

// setup empties the synthetics tables (only this package uses them) and
// returns a router with the admin and probe APIs.
func setup(t *testing.T) (*gin.Engine, string, string) {
	t.Helper()
	db := storage.GetDb()
	require.NoError(t, db.Exec("DELETE FROM cloud_connections").Error)
	require.NoError(t, db.Exec("DELETE FROM synthetic_checks").Error)
	require.NoError(t, db.Exec("DELETE FROM synthetic_destinations").Error)
	require.NoError(t, db.Exec("DELETE FROM synthetic_probes").Error)
	router := projects_testing.CreateTestRouter(synthetics.GetController())
	synthetics.GetProbeController().RegisterRoutes(router)
	return router, users_testing.CreateTestUser(users_enums.UserRoleAdmin).Token,
		users_testing.CreateTestUser(users_enums.UserRoleMember).Token
}

func createDestination(t *testing.T, router *gin.Engine, admin, name string) synthetics.Destination {
	t.Helper()
	var destination synthetics.Destination
	test_utils.MakePostRequestAndUnmarshal(
		t,
		router,
		"/api/v1/synthetics/destinations",
		bearer(admin),
		synthetics.DestinationRequest{
			Name:      name,
			SiteID:    "site-a",
			ProjectID: uuid.NewString(),
			OTLPURL:   "https://site-a.example.com/",
			APIKey:    "lb_destination_key_1234", // gitleaks:allow (test fixture)
		},
		http.StatusOK,
		&destination,
	)
	require.Equal(t, "lb_…1234", destination.APIKeyHint)
	return destination
}

func createCheck(t *testing.T, router *gin.Engine, admin string, request synthetics.CheckRequest) synthetics.CheckView {
	t.Helper()
	var check synthetics.CheckView
	test_utils.MakePostRequestAndUnmarshal(
		t,
		router,
		"/api/v1/synthetics/checks",
		bearer(admin),
		request,
		http.StatusOK,
		&check,
	)
	return check
}

func createProbe(t *testing.T, router *gin.Engine, admin, name, location string) synthetics.ProbeTokenResponse {
	t.Helper()
	var probe synthetics.ProbeTokenResponse
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/synthetics/probes", bearer(admin),
		synthetics.ProbeRequest{Name: name, Location: location}, http.StatusOK, &probe)
	require.True(t, strings.HasPrefix(probe.Token, spec.TokenPrefix))
	return probe
}

func httpCheckRequest(name string, destinationID uuid.UUID) synthetics.CheckRequest {
	return synthetics.CheckRequest{
		Name: name, Enabled: true, DestinationID: destinationID,
		Definition: spec.Definition{Type: spec.TypeHTTP, Request: &spec.HTTPRequest{
			URL:     "https://api.example.com/health",
			Headers: map[string]string{"Authorization": "Bearer ${env:ORDERS_TOKEN}"},
		}, Assertions: []spec.Assertion{{Kind: spec.AssertStatus, Op: spec.OpEquals, Value: "200"}}},
	}
}

func probeRequest(
	t *testing.T,
	router *gin.Engine,
	token, method, url string,
	body any,
	headers map[string]string,
	status int,
) *test_utils.TestResponse {
	t.Helper()
	return test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: method, URL: url, Body: body, AuthToken: "Bearer " + token, Headers: headers, ExpectedStatus: status,
	})
}

func bearer(token string) string {
	return "Bearer " + token
}
