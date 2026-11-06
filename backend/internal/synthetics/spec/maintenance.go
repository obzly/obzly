package spec

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Maintenance schedule kinds.
const (
	ScheduleOnce           = "once"
	ScheduleDaily          = "daily"
	ScheduleWeekly         = "weekly"
	ScheduleMonthlyDay     = "monthly_day"
	ScheduleMonthlyWeekday = "monthly_weekday"
)

const (
	dateLayout = "2006-01-02"
	// maxOneOff bounds a one-off window, so a typo can't mute alerts for a year.
	maxOneOff = 31 * 24 * time.Hour
	// maxExpandDays bounds one expansion.
	maxExpandDays = 800
	// LastWeek is WeekOfMonth for "the last <weekday> of the month".
	LastWeek = -1
)

var weekdayNames = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// MaintenanceSchedule is when a window applies: once, StartsAt to EndsAt, or at StartTime (in
// TimeZone) for DurationMinutes on the kind's days, from StartDate through the optional EndDate.
type MaintenanceSchedule struct {
	Kind            string     `json:"kind"`
	StartsAt        *time.Time `json:"startsAt,omitempty"`
	EndsAt          *time.Time `json:"endsAt,omitempty"`
	TimeZone        string     `json:"timeZone,omitempty"`
	StartTime       string     `json:"startTime,omitempty"`
	DurationMinutes int        `json:"durationMinutes,omitempty"`
	// Weekdays (weekly) are "mon".."sun".
	Weekdays []string `json:"weekdays,omitempty"`
	// DayOfMonth (monthly_day): a month shorter than it uses its last day.
	DayOfMonth int `json:"dayOfMonth,omitempty"`
	// WeekOfMonth and Weekday (monthly_weekday): 1-4, or LastWeek.
	WeekOfMonth int    `json:"weekOfMonth,omitempty"`
	Weekday     string `json:"weekday,omitempty"`
	StartDate   string `json:"startDate,omitempty"`
	EndDate     string `json:"endDate,omitempty"`
}

// Occurrence is one period of maintenance.
type Occurrence struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Maintenance is a window in the YAML format: monitors by name.
type Maintenance struct {
	Name     string
	Reason   string
	All      bool
	Monitors []string
	Folders  []string
	Enabled  bool
	Schedule MaintenanceSchedule
}

type yamlMaintenance struct {
	Name     string       `yaml:"name"`
	Reason   string       `yaml:"reason,omitempty"`
	Enabled  *bool        `yaml:"enabled,omitempty"`
	All      bool         `yaml:"all,omitempty"`
	Monitors []string     `yaml:"monitors,omitempty"`
	Folders  []string     `yaml:"folders,omitempty"`
	Schedule yamlSchedule `yaml:"schedule"`
}

type yamlSchedule struct {
	Repeat   string   `yaml:"repeat"`
	Start    string   `yaml:"start,omitempty"`
	End      string   `yaml:"end,omitempty"`
	At       string   `yaml:"at,omitempty"`
	Duration string   `yaml:"duration,omitempty"`
	Days     []string `yaml:"days,omitempty"`
	Day      int      `yaml:"day,omitempty"`
	Week     string   `yaml:"week,omitempty"`
	Weekday  string   `yaml:"weekday,omitempty"`
	TimeZone string   `yaml:"time_zone,omitempty"`
	From     string   `yaml:"from,omitempty"`
	Until    string   `yaml:"until,omitempty"`
}

