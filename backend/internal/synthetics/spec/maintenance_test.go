package spec

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func Test_Occurrences_Weekly_InItsTimeZoneFromTheStartDateToTheEndDate(t *testing.T) {
	schedule := recurring(t, MaintenanceSchedule{
		Kind: ScheduleWeekly, Weekdays: []string{"Tuesday", "fri"}, StartTime: "1:00", DurationMinutes: 60,
		TimeZone: "Asia/Kolkata", StartDate: "2026-10-30", EndDate: "2026-11-10",
	})
	require.Equal(t, []string{"tue", "fri"}, schedule.Weekdays)
	require.Equal(t, "01:00", schedule.StartTime)

	got := schedule.Occurrences(utc("2026-10-01T00:00:00Z"), utc("2026-12-01T00:00:00Z"))

	// 01:00 IST is 19:30 UTC the day before.
	require.Equal(t, []Occurrence{
		{utc("2026-10-29T19:30:00Z"), utc("2026-10-29T20:30:00Z")},
		{utc("2026-11-02T19:30:00Z"), utc("2026-11-02T20:30:00Z")},
		{utc("2026-11-05T19:30:00Z"), utc("2026-11-05T20:30:00Z")},
		{utc("2026-11-09T19:30:00Z"), utc("2026-11-09T20:30:00Z")},
	}, got)
}

func Test_Occurrences_OverlappingTheRange_IncludeOneThatStartedBeforeIt(t *testing.T) {
	schedule := recurring(t, MaintenanceSchedule{
		Kind: ScheduleDaily, StartTime: "23:00", DurationMinutes: 120, StartDate: "2026-01-01",
	})

	got := schedule.Occurrences(utc("2026-03-02T00:30:00Z"), utc("2026-03-02T01:00:00Z"))

	require.Equal(t, []Occurrence{{utc("2026-03-01T23:00:00Z"), utc("2026-03-02T01:00:00Z")}}, got)
	require.Empty(t, schedule.Occurrences(utc("2026-03-02T01:00:00Z"), utc("2026-03-02T22:59:00Z")))
}

func Test_Occurrences_AcrossDSTChanges_KeepTheWallClockTime(t *testing.T) {
	schedule := recurring(t, MaintenanceSchedule{
		Kind: ScheduleDaily, StartTime: "01:30", DurationMinutes: 60, TimeZone: "Europe/London",
		StartDate: "2026-03-28",
	})

	got := schedule.Occurrences(utc("2026-03-28T00:00:00Z"), utc("2026-03-31T00:00:00Z"))

	require.Equal(t, []Occurrence{
		{utc("2026-03-28T01:30:00Z"), utc("2026-03-28T02:30:00Z")}, // GMT
		// 01:30 doesn't exist on the 29th: it starts at the same instant, 02:30 BST.
		{utc("2026-03-29T01:30:00Z"), utc("2026-03-29T02:30:00Z")},
		{utc("2026-03-30T00:30:00Z"), utc("2026-03-30T01:30:00Z")}, // BST
	}, got)

	// 01:30 happens twice on 25 October: the first one (BST) counts.
	autumn := schedule.Occurrences(utc("2026-10-24T00:00:00Z"), utc("2026-10-27T00:00:00Z"))
	require.Equal(t, []Occurrence{
		{utc("2026-10-24T00:30:00Z"), utc("2026-10-24T01:30:00Z")},
		{utc("2026-10-25T00:30:00Z"), utc("2026-10-25T01:30:00Z")},
		{utc("2026-10-26T01:30:00Z"), utc("2026-10-26T02:30:00Z")},
	}, autumn)
}

func Test_Occurrences_MonthlyDay_UsesTheLastDayOfShortMonths(t *testing.T) {
	schedule := recurring(t, MaintenanceSchedule{
		Kind: ScheduleMonthlyDay, DayOfMonth: 31, StartTime: "22:00", DurationMinutes: 30, StartDate: "2026-01-01",
	})

	got := schedule.Occurrences(utc("2026-01-15T00:00:00Z"), utc("2026-05-01T00:00:00Z"))

	require.Equal(t, []string{"2026-01-31", "2026-02-28", "2026-03-31", "2026-04-30"}, startDates(got))
}

func Test_Occurrences_MonthlyWeekday_NthAndLast(t *testing.T) {
	second := recurring(t, MaintenanceSchedule{
		Kind: ScheduleMonthlyWeekday, WeekOfMonth: 2, Weekday: "tue", StartTime: "03:00", DurationMinutes: 60,
		StartDate: "2026-01-01",
	})
	last := recurring(t, MaintenanceSchedule{
		Kind: ScheduleMonthlyWeekday, WeekOfMonth: LastWeek, Weekday: "sun", StartTime: "03:00",
		DurationMinutes: 60, StartDate: "2026-01-01",
	})
	from, to := utc("2026-01-01T00:00:00Z"), utc("2026-04-01T00:00:00Z")

	require.Equal(t, []string{"2026-01-13", "2026-02-10", "2026-03-10"}, startDates(second.Occurrences(from, to)))
	require.Equal(t, []string{"2026-01-25", "2026-02-22", "2026-03-29"}, startDates(last.Occurrences(from, to)))
}

func Test_Occurrences_OneOff(t *testing.T) {
	start, end := utc("2026-11-02T22:00:00Z"), utc("2026-11-03T01:30:00Z")
	schedule := MaintenanceSchedule{Kind: "Once", StartsAt: &start, EndsAt: &end}
	require.NoError(t, schedule.Normalize(time.Now()))

	require.Len(t, schedule.Occurrences(utc("2026-11-03T00:00:00Z"), utc("2026-11-04T00:00:00Z")), 1)
	require.Empty(t, schedule.Occurrences(utc("2026-11-03T01:30:00Z"), utc("2026-11-04T00:00:00Z")))
	require.Equal(t, 210*time.Minute, schedule.Duration())
}

