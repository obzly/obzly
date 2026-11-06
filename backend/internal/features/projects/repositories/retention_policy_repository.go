package projects_repositories

import (
	"time"

	projects_models "obzly/internal/features/projects/models"
	"obzly/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm/clause"
)

type RetentionPolicyRepository struct{}

func (r *RetentionPolicyRepository) ListByProject(projectID uuid.UUID) ([]projects_models.RetentionPolicy, error) {
	var policies []projects_models.RetentionPolicy
	err := storage.GetDb().Where("project_id = ?", projectID).Find(&policies).Error
	return policies, err
}

func (r *RetentionPolicyRepository) ListAll() ([]projects_models.RetentionPolicy, error) {
	var policies []projects_models.RetentionPolicy
	err := storage.GetDb().Find(&policies).Error
	return policies, err
}

// Upsert writes policies, replacing each project's existing row per signal.
func (r *RetentionPolicyRepository) Upsert(policies []projects_models.RetentionPolicy) error {
	if len(policies) == 0 {
		return nil
	}
	now := time.Now().UTC()
	for i := range policies {
		policies[i].UpdatedAt = now
	}
	return storage.GetDb().Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "project_id"}, {Name: "signal"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"retention_days", "rollup_days", "sample_rate", "slow_ms", "sample_rules", "updated_at",
		}),
	}).Create(&policies).Error
}