// Normalize validates s and fills its defaults. A recurring schedule without a StartDate starts
// today in its time zone, unless now is zero (the caller fills it).
func (s *MaintenanceSchedule) Normalize(now time.Time) error {
	s.Kind = strings.ToLower(strings.TrimSpace(s.Kind))
	s.TimeZone = strings.TrimSpace(s.TimeZone)
	if s.TimeZone == "" {
		s.TimeZone = "UTC"
	}
	location, err := time.LoadLocation(s.TimeZone)
	if err != nil {
		return fmt.Errorf("unknown time zone %q", s.TimeZone)
	}
	if s.Kind == ScheduleOnce {
		if s.StartsAt == nil || s.EndsAt == nil {
			return errors.New("a one-off window needs a start and an end")
		}
		start, end := s.StartsAt.UTC(), s.EndsAt.UTC()
		if !end.After(start) {
			return errors.New("a window must end after it starts")
		}
		if end.Sub(start) > maxOneOff {
			return errors.New("a one-off window can last at most 31 days")
		}
		*s = MaintenanceSchedule{Kind: ScheduleOnce, StartsAt: &start, EndsAt: &end, TimeZone: s.TimeZone}
		return nil
	}
	recurring := MaintenanceSchedule{
		Kind: s.Kind, TimeZone: s.TimeZone, DurationMinutes: s.DurationMinutes,
		StartDate: strings.TrimSpace(s.StartDate), EndDate: strings.TrimSpace(s.EndDate),
	}
	if _, _, err := clock(s.StartTime); err != nil {
		return err
	}
	recurring.StartTime = strings.TrimSpace(s.StartTime)
	if len(recurring.StartTime) == 4 {
		recurring.StartTime = "0" + recurring.StartTime
	}
	maxMinutes := 7 * 24 * 60
	switch s.Kind {
	case ScheduleDaily:
		maxMinutes = 24 * 60
	case ScheduleWeekly:
		days, err := normalizeWeekdays(s.Weekdays)
		if err != nil {
			return err
		}
		recurring.Weekdays = days
	case ScheduleMonthlyDay:
		if s.DayOfMonth < 1 || s.DayOfMonth > 31 {
			return errors.New("the day of the month must be 1 to 31")
		}
		recurring.DayOfMonth = s.DayOfMonth
	case ScheduleMonthlyWeekday:
		if (s.WeekOfMonth < 1 || s.WeekOfMonth > 4) && s.WeekOfMonth != LastWeek {
			return errors.New("the week of the month must be 1 to 4, or last")
		}
		day := strings.ToLower(strings.TrimSpace(s.Weekday))
		if !slices.Contains(weekdayNames, day) {
			return fmt.Errorf("unknown weekday %q (use mon to sun)", s.Weekday)
		}
		recurring.WeekOfMonth, recurring.Weekday = s.WeekOfMonth, day
	default:
		return fmt.Errorf("unknown schedule %q (once, daily, weekly, monthly_day or monthly_weekday)", s.Kind)
	}
	if recurring.DurationMinutes < 1 || recurring.DurationMinutes > maxMinutes {
		return fmt.Errorf("the duration must be 1 minute to %s", minutesText(maxMinutes))
	}
	if recurring.StartDate == "" && !now.IsZero() {
		recurring.StartDate = now.In(location).Format(dateLayout)
	}
	if recurring.StartDate != "" {
		if _, err := time.Parse(dateLayout, recurring.StartDate); err != nil {
			return fmt.Errorf("the first date must look like 2026-11-02, not %q", recurring.StartDate)
		}
	}
	if recurring.EndDate != "" {
		if _, err := time.Parse(dateLayout, recurring.EndDate); err != nil {
			return fmt.Errorf("the end date must look like 2026-11-02, not %q", recurring.EndDate)
		}
		if recurring.StartDate != "" && recurring.EndDate < recurring.StartDate {
			return errors.New("the end date is before the first date")
		}
	}
	*s = recurring
	return nil
}

// Duration is how long each occurrence lasts.
func (s MaintenanceSchedule) Duration() time.Duration {
	if s.Kind == ScheduleOnce && s.StartsAt != nil && s.EndsAt != nil {
		return s.EndsAt.Sub(*s.StartsAt)
	}
	return time.Duration(s.DurationMinutes) * time.Minute
}

