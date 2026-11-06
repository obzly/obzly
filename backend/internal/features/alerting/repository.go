package alerting

import (
	"context"
	"errors"
	"time"

	"obzly/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type repository struct{}

// Rules

func (r *repository) saveRule(rule *AlertRule) error {
	return storage.GetDb().Transaction(func(tx *gorm.DB) error { return saveRuleTx(tx, rule) })
}

// saveRuleTx saves rule and replaces its channels, inside tx.
func saveRuleTx(tx *gorm.DB, rule *AlertRule) error {
	if err := tx.Save(rule).Error; err != nil {
		return err
	}
	if err := tx.Where("rule_id = ?", rule.ID).Delete(&ruleChannel{}).Error; err != nil {
		return err
	}
	for _, channelID := range rule.ChannelIDs {
		if err := tx.Create(&ruleChannel{RuleID: rule.ID, ChannelID: channelID}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (r *repository) rule(projectID, ruleID uuid.UUID) (*AlertRule, error) {
	var rule AlertRule
	if err := storage.GetDb().Where("project_id = ? AND id = ?", projectID, ruleID).First(&rule).Error; err != nil {
		return nil, err
	}
	return &rule, r.attachChannels([]*AlertRule{&rule})
}

func (r *repository) rules(projectID uuid.UUID) ([]*AlertRule, error) {
	var rules []*AlertRule
	if err := storage.GetDb().Where("project_id = ?", projectID).Order("name").Find(&rules).Error; err != nil {
		return nil, err
	}
	return rules, r.attachChannels(rules)
}

func (r *repository) enabledRules() ([]*AlertRule, error) {
	var rules []*AlertRule
	if err := storage.GetDb().Where("enabled").Find(&rules).Error; err != nil {
		return nil, err
	}
	return rules, r.attachChannels(rules)
}

// claimRule moves a rule's last_eval_at from previous to at, only if no
// other runner has moved it since; whether this caller got the rule.
func (r *repository) claimRule(ruleID uuid.UUID, previous *time.Time, at time.Time) (bool, error) {
	result := storage.GetDb().Exec(
		"UPDATE alert_rules SET last_eval_at = ? WHERE id = ? AND last_eval_at IS NOT DISTINCT FROM ?",
		at, ruleID, previous,
	)
	return result.RowsAffected == 1, result.Error
}

func (r *repository) deleteRule(projectID, ruleID uuid.UUID) (bool, error) {
	result := storage.GetDb().Where("project_id = ? AND id = ?", projectID, ruleID).Delete(&AlertRule{})
	return result.RowsAffected > 0, result.Error
}

type ruleChannel struct {
	RuleID    uuid.UUID `gorm:"column:rule_id;primaryKey"`
	ChannelID uuid.UUID `gorm:"column:channel_id;primaryKey"`
}

func (ruleChannel) TableName() string { return "alert_rule_channels" }

func (r *repository) attachChannels(rules []*AlertRule) error {
	if len(rules) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(rules))
	byID := make(map[uuid.UUID]*AlertRule, len(rules))
	for i, rule := range rules {
		ids[i] = rule.ID
		byID[rule.ID] = rule
		rule.ChannelIDs = []uuid.UUID{}
	}
	var links []ruleChannel
	if err := storage.GetDb().Where("rule_id IN ?", ids).Find(&links).Error; err != nil {
		return err
	}
	for _, link := range links {
		byID[link.RuleID].ChannelIDs = append(byID[link.RuleID].ChannelIDs, link.ChannelID)
	}
	return nil
}

// Channels

func (r *repository) saveChannel(channel *NotificationChannel) error {
	return storage.GetDb().Save(channel).Error
}

// availableTo narrows a query to the channels projectID may use.
func availableTo(query *gorm.DB, projectID uuid.UUID) *gorm.DB {
	return query.Where("all_projects OR project_ids @> ?::jsonb", `["`+projectID.String()+`"]`)
}

func (r *repository) channel(projectID, channelID uuid.UUID) (*NotificationChannel, error) {
	var channel NotificationChannel
	err := availableTo(storage.GetDb().Where("id = ?", channelID), projectID).First(&channel).Error
	return &channel, err
}

func (r *repository) channels(projectID uuid.UUID) ([]*NotificationChannel, error) {
	var channels []*NotificationChannel
	err := availableTo(storage.GetDb(), projectID).Order("name").Find(&channels).Error
	return channels, err
}

func (r *repository) allChannels() ([]*NotificationChannel, error) {
	var channels []*NotificationChannel
	err := storage.GetDb().Order("lower(name)").Find(&channels).Error
	return channels, err
}

// ruleCounts is how many alert rules use each channel.
func (r *repository) ruleCounts() (map[uuid.UUID]int, error) {
	var rows []struct {
		ChannelID uuid.UUID
		Count     int
	}
	err := storage.GetDb().Table("alert_rule_channels").Select("channel_id, count(*) AS count").
		Group("channel_id").Scan(&rows).Error
	counts := make(map[uuid.UUID]int, len(rows))
	for _, row := range rows {
		counts[row.ChannelID] = row.Count
	}
	return counts, err
}

func (r *repository) channelByID(channelID uuid.UUID) (*NotificationChannel, error) {
	var channel NotificationChannel
	err := storage.GetDb().Where("id = ?", channelID).First(&channel).Error
	return &channel, err
}

func (r *repository) deleteChannel(channelID uuid.UUID) (bool, error) {
	result := storage.GetDb().Where("id = ?", channelID).Delete(&NotificationChannel{})
	return result.RowsAffected > 0, result.Error
}

// countChannels is how many of ids projectID may use.
func (r *repository) countChannels(projectID uuid.UUID, ids []uuid.UUID) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	var count int64
	err := availableTo(storage.GetDb().Model(&NotificationChannel{}).Where("id IN ?", ids), projectID).
		Count(&count).Error
	return count, err
}

// States

func (r *repository) projectStates(projectID uuid.UUID) ([]*AlertState, error) {
	var states []*AlertState
	err := storage.GetDb().
		Where("rule_id IN (SELECT id FROM alert_rules WHERE project_id = ?)", projectID).
		Order("state, active_at DESC").Find(&states).Error
	return states, err
}

// Silences

func (r *repository) saveSilence(silence *Silence) error {
	return storage.GetDb().Create(silence).Error
}

func (r *repository) silences(projectID uuid.UUID) ([]*Silence, error) {
	var silences []*Silence
	err := storage.GetDb().Where("project_id = ? AND ends_at > ?", projectID, time.Now().UTC()).
		Order("ends_at").Find(&silences).Error
	return silences, err
}

func (r *repository) activeSilences(projectID uuid.UUID, at time.Time) ([]*Silence, error) {
	var silences []*Silence
	err := storage.GetDb().Where("project_id = ? AND starts_at <= ? AND ends_at > ?", projectID, at, at).
		Find(&silences).Error
	return silences, err
}

// silence is one of the project's silences, or nil.
func (r *repository) silence(projectID, silenceID uuid.UUID) (*Silence, error) {
	var silence Silence
	err := storage.GetDb().Where("project_id = ? AND id = ?", projectID, silenceID).First(&silence).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &silence, err
}

func (r *repository) deleteSilence(projectID, silenceID uuid.UUID) (bool, error) {
	result := storage.GetDb().Where("project_id = ? AND id = ?", projectID, silenceID).Delete(&Silence{})
	return result.RowsAffected > 0, result.Error
}

// Outbox

// claimOutbox takes up to limit notifications due now, oldest first, and
// leases them until leaseUntil: no other dispatcher sees them before then,
// and the claim is committed before anything is sent.
func (r *repository) claimOutbox(limit int, now, leaseUntil time.Time) ([]*OutboxItem, error) {
	var items []*OutboxItem
	err := storage.GetDb().Transaction(func(tx *gorm.DB) error {
		err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("sent_at IS NULL AND failed_at IS NULL AND next_attempt_at <= ?", now).
			Order("next_attempt_at").Limit(limit).Find(&items).Error
		if err != nil || len(items) == 0 {
			return err
		}
		ids := make([]uuid.UUID, len(items))
		for i, item := range items {
			ids[i] = item.ID
		}
		return tx.Model(&OutboxItem{}).Where("id IN ?", ids).Update("next_attempt_at", leaseUntil).Error
	})
	return items, err
}

func (r *repository) updateOutbox(id uuid.UUID, updates map[string]any) error {
	return storage.GetDb().Model(&OutboxItem{}).Where("id = ?", id).Updates(updates).Error
}

// pruneOutbox deletes up to limit notifications sent or given up before
// before; how many it deleted.
func (r *repository) pruneOutbox(ctx context.Context, before time.Time, limit int) (int64, error) {
	result := storage.GetDb().WithContext(ctx).Exec(`DELETE FROM notification_outbox WHERE id IN (
		SELECT id FROM notification_outbox WHERE sent_at < ? OR failed_at < ? LIMIT ?)`, before, before, limit)
	return result.RowsAffected, result.Error
}
