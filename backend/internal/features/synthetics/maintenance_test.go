package synthetics_test

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"obzly/internal/features/synthetics"
	"obzly/internal/storage"
	"obzly/internal/synthetics/spec"
	test_utils "obzly/internal/util/testing"
)

func Test_MaintenanceAPI_AdminsManage_MembersRead_StaleVersionRefused(t *testing.T) {
	router, admin, member := maintenanceSetup(t)
	destination := createDestination(t, router, admin, "results")
	checkout := createCheck(t, router, admin, httpCheckRequest("Checkout", destination.ID))
	request := synthetics.MaintenanceRequest{
		Name: "Weekly DB patching", Reason: "RDS minor version patching", Enabled: true,
		Scope: synthetics.MaintenanceScope{MonitorIDs: []uuid.UUID{checkout.ID}},
		Schedule: spec.MaintenanceSchedule{
			Kind: spec.ScheduleWeekly, Weekdays: []string{"tue"}, StartTime: "01:00", DurationMinutes: 60,
		},
	}

	test_utils.MakePostRequest(t, router, "/api/v1/synthetics/maintenance", bearer(member), request, http.StatusForbidden)
	created := createWindow(t, router, admin, request)
	require.Equal(t, synthetics.MaintenanceUpcoming, created.State)
	require.NotNil(t, created.Next)
	require.Equal(t, time.Tuesday, created.Next.Start.Weekday())
	require.NotEmpty(t, created.Schedule.StartDate, "a recurring window starts today unless told otherwise")
	require.Equal(t, []uuid.UUID{checkout.ID}, created.MonitorIDs)

	duplicate := request
	test_utils.MakePostRequest(t, router, "/api/v1/synthetics/maintenance", bearer(admin), duplicate, http.StatusBadRequest)

	var listed []synthetics.MaintenanceView
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/synthetics/maintenance", bearer(member),
		http.StatusOK, &listed)
	require.Len(t, listed, 1)

	path := "/api/v1/synthetics/maintenance/" + created.ID.String()
	update := request
	update.Reason, update.Version = "Patching and a reboot", created.Version
	test_utils.MakePutRequest(t, router, path, bearer(member), update, http.StatusForbidden)
	var updated synthetics.MaintenanceView
	test_utils.MakePutRequestAndUnmarshal(t, router, path, bearer(admin), update, http.StatusOK, &updated)
	require.Equal(t, created.Version+1, updated.Version)
	require.Equal(t, created.Schedule.StartDate, updated.Schedule.StartDate)
	test_utils.MakePutRequest(t, router, path, bearer(admin), update, http.StatusConflict)

	bad := request
	bad.Name, bad.Scope = "Nothing", synthetics.MaintenanceScope{}
	test_utils.MakePostRequest(t, router, "/api/v1/synthetics/maintenance", bearer(admin), bad, http.StatusBadRequest)
	bad.Scope = synthetics.MaintenanceScope{MonitorIDs: []uuid.UUID{uuid.New()}}
	test_utils.MakePostRequest(t, router, "/api/v1/synthetics/maintenance", bearer(admin), bad, http.StatusBadRequest)

	test_utils.MakeDeleteRequest(t, router, path, bearer(member), http.StatusForbidden)
	test_utils.MakeDeleteRequest(t, router, path, bearer(admin), http.StatusOK)
	test_utils.MakeGetRequest(t, router, path, bearer(member), http.StatusNotFound)
	requireAudit(t, `Synthetics maintenance window "Weekly DB patching" created`)
	requireAudit(t, `Synthetics maintenance window "Weekly DB patching" deleted`)
}