// Occurrences are the occurrences overlapping [from, to), in order, in UTC, each lasting its
// duration in real time. A start a DST change skips begins at the instant it would have before the
// change (02:30 on a spring-forward night is 03:30); one it repeats begins at its first instance.
func (s MaintenanceSchedule) Occurrences(from, to time.Time) []Occurrence {
	if !to.After(from) {
		return nil
	}
	if s.Kind == ScheduleOnce {
		if s.StartsAt == nil || s.EndsAt == nil || !s.EndsAt.After(from) || !s.StartsAt.Before(to) {
			return nil
		}
		return []Occurrence{{Start: s.StartsAt.UTC(), End: s.EndsAt.UTC()}}
	}
	location, err := time.LoadLocation(s.TimeZone)
	if err != nil {
		return nil
	}
	hour, minute, err := clock(s.StartTime)
	if err != nil {
		return nil
	}
	duration := s.Duration()
	if duration <= 0 {
		return nil
	}
	// A day's occurrence can start the local day before from (it runs past
	// midnight), and local days can be 25 hours long.
	first := from.Add(-duration).In(location)
	day := time.Date(first.Year(), first.Month(), first.Day()-1, 12, 0, 0, 0, location)
	last := to.In(location)
	lastDay := time.Date(last.Year(), last.Month(), last.Day(), 12, 0, 0, 0, location)
	var occurrences []Occurrence
	for i := 0; !day.After(lastDay) && i < maxExpandDays; i++ {
		date := day.Format(dateLayout)
		inRange := (s.StartDate == "" || date >= s.StartDate) && (s.EndDate == "" || date <= s.EndDate)
		if inRange && s.onDay(day) {
			start := wallTime(day.Year(), day.Month(), day.Day(), hour, minute, location)
			end := start.Add(duration)
			if end.After(from) && start.Before(to) {
				occurrences = append(occurrences, Occurrence{Start: start.UTC(), End: end.UTC()})
			}
		}
		day = time.Date(day.Year(), day.Month(), day.Day()+1, 12, 0, 0, 0, location)
	}
	return occurrences
}

// NormalizeFolder is a folder path as monitors store it ("Shop / EU" is
// "Shop/EU").
func NormalizeFolder(folder string) (string, error) { return normalizeFolder(folder) }

// Normalize validates m (a window from YAML) and fills its defaults; the
// schedule's StartDate stays empty when the file leaves it out.
func (m *Maintenance) Normalize() error {
	m.Name, m.Reason = strings.TrimSpace(m.Name), strings.TrimSpace(m.Reason)
	if m.Name == "" {
		return errors.New("name is required")
	}
	if len(m.Name) > 200 || len(m.Reason) > 500 {
		return errors.New("the name can be at most 200 characters and the reason 500")
	}
	folders := []string{}
	for _, folder := range m.Folders {
		normalized, err := normalizeFolder(folder)
		if err != nil {
			return err
		}
		if normalized != "" && !slices.Contains(folders, normalized) {
			folders = append(folders, normalized)
		}
	}
	m.Folders = folders
	if !m.All && len(m.Monitors) == 0 && len(m.Folders) == 0 {
		return errors.New("choose the monitors, folders or all monitors it covers")
	}
	if m.All {
		m.Monitors, m.Folders = nil, []string{}
	}
	return m.Schedule.Normalize(time.Time{})
}

func (s MaintenanceSchedule) onDay(day time.Time) bool {
	switch s.Kind {
	case ScheduleDaily:
		return true
	case ScheduleWeekly:
		return slices.Contains(s.Weekdays, weekdayName(day.Weekday()))
	case ScheduleMonthlyDay:
		return day.Day() == min(s.DayOfMonth, daysIn(day))
	case ScheduleMonthlyWeekday:
		if weekdayName(day.Weekday()) != s.Weekday {
			return false
		}
		if s.WeekOfMonth == LastWeek {
			return day.Day()+7 > daysIn(day)
		}
		return (day.Day()-1)/7+1 == s.WeekOfMonth
	}
	return false
}

// wallTime is the first instant the clock in location reads the given
// time; a time a DST change skips keeps the offset from before it.
func wallTime(year int, month time.Month, day, hour, minute int, location *time.Location) time.Time {
	at := time.Date(year, month, day, hour, minute, 0, 0, location)
	if at.Hour() != hour || at.Minute() != minute {
		// Skipped: the instant the wall time names under the earlier offset.
		before := time.Date(year, month, day, 0, 0, 0, 0, location)
		_, offset := before.Zone()
		return time.Date(year, month, day, hour, minute, 0, 0, time.UTC).Add(-time.Duration(offset) * time.Second)
	}
	for _, back := range []time.Duration{time.Hour, 30 * time.Minute} {
		earlier := at.Add(-back).In(location)
		if earlier.Day() == day && earlier.Hour() == hour && earlier.Minute() == minute {
			return earlier
		}
	}
	return at
}

