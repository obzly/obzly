package webanalytics

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"testing"
	"time"

	"obzly/internal/features/audit_logs"
	logs_core "obzly/internal/features/logs/core"
	projects_controllers "obzly/internal/features/projects/controllers"
	projects_models "obzly/internal/features/projects/models"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	"obzly/internal/storage"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_GetReport_MergesBuiltHoursWithTheLiveHour(t *testing.T) {
	env := newTestEnv(t)
	liveStart := env.now().Truncate(time.Hour)
	closed := liveStart.Add(-2 * time.Hour)
	storeLogs(t, env.project.ID,
		accessLog(closed.Add(5*time.Minute), "", nginxRequest("198.51.100.1", "/", "200", "web-1")),
		accessLog(closed.Add(6*time.Minute), "", nginxRequest("198.51.100.2", "/cart", "500", "web-1")),
		accessLog(liveStart, "", nginxRequest("198.51.100.1", "/", "200", "web-1")),
	)
	settings := env.settings(t)
	require.NoError(t, env.builder.BuildHour(context.Background(), settings, closed))

	report := env.report(t, liveStart.Add(-3*time.Hour), time.Time{}, "")

	assert.True(t, report.Rollups.Used)
	assert.Equal(t, 1, report.Rollups.Hours, "the built hour")
	assert.Equal(t, 1, report.Rollups.Missing, "3 hours ago isn't built and is too old to read live")
	assert.Equal(t, liveStart.Add(-time.Hour), report.Live.From, "the unbuilt last closed hour is read live too")
	assert.Equal(t, int64(3), report.Totals.Requests, "2 from the rollup + 1 live")
	assert.Equal(t, int64(1), report.Totals.Failed)
	require.NotNil(t, report.Totals.Visitors)
	assert.Equal(t, int64(2), *report.Totals.Visitors, "198.51.100.1 is in both parts, counted once")
	assert.Equal(t, "/", report.Panels.Pages[0].Key)
	assert.Equal(t, int64(2), report.Panels.Pages[0].Hits, "the page's hits add up across the parts")
	require.NotEmpty(t, report.Series)
	assert.True(t, report.Series[len(report.Series)-1].Live)
	assert.False(t, report.Series[0].Live)
	var seriesTotal int64
	for _, bucket := range report.Series {
		seriesTotal += bucket.Requests
	}
	assert.Equal(t, int64(3), seriesTotal)
}

func Test_GetReport_WithASourceSelected_CountsOnlyThatSourceAndItsMerges(t *testing.T) {
	env := newTestEnv(t)
	closed := env.now().Truncate(time.Hour).Add(-2 * time.Hour)
	storeLogs(t, env.project.ID,
		accessLog(closed.Add(time.Minute), "", nginxRequest("198.51.100.1", "/a", "200", "web-1")),
		accessLog(closed.Add(2*time.Minute), "", nginxRequest("198.51.100.2", "/b", "200", "web-2")),
		accessLog(closed.Add(3*time.Minute), "", nginxRequest("198.51.100.3", "/c", "404", "web-2")),
	)
	require.NoError(t, env.builder.BuildHour(context.Background(), env.settings(t), closed))
	web1, web2 := sourceKey("service\n\nnginx\nweb-1"), sourceKey("service\n\nnginx\nweb-2")

	all := env.report(t, closed, closed.Add(time.Hour), "")
	require.Len(t, all.Sources, 2)
	assert.Equal(t, web2, all.Sources[0].Key, "busiest first")
	assert.InDelta(t, 2.0/3, all.Sources[0].Share, 1e-9)
	assert.Equal(t, "nginx@web-2", all.Sources[0].Name)
	assert.Equal(t, "Nginx", all.Sources[0].Kind)
	assert.Equal(t, "combined", all.Sources[0].Format)
	assert.Equal(t, int64(3), all.Totals.Requests)

	one := env.report(t, closed, closed.Add(time.Hour), web1)
	assert.Equal(t, []string{web1}, one.Selected)
	assert.Equal(t, int64(1), one.Totals.Requests)
	assert.Equal(t, "/a", one.Panels.Pages[0].Key)
	assert.Len(t, one.Sources, 2, "the picker still lists every source")

	merge := web1
	test_utils.MakePutRequest(t, env.router, env.url("settings"), "Bearer "+env.ownerToken,
		UpdateSettingsDTO{Sources: []UpdateSourceDTO{{Key: web2, Name: "", MergedInto: merge}}}, http.StatusOK)
	merged := env.report(t, closed, closed.Add(time.Hour), web1)
	require.Len(t, merged.Sources, 1, "web-2 counts as web-1 now")
	assert.Equal(t, []string{web2}, merged.Sources[0].MergedFrom)
	assert.Equal(t, int64(3), merged.Totals.Requests)
}

