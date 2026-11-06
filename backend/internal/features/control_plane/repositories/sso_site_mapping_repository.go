package control_plane_repositories

import (
	control_plane_models "obzly/internal/features/control_plane/models"
	"obzly/internal/storage"

	"github.com/google/uuid"
)

// SSOSiteMappingRepository stores sso_site_group_mappings and the
// SSO-owned rows of site_grants (sso_connector_id set).
type SSOSiteMappingRepository struct{}

func (r *SSOSiteMappingRepository) ListByConnector(
	connectorID uuid.UUID,
) ([]*control_plane_models.SSOSiteGroupMapping, error) {
	var mappings []*control_plane_models.SSOSiteGroupMapping
	err := storage.GetDb().
		Where("connector_id = ?", connectorID).
		Order("group_name ASC, site_id ASC, created_at ASC").
		Find(&mappings).Error
	return mappings, err
}

func (r *SSOSiteMappingRepository) ListByConnectorAndGroups(
	connectorID uuid.UUID,
	groups []string,
) ([]*control_plane_models.SSOSiteGroupMapping, error) {
	var mappings []*control_plane_models.SSOSiteGroupMapping
	if len(groups) == 0 {
		return mappings, nil
	}
	err := storage.GetDb().
		Where("connector_id = ? AND group_name IN ?", connectorID, groups).
		Find(&mappings).Error
	return mappings, err
}

func (r *SSOSiteMappingRepository) Create(mapping *control_plane_models.SSOSiteGroupMapping) error {
	if mapping.ID == uuid.Nil {
		mapping.ID = uuid.New()
	}
	return storage.GetDb().Create(mapping).Error
}

// Delete removes one mapping of connectorID; false if there was none.
func (r *SSOSiteMappingRepository) Delete(connectorID, mappingID uuid.UUID) (bool, error) {
	result := storage.GetDb().
		Where("id = ? AND connector_id = ?", mappingID, connectorID).
		Delete(&control_plane_models.SSOSiteGroupMapping{})
	return result.RowsAffected > 0, result.Error
}

// ListSSOGrants returns the site_grants connectorID's group sync owns for
// userID.
func (r *SSOSiteMappingRepository) ListSSOGrants(
	userID, connectorID uuid.UUID,
) ([]*control_plane_models.SiteGrant, error) {
	var grants []*control_plane_models.SiteGrant
	err := storage.GetDb().
		Where("user_id = ? AND sso_connector_id = ?", userID, connectorID).
		Find(&grants).Error
	return grants, err
}

func (r *SSOSiteMappingRepository) UpdateGrantRole(grantID uuid.UUID, role string) error {
	return storage.GetDb().Model(&control_plane_models.SiteGrant{}).
		Where("id = ?", grantID).
		Update("role", role).Error
}
