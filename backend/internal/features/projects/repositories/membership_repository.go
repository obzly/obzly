package projects_repositories

import (
	"fmt"
	"time"

	projects_dto "obzly/internal/features/projects/dto"
	projects_models "obzly/internal/features/projects/models"
	users_enums "obzly/internal/features/users/enums"
	"obzly/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type MembershipRepository struct{}

func (r *MembershipRepository) CreateMembership(membership *projects_models.ProjectMembership) error {
	if membership.ID == uuid.Nil {
		membership.ID = uuid.New()
	}

	if membership.CreatedAt.IsZero() {
		membership.CreatedAt = time.Now().UTC()
	}

	return storage.GetDb().Create(membership).Error
}

func (r *MembershipRepository) GetMembershipByUserAndProject(
	userID, projectID uuid.UUID,
) (*projects_models.ProjectMembership, error) {
	var membership projects_models.ProjectMembership

	if err := storage.GetDb().
		Where("user_id = ? AND project_id = ?", userID, projectID).
		First(&membership).Error; err != nil {
		return nil, err
	}

	return &membership, nil
}

func (r *MembershipRepository) GetProjectMembers(
	projectID uuid.UUID,
) ([]*projects_dto.ProjectMemberResponseDTO, error) {
	var members []*projects_dto.ProjectMemberResponseDTO

	err := storage.GetDb().
		Table("project_memberships pm").
		Select("pm.id, pm.user_id, u.email, u.name, pm.role, pm.created_at").
		Joins("JOIN users u ON pm.user_id = u.id").
		Where("pm.project_id = ?", projectID).
		Order("pm.created_at ASC").
		Scan(&members).Error

	return members, err
}

// UpdateMemberRole is a manual role change: it also detaches the membership
// from any SSO connector, so group sync never overrides an admin's decision.
func (r *MembershipRepository) UpdateMemberRole(userID, projectID uuid.UUID, role users_enums.ProjectRole) error {
	return storage.GetDb().
		Model(&projects_models.ProjectMembership{}).
		Where("user_id = ? AND project_id = ?", userID, projectID).
		Updates(map[string]any{"role": role, "sso_connector_id": nil}).Error
}

// SwapOwner demotes the current OWNER of projectID to PROJECT_ADMIN and promotes
// newOwnerUserID to OWNER in one transaction, so the project never ends with zero or two
// owners.
func (r *MembershipRepository) SwapOwner(currentOwnerUserID, newOwnerUserID, projectID uuid.UUID) error {
	return storage.GetDb().Transaction(func(tx *gorm.DB) error {
		// Demote first: the one-OWNER partial unique index is checked per statement, so
		// promoting first would briefly have two owners and fail.
		if err := tx.Model(&projects_models.ProjectMembership{}).
			Where("user_id = ? AND project_id = ?", currentOwnerUserID, projectID).
			Updates(map[string]any{"role": users_enums.ProjectRoleAdmin, "sso_connector_id": nil}).Error; err != nil {
			return fmt.Errorf("failed to demote previous owner: %w", err)
		}

		if err := tx.Model(&projects_models.ProjectMembership{}).
			Where("user_id = ? AND project_id = ?", newOwnerUserID, projectID).
			Updates(map[string]any{"role": users_enums.ProjectRoleOwner, "sso_connector_id": nil}).Error; err != nil {
			return fmt.Errorf("failed to promote new owner: %w", err)
		}

		return nil
	})
}

// UpdateSSOManagedRole changes the role of a group-synced membership and
// keeps it attached to its connector.
func (r *MembershipRepository) UpdateSSOManagedRole(userID, projectID uuid.UUID, role users_enums.ProjectRole) error {
	return storage.GetDb().
		Model(&projects_models.ProjectMembership{}).
		Where("user_id = ? AND project_id = ?", userID, projectID).
		Update("role", role).Error
}

func (r *MembershipRepository) ListSSOManagedMemberships(
	userID, connectorID uuid.UUID,
) ([]*projects_models.ProjectMembership, error) {
	var memberships []*projects_models.ProjectMembership
	err := storage.GetDb().
		Where("user_id = ? AND sso_connector_id = ?", userID, connectorID).
		Find(&memberships).Error
	return memberships, err
}

// UpdateCentralManagedRole is UpdateSSOManagedRole's twin for a control
// plane's token-exchange assertion - see ProjectMembership.CentralManaged.
func (r *MembershipRepository) UpdateCentralManagedRole(
	userID, projectID uuid.UUID,
	role users_enums.ProjectRole,
) error {
	return storage.GetDb().
		Model(&projects_models.ProjectMembership{}).
		Where("user_id = ? AND project_id = ?", userID, projectID).
		Update("role", role).Error
}

// ListCentralManagedMemberships is every membership the control plane's token exchange
// owns for userID. A Site trusts only one control plane, so no issuer scoping is needed.
func (r *MembershipRepository) ListCentralManagedMemberships(
	userID uuid.UUID,
) ([]*projects_models.ProjectMembership, error) {
	var memberships []*projects_models.ProjectMembership
	err := storage.GetDb().
		Where("user_id = ? AND central_managed = TRUE", userID).
		Find(&memberships).Error
	return memberships, err
}

func (r *MembershipRepository) RemoveMember(userID, projectID uuid.UUID) error {
	return storage.GetDb().
		Where("user_id = ? AND project_id = ?", userID, projectID).
		Delete(&projects_models.ProjectMembership{}).Error
}

func (r *MembershipRepository) GetUserProjectRole(projectID, userID uuid.UUID) (*users_enums.ProjectRole, error) {
	var roles []users_enums.ProjectRole
	err := storage.GetDb().Raw(effectiveRolesSQL+
		" SELECT role FROM ranked WHERE project_id = ? AND rank_order = 1", userID, userID, userID, projectID).
		Scan(&roles).Error
	if err != nil {
		return nil, err
	}
	if len(roles) == 0 {
		return nil, nil
	}
	return &roles[0], nil
}

// effectiveRolesSQL ranks a user's roles per project: their membership and, on a
// standalone install, their teams' grants on its own projects (site ""; a Site-wide one
// applies to every non-system project). The strongest wins, a team never makes an owner,
// and a suspended organization member gets nothing from its teams. Arguments: the user
// id three times.
const effectiveRolesSQL = `WITH roles AS (
	SELECT project_id, role FROM project_memberships WHERE user_id = ?
	UNION ALL
	SELECT g.project_id, g.role FROM team_grants g JOIN team_memberships tm ON tm.team_id = g.team_id
	JOIN teams t ON t.id = g.team_id
	JOIN organization_memberships om ON om.org_id = t.org_id AND om.user_id = tm.user_id AND NOT om.suspended
	WHERE tm.user_id = ? AND g.site_id = '' AND g.project_id IS NOT NULL
	UNION ALL
	SELECT p.id, CASE g.role WHEN 'MEMBER' THEN 'PROJECT_MEMBER' ELSE 'PROJECT_VIEWER' END
	FROM team_grants g JOIN team_memberships tm ON tm.team_id = g.team_id
	JOIN teams t ON t.id = g.team_id
	JOIN organization_memberships om ON om.org_id = t.org_id AND om.user_id = tm.user_id AND NOT om.suspended
	CROSS JOIN projects p
	WHERE tm.user_id = ? AND g.site_id = '' AND g.project_id IS NULL AND NOT p.is_system
), ranked AS (
	SELECT project_id, role, row_number() OVER (PARTITION BY project_id ORDER BY
		CASE role WHEN 'OWNER' THEN 4 WHEN 'PROJECT_ADMIN' THEN 3 WHEN 'PROJECT_MEMBER' THEN 2 ELSE 1 END DESC) AS rank_order
	FROM roles
)`

func (r *MembershipRepository) GetProjectOwner(projectID uuid.UUID) (*projects_models.ProjectMembership, error) {
	var membership projects_models.ProjectMembership

	// Ordered explicitly in case a project predating the one-owner index has two.
	err := storage.GetDb().
		Where("project_id = ? AND role = ?", projectID, users_enums.ProjectRoleOwner).
		Order("created_at ASC").
		First(&membership).Error

	if err != nil {
		return nil, err
	}

	return &membership, nil
}

func (r *MembershipRepository) GetProjectsWithRolesByUserID(
	userRole users_enums.UserRole,
	userID uuid.UUID,
) ([]projects_dto.ProjectResponseDTO, error) {
	results := make([]projects_dto.ProjectResponseDTO, 0)

	if userRole == users_enums.UserRoleAdmin {
		err := storage.GetDb().Table("projects").Order("name ASC").Scan(&results).Error
		return results, err
	}

	err := storage.GetDb().Raw(effectiveRolesSQL+
		" SELECT p.id, p.name, p.created_at, p.is_system, r.role AS user_role FROM projects p"+
		" JOIN ranked r ON r.project_id = p.id AND r.rank_order = 1 ORDER BY p.name ASC",
		userID, userID, userID).Scan(&results).Error

	return results, err
}