func Test_GetReport_ForAShortRange_ReadsRawLogsWithAPreviousPeriod(t *testing.T) {
	env := newTestEnv(t)
	now := env.now()
	storeLogs(t, env.project.ID,
		accessLog(now.Add(-10*time.Minute), "", nginxRequest("198.51.100.1", "/", "200", "web-1")),
		accessLog(now.Add(-70*time.Minute), "", nginxRequest("198.51.100.1", "/", "200", "web-1")),
		accessLog(now.Add(-80*time.Minute), "", nginxRequest("198.51.100.1", "/", "200", "web-1")),
	)

	report := env.report(t, now.Add(-time.Hour), time.Time{}, "")

	assert.False(t, report.Rollups.Used)
	assert.Equal(t, int64(300), report.BucketSeconds)
	assert.Equal(t, int64(1), report.Totals.Requests)
	require.NotNil(t, report.Previous)
	assert.Equal(t, int64(2), report.Previous.Requests, "the hour before")
}

func Test_GetReport_ByRole_OnlyProjectMembersMayRead(t *testing.T) {
	env := newTestEnv(t)
	viewer := users_testing.CreateTestUser(users_enums.UserRoleMember)
	outsider := users_testing.CreateTestUser(users_enums.UserRoleMember)
	projects_testing.AddMemberToProject(env.project, viewer, users_enums.ProjectRoleViewer, env.ownerToken, env.router)

	test_utils.MakeGetRequest(t, env.router, env.url("report"), "Bearer "+viewer.Token, http.StatusOK)
	test_utils.MakeGetRequest(t, env.router, env.url("settings"), "Bearer "+viewer.Token, http.StatusOK)
	test_utils.MakeGetRequest(t, env.router, env.url("report"), "Bearer "+outsider.Token, http.StatusForbidden)
	test_utils.MakeGetRequest(t, env.router, env.url("settings"), "Bearer "+outsider.Token, http.StatusForbidden)
	test_utils.MakeGetRequest(t, env.router, env.url("report"), "", http.StatusUnauthorized)
	test_utils.MakeGetRequest(t, env.router, "/api/v1/projects/not-a-uuid/web-analytics/report",
		"Bearer "+env.ownerToken, http.StatusBadRequest)
	test_utils.MakeGetRequest(t, env.router, env.url("report")+"?from=yesterday", "Bearer "+env.ownerToken,
		http.StatusBadRequest)
	now := env.now()
	backwards := url.Values{"from": {now.Format(time.RFC3339)}, "to": {now.Add(-time.Hour).Format(time.RFC3339)}}
	test_utils.MakeGetRequest(t, env.router, env.url("report")+"?"+backwards.Encode(), "Bearer "+env.ownerToken,
		http.StatusBadRequest)
}