func Test_NormalizeSchedule_RejectsBadSchedules(t *testing.T) {
	start := utc("2026-11-02T22:00:00Z")
	long := start.Add(32 * 24 * time.Hour)
	for name, schedule := range map[string]MaintenanceSchedule{
		"one-off without end":   {Kind: ScheduleOnce, StartsAt: &start},
		"one-off over 31 days":  {Kind: ScheduleOnce, StartsAt: &start, EndsAt: &long},
		"unknown kind":          {Kind: "yearly", StartTime: "01:00", DurationMinutes: 5},
		"bad time":              {Kind: ScheduleDaily, StartTime: "25:00", DurationMinutes: 5},
		"daily over a day":      {Kind: ScheduleDaily, StartTime: "01:00", DurationMinutes: 24*60 + 1},
		"weekly without days":   {Kind: ScheduleWeekly, StartTime: "01:00", DurationMinutes: 5},
		"monthly day 32":        {Kind: ScheduleMonthlyDay, DayOfMonth: 32, StartTime: "01:00", DurationMinutes: 5},
		"fifth weekday":         {Kind: ScheduleMonthlyWeekday, WeekOfMonth: 5, Weekday: "mon", StartTime: "01:00", DurationMinutes: 5},
		"unknown zone":          {Kind: ScheduleDaily, StartTime: "01:00", DurationMinutes: 5, TimeZone: "Mars/Olympus"},
		"end before first date": {Kind: ScheduleDaily, StartTime: "01:00", DurationMinutes: 5, StartDate: "2026-02-01", EndDate: "2026-01-01"},
	} {
		require.Error(t, schedule.Normalize(start), name)
	}
}

func Test_NormalizeSchedule_DefaultsTheStartDateToTodayInItsZone(t *testing.T) {
	schedule := MaintenanceSchedule{Kind: ScheduleDaily, StartTime: "01:00", DurationMinutes: 60, TimeZone: "Asia/Kolkata"}

	require.NoError(t, schedule.Normalize(utc("2026-10-27T20:00:00Z")))

	require.Equal(t, "2026-10-28", schedule.StartDate)
}

func Test_ParseMonitorList_MaintenanceRoundTrips(t *testing.T) {
	file := `apiVersion: obzly.com/synthetics/v1
kind: MonitorList
monitors: []
maintenance:
  - name: Weekly DB patching
    reason: RDS minor version patching
    folders: [Shop / Checkout]
    monitors: [Payments gateway TLS]
    schedule:
      repeat: weekly
      days: [tue]
      at: "01:00"
      duration: 1h
      from: 2026-10-27
  - name: Card processor cut-over
    all: true
    enabled: false
    schedule:
      repeat: once
      start: 2026-11-02 22:00
      end: 2026-11-03 01:30
      time_zone: Europe/London
  - name: Last Sunday reboot
    monitors: [Search API]
    schedule: {repeat: monthly, week: last, weekday: sun, at: "03:00", duration: 90m, until: 2027-06-30}
`
	list, problems := ParseMonitorList([]byte(file))
	require.Empty(t, problems)
	require.True(t, list.HasMaintenance)
	require.Len(t, list.Maintenance, 3)
	weekly := list.Maintenance[0]
	require.Equal(t, []string{"Shop/Checkout"}, weekly.Folders)
	require.True(t, weekly.Enabled)
	require.Equal(t, ScheduleWeekly, weekly.Schedule.Kind)
	require.Equal(t, 60, weekly.Schedule.DurationMinutes)
	once := list.Maintenance[1]
	require.False(t, once.Enabled)
	require.True(t, once.All)
	require.Equal(t, utc("2026-11-02T22:00:00Z"), *once.Schedule.StartsAt, "GMT in November")
	last := list.Maintenance[2]
	require.Equal(t, LastWeek, last.Schedule.WeekOfMonth)
	require.Empty(t, last.Schedule.StartDate, "left for the importer to fill")

	data, err := MarshalMonitorFile(nil, list.Maintenance)
	require.NoError(t, err)
	again, problems := ParseMonitorList(data)
	require.Empty(t, problems, string(data))
	require.Equal(t, sortedMaintenance(list.Maintenance), again.Maintenance, string(data))
}

func Test_ParseMonitorList_WithoutAMaintenanceList_LeavesWindowsAlone(t *testing.T) {
	list, problems := ParseMonitorList([]byte("apiVersion: obzly.com/synthetics/v1\nkind: MonitorList\nmonitors: []\n"))
	require.Empty(t, problems)
	require.False(t, list.HasMaintenance)

	_, problems = ParseMonitorList([]byte(`apiVersion: obzly.com/synthetics/v1
kind: MonitorList
maintenance:
  - name: Nothing covered
    schedule: {repeat: daily, at: "01:00", duration: 1h}
  - name: Bad
    all: true
    schedule: {repeat: monthly, at: "01:00", duration: 1h}
`))
	require.Len(t, problems, 2)
	require.Equal(t, "Nothing covered", problems[0].Window)
}

func recurring(t *testing.T, schedule MaintenanceSchedule) MaintenanceSchedule {
	t.Helper()
	require.NoError(t, schedule.Normalize(time.Time{}))
	return schedule
}

func utc(text string) time.Time {
	at, err := time.Parse(time.RFC3339, text)
	if err != nil {
		panic(err)
	}
	return at
}

func startDates(occurrences []Occurrence) []string {
	dates := []string{}
	for _, occurrence := range occurrences {
		dates = append(dates, occurrence.Start.Format(dateLayout))
	}
	return dates
}
