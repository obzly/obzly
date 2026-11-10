package alerting

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"obzly/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Managed-by prefixes: synthetics rules come from the control plane, budget
// rules are kept by this Site's AI budgets.
const (
	ManagedBySyntheticsPrefix = "synthetics:"
	ManagedByBudgetPrefix     = "ai-budget:"
	// ManagedByCloudCostPrefix: cost budgets and anomaly alerts kept by this Site's cloud cost.
	ManagedByCloudCostPrefix = "cloud-cost:"
	// ManagedByCloudPosturePrefix: finding alerts kept by this Site's cloud posture.
	ManagedByCloudPosturePrefix = "cloud-posture:"
)

// SourceSample is one series' value from a source another feature computes.
type SourceSample struct {
	Labels Labels
	Value  float64
}

// AICostSource computes an ai_cost rule's series at now.
type AICostSource func(ctx context.Context, rule *AlertRule, now time.Time) ([]SourceSample, error)

var aiCostSource AICostSource

// CloudCostSource computes a cloud_cost rule's series at now.
type CloudCostSource = AICostSource

var cloudCostSource CloudCostSource

// SetCloudCostSource wires the cloud_cost source (set at startup, before the evaluator runs).
func SetCloudCostSource(source CloudCostSource) {
	cloudCostSource = source
}

var cloudPostureSource AICostSource

// SetCloudPostureSource wires the cloud_posture source (set at startup, before the evaluator runs).
func SetCloudPostureSource(source AICostSource) {
	cloudPostureSource = source
}

// ManagedRule is a rule a feature on this Site keeps in sync; Key is its
// managed_by. Users can't edit or delete it in Alerts.
type ManagedRule struct {
	Key             string
	ProjectID       uuid.UUID
	Name            string
	Source          string
	Query           RuleQuery
	Comparison      string
	Threshold       float64
	IntervalSeconds int
	RepeatSeconds   int
	Labels          Labels
	Summary         string
	ChannelIDs      []uuid.UUID
}

// SetAICostSource wires the ai_cost source (set at startup, before the
// evaluator runs).
func SetAICostSource(source AICostSource) {
	aiCostSource = source
}

// SaveManagedRule creates or updates the rule keyed by managed.Key, keeping
// its alert states. Channels the project can't use are left off.
func SaveManagedRule(managed *ManagedRule) error {
	if managed.Key == "" || strings.HasPrefix(managed.Key, ManagedBySyntheticsPrefix) {
		return errors.New("invalid managed rule key")
	}
	now := time.Now().UTC()
	return storage.GetDb().Transaction(func(tx *gorm.DB) error {
		var rule AlertRule
		err := tx.Where("managed_by = ?", managed.Key).First(&rule).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			rule = AlertRule{ID: uuid.New(), CreatedAt: now, ManagedBy: managed.Key, Enabled: true}
		} else if err != nil {
			return err
		}
		channels, err := usableChannelIDs(tx, managed.ProjectID, managed.ChannelIDs)
		if err != nil {
			return err
		}
		rule.ProjectID, rule.Name, rule.Source, rule.Query = managed.ProjectID, managed.Name, managed.Source, managed.Query
		rule.Comparison, rule.Threshold = managed.Comparison, managed.Threshold
		rule.IntervalSeconds, rule.WindowSeconds = managed.IntervalSeconds, managed.IntervalSeconds
		rule.RepeatSeconds, rule.ForSeconds = managed.RepeatSeconds, 0
		rule.Labels, rule.Summary = managed.Labels, truncate(managed.Summary, 2000)
		if rule.Labels == nil {
			rule.Labels = Labels{}
		}
		rule.ChannelIDs, rule.UpdatedAt, rule.LastEvalAt = channels, now, nil
		return saveRuleTx(tx, &rule)
	})
}

// DeleteManagedRule removes the rule keyed by key with its alerts.
func DeleteManagedRule(key string) error {
	return storage.GetDb().Transaction(func(tx *gorm.DB) error {
		var ids []uuid.UUID
		if err := tx.Model(&AlertRule{}).Where("managed_by = ?", key).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		if err := tx.Where("rule_id IN ?", ids).Delete(&AlertState{}).Error; err != nil {
			return err
		}
		if err := tx.Where("rule_id IN ?", ids).Delete(&ruleChannel{}).Error; err != nil {
			return err
		}
		return tx.Where("id IN ?", ids).Delete(&AlertRule{}).Error
	})
}

// ManagedRuleKeys are the managed_by keys of the rules starting with prefix.
func ManagedRuleKeys(prefix string) (map[string]bool, error) {
	var keys []string
	err := storage.GetDb().Model(&AlertRule{}).Where("managed_by LIKE ?", prefix+"%").Pluck("managed_by", &keys).Error
	result := make(map[string]bool, len(keys))
	for _, key := range keys {
		result[key] = true
	}
	return result, err
}

// UsableChannelIDs are those of ids (shared notification channels) that
// projectID may use, deduplicated.
func UsableChannelIDs(projectID uuid.UUID, ids []uuid.UUID) ([]uuid.UUID, error) {
	return usableChannelIDs(storage.GetDb(), projectID, ids)
}

// WithClock is this evaluator reading the time from now (tests, replays).
func (e *Evaluator) WithClock(now func() time.Time) *Evaluator {
	clone := *e
	clone.now = now
	return &clone
}

func usableChannelIDs(tx *gorm.DB, projectID uuid.UUID, ids []uuid.UUID) ([]uuid.UUID, error) {
	usable := []uuid.UUID{}
	if len(ids) == 0 {
		return usable, nil
	}
	var have []uuid.UUID
	err := availableTo(tx.Model(&NotificationChannel{}).Where("id IN ?", ids), projectID).Pluck("id", &have).Error
	if err != nil {
		return nil, err
	}
	for _, id := range uniqueIDs(ids) {
		if slices.Contains(have, id) {
			usable = append(usable, id)
		}
	}
	return usable, nil
}
