package organizations

import (
	"errors"
	"fmt"
	"time"

	control_plane_services "obzly/internal/features/control_plane/services"
	"obzly/internal/features/orgplan"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// GetPlan is an organization's plan (members); zero limits without one.
func (s *Service) GetPlan(orgID uuid.UUID, user *users_models.User) (*orgplan.OrgPlan, error) {
	if _, err := s.repo.org(orgID); err != nil {
		return nil, notFound(err)
	}
	if _, err := s.access(orgID, user, false); err != nil {
		return nil, err
	}
	return s.planOf(storage.GetDb(), orgID)
}

// SetPlan replaces an organization's plan. Global admins only: an owner
// setting their own quotas would make them no quotas.
func (s *Service) SetPlan(orgID uuid.UUID, limits orgplan.Limits, user *users_models.User) (*orgplan.OrgPlan, error) {
	if !isAdmin(user) {
		return nil, ErrForbidden
	}
	org, err := s.repo.org(orgID)
	if err != nil {
		return nil, notFound(err)
	}
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	plan := &orgplan.OrgPlan{OrgID: orgID, Limits: limits, UpdatedBy: &user.ID, UpdatedAt: time.Now().UTC()}
	if err := storage.GetDb().Save(plan).Error; err != nil {
		return nil, err
	}
	s.audit.WriteAuditLog(fmt.Sprintf("Organization plan set for %s: %+v", org.Name, limits), &user.ID, nil)
	return plan, nil
}

// SitePlanDocument is the signed plan a Site fetches (control plane).
func (s *Service) SitePlanDocument(siteID string) (string, error) {
	site, err := control_plane_services.GetSiteService().GetSite(siteID)
	if err != nil || site == nil {
		return "", ErrNotFound
	}
	orgID, assigned, err := s.repo.siteOrg(siteID)
	if err != nil {
		return "", err
	}
	if !assigned {
		return control_plane_services.GetSigningKeyService().SignSitePlan(siteID, nil, orgplan.Limits{})
	}
	plan, err := s.planOf(storage.GetDb(), orgID)
	if err != nil {
		return "", err
	}
	return control_plane_services.GetSigningKeyService().SignSitePlan(siteID, &orgID, plan.Limits)
}

func (s *Service) planOf(db *gorm.DB, orgID uuid.UUID) (*orgplan.OrgPlan, error) {
	plan := &orgplan.OrgPlan{OrgID: orgID}
	err := db.Where("org_id = ?", orgID).First(plan).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &orgplan.OrgPlan{OrgID: orgID}, nil
	}
	return plan, err
}

// checkMemberQuota refuses a new member past the plan's seats; suspended
// members hold theirs until removed.
func (s *Service) checkMemberQuota(db *gorm.DB, orgID uuid.UUID) error {
	plan, err := s.planOf(db, orgID)
	if err != nil || plan.MaxMembers == 0 {
		return err
	}
	var count int64
	if err := db.Table("organization_memberships").Where("org_id = ?", orgID).Count(&count).Error; err != nil {
		return err
	}
	if count >= int64(plan.MaxMembers) {
		return fmt.Errorf("%w: the organization's plan allows %d members", ErrPlanLimit, plan.MaxMembers)
	}
	return nil
}

func (s *Service) checkSiteQuota(db *gorm.DB, orgID uuid.UUID) error {
	plan, err := s.planOf(db, orgID)
	if err != nil || plan.MaxSites == 0 {
		return err
	}
	var count int64
	if err := db.Table("organization_sites").Where("org_id = ?", orgID).Count(&count).Error; err != nil {
		return err
	}
	if count >= int64(plan.MaxSites) {
		return fmt.Errorf("%w: the organization's plan allows %d Sites", ErrPlanLimit, plan.MaxSites)
	}
	return nil
}