func Test_EndAndStartMaintenance_MoveTheOccurrence_AndAreAudited(t *testing.T) {
	router, admin, member := maintenanceSetup(t)
	now := time.Now().UTC()
	start, end := now.Add(-10*time.Minute), now.Add(time.Hour)
	active := createWindow(t, router, admin, synthetics.MaintenanceRequest{
		Name: "Cluster upgrade", Enabled: true, Scope: synthetics.MaintenanceScope{All: true},
		Schedule: spec.MaintenanceSchedule{Kind: spec.ScheduleOnce, StartsAt: &start, EndsAt: &end},
	})
	require.Equal(t, synthetics.MaintenanceActive, active.State)
	activePath := "/api/v1/synthetics/maintenance/" + active.ID.String()

	test_utils.MakePostRequest(t, router, activePath+"/end", bearer(member), nil, http.StatusForbidden)
	test_utils.MakePostRequest(t, router, activePath+"/start", bearer(admin), nil, http.StatusBadRequest)
	var ended synthetics.MaintenanceView
	test_utils.MakePostRequestAndUnmarshal(t, router, activePath+"/end", bearer(admin), nil, http.StatusOK, &ended)
	require.Equal(t, synthetics.MaintenancePast, ended.State)
	require.NotNil(t, ended.LastEnded)
	require.WithinDuration(t, time.Now(), *ended.LastEnded, 5*time.Second)
	test_utils.MakePostRequest(t, router, activePath+"/end", bearer(admin), nil, http.StatusBadRequest)
	occurrences := occurrencesBetween(t, router, member, now.Add(-time.Hour), now.Add(2*time.Hour))
	require.Len(t, occurrences, 1)
	require.WithinDuration(t, time.Now(), occurrences[0].End, 5*time.Second, "ended early")
	requireAudit(t, `Synthetics maintenance window "Cluster upgrade" ended early`)

	upcoming := createWindow(t, router, admin, synthetics.MaintenanceRequest{
		Name: "Nightly cache rebuild", Enabled: true, Scope: synthetics.MaintenanceScope{All: true},
		Schedule: spec.MaintenanceSchedule{
			Kind: spec.ScheduleDaily, TimeZone: "UTC", DurationMinutes: 20,
			StartTime: now.Add(3 * time.Hour).Format("15:04"),
		},
	})
	require.Equal(t, synthetics.MaintenanceUpcoming, upcoming.State)
	var started synthetics.MaintenanceView
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/synthetics/maintenance/"+upcoming.ID.String()+"/start",
		bearer(admin), nil, http.StatusOK, &started)
	require.Equal(t, synthetics.MaintenanceActive, started.State)
	require.WithinDuration(t, time.Now(), started.Current.Start, 5*time.Second)
	require.Equal(t, 20*time.Minute, started.Current.End.Sub(started.Current.Start), "its planned duration")
	require.True(t, started.Next.Start.After(started.Current.End), "the moved occurrence isn't also still ahead")
	requireAudit(t, `Synthetics maintenance window "Nightly cache rebuild" started early`)
}

func Test_MaintenanceOccurrences_CoverFoldersWithSubfolders(t *testing.T) {
	router, admin, member := maintenanceSetup(t)
	destination := createDestination(t, router, admin, "results")
	inFolder := httpCheckRequest("Cart", destination.ID)
	inFolder.Folder = "Shop/Checkout"
	cart := createCheck(t, router, admin, inFolder)
	below := httpCheckRequest("Cart EU", destination.ID)
	below.Folder = "Shop/Checkout/EU"
	cartEU := createCheck(t, router, admin, below)
	sibling := httpCheckRequest("Checkout banner", destination.ID)
	sibling.Folder = "Shop/CheckoutBanner"
	createCheck(t, router, admin, sibling)
	start := time.Now().UTC().Add(time.Hour).Truncate(time.Minute)
	end := start.Add(30 * time.Minute)
	createWindow(t, router, admin, synthetics.MaintenanceRequest{
		Name: "Checkout release", Enabled: true, Scope: synthetics.MaintenanceScope{Folders: []string{" Shop / Checkout "}},
		Schedule: spec.MaintenanceSchedule{Kind: spec.ScheduleOnce, StartsAt: &start, EndsAt: &end},
	})
	createWindow(t, router, admin, synthetics.MaintenanceRequest{
		Name: "Paused", Enabled: false, Scope: synthetics.MaintenanceScope{All: true},
		Schedule: spec.MaintenanceSchedule{Kind: spec.ScheduleOnce, StartsAt: &start, EndsAt: &end},
	})

	occurrences := occurrencesBetween(t, router, member, start.Add(-time.Hour), start.Add(time.Hour))

	require.Len(t, occurrences, 1, "a paused window has none")
	require.Equal(t, "Checkout release", occurrences[0].Name)
	require.ElementsMatch(t, []uuid.UUID{cart.ID, cartEU.ID}, occurrences[0].MonitorIDs)
	require.True(t, occurrences[0].Start.Equal(start))

	test_utils.MakeGetRequest(t, router, "/api/v1/synthetics/maintenance/occurrences?from=yesterday&to=now",
		bearer(member), http.StatusBadRequest)
	tooLong := "/api/v1/synthetics/maintenance/occurrences?from=" + url.QueryEscape(start.Format(time.RFC3339)) +
		"&to=" + url.QueryEscape(start.Add(100*24*time.Hour).Format(time.RFC3339))
	test_utils.MakeGetRequest(t, router, tooLong, bearer(member), http.StatusBadRequest)
}

