package alerting

import (
	"sort"
	"strings"
	"time"

	"obzly/internal/features/sharedconfig"
	"obzly/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ApplyManagedSilences replaces this Site's managed silences with the
// maintenance its control plane sent: one silence per occurrence and
// project, matching the monitors' check_id label. Projects this Site
// doesn't have are skipped.
func ApplyManagedSilences(items []sharedconfig.MaintenanceSilence) error {
	now := time.Now().UTC()
	return storage.GetDb().Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("managed_by <> ''").Delete(&Silence{}).Error; err != nil {
			return err
		}
		known := map[uuid.UUID]bool{}
		for _, item := range items {
			projectID, err := uuid.Parse(item.ProjectID)
			if err != nil || !item.EndsAt.After(now) || item.Key == "" {
				continue
			}
			exists, checked := known[projectID]
			if !checked {
				var projects int64
				if err := tx.Table("projects").Where("id = ?", projectID).Count(&projects).Error; err != nil {
					return err
				}
				exists, known[projectID] = projects > 0, projects > 0
			}
			matcher, ok := checkMatcher(item.CheckIDs)
			if !exists || !ok {
				continue
			}
			silence := &Silence{
				ID: uuid.New(), ProjectID: projectID, Matchers: Matchers{matcher}, StartsAt: item.StartsAt.UTC(),
				EndsAt: item.EndsAt.UTC(), Comment: truncate(item.Comment, 1000), CreatedAt: now,
				ManagedBy: truncate(item.Key, 300),
			}
			if err := tx.Create(silence).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// checkMatcher matches any of ids (only UUIDs, so the regex needs no escaping).
func checkMatcher(ids []string) (Matcher, bool) {
	valid := []string{}
	for _, id := range ids {
		if parsed, err := uuid.Parse(id); err == nil {
			valid = append(valid, parsed.String())
		}
	}
	switch len(valid) {
	case 0:
		return Matcher{}, false
	case 1:
		return Matcher{Name: "check_id", Value: valid[0]}, true
	}
	sort.Strings(valid)
	return Matcher{Name: "check_id", Value: strings.Join(valid, "|"), Regex: true}, true
}
