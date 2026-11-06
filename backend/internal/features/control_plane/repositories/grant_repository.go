package control_plane_repositories

import (
	"errors"

	control_plane_models "obzly/internal/features/control_plane/models"
	"obzly/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type GrantRepository struct{}

func (r *GrantRepository) Create(grant *control_plane_models.SiteGrant) error {
	if grant.ID == uuid.Nil {
		grant.ID = uuid.New()
	}
	return storage.GetDb().Create(grant).Error
}

func (r *GrantRepository) GetByID(id uuid.UUID) (*control_plane_models.SiteGrant, error) {
	var grant control_plane_models.SiteGrant
	err := storage.GetDb().Where("id = ?", id).First(&grant).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &grant, nil
}

// ListForUser returns every grant userID holds, across all Sites.
func (r *GrantRepository) ListForUser(userID uuid.UUID) ([]*control_plane_models.SiteGrant, error) {
	var grants []*control_plane_models.SiteGrant
	err := storage.GetDb().Where("user_id = ?", userID).Find(&grants).Error
	return grants, err
}

// ListForUserAndSite is ListForUser scoped to one Site, so an assertion never carries
// another Site's grants.
func (r *GrantRepository) ListForUserAndSite(
	userID uuid.UUID,
	siteID string,
) ([]*control_plane_models.SiteGrant, error) {
	var grants []*control_plane_models.SiteGrant
	err := storage.GetDb().Where("user_id = ? AND site_id = ?", userID, siteID).Find(&grants).Error
	return grants, err
}

func (r *GrantRepository) ListForSite(siteID string) ([]*control_plane_models.SiteGrant, error) {
	var grants []*control_plane_models.SiteGrant
	err := storage.GetDb().Where("site_id = ?", siteID).Find(&grants).Error
	return grants, err
}

func (r *GrantRepository) Delete(id uuid.UUID) error {
	return storage.GetDb().Where("id = ?", id).Delete(&control_plane_models.SiteGrant{}).Error
}

// TeamGrants returns userID's team grants shaped as Site grants, filtered to siteID
// when it isn't "". Grants on this instance's own projects (site "") are excluded.
func (r *GrantRepository) TeamGrants(userID uuid.UUID, siteID string) ([]*control_plane_models.SiteGrant, error) {
	query := storage.GetDb().Table("team_grants g").
		Select("g.id, tm.user_id, g.site_id, g.project_id, g.role, "+
			"COALESCE(g.created_by, '00000000-0000-0000-0000-000000000000'::uuid) AS created_by, g.created_at").
		Joins("JOIN team_memberships tm ON tm.team_id = g.team_id").
		Joins("JOIN teams t ON t.id = g.team_id").
		Joins("JOIN organization_memberships om ON om.org_id = t.org_id AND om.user_id = tm.user_id").
		Where("tm.user_id = ? AND g.site_id <> '' AND NOT om.suspended", userID)
	if siteID != "" {
		query = query.Where("g.site_id = ?", siteID)
	}
	var grants []*control_plane_models.SiteGrant
	err := query.Scan(&grants).Error
	return grants, err
}