func Test_PreviewMaintenance_ListsTheNextOccurrences_OrTheProblem(t *testing.T) {
	router, _, member := maintenanceSetup(t)
	var preview synthetics.SchedulePreview
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/synthetics/maintenance/preview", bearer(member),
		synthetics.SchedulePreviewRequest{Schedule: spec.MaintenanceSchedule{
			Kind: spec.ScheduleMonthlyWeekday, WeekOfMonth: spec.LastWeek, Weekday: "fri", StartTime: "22:00",
			DurationMinutes: 90, TimeZone: "Europe/London",
		}}, http.StatusOK, &preview)

	require.Len(t, preview.Occurrences, 3)
	for _, item := range preview.Occurrences {
		require.Equal(t, 90*time.Minute, item.End.Sub(item.Start))
		require.True(t, item.Start.After(time.Now()))
	}
	require.NotEmpty(t, preview.Schedule.StartDate)

	response := test_utils.MakePostRequest(t, router, "/api/v1/synthetics/maintenance/preview", bearer(member),
		synthetics.SchedulePreviewRequest{Schedule: spec.MaintenanceSchedule{Kind: spec.ScheduleWeekly,
			StartTime: "01:00", DurationMinutes: 60}}, http.StatusBadRequest)
	require.Contains(t, string(response.Body), "day of the week")
}

// The shared-config side: each Site gets the next 48 hours of occurrences
// for its own alerting monitors, one silence per project and occurrence,
// and the monitors' managed rules carry the check_id label they match.
func Test_MaintenanceFor_SendsTheSitesOccurrencesForItsAlertingMonitors(t *testing.T) {
	router, admin, _ := maintenanceSetup(t)
	destination := createDestination(t, router, admin, "results")
	channel := createSharedChannel(t, "ops")
	alerting := httpCheckRequest("Checkout", destination.ID)
	alerting.Alerts = &synthetics.CheckAlerts{ChannelIDs: []uuid.UUID{channel}}
	checkout := createCheck(t, router, admin, alerting)
	createCheck(t, router, admin, httpCheckRequest("No alerts", destination.ID))
	now := time.Now().UTC()
	createWindow(t, router, admin, synthetics.MaintenanceRequest{
		Name: "Hourly-ish", Reason: "Rolling restarts", Enabled: true, Scope: synthetics.MaintenanceScope{All: true},
		Schedule: spec.MaintenanceSchedule{
			Kind: spec.ScheduleDaily, StartTime: now.Add(2 * time.Hour).Format("15:04"), DurationMinutes: 30,
			StartDate: now.Add(-48 * time.Hour).Format("2006-01-02"),
		},
	})
	later := now.Add(72 * time.Hour)
	laterEnd := later.Add(time.Hour)
	createWindow(t, router, admin, synthetics.MaintenanceRequest{
		Name: "Beyond the horizon", Enabled: true, Scope: synthetics.MaintenanceScope{All: true},
		Schedule: spec.MaintenanceSchedule{Kind: spec.ScheduleOnce, StartsAt: &later, EndsAt: &laterEnd},
	})

	silences, err := synthetics.GetService().MaintenanceFor("site-a")
	require.NoError(t, err)

	require.Len(t, silences, 2, "today's and tomorrow's occurrence, not the one beyond 48 hours")
	for _, silence := range silences {
		require.Equal(t, destination.ProjectID, silence.ProjectID)
		require.Equal(t, []string{checkout.ID.String()}, silence.CheckIDs, "only monitors that alert")
		require.Equal(t, "Synthetics maintenance: Hourly-ish (Rolling restarts)", silence.Comment)
		require.Equal(t, 30*time.Minute, silence.EndsAt.Sub(silence.StartsAt))
	}
	require.NotEqual(t, silences[0].Key, silences[1].Key)
	none, err := synthetics.GetService().MaintenanceFor("site-b")
	require.NoError(t, err)
	require.Empty(t, none)

	rules, err := synthetics.GetService().MonitorAlertsFor("site-a")
	require.NoError(t, err)
	require.NotEmpty(t, rules)
	for _, rule := range rules {
		require.Equal(t, checkout.ID.String(), rule.CheckID)
	}
}

