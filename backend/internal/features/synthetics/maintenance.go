package synthetics

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"obzly/internal/features/sharedconfig"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/storage"
	"obzly/internal/synthetics/spec"
)

// Maintenance window states, as the list shows them.
const (
	MaintenanceActive   = "active"
	MaintenanceUpcoming = "upcoming"
	MaintenancePast     = "past"
	MaintenancePaused   = "paused"
)

const (
	// maintenanceHorizon: how far ahead a Site gets occurrences; it pulls
	// every 5 minutes, so this outlasts a long control-plane outage.
	maintenanceHorizon = 48 * time.Hour
	// maxOccurrencesRange bounds GET .../occurrences.
	maxOccurrencesRange = 93 * 24 * time.Hour
	// adjustmentsKept: how long an ended-early or started-early occurrence
	// is remembered (uptime over past ranges uses it).
	adjustmentsKept = 400 * 24 * time.Hour
	// lookBack / lookAhead bound the search for the last and next occurrence.
	lookBack  = 400 * 24 * time.Hour
	lookAhead = 400 * 24 * time.Hour
)

var ErrMaintenanceConflict = errors.New(
	"this maintenance window was changed by someone else; reload it to see their changes",
)

// MaintenanceWindow says when monitors are expected to be down: their
// alerts are muted and the time leaves their uptime.
type MaintenanceWindow struct {
	ID          uuid.UUID           `gorm:"column:id;primaryKey"`
	Name        string              `gorm:"column:name"`
	Reason      string              `gorm:"column:reason"`
	Scope       MaintenanceScope    `gorm:"column:scope"`
	Schedule    ScheduleJSON        `gorm:"column:schedule"`
	Adjustments OccurrenceOverrides `gorm:"column:adjustments"`
	Enabled     bool                `gorm:"column:enabled"`
	Version     int                 `gorm:"column:version"`
	CreatedBy   *uuid.UUID          `gorm:"column:created_by"`
	CreatedAt   time.Time           `gorm:"column:created_at"`
	UpdatedAt   time.Time           `gorm:"column:updated_at"`
}

func (MaintenanceWindow) TableName() string { return "synthetic_maintenance_windows" }

// MaintenanceScope is what a window covers: every monitor, or the listed
// ones and every monitor in the listed folders or below them.
type MaintenanceScope struct {
	All        bool        `json:"all"`
	MonitorIDs []uuid.UUID `json:"monitorIds"`
	Folders    []string    `json:"folders"`
}

func (s MaintenanceScope) Value() (driver.Value, error) {
	if s.MonitorIDs == nil {
		s.MonitorIDs = []uuid.UUID{}
	}
	if s.Folders == nil {
		s.Folders = []string{}
	}
	return jsonValue(s)
}
func (s *MaintenanceScope) Scan(value any) error { return scanJSON(value, s) }

type ScheduleJSON spec.MaintenanceSchedule

func (s ScheduleJSON) Value() (driver.Value, error) { return jsonValue(s) }
func (s *ScheduleJSON) Scan(value any) error        { return scanJSON(value, s) }

