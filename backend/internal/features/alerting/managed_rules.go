package alerting

import (
	"slices"
	"time"

	"obzly/internal/features/sharedconfig"
	"obzly/internal/storage"
	"obzly/internal/util/logger"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ApplyManagedRules syncs this Site's managed rules to what its control
// plane sent, keeping changed rules' state. Projects not on this Site are
// skipped and missing channels left off (warned), so one bad rule can't
// fail the whole sync.
func ApplyManagedRules(alerts []sharedconfig.MonitorAlert) error {
	now := time.Now().UTC()
	return storage.GetDb().Transaction(func(tx *gorm.DB) error {
		var existing []*AlertRule
		// Only Synthetics' rules: budget rules are this Site's own.
		if err := tx.Where("managed_by LIKE ?", ManagedBySyntheticsPrefix+"%").Find(&existing).Error; err != nil {
			return err
		}
		byKey := map[string]*AlertRule{}
		for _, rule := range existing {
			byKey[rule.ManagedBy] = rule
		}
		keep := map[string]bool{}
		for _, alert := range alerts {
			projectID, err := uuid.Parse(alert.ProjectID)
			if err != nil {
				continue
			}
			var projects int64
			if err := tx.Table("projects").Where("id = ?", projectID).Count(&projects).Error; err != nil {
				return err
			}
			if projects == 0 {
				continue
			}
			keep[alert.Key] = true
			if alert.ChannelIDs, err = usableChannels(tx, projectID, alert); err != nil {
				return err
			}
			rule, ok := byKey[alert.Key]
			if !ok {
				rule = &AlertRule{ID: uuid.New(), CreatedAt: now, ManagedBy: alert.Key, Enabled: true}
			}
			changed := !ok || rule.ProjectID != projectID || rule.Name != alert.Name ||
				rule.Query.PromQL != alert.PromQL || rule.Comparison != alert.Comparison ||
				rule.Threshold != alert.Threshold || rule.RepeatSeconds != alert.RepeatSeconds ||
				rule.IntervalSeconds != alert.IntervalSeconds || rule.Labels["severity"] != alert.Severity ||
				rule.Labels["check_id"] != alert.CheckID ||
				!sameChannels(tx, rule.ID, alert.ChannelIDs)
			if !changed {
				continue
			}
			rule.ProjectID, rule.Name, rule.Source = projectID, alert.Name, SourceMetrics
			rule.Query = RuleQuery{PromQL: alert.PromQL}
			rule.Comparison, rule.Threshold = alert.Comparison, alert.Threshold
			// The window is unused by PromQL rules but must be positive.
			rule.IntervalSeconds, rule.RepeatSeconds, rule.WindowSeconds = alert.IntervalSeconds, alert.RepeatSeconds, alert.IntervalSeconds
			rule.Labels = Labels{"severity": alert.Severity, "source": "synthetics"}
			if alert.CheckID != "" {
				rule.Labels["check_id"] = alert.CheckID
			}
			rule.Summary, rule.ChannelIDs, rule.UpdatedAt = alert.Summary, alert.ChannelIDs, now
			rule.LastEvalAt = nil
			if err := saveRuleTx(tx, rule); err != nil {
				return err
			}
		}
		for key, rule := range byKey {
			if !keep[key] {
				if err := tx.Where("rule_id = ?", rule.ID).Delete(&AlertState{}).Error; err != nil {
					return err
				}
				if err := tx.Where("rule_id = ?", rule.ID).Delete(&ruleChannel{}).Error; err != nil {
					return err
				}
				if err := tx.Delete(&AlertRule{}, "id = ?", rule.ID).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// usableChannels are the alert's channels this Site has for projectID.
func usableChannels(tx *gorm.DB, projectID uuid.UUID, alert sharedconfig.MonitorAlert) ([]uuid.UUID, error) {
	if len(alert.ChannelIDs) == 0 {
		return alert.ChannelIDs, nil
	}
	var have []uuid.UUID
	err := availableTo(tx.Model(&NotificationChannel{}).Where("id IN ?", alert.ChannelIDs), projectID).
		Pluck("id", &have).Error
	if err != nil {
		return nil, err
	}
	usable := make([]uuid.UUID, 0, len(have))
	for _, id := range uniqueIDs(alert.ChannelIDs) {
		if slices.Contains(have, id) {
			usable = append(usable, id)
		} else {
			logger.GetLogger().Warn("managed alert rule: channel not available on this Site, left out",
				"rule", alert.Key, "channelId", id, "projectId", projectID)
		}
	}
	return usable, nil
}

func sameChannels(tx *gorm.DB, ruleID uuid.UUID, want []uuid.UUID) bool {
	var have []uuid.UUID
	if err := tx.Model(&ruleChannel{}).Where("rule_id = ?", ruleID).Pluck("channel_id", &have).Error; err != nil {
		return false
	}
	if len(have) != len(want) {
		return false
	}
	seen := map[uuid.UUID]bool{}
	for _, id := range have {
		seen[id] = true
	}
	for _, id := range want {
		if !seen[id] {
			return false
		}
	}
	return true
}