func Test_Import_MaintenanceWindows_RoundTripAndPrune(t *testing.T) {
	router, admin, _ := maintenanceSetup(t)
	createDestination(t, router, admin, "synthetics")
	file := `apiVersion: obzly.com/synthetics/v1
kind: MonitorList
monitors:
  - name: Payments gateway TLS
    type: tls
    target: pay.example.com:443
maintenance:
  - name: Weekly DB patching
    reason: RDS minor version patching
    monitors: [Payments gateway TLS]
    folders: [Shop/Checkout]
    schedule: {repeat: weekly, days: [tue], at: "01:00", duration: 1h}
  - name: Unknown monitor
    monitors: [Nope]
    schedule: {repeat: daily, at: "01:00", duration: 1h}
`
	var preview synthetics.ImportPreview
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/synthetics/checks/import", bearer(admin),
		synthetics.ImportRequest{YAML: file}, http.StatusOK, &preview)
	require.False(t, preview.Applied, "all or nothing")
	require.Len(t, preview.Errors, 1)
	require.Equal(t, "Unknown monitor", preview.Errors[0].Window)

	file = file[:len(file)-len("  - name: Unknown monitor\n    monitors: [Nope]\n    schedule: {repeat: daily, at: \"01:00\", duration: 1h}\n")]
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/synthetics/checks/import", bearer(admin),
		synthetics.ImportRequest{YAML: file}, http.StatusOK, &preview)
	require.True(t, preview.Applied, preview.Errors)
	require.Equal(t, []string{"Weekly DB patching"}, preview.Maintenance.Create)
	var windows []synthetics.MaintenanceView
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/synthetics/maintenance", bearer(admin),
		http.StatusOK, &windows)
	require.Len(t, windows, 1)
	require.Len(t, windows[0].Scope.MonitorIDs, 1)
	require.Equal(t, []string{"Shop/Checkout"}, windows[0].Scope.Folders)

	exported := test_utils.MakeGetRequest(t, router, "/api/v1/synthetics/checks/export", bearer(admin), http.StatusOK)
	var again synthetics.ImportPreview
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/synthetics/checks/import/preview", bearer(admin),
		synthetics.ImportRequest{YAML: string(exported.Body)}, http.StatusOK, &again)
	require.Empty(t, again.Errors, string(exported.Body))
	require.Equal(t, []string{"Weekly DB patching"}, again.Maintenance.Unchanged, string(exported.Body))
	require.Empty(t, again.Maintenance.Update)

	monitorsOnly := "apiVersion: obzly.com/synthetics/v1\nkind: MonitorList\nprune: true\nmonitors:\n" +
		"  - name: Payments gateway TLS\n    type: tls\n    target: pay.example.com:443\n"
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/synthetics/checks/import/preview", bearer(admin),
		synthetics.ImportRequest{YAML: monitorsOnly}, http.StatusOK, &again)
	require.Empty(t, again.Maintenance.Delete, "a file without a maintenance list leaves windows alone")
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/synthetics/checks/import", bearer(admin),
		synthetics.ImportRequest{YAML: monitorsOnly + "maintenance: []\n"}, http.StatusOK, &again)
	require.True(t, again.Applied)
	require.Equal(t, []string{"Weekly DB patching"}, again.Maintenance.Delete)
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/synthetics/maintenance", bearer(admin),
		http.StatusOK, &windows)
	require.Empty(t, windows)
}

func maintenanceSetup(t *testing.T) (*gin.Engine, string, string) {
	t.Helper()
	require.NoError(t, storage.GetDb().Exec("DELETE FROM synthetic_maintenance_windows").Error)
	return setup(t)
}

func createWindow(
	t *testing.T, router *gin.Engine, admin string, request synthetics.MaintenanceRequest,
) synthetics.MaintenanceView {
	t.Helper()
	var window synthetics.MaintenanceView
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/synthetics/maintenance", bearer(admin), request,
		http.StatusOK, &window)
	return window
}

func occurrencesBetween(
	t *testing.T, router *gin.Engine, token string, from, to time.Time,
) []synthetics.MaintenanceOccurrence {
	t.Helper()
	var occurrences []synthetics.MaintenanceOccurrence
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/synthetics/maintenance/occurrences?from="+
		url.QueryEscape(from.Format(time.RFC3339))+"&to="+url.QueryEscape(to.Format(time.RFC3339)),
		bearer(token), http.StatusOK, &occurrences)
	return occurrences
}

func requireAudit(t *testing.T, prefix string) {
	t.Helper()
	require.Eventually(t, func() bool {
		var count int64
		storage.GetDb().Table("audit_logs").Where("message LIKE ? AND created_at > ?", prefix+"%",
			time.Now().Add(-time.Minute)).Count(&count)
		return count > 0
	}, 5*time.Second, 50*time.Millisecond, prefix)
}
