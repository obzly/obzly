package useraccess

import (
	"errors"

	"obzly/internal/config"
	users_models "obzly/internal/features/users/models"
	users_repositories "obzly/internal/features/users/repositories"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var (
	ErrForbidden    = errors.New("insufficient permissions to view users")
	ErrUserNotFound = errors.New("user not found")
)

type Service struct {
	users *users_repositories.UserRepository
	repo  *repository
}

// GetUserDetail is one user's sign-in methods and access (admins).
func (s *Service) GetUserDetail(userID uuid.UUID, requester *users_models.User) (*UserDetail, error) {
	if !requester.CanManageUsers() {
		return nil, ErrForbidden
	}
	user, err := s.users.GetUserByIDWithoutPlan(userID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	detail := &UserDetail{
		ID:                  user.ID,
		Email:               user.Email,
		Name:                user.Name,
		Role:                user.Role,
		Status:              user.Status,
		IsActive:            user.IsActiveUser(),
		IsRootAdmin:         user.IsRootAdmin,
		CreatedAt:           user.CreatedAt,
		LastLoginAt:         user.LastLoginAt,
		LastActiveAt:        user.LastActiveAt,
		MFAEnabled:          user.MFAEnabled,
		MFARequired:         true,
		SignInMethods:       []SignInMethod{},
		Organizations:       []OrgMembership{},
		SiteAccess:          []SiteAccess{},
		ProjectRoles:        []LocalProjectRole{},
		ProjectRolesOnSites: config.GetEnv().ServiceMode.IsControlPlane(),
	}
	if err := s.fillSignIn(detail, user); err != nil {
		return nil, err
	}
	if err := s.fillOrganizations(detail); err != nil {
		return nil, err
	}
	if err := s.fillAccess(detail); err != nil {
		return nil, err
	}
	return detail, nil
}

func (s *Service) fillSignIn(detail *UserDetail, user *users_models.User) error {
	if user.HasPassword() {
		detail.SignInMethods = append(detail.SignInMethods, SignInMethod{Kind: "password"})
	}
	connectors, err := s.repo.ssoConnectors(user.ID)
	if err != nil {
		return err
	}
	for _, connector := range connectors {
		detail.SignInMethods = append(detail.SignInMethods, SignInMethod{Kind: "sso", Name: connector.Name})
	}
	if user.GitHubOAuthID != nil {
		detail.SignInMethods = append(detail.SignInMethods, SignInMethod{Kind: "github"})
	}
	if user.GoogleOAuthID != nil {
		detail.SignInMethods = append(detail.SignInMethods, SignInMethod{Kind: "google"})
	}
	if user.SCIMOrgID != nil {
		name, err := s.repo.orgName(*user.SCIMOrgID)
		if err != nil {
			return err
		}
		if name != "" {
			detail.ProvisionedBy = &OrgRef{ID: *user.SCIMOrgID, Name: name}
		}
	}
	return nil
}

func (s *Service) fillOrganizations(detail *UserDetail) error {
	orgs, err := s.repo.orgs(detail.ID)
	if err != nil {
		return err
	}
	teams, err := s.repo.teams(detail.ID)
	if err != nil {
		return err
	}
	byOrg := map[uuid.UUID][]string{}
	for _, team := range teams {
		byOrg[team.OrgID] = append(byOrg[team.OrgID], team.Name)
	}
	for _, org := range orgs {
		names := byOrg[org.OrgID]
		if names == nil {
			names = []string{}
		}
		detail.Organizations = append(detail.Organizations, OrgMembership{
			OrgID: org.OrgID, Name: org.Name, Role: org.Role, Suspended: org.Suspended, Teams: names,
		})
	}
	return nil
}

func (s *Service) fillAccess(detail *UserDetail) error {
	grants, err := s.repo.siteGrants(detail.ID)
	if err != nil {
		return err
	}
	for _, grant := range grants {
		via := "direct"
		if grant.ViaSSO {
			via = "sso"
		}
		detail.SiteAccess = append(detail.SiteAccess, SiteAccess{
			SiteID: grant.SiteID, SiteName: grant.SiteName, ProjectID: grant.ProjectID, Role: grant.Role, Via: via,
		})
	}
	memberships, err := s.repo.projectMemberships(detail.ID)
	if err != nil {
		return err
	}
	for _, membership := range memberships {
		via := "direct"
		switch {
		case membership.ViaSSO:
			via = "sso"
		case membership.CentralManaged:
			via = "control-plane"
		}
		projectID := membership.ProjectID
		detail.ProjectRoles = append(detail.ProjectRoles, LocalProjectRole{
			ProjectID: &projectID, ProjectName: membership.ProjectName, Role: membership.Role, Via: via,
		})
	}
	teamGrants, err := s.repo.teamGrants(detail.ID)
	if err != nil {
		return err
	}
	for _, grant := range teamGrants {
		if grant.SiteID == "" {
			detail.ProjectRoles = append(detail.ProjectRoles, LocalProjectRole{
				ProjectID: grant.ProjectID, ProjectName: grant.ProjectName, Role: grant.Role,
				Via: "team", Team: grant.Team, Org: grant.Org,
			})
			continue
		}
		detail.SiteAccess = append(detail.SiteAccess, SiteAccess{
			SiteID: grant.SiteID, SiteName: grant.SiteName, ProjectID: grant.ProjectID, Role: grant.Role,
			Via: "team", Team: grant.Team, Org: grant.Org,
		})
	}
	return nil
}