func clock(text string) (hour, minute int, err error) {
	parts := strings.Split(strings.TrimSpace(text), ":")
	if len(parts) == 2 {
		hour, errHour := strconv.Atoi(parts[0])
		minute, errMinute := strconv.Atoi(parts[1])
		if errHour == nil && errMinute == nil && hour >= 0 && hour < 24 && minute >= 0 && minute < 60 &&
			len(parts[1]) == 2 {
			return hour, minute, nil
		}
	}
	return 0, 0, fmt.Errorf("the start time must look like 01:00 (24-hour), not %q", text)
}

func normalizeWeekdays(days []string) ([]string, error) {
	chosen := map[string]bool{}
	for _, day := range days {
		day = strings.ToLower(strings.TrimSpace(day))
		if len(day) > 3 {
			day = day[:3]
		}
		if !slices.Contains(weekdayNames, day) {
			return nil, fmt.Errorf("unknown weekday %q (use mon to sun)", day)
		}
		chosen[day] = true
	}
	if len(chosen) == 0 {
		return nil, errors.New("choose at least one day of the week")
	}
	ordered := []string{}
	for _, day := range weekdayNames {
		if chosen[day] {
			ordered = append(ordered, day)
		}
	}
	return ordered, nil
}

func weekdayName(day time.Weekday) string {
	return weekdayNames[(int(day)+6)%7]
}

func daysIn(day time.Time) int {
	return time.Date(day.Year(), day.Month()+1, 0, 12, 0, 0, 0, day.Location()).Day()
}

func minutesText(minutes int) string {
	if minutes%(24*60) == 0 {
		return fmt.Sprintf("%d days", minutes/(24*60))
	}
	return fmt.Sprintf("%d hours", minutes/60)
}

// parseMaintenance reads the file's maintenance list; problems name the
// window and line.
func parseMaintenance(nodes []yaml.Node) ([]Maintenance, []ImportError) {
	var windows []Maintenance
	var problems []ImportError
	seen := map[string]int{}
	for i := range nodes {
		node := &nodes[i]
		var raw yamlMaintenance
		if err := node.Decode(&raw); err != nil {
			problems = append(problems, ImportError{Line: node.Line, Message: "maintenance: " + err.Error()})
			continue
		}
		window, err := raw.toMaintenance()
		if err == nil {
			err = window.Normalize()
		}
		if err != nil {
			problems = append(problems, ImportError{Line: node.Line, Window: raw.Name, Message: err.Error()})
			continue
		}
		key := strings.ToLower(window.Name)
		if first, ok := seen[key]; ok {
			problems = append(problems, ImportError{Line: node.Line, Window: window.Name,
				Message: fmt.Sprintf("duplicate maintenance window name (first on line %d)", first)})
			continue
		}
		seen[key] = node.Line
		windows = append(windows, *window)
	}
	return windows, problems
}

func (raw yamlMaintenance) toMaintenance() (*Maintenance, error) {
	window := &Maintenance{
		Name: raw.Name, Reason: raw.Reason, All: raw.All, Monitors: raw.Monitors, Folders: raw.Folders,
		Enabled: raw.Enabled == nil || *raw.Enabled,
	}
	schedule, err := raw.Schedule.toSchedule()
	if err != nil {
		return nil, fmt.Errorf("schedule: %w", err)
	}
	window.Schedule = schedule
	return window, nil
}