func Test_UpdateSettings_ByRole_OnlyAdminsMayChangeThemAndChangesAreAudited(t *testing.T) {
	env := newTestEnv(t)
	viewer := users_testing.CreateTestUser(users_enums.UserRoleMember)
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)
	projects_testing.AddMemberToProject(env.project, viewer, users_enums.ProjectRoleViewer, env.ownerToken, env.router)
	projects_testing.AddMemberToProject(env.project, member, users_enums.ProjectRoleMember, env.ownerToken, env.router)
	hash := ipModeHash
	keep := 90
	body := UpdateSettingsDTO{IPMode: &hash, RetentionDays: &keep}

	test_utils.MakePutRequest(t, env.router, env.url("settings"), "Bearer "+viewer.Token, body, http.StatusForbidden)
	test_utils.MakePutRequest(t, env.router, env.url("settings"), "Bearer "+member.Token, body, http.StatusForbidden)

	var viewerSettings SettingsDTO
	test_utils.MakeGetRequestAndUnmarshal(t, env.router, env.url("settings"), "Bearer "+viewer.Token, http.StatusOK,
		&viewerSettings)
	assert.False(t, viewerSettings.CanManage)
	assert.Equal(t, ipModeTruncate, viewerSettings.IPMode, "truncated IPs by default")
	assert.True(t, viewerSettings.ExcludeBots)

	var saved SettingsDTO
	test_utils.MakePutRequestAndUnmarshal(t, env.router, env.url("settings"), "Bearer "+env.ownerToken, body,
		http.StatusOK, &saved)
	assert.True(t, saved.CanManage)
	assert.Equal(t, ipModeHash, saved.IPMode)
	assert.Equal(t, 90, saved.RetentionDays)
	assert.NotEmpty(t, saved.Schema)

	var messages []string
	require.NoError(t, storage.GetDb().Model(&audit_logs.AuditLog{}).Where("project_id = ?", env.project.ID).
		Pluck("message", &messages).Error)
	assert.Contains(t, messages, "Web analytics settings changed: visitor IPs truncate → hash")
	assert.Contains(t, messages, "Web analytics settings changed: rollup retention like the logs → 90 days")

	bad := "everything"
	test_utils.MakePutRequest(t, env.router, env.url("settings"), "Bearer "+env.ownerToken,
		UpdateSettingsDTO{IPMode: &bad}, http.StatusBadRequest)
	test_utils.MakePutRequest(t, env.router, env.url("settings"), "Bearer "+env.ownerToken,
		UpdateSettingsDTO{Sources: []UpdateSourceDTO{{Key: "missing"}}}, http.StatusBadRequest)
}

func Test_UpdateSettings_WithStaticExtensions_KeepsOnlySafeOnes(t *testing.T) {
	env := newTestEnv(t)
	extensions := ".CSS, js; png ' OR 1=1 --  woff2"
	var saved SettingsDTO
	test_utils.MakePutRequestAndUnmarshal(t, env.router, env.url("settings"), "Bearer "+env.ownerToken,
		UpdateSettingsDTO{StaticExtensions: &extensions}, http.StatusOK, &saved)
	assert.Equal(t, "css js png or woff2", saved.StaticExtensions, "quotes, = and dashes are dropped")
}

func Test_BuildProject_BuildsClosedHoursOnceAndNotTheCurrentOne(t *testing.T) {
	env := newTestEnv(t)
	liveStart := env.now().Truncate(time.Hour)
	closed := liveStart.Add(-3 * time.Hour)
	storeLogs(t, env.project.ID,
		accessLog(closed.Add(time.Minute), "", nginxRequest("198.51.100.1", "/", "200", "web-1")),
		accessLog(closed.Add(2*time.Minute), "", nginxRequest("198.51.100.2", "/", "200", "web-2")),
		accessLog(liveStart, "", nginxRequest("198.51.100.3", "/", "200", "web-1")),
	)
	settings := env.settings(t)

	require.NoError(t, env.builder.BuildProject(context.Background(), settings))

	builds, err := repo.builds(context.Background(), env.project.ID, granularityHour, liveStart.Add(-48*time.Hour),
		liveStart.Add(time.Hour))
	require.NoError(t, err)
	assert.Contains(t, builds, closed)
	assert.NotContains(t, builds, liveStart, "the hour in progress is read live, never built")
	assert.Len(t, builds, hoursPerPass, "a pass builds at most hoursPerPass hours")
	for hour := range builds {
		assert.False(t, hour.After(lastBuildableHour(env.now())), "only hours closed for buildLag")
	}

	totals, err := repo.totalsOfBuckets(context.Background(), env.project.ID, granularityHour, []time.Time{closed})
	require.NoError(t, err)
	require.Len(t, totals, 2, "one row per source")
	for _, row := range totals {
		assert.Equal(t, int64(1), row.Requests)
	}
	firstBuild := builds[closed].BuiltAt

	require.NoError(t, env.builder.BuildProject(context.Background(), settings))
	again, err := repo.builds(context.Background(), env.project.ID, granularityHour, closed, closed.Add(time.Hour))
	require.NoError(t, err)
	assert.True(t, again[closed].BuiltAt.Equal(firstBuild), "a built hour isn't rebuilt")
}

