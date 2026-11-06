package synthetics

import (
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"obzly/internal/synthetics/spec"
)

// MaintenancePreview is what an import would do to maintenance windows.
type MaintenancePreview struct {
	Create    []string       `json:"create"`
	Update    []ImportChange `json:"update"`
	Unchanged []string       `json:"unchanged"`
	Delete    []string       `json:"delete"`
}

// maintenancePlan is what applying a file would write to windows.
type maintenancePlan struct {
	create, update []*MaintenanceWindow
	deleteIDs      []uuid.UUID
}

// planMaintenance matches the file's windows to stored ones by name. monitorIDs resolves monitor
// names (stored or in the file); pruned are the monitors the import deletes.
func (s *Service) planMaintenance(
	list *spec.MonitorList, monitorIDs map[string]uuid.UUID, pruned map[string]bool, plan *importPlan,
) error {
	plan.preview.Maintenance = MaintenancePreview{
		Create: []string{}, Update: []ImportChange{}, Unchanged: []string{}, Delete: []string{},
	}
	if !list.HasMaintenance {
		return nil
	}
	existing, err := s.repository.maintenanceWindows()
	if err != nil {
		return err
	}
	byName := map[string]*MaintenanceWindow{}
	for _, window := range existing {
		byName[strings.ToLower(window.Name)] = window
	}
	inFile := map[string]bool{}
	for _, item := range list.Maintenance {
		inFile[strings.ToLower(item.Name)] = true
		previous := byName[strings.ToLower(item.Name)]
		window, problem := s.importedWindow(item, previous, monitorIDs, pruned)
		if problem != "" {
			plan.preview.Errors = append(plan.preview.Errors, spec.ImportError{Window: item.Name, Message: problem})
			continue
		}
		if previous == nil {
			window.ID, window.Version, window.CreatedAt, window.UpdatedAt = uuid.New(), 1, s.now(), s.now()
			plan.windows.create = append(plan.windows.create, window)
			plan.preview.Maintenance.Create = append(plan.preview.Maintenance.Create, window.Name)
			continue
		}
		changes := describeWindowChanges(previous, window)
		if len(changes) == 0 {
			plan.preview.Maintenance.Unchanged = append(plan.preview.Maintenance.Unchanged, window.Name)
			continue
		}
		window.ID, window.Version, window.CreatedAt, window.CreatedBy = previous.ID, previous.Version+1, previous.CreatedAt, previous.CreatedBy
		window.UpdatedAt = s.now()
		if jsonEqual(previous.Schedule, window.Schedule) {
			window.Adjustments = previous.Adjustments
		}
		plan.windows.update = append(plan.windows.update, window)
		plan.preview.Maintenance.Update = append(plan.preview.Maintenance.Update,
			ImportChange{Name: window.Name, Changes: changes})
	}
	if list.Prune {
		for _, window := range existing {
			if !inFile[strings.ToLower(window.Name)] {
				plan.windows.deleteIDs = append(plan.windows.deleteIDs, window.ID)
				plan.preview.Maintenance.Delete = append(plan.preview.Maintenance.Delete, window.Name)
			}
		}
	}
	return nil
}

// importedWindow builds the stored form of item; a problem is a monitor
// name that doesn't resolve.
func (s *Service) importedWindow(
	item spec.Maintenance, previous *MaintenanceWindow, monitorIDs map[string]uuid.UUID, pruned map[string]bool,
) (*MaintenanceWindow, string) {
	schedule := item.Schedule
	if schedule.Kind != spec.ScheduleOnce && schedule.StartDate == "" {
		if previous != nil && previous.Schedule.StartDate != "" {
			schedule.StartDate = previous.Schedule.StartDate
		} else if err := schedule.Normalize(s.now()); err != nil {
			return nil, err.Error()
		}
	}
	scope := MaintenanceScope{All: item.All, Folders: item.Folders, MonitorIDs: []uuid.UUID{}}
	for _, name := range item.Monitors {
		key := strings.ToLower(strings.TrimSpace(name))
		id, ok := monitorIDs[key]
		if !ok || pruned[key] {
			return nil, fmt.Sprintf("monitors: no monitor named %q", name)
		}
		if !slices.Contains(scope.MonitorIDs, id) {
			scope.MonitorIDs = append(scope.MonitorIDs, id)
		}
	}
	return &MaintenanceWindow{
		Name: item.Name, Reason: item.Reason, Scope: scope, Schedule: ScheduleJSON(schedule),
		Adjustments: OccurrenceOverrides{}, Enabled: item.Enabled,
	}, ""
}

// exportMaintenance is every window in the YAML format, monitors by name.
func (s *Service) exportMaintenance(checkNames map[uuid.UUID]string) ([]spec.Maintenance, error) {
	windows, err := s.repository.maintenanceWindows()
	if err != nil {
		return nil, err
	}
	items := make([]spec.Maintenance, 0, len(windows))
	for _, window := range windows {
		item := spec.Maintenance{
			Name: window.Name, Reason: window.Reason, All: window.Scope.All, Folders: window.Scope.Folders,
			Enabled: window.Enabled, Schedule: spec.MaintenanceSchedule(window.Schedule),
		}
		for _, id := range window.Scope.MonitorIDs {
			if name, ok := checkNames[id]; ok {
				item.Monitors = append(item.Monitors, name)
			}
		}
		items = append(items, item)
	}
	return items, nil
}

func describeWindowChanges(old, updated *MaintenanceWindow) []string {
	var changes []string
	if old.Name != updated.Name {
		changes = append(changes, fmt.Sprintf("renamed %q → %q", old.Name, updated.Name))
	}
	if old.Reason != updated.Reason {
		changes = append(changes, "reason changed")
	}
	if old.Enabled != updated.Enabled {
		changes = append(changes, map[bool]string{true: "enabled", false: "paused"}[updated.Enabled])
	}
	if !jsonEqual(normalizedScope(old.Scope), normalizedScope(updated.Scope)) {
		changes = append(changes, "monitors covered changed")
	}
	if !jsonEqual(old.Schedule, updated.Schedule) {
		changes = append(changes, "schedule changed")
	}
	return changes
}

func normalizedScope(scope MaintenanceScope) MaintenanceScope {
	ids := append([]uuid.UUID{}, scope.MonitorIDs...)
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	folders := append([]string{}, scope.Folders...)
	slices.Sort(folders)
	return MaintenanceScope{All: scope.All, MonitorIDs: ids, Folders: folders}
}

// applyMaintenancePlan writes plan inside an import's transaction.
func applyMaintenancePlan(tx *gorm.DB, plan maintenancePlan) error {
	for _, window := range plan.create {
		if err := tx.Create(window).Error; err != nil {
			return err
		}
	}
	for _, window := range plan.update {
		result := tx.Model(&MaintenanceWindow{}).Where("id = ? AND version = ?", window.ID, window.Version-1).
			Select("*").Omit("id", "created_at", "created_by").Updates(window)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrConflict
		}
	}
	if len(plan.deleteIDs) > 0 {
		return tx.Where("id IN ?", plan.deleteIDs).Delete(&MaintenanceWindow{}).Error
	}
	return nil
}