func (raw yamlSchedule) toSchedule() (MaintenanceSchedule, error) {
	schedule := MaintenanceSchedule{
		TimeZone: raw.TimeZone, StartTime: raw.At, StartDate: raw.From, EndDate: raw.Until,
	}
	location, err := time.LoadLocation(strings.TrimSpace(raw.TimeZone))
	if err != nil {
		return schedule, fmt.Errorf("unknown time zone %q", raw.TimeZone)
	}
	switch strings.ToLower(strings.TrimSpace(raw.Repeat)) {
	case "once", "":
		schedule.Kind = ScheduleOnce
		start, err := parseInstant(raw.Start, location)
		if err != nil {
			return schedule, fmt.Errorf("start: %w", err)
		}
		end, err := parseInstant(raw.End, location)
		if err != nil {
			return schedule, fmt.Errorf("end: %w", err)
		}
		schedule.StartsAt, schedule.EndsAt = &start, &end
		return schedule, nil
	case "daily":
		schedule.Kind = ScheduleDaily
	case "weekly":
		schedule.Kind, schedule.Weekdays = ScheduleWeekly, raw.Days
	case "monthly":
		switch {
		case raw.Day > 0 && raw.Week == "":
			schedule.Kind, schedule.DayOfMonth = ScheduleMonthlyDay, raw.Day
		case raw.Day == 0 && raw.Week != "":
			schedule.Kind, schedule.Weekday = ScheduleMonthlyWeekday, raw.Weekday
			if strings.EqualFold(strings.TrimSpace(raw.Week), "last") {
				schedule.WeekOfMonth = LastWeek
			} else if week, err := strconv.Atoi(strings.TrimSpace(raw.Week)); err == nil {
				schedule.WeekOfMonth = week
			} else {
				return schedule, fmt.Errorf("week must be 1 to 4 or last, not %q", raw.Week)
			}
		default:
			return schedule, errors.New("a monthly window needs day (1-31), or week (1-4 or last) and weekday")
		}
	default:
		return schedule, fmt.Errorf("repeat must be once, daily, weekly or monthly, not %q", raw.Repeat)
	}
	duration, err := time.ParseDuration(strings.TrimSpace(raw.Duration))
	if err != nil || duration%time.Minute != 0 {
		return schedule, fmt.Errorf("duration must be whole minutes like 1h or 90m, not %q", raw.Duration)
	}
	schedule.DurationMinutes = int(duration / time.Minute)
	return schedule, nil
}

// parseInstant reads RFC 3339, or "2026-11-02 22:00" in location.
func parseInstant(text string, location *time.Location) (time.Time, error) {
	text = strings.TrimSpace(text)
	if at, err := time.Parse(time.RFC3339, text); err == nil {
		return at, nil
	}
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02T15:04"} {
		if at, err := time.ParseInLocation(layout, text, location); err == nil {
			return at, nil
		}
	}
	return time.Time{}, fmt.Errorf("%q isn't a time like 2026-11-02T22:00:00Z or 2026-11-02 22:00", text)
}

func fromMaintenance(window Maintenance) yamlMaintenance {
	raw := yamlMaintenance{
		Name: window.Name, Reason: window.Reason, All: window.All, Monitors: window.Monitors,
		Folders: window.Folders,
	}
	if !window.Enabled {
		disabled := false
		raw.Enabled = &disabled
	}
	s := window.Schedule
	schedule := yamlSchedule{TimeZone: s.TimeZone}
	if schedule.TimeZone == "UTC" {
		schedule.TimeZone = ""
	}
	if s.Kind == ScheduleOnce {
		schedule.Repeat = "once"
		if s.StartsAt != nil && s.EndsAt != nil {
			schedule.Start, schedule.End = s.StartsAt.UTC().Format(time.RFC3339), s.EndsAt.UTC().Format(time.RFC3339)
		}
		raw.Schedule = schedule
		return raw
	}
	schedule.At, schedule.From, schedule.Until = s.StartTime, s.StartDate, s.EndDate
	schedule.Duration = durationText(s.DurationMinutes)
	switch s.Kind {
	case ScheduleDaily:
		schedule.Repeat = "daily"
	case ScheduleWeekly:
		schedule.Repeat, schedule.Days = "weekly", s.Weekdays
	case ScheduleMonthlyDay:
		schedule.Repeat, schedule.Day = "monthly", s.DayOfMonth
	case ScheduleMonthlyWeekday:
		schedule.Repeat, schedule.Weekday = "monthly", s.Weekday
		schedule.Week = strconv.Itoa(s.WeekOfMonth)
		if s.WeekOfMonth == LastWeek {
			schedule.Week = "last"
		}
	}
	raw.Schedule = schedule
	return raw
}

func durationText(minutes int) string {
	switch {
	case minutes%60 == 0:
		return fmt.Sprintf("%dh", minutes/60)
	case minutes > 60:
		return fmt.Sprintf("%dh%dm", minutes/60, minutes%60)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}

func sortedMaintenance(windows []Maintenance) []Maintenance {
	sorted := append([]Maintenance(nil), windows...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return strings.ToLower(sorted[i].Name) < strings.ToLower(sorted[j].Name)
	})
	return sorted
}