func Test_BuildDays_WhenEveryHourOfADayIsBuilt_MergesThemIntoADay(t *testing.T) {
	env := newTestEnv(t)
	day := env.now().Truncate(24 * time.Hour).Add(-48 * time.Hour)
	storeLogs(t, env.project.ID,
		accessLog(day.Add(1*time.Hour), "", nginxRequest("198.51.100.1", "/", "200", "web-1")),
		accessLog(day.Add(5*time.Hour), "", nginxRequest("198.51.100.1", "/", "200", "web-1")),
		accessLog(day.Add(5*time.Hour+time.Minute), "", nginxRequest("198.51.100.9", "/x", "404", "web-1")),
	)
	settings := env.settings(t)
	for hour := day; hour.Before(day.Add(24 * time.Hour)); hour = hour.Add(time.Hour) {
		require.NoError(t, env.builder.BuildHour(context.Background(), settings, hour))
	}

	require.NoError(t, env.builder.buildDays(context.Background(), settings, day, day.Add(24*time.Hour)))

	totals, err := repo.totalsOfBuckets(context.Background(), env.project.ID, granularityDay, []time.Time{day})
	require.NoError(t, err)
	require.Len(t, totals, 1)
	assert.Equal(t, int64(3), totals[0].Requests)
	assert.Equal(t, int64(1), totals[0].NotFound)
	assert.Equal(t, int64(2), totals[0].aggregate().visitors.estimate(), "198.51.100.1 came back, counted once")
	keys, err := repo.keysOfBuckets(context.Background(), env.project.ID, granularityDay, []time.Time{day})
	require.NoError(t, err)
	hits := map[string]int64{}
	for _, row := range keys {
		if row.Dim == dimPage {
			hits[row.Key] = row.Hits
		}
	}
	assert.Equal(t, map[string]int64{"/": 2, "/x": 1}, hits)

	report := env.report(t, day.Add(-7*24*time.Hour), time.Time{}, "")
	assert.Equal(t, int64(3), report.Totals.Requests, "a long range reads the day")
	assert.Equal(t, int64(86400), report.BucketSeconds)
}

func Test_BuildProject_WhenTheProjectHasNoAccessLogs_ClosesItsHoursAndTheReportReadsOnlyTheOpenHourLive(
	t *testing.T,
) {
	env := newTestEnv(t)
	liveStart := env.now().Truncate(time.Hour)
	var items []*logs_core.LogItem
	for hour := liveStart.Add(-3 * time.Hour); !hour.After(liveStart); hour = hour.Add(time.Hour) {
		for i := range 50 {
			items = append(items,
				accessLog(hour.Add(time.Duration(i)*time.Second), fmt.Sprintf("Nov 18 vm-1 systemd[1]: Started session %d", i),
					map[string]any{"host_name": "vm-1", "log_file_path": "/var/log/syslog"}),
				accessLog(hour.Add(time.Duration(i)*time.Second), `{"level":"info","msg":"job done","status":"ok"}`,
					map[string]any{"service_name": "worker", "host_name": "vm-1"}))
		}
	}
	storeLogs(t, env.project.ID, items...)
	settings := env.settings(t)

	require.NoError(t, env.builder.BuildProject(context.Background(), settings))

	lastClosed := lastBuildableHour(env.now())
	builds, err := repo.builds(context.Background(), env.project.ID, granularityHour, lastClosed.Add(-2*time.Hour),
		lastClosed.Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, builds, 3, "hours without access logs are built too")
	for _, build := range builds {
		assert.Zero(t, build.RowsRead)
	}

	report := env.report(t, liveStart.Add(-3*time.Hour), time.Time{}, "")
	assert.Equal(t, lastClosed.Add(time.Hour), report.Live.From, "only hours not closed yet are read live")
	assert.Zero(t, report.Rollups.Missing)
	assert.Zero(t, report.Totals.Requests)
	assert.False(t, report.Live.Partial)
}

