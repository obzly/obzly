package sso_repositories

import (
	sso_models "obzly/internal/features/users/sso/models"
	"obzly/internal/storage"

	"github.com/google/uuid"
)

type GroupMappingRepository struct{}

func (r *GroupMappingRepository) Create(mapping *sso_models.GroupMapping) error {
	if mapping.ID == uuid.Nil {
		mapping.ID = uuid.New()
	}
	return storage.GetDb().Create(mapping).Error
}

func (r *GroupMappingRepository) ListByConnector(connectorID uuid.UUID) ([]*sso_models.GroupMapping, error) {
	var mappings []*sso_models.GroupMapping
	err := storage.GetDb().
		Where("connector_id = ?", connectorID).
		Order("group_name ASC, created_at ASC").
		Find(&mappings).Error
	return mappings, err
}

func (r *GroupMappingRepository) ListByConnectorAndGroups(
	connectorID uuid.UUID,
	groups []string,
) ([]*sso_models.GroupMapping, error) {
	if len(groups) == 0 {
		return nil, nil
	}
	var mappings []*sso_models.GroupMapping
	err := storage.GetDb().
		Where("connector_id = ? AND group_name IN ?", connectorID, groups).
		Find(&mappings).Error
	return mappings, err
}

func (r *GroupMappingRepository) GetByID(connectorID, mappingID uuid.UUID) (*sso_models.GroupMapping, error) {
	var mapping sso_models.GroupMapping
	err := storage.GetDb().
		Where("id = ? AND connector_id = ?", mappingID, connectorID).
		First(&mapping).Error
	if err != nil {
		return nil, err
	}
	return &mapping, nil
}

// Delete removes the mapping only if it belongs to connectorID; reports
// whether a row was deleted.
func (r *GroupMappingRepository) Delete(connectorID, mappingID uuid.UUID) (bool, error) {
	result := storage.GetDb().
		Where("id = ? AND connector_id = ?", mappingID, connectorID).
		Delete(&sso_models.GroupMapping{})
	return result.RowsAffected > 0, result.Error
}