// OccurrenceOverride moves the occurrence that was scheduled to start at
// Occurrence: "End now" and "Start now".
type OccurrenceOverride struct {
	Occurrence time.Time `json:"occurrence"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
}

type OccurrenceOverrides []OccurrenceOverride

func (o OccurrenceOverrides) Value() (driver.Value, error) {
	if o == nil {
		o = OccurrenceOverrides{}
	}
	return jsonValue(o)
}
func (o *OccurrenceOverrides) Scan(value any) error { return scanJSON(value, o) }

// MaintenanceRequest creates or updates a window; Version is the one the
// editor loaded (an update with an older one is refused).
type MaintenanceRequest struct {
	Name     string                   `json:"name"`
	Reason   string                   `json:"reason"`
	Scope    MaintenanceScope         `json:"scope"`
	Schedule spec.MaintenanceSchedule `json:"schedule"`
	Enabled  bool                     `json:"enabled"`
	Version  int                      `json:"version"`
}

// MaintenanceView is a window with where it stands now.
type MaintenanceView struct {
	ID        uuid.UUID                `json:"id"`
	Name      string                   `json:"name"`
	Reason    string                   `json:"reason"`
	Scope     MaintenanceScope         `json:"scope"`
	Schedule  spec.MaintenanceSchedule `json:"schedule"`
	Enabled   bool                     `json:"enabled"`
	Version   int                      `json:"version"`
	CreatedBy *uuid.UUID               `json:"createdBy"`
	CreatedAt time.Time                `json:"createdAt"`
	UpdatedAt time.Time                `json:"updatedAt"`
	// State is active, upcoming, past or paused.
	State     string           `json:"state"`
	Current   *spec.Occurrence `json:"current,omitempty"`
	Next      *spec.Occurrence `json:"next,omitempty"`
	LastEnded *time.Time       `json:"lastEnded,omitempty"`
	// MonitorIDs are the monitors it covers now (folders and all resolved).
	MonitorIDs []uuid.UUID `json:"monitorIds"`
}

// MaintenanceOccurrence is one occurrence of a window and the monitors it
// covers, for status, strips and uptime.
type MaintenanceOccurrence struct {
	WindowID   uuid.UUID   `json:"windowId"`
	Name       string      `json:"name"`
	Reason     string      `json:"reason"`
	Start      time.Time   `json:"start"`
	End        time.Time   `json:"end"`
	MonitorIDs []uuid.UUID `json:"monitorIds"`
}

// SchedulePreviewRequest asks for a schedule's next occurrences.
type SchedulePreviewRequest struct {
	Schedule spec.MaintenanceSchedule `json:"schedule"`
}

// SchedulePreview is the normalized schedule and its next occurrences.
type SchedulePreview struct {
	Schedule    spec.MaintenanceSchedule `json:"schedule"`
	Occurrences []spec.Occurrence        `json:"occurrences"`
}

// previewCount: how many upcoming occurrences the editor shows.
const previewCount = 3

// occurrence is a window's occurrence and the start it was scheduled for.
type occurrence struct {
	spec.Occurrence
	scheduled time.Time
}

// ListMaintenance is every window, for anyone who can see monitors.
func (s *Service) ListMaintenance() ([]MaintenanceView, error) {
	windows, err := s.repository.maintenanceWindows()
	if err != nil {
		return nil, err
	}
	checks, err := s.repository.checks()
	if err != nil {
		return nil, err
	}
	views := make([]MaintenanceView, 0, len(windows))
	for _, window := range windows {
		views = append(views, s.maintenanceView(window, checks))
	}
	return views, nil
}

func (s *Service) GetMaintenance(id uuid.UUID) (*MaintenanceView, error) {
	window, err := s.findWindow(id)
	if err != nil {
		return nil, err
	}
	return s.windowView(window)
}

func (s *Service) CreateMaintenance(user *users_models.User, request MaintenanceRequest) (*MaintenanceView, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrForbidden
	}
	window, err := s.windowFrom(request, nil)
	if err != nil {
		return nil, err
	}
	window.ID, window.Version, window.CreatedBy = uuid.New(), 1, &user.ID
	window.CreatedAt, window.UpdatedAt = s.now(), s.now()
	if err := s.repository.createWindow(window); err != nil {
		return nil, uniqueError(err, "a maintenance window named %q already exists", window.Name)
	}
	s.audit(user, fmt.Sprintf("Synthetics maintenance window %q created", window.Name))
	return s.windowView(window)
}

func (s *Service) UpdateMaintenance(
	user *users_models.User, id uuid.UUID, request MaintenanceRequest,
) (*MaintenanceView, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrForbidden
	}
	existing, err := s.findWindow(id)
	if err != nil {
		return nil, err
	}
	if request.Version != existing.Version {
		return nil, ErrMaintenanceConflict
	}
	window, err := s.windowFrom(request, existing)
	if err != nil {
		return nil, err
	}
	window.ID, window.CreatedAt, window.CreatedBy, window.UpdatedAt = existing.ID, existing.CreatedAt, existing.CreatedBy, s.now()
	if err := s.saveWindow(window, existing.Version); err != nil {
		return nil, err
	}
	s.audit(user, fmt.Sprintf("Synthetics maintenance window %q updated", window.Name))
	return s.windowView(window)
}

func (s *Service) DeleteMaintenance(user *users_models.User, id uuid.UUID) error {
	if !user.CanUpdateSettings() {
		return ErrForbidden
	}
	window, err := s.findWindow(id)
	if err != nil {
		return err
	}
	if err := s.repository.deleteWindow(id); err != nil {
		return err
	}
	s.audit(user, fmt.Sprintf("Synthetics maintenance window %q deleted", window.Name))
	return nil
}

// EndMaintenance ends the window's current occurrence now.
func (s *Service) EndMaintenance(user *users_models.User, id uuid.UUID) (*MaintenanceView, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrForbidden
	}
	window, err := s.findWindow(id)
	if err != nil {
		return nil, err
	}
	now := s.now()
	current := currentOccurrence(window, now)
	if current == nil {
		return nil, errors.New("this window isn't in progress")
	}
	expected := window.Version
	window.Adjustments = override(window.Adjustments, OccurrenceOverride{
		Occurrence: current.scheduled, Start: current.Start, End: now,
	}, now)
	window.UpdatedAt = now
	if err := s.saveWindow(window, expected); err != nil {
		return nil, err
	}
	s.audit(user, fmt.Sprintf("Synthetics maintenance window %q ended early (planned until %s)",
		window.Name, current.End.Format(time.RFC3339)))
	return s.windowView(window)
}

// StartMaintenance starts the window's next occurrence now; it runs its
// planned duration from now.
func (s *Service) StartMaintenance(user *users_models.User, id uuid.UUID) (*MaintenanceView, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrForbidden
	}
	window, err := s.findWindow(id)
	if err != nil {
		return nil, err
	}
	if !window.Enabled {
		return nil, errors.New("this window is paused; enable it first")
	}
	now := s.now()
	if currentOccurrence(window, now) != nil {
		return nil, errors.New("this window is already in progress")
	}
	next := nextOccurrence(window, now)
	if next == nil {
		return nil, errors.New("this window has no upcoming occurrence")
	}
	expected := window.Version
	window.Adjustments = override(window.Adjustments, OccurrenceOverride{
		Occurrence: next.scheduled, Start: now, End: now.Add(next.End.Sub(next.Start)),
	}, now)
	window.UpdatedAt = now
	if err := s.saveWindow(window, expected); err != nil {
		return nil, err
	}
	s.audit(user, fmt.Sprintf("Synthetics maintenance window %q started early (planned for %s)",
		window.Name, next.Start.Format(time.RFC3339)))
	return s.windowView(window)
}

// MaintenanceOccurrences are the occurrences of enabled windows in
// [from, to), with the monitors each covers.
func (s *Service) MaintenanceOccurrences(from, to time.Time) ([]MaintenanceOccurrence, error) {
	if !to.After(from) || to.Sub(from) > maxOccurrencesRange {
		return nil, errors.New("from must be before to, at most 93 days apart")
	}
	windows, err := s.repository.maintenanceWindows()
	if err != nil {
		return nil, err
	}
	checks, err := s.repository.checks()
	if err != nil {
		return nil, err
	}
	result := []MaintenanceOccurrence{}
	for _, window := range windows {
		occurrences := windowOccurrences(window, from, to)
		if len(occurrences) == 0 {
			continue
		}
		covered := coveredIDs(window.Scope, checks)
		for _, item := range occurrences {
			result = append(result, MaintenanceOccurrence{
				WindowID: window.ID, Name: window.Name, Reason: window.Reason,
				Start: item.Start, End: item.End, MonitorIDs: covered,
			})
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Start.Before(result[j].Start) })
	return result, nil
}

// MaintenanceFor is siteID's shared-config silences for the next maintenanceHorizon, one per
// occurrence and project of the monitors alerting there.
func (s *Service) MaintenanceFor(siteID string) ([]sharedconfig.MaintenanceSilence, error) {
	windows, err := s.repository.maintenanceWindows()
	if err != nil {
		return nil, err
	}
	if len(windows) == 0 {
		return nil, nil
	}
	checks, err := s.repository.checks()
	if err != nil {
		return nil, err
	}
	destinations, err := s.destinationsByID()
	if err != nil {
		return nil, err
	}
	now := s.now()
	var silences []sharedconfig.MaintenanceSilence
	for _, window := range windows {
		occurrences := windowOccurrences(window, now, now.Add(maintenanceHorizon))
		if len(occurrences) == 0 {
			continue
		}
		byProject := map[string][]string{}
		for _, check := range checks {
			destination, ok := destinations[check.DestinationID]
			if !ok || destination.SiteID != siteID || !check.Enabled || len(check.Alerts.ChannelIDs) == 0 ||
				!covers(window.Scope, check) {
				continue
			}
			byProject[destination.ProjectID] = append(byProject[destination.ProjectID], check.ID.String())
		}
		comment := "Synthetics maintenance: " + window.Name
		if window.Reason != "" {
			comment += " (" + window.Reason + ")"
		}
		for _, projectID := range sortedKeys(keysOf(byProject)) {
			ids := byProject[projectID]
			sort.Strings(ids)
			for _, item := range occurrences {
				silences = append(silences, sharedconfig.MaintenanceSilence{
					Key: "synthetics-maintenance:" + window.ID.String() + ":" + projectID + ":" +
						strconv.FormatInt(item.scheduled.Unix(), 10),
					ProjectID: projectID, Comment: comment, CheckIDs: ids, StartsAt: item.Start, EndsAt: item.End,
				})
			}
		}
	}
	return silences, nil
}

// PreviewSchedule validates a schedule and lists its next occurrences.
func (s *Service) PreviewSchedule(request SchedulePreviewRequest) (*SchedulePreview, error) {
	schedule := request.Schedule
	now := s.now()
	if err := schedule.Normalize(now); err != nil {
		return nil, err
	}
	preview := &SchedulePreview{Schedule: schedule, Occurrences: []spec.Occurrence{}}
	if schedule.Kind == spec.ScheduleOnce {
		once := spec.Occurrence{Start: *schedule.StartsAt, End: *schedule.EndsAt}
		preview.Occurrences = append(preview.Occurrences, once)
		return preview, nil
	}
	for _, horizon := range []time.Duration{40 * 24 * time.Hour, lookAhead} {
		upcoming := schedule.Occurrences(now, now.Add(horizon))
		if len(upcoming) >= previewCount || horizon == lookAhead {
			preview.Occurrences = upcoming[:min(previewCount, len(upcoming))]
			break
		}
	}
	return preview, nil
}

// windowFrom validates request into a window. existing (an update) keeps
// its start/end overrides unless the schedule changed.
func (s *Service) windowFrom(request MaintenanceRequest, existing *MaintenanceWindow) (*MaintenanceWindow, error) {
	schedule := request.Schedule
	if existing != nil && schedule.Kind != spec.ScheduleOnce && strings.TrimSpace(schedule.StartDate) == "" {
		schedule.StartDate = existing.Schedule.StartDate
	}
	if err := schedule.Normalize(s.now()); err != nil {
		return nil, err
	}
	monitor := spec.Maintenance{
		Name: request.Name, Reason: request.Reason, All: request.Scope.All, Folders: request.Scope.Folders,
		Schedule: schedule,
	}
	for _, id := range request.Scope.MonitorIDs {
		monitor.Monitors = append(monitor.Monitors, id.String())
	}
	if err := monitor.Normalize(); err != nil {
		return nil, err
	}
	scope := MaintenanceScope{All: monitor.All, Folders: monitor.Folders, MonitorIDs: []uuid.UUID{}}
	if !scope.All {
		for _, id := range request.Scope.MonitorIDs {
			if slices.Contains(scope.MonitorIDs, id) {
				continue
			}
			if _, err := s.repository.check(id); err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil, errors.New("one of the chosen monitors no longer exists; reload and choose again")
				}
				return nil, err
			}
			scope.MonitorIDs = append(scope.MonitorIDs, id)
		}
	}
	if existing != nil && !strings.EqualFold(monitor.Name, existing.Name) {
		if other, err := s.repository.windowByName(monitor.Name); err != nil {
			return nil, err
		} else if other != nil {
			return nil, fmt.Errorf("a maintenance window named %q already exists", monitor.Name)
		}
	}
	window := &MaintenanceWindow{
		Name: monitor.Name, Reason: monitor.Reason, Scope: scope, Schedule: ScheduleJSON(schedule),
		Adjustments: OccurrenceOverrides{}, Enabled: request.Enabled,
	}
	if existing != nil && jsonEqual(existing.Schedule, window.Schedule) {
		window.Adjustments = existing.Adjustments
	}
	return window, nil
}

func (s *Service) maintenanceView(window *MaintenanceWindow, checks []*Check) MaintenanceView {
	now := s.now()
	view := MaintenanceView{
		ID: window.ID, Name: window.Name, Reason: window.Reason, Scope: window.Scope,
		Schedule: spec.MaintenanceSchedule(window.Schedule), Enabled: window.Enabled, Version: window.Version,
		CreatedBy: window.CreatedBy, CreatedAt: window.CreatedAt, UpdatedAt: window.UpdatedAt,
		MonitorIDs: coveredIDs(window.Scope, checks),
	}
	if view.Scope.MonitorIDs == nil {
		view.Scope.MonitorIDs = []uuid.UUID{}
	}
	if view.Scope.Folders == nil {
		view.Scope.Folders = []string{}
	}
	if current := currentOccurrence(window, now); current != nil {
		view.Current = &current.Occurrence
	}
	if next := nextOccurrence(window, now); next != nil {
		view.Next = &next.Occurrence
	}
	past := windowOccurrences(window, now.Add(-lookBack), now)
	for i := len(past) - 1; i >= 0; i-- {
		if !past[i].End.After(now) {
			end := past[i].End
			view.LastEnded = &end
			break
		}
	}
	switch {
	case !window.Enabled:
		view.State = MaintenancePaused
	case view.Current != nil:
		view.State = MaintenanceActive
	case view.Next != nil:
		view.State = MaintenanceUpcoming
	default:
		view.State = MaintenancePast
	}
	return view
}

func (s *Service) windowView(window *MaintenanceWindow) (*MaintenanceView, error) {
	checks, err := s.repository.checks()
	if err != nil {
		return nil, err
	}
	view := s.maintenanceView(window, checks)
	return &view, nil
}

func (s *Service) findWindow(id uuid.UUID) (*MaintenanceWindow, error) {
	window, err := s.repository.window(id)
	return window, notFound(err)
}

func (s *Service) saveWindow(window *MaintenanceWindow, expected int) error {
	updated, err := s.repository.updateWindow(window, expected)
	if err != nil {
		return uniqueError(err, "a maintenance window named %q already exists", window.Name)
	}
	if !updated {
		return ErrMaintenanceConflict
	}
	return nil
}

// windowOccurrences are the window's occurrences overlapping [from, to)
// with its overrides applied; none while it's paused.
func windowOccurrences(window *MaintenanceWindow, from, to time.Time) []occurrence {
	if !window.Enabled {
		return nil
	}
	moved := map[int64]OccurrenceOverride{}
	for _, adjustment := range window.Adjustments {
		moved[adjustment.Occurrence.Unix()] = adjustment
	}
	var result []occurrence
	for _, item := range spec.MaintenanceSchedule(window.Schedule).Occurrences(from, to) {
		if _, ok := moved[item.Start.Unix()]; !ok {
			result = append(result, occurrence{Occurrence: item, scheduled: item.Start})
		}
	}
	// A moved occurrence counts where it is now, wherever it was scheduled.
	for _, adjustment := range window.Adjustments {
		if adjustment.End.After(adjustment.Start) && adjustment.End.After(from) && adjustment.Start.Before(to) {
			result = append(result, occurrence{
				Occurrence: spec.Occurrence{Start: adjustment.Start.UTC(), End: adjustment.End.UTC()},
				scheduled:  adjustment.Occurrence.UTC(),
			})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Start.Before(result[j].Start) })
	return result
}

func currentOccurrence(window *MaintenanceWindow, now time.Time) *occurrence {
	for _, item := range windowOccurrences(window, now, now.Add(time.Second)) {
		if !item.Start.After(now) && item.End.After(now) {
			return &item
		}
	}
	return nil
}

func nextOccurrence(window *MaintenanceWindow, now time.Time) *occurrence {
	for _, horizon := range []time.Duration{72 * time.Hour, 40 * 24 * time.Hour, lookAhead} {
		for _, item := range windowOccurrences(window, now, now.Add(horizon)) {
			if item.Start.After(now) {
				return &item
			}
		}
	}
	return nil
}

// override sets the override for its occurrence and forgets old ones.
func override(overrides OccurrenceOverrides, next OccurrenceOverride, now time.Time) OccurrenceOverrides {
	kept := OccurrenceOverrides{}
	for _, existing := range overrides {
		if !existing.Occurrence.Equal(next.Occurrence) && existing.End.After(now.Add(-adjustmentsKept)) {
			kept = append(kept, existing)
		}
	}
	return append(kept, next)
}

func covers(scope MaintenanceScope, check *Check) bool {
	if scope.All || slices.Contains(scope.MonitorIDs, check.ID) {
		return true
	}
	for _, folder := range scope.Folders {
		if check.Folder == folder || strings.HasPrefix(check.Folder, folder+"/") {
			return true
		}
	}
	return false
}

func coveredIDs(scope MaintenanceScope, checks []*Check) []uuid.UUID {
	ids := []uuid.UUID{}
	for _, check := range checks {
		if covers(scope, check) {
			ids = append(ids, check.ID)
		}
	}
	return ids
}

func keysOf[V any](values map[string]V) map[string]bool {
	keys := make(map[string]bool, len(values))
	for key := range values {
		keys[key] = true
	}
	return keys
}

// Repository

func (r *repository) maintenanceWindows() ([]*MaintenanceWindow, error) {
	var windows []*MaintenanceWindow
	err := storage.GetDb().Order("lower(name)").Find(&windows).Error
	return windows, err
}

func (r *repository) window(id uuid.UUID) (*MaintenanceWindow, error) {
	var window MaintenanceWindow
	if err := storage.GetDb().Where("id = ?", id).First(&window).Error; err != nil {
		return nil, err
	}
	return &window, nil
}

func (r *repository) windowByName(name string) (*MaintenanceWindow, error) {
	var window MaintenanceWindow
	err := storage.GetDb().Where("lower(name) = ?", strings.ToLower(name)).First(&window).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &window, err
}

func (r *repository) createWindow(window *MaintenanceWindow) error {
	return storage.GetDb().Create(window).Error
}

// updateWindow writes window if its stored version is still expected, and
// bumps the version; false means someone saved in between.
func (r *repository) updateWindow(window *MaintenanceWindow, expected int) (bool, error) {
	window.Version = expected + 1
	result := storage.GetDb().Model(&MaintenanceWindow{}).Where("id = ? AND version = ?", window.ID, expected).
		Select("*").Omit("id", "created_at", "created_by").Updates(window)
	return result.RowsAffected == 1, result.Error
}

func (r *repository) deleteWindow(id uuid.UUID) error {
	return storage.GetDb().Where("id = ?", id).Delete(&MaintenanceWindow{}).Error
}