func Test_GetReport_WhenTheLiveReadFails_ReturnsTheRollupsWithTheLivePartMarkedPartial(t *testing.T) {
	env := newTestEnv(t)
	liveStart := env.now().Truncate(time.Hour)
	closed := liveStart.Add(-2 * time.Hour)
	storeLogs(t, env.project.ID,
		accessLog(closed.Add(5*time.Minute), "", nginxRequest("198.51.100.1", "/", "200", "web-1")),
		accessLog(closed.Add(6*time.Minute), "", nginxRequest("198.51.100.2", "/cart", "500", "web-1")),
		accessLog(liveStart, "", nginxRequest("198.51.100.1", "/", "200", "web-1")),
	)
	require.NoError(t, env.builder.BuildHour(context.Background(), env.settings(t), closed))
	// Every raw read times out, as a project's live read past its budget does.
	env.useLogs(&cappedRepository{LogRepository: logs_core.GetLogCoreRepository(), maxWindow: time.Hour,
		brokenUntil: liveStart.Add(2 * time.Hour)})

	report := env.report(t, liveStart.Add(-3*time.Hour), time.Time{}, "")

	assert.True(t, report.Live.Partial)
	assert.Zero(t, report.Live.Rows)
	assert.Equal(t, 1, report.Rollups.Hours)
	assert.Equal(t, int64(2), report.Totals.Requests, "the built hour still counts")
}

type testEnv struct {
	router     *gin.Engine
	project    *projects_models.Project
	ownerToken string
	builder    *Builder
	now        func() time.Time
	service    *Service
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	now := func() time.Time { return time.Now().UTC() }
	geoOff := newCountryDB(func() string { return "" }, slog.Default())
	testService := &Service{repo, logs_core.GetLogCoreRepository, geoOff, audit_logs.GetAuditLogService(), now,
		newLiveCache(now)}
	router := projects_testing.CreateTestRouter(projects_controllers.GetProjectController(),
		projects_controllers.GetMembershipController(), &Controller{testService})
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, token := projects_testing.CreateTestProjectWithToken("Web analytics "+uuid.NewString()[:8], owner.Token,
		router)
	return &testEnv{router: router, project: project, ownerToken: token, now: now,
		builder: &Builder{repo, logs_core.GetLogCoreRepository, geoOff, slog.Default(), now}, service: testService}
}

// useLogs makes the report (not the builder) read raw logs from repository.
func (e *testEnv) useLogs(repository logs_core.LogRepository) {
	e.service.logs = func() logs_core.LogRepository { return repository }
}

func (e *testEnv) url(path string) string {
	return "/api/v1/projects/" + e.project.ID.String() + "/web-analytics/" + path
}

func (e *testEnv) settings(t *testing.T) *Settings {
	t.Helper()
	settings, err := repo.ensureSettings(context.Background(), e.project.ID)
	require.NoError(t, err)
	return settings
}

func (e *testEnv) report(t *testing.T, from, to time.Time, sources string) *ReportDTO {
	t.Helper()
	query := url.Values{"from": {from.Format(time.RFC3339)}}
	if !to.IsZero() {
		query.Set("to", to.Format(time.RFC3339))
	}
	if sources != "" {
		query.Set("sources", sources)
	}
	var report ReportDTO
	test_utils.MakeGetRequestAndUnmarshal(t, e.router, e.url("report")+"?"+query.Encode(), "Bearer "+e.ownerToken,
		http.StatusOK, &report)
	return &report
}

func nginxRequest(client, path, status, host string) map[string]any {
	return map[string]any{"service_name": "nginx", "host_name": host, "remote_addr": client, "method": "GET",
		"path": path, "status": status, "body_bytes_sent": "100", "user_agent": chromeUA, "referer": "-"}
}
