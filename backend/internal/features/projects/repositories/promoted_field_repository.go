package projects_repositories

import (
	"time"

	projects_models "obzly/internal/features/projects/models"
	"obzly/internal/storage"

	"github.com/google/uuid"
)

type PromotedFieldRepository struct{}

// ListByProject is every promotion the project ever made, retired ones
// included (their slots stay taken).
func (r *PromotedFieldRepository) ListByProject(projectID uuid.UUID) ([]projects_models.PromotedField, error) {
	var fields []projects_models.PromotedField
	err := storage.GetDb().Where("project_id = ?", projectID).Order("created_at").Find(&fields).Error
	return fields, err
}

func (r *PromotedFieldRepository) Create(field *projects_models.PromotedField) error {
	return storage.GetDb().Create(field).Error
}

// Retire ends field's promotion; false when it isn't promoted.
func (r *PromotedFieldRepository) Retire(projectID uuid.UUID, field string) (bool, error) {
	result := storage.GetDb().Model(&projects_models.PromotedField{}).
		Where("project_id = ? AND field = ? AND retired_at IS NULL", projectID, field).
		Update("retired_at", time.Now().UTC())
	return result.RowsAffected > 0, result.Error
}
