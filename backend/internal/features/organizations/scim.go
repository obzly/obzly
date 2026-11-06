package organizations

import (
	"errors"
	"fmt"
	"strings"
	"time"

	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/storage"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// SCIM 2.0 provisioning (RFC 7643/7644) for one organization: Users are
// its members, Groups its teams. An account the organization's SCIM
// created (and isn't a global admin) is the organization's to deactivate;
// for any other, deprovisioning suspends its membership, so its teams
// grant nothing. Roles stay with team grants, managed in Obzly.

// scimError is returned as a SCIM error response.
type scimError struct {
	status   int
	scimType string
	detail   string
}

// scimUserRow is a member as SCIM sees it.
type scimUserRow struct {
	UserID      uuid.UUID              `gorm:"column:user_id"`
	Email       string                 `gorm:"column:email"`
	Name        string                 `gorm:"column:name"`
	Status      users_enums.UserStatus `gorm:"column:status"`
	Role        users_enums.UserRole   `gorm:"column:role"`
	IsRootAdmin bool                   `gorm:"column:is_root_admin"`
	SCIMOrgID   *uuid.UUID             `gorm:"column:scim_org_id"`
	CreatedAt   time.Time              `gorm:"column:created_at"`
	ExternalID  *string                `gorm:"column:external_id"`
	Suspended   bool                   `gorm:"column:suspended"`
	OrgRole     string                 `gorm:"column:org_role"`
}

// scimUserChange is what a create, replace or patch asks for; nil leaves
// a value as it is.
type scimUserChange struct {
	email      *string
	name       *string
	givenName  *string
	familyName *string
	externalID *string
	active     *bool
}

// scimGroupChange: members is the full new set when replace is true,
// else add and remove apply.
type scimGroupChange struct {
	name       *string
	externalID *string
	replace    bool
	members    []uuid.UUID
	add        []uuid.UUID
	remove     []uuid.UUID
}

// scimFilter is a supported "attribute eq value" filter; column is the
// SQL it compares (case-insensitively for names and emails).
type scimFilter struct {
	column string
	value  string
}

const scimPageMax = 200

func (e *scimError) Error() string { return e.detail }

func (r *scimUserRow) owned(orgID uuid.UUID) bool {
	return r.SCIMOrgID != nil && *r.SCIMOrgID == orgID && r.Role != users_enums.UserRoleAdmin && !r.IsRootAdmin
}

func (r *scimUserRow) active() bool {
	return r.Status != users_enums.UserStatusInactive && !r.Suspended
}

func (s *Service) scimListUsers(orgID uuid.UUID, filter *scimFilter, start, count int) ([]scimUserRow, int64, error) {
	query := scimUserQuery(orgID)
	if filter != nil {
		query = query.Where(filter.column+" = ?", filter.value)
	}
	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := []scimUserRow{}
	if count == 0 {
		return rows, total, nil
	}
	err := query.Order("u.created_at, u.id").Offset(start - 1).Limit(count).Scan(&rows).Error
	return rows, total, err
}

func (s *Service) scimGetUser(orgID, userID uuid.UUID) (*scimUserRow, error) {
	var rows []scimUserRow
	if err := scimUserQuery(orgID).Where("u.id = ?", userID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, &scimError{404, "", "User not found"}
	}
	return &rows[0], nil
}

// scimCreateUser provisions a new account the organization owns; it gets
// no password and signs in with SSO. An existing account is never joined by
// email - any organization's IdP could pull in, and see the name of, any
// account - so an owner adds it first, and the IdP finds it by userName.
func (s *Service) scimCreateUser(token *SCIMToken, change scimUserChange) (*scimUserRow, error) {
	if change.email == nil || !strings.Contains(*change.email, "@") || len(*change.email) > 254 {
		return nil, &scimError{400, "invalidValue", "userName (or a primary email) must be an email address"}
	}
	email := strings.ToLower(strings.TrimSpace(*change.email))
	active := change.active == nil || *change.active
	existing, err := s.users.GetUserByEmail(email)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, s.scimExistingAccountError(token.OrgID, existing.ID)
	}
	status := users_enums.UserStatusActive
	if !active {
		status = users_enums.UserStatusInactive
	}
	user := &users_models.User{
		ID:                   uuid.New(),
		Email:                email,
		Name:                 scimDisplayName(change, email),
		PasswordCreationTime: time.Now().UTC(),
		Role:                 users_enums.UserRoleMember,
		Status:               status,
		CreatedAt:            time.Now().UTC(),
		SCIMOrgID:            &token.OrgID,
	}
	if plan, err := s.plans.GetPlanByType(users_enums.UserPlanTypeDefault); err != nil {
		return nil, err
	} else if plan != nil {
		user.PlanID = &plan.ID
	}
	err = storage.GetDb().Transaction(func(tx *gorm.DB) error {
		if err := s.checkMemberQuota(tx, token.OrgID); err != nil {
			return err
		}
		if err := tx.Create(user).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO organization_memberships (org_id, user_id, role, external_id)
			VALUES (?, ?, ?, ?)`, token.OrgID, user.ID, RoleMember, change.externalID).Error
	})
	if err != nil {
		if isUniqueViolation(err) {
			// Created concurrently.
			return nil, &scimError{409, "uniqueness", "User already exists"}
		}
		if errors.Is(err, ErrPlanLimit) {
			return nil, &scimError{403, "", err.Error()}
		}
		return nil, err
	}
	s.audit.WriteAuditLog(fmt.Sprintf("SCIM (%s): user %s created", token.Name, email), nil, nil)
	return s.scimGetUser(token.OrgID, user.ID)
}

// scimExistingAccountError is the 409 for an email that has an account:
// a member is for the IdP to match with a filter, anyone else for an owner
// to add first. Neither reveals more than that the account exists.
func (s *Service) scimExistingAccountError(orgID, userID uuid.UUID) error {
	role, err := s.repo.role(orgID, userID)
	if err != nil {
		return err
	}
	if role != "" {
		return &scimError{409, "uniqueness", "User already exists in this organization"}
	}
	return &scimError{409, "uniqueness",
		"An account with this email already exists; an organization owner must add it to the organization first"}
}

// scimUpdateUser applies a replace or patch. Name and email change only
// on an account the organization owns: others belong to their owner.
func (s *Service) scimUpdateUser(token *SCIMToken, userID uuid.UUID, change scimUserChange) (*scimUserRow, error) {
	row, err := s.scimGetUser(token.OrgID, userID)
	if err != nil {
		return nil, err
	}
	owned := row.owned(token.OrgID)
	if owned {
		var name, email *string
		if change.name != nil || change.givenName != nil || change.familyName != nil {
			full := scimMergeName(row.Name, change)
			name = &full
		}
		if change.email != nil {
			normalized := strings.ToLower(strings.TrimSpace(*change.email))
			if !strings.Contains(normalized, "@") {
				return nil, &scimError{400, "invalidValue", "userName must be an email address"}
			}
			if normalized != row.Email {
				email = &normalized
			}
		}
		if name != nil || email != nil {
			if err := s.users.UpdateUserInfo(userID, name, email); err != nil {
				if isUniqueViolation(err) {
					return nil, &scimError{409, "uniqueness", "Another account has that email"}
				}
				return nil, err
			}
		}
	}
	if change.externalID != nil {
		if err := storage.GetDb().
			Exec("UPDATE organization_memberships SET external_id = ? WHERE org_id = ? AND user_id = ?",
				*change.externalID, token.OrgID, userID).
			Error; err != nil {
			return nil, err
		}
	}
	if change.active != nil && *change.active != row.active() {
		if err := s.scimSetActive(token, row, *change.active); err != nil {
			return nil, err
		}
	}
	return s.scimGetUser(token.OrgID, userID)
}

// scimDeleteUser removes a member: an owned account is also deactivated
// (never deleted - it may own projects).
func (s *Service) scimDeleteUser(token *SCIMToken, userID uuid.UUID) error {
	row, err := s.scimGetUser(token.OrgID, userID)
	if err != nil {
		return err
	}
	if row.owned(token.OrgID) && row.Status != users_enums.UserStatusInactive {
		if err := s.scimSetActive(token, row, false); err != nil {
			return err
		}
	}
	if err := s.dropMember(token.OrgID, userID, row.OrgRole); err != nil {
		if errors.Is(err, ErrLastOwner) {
			return &scimError{409, "mutability", err.Error()}
		}
		return err
	}
	s.audit.WriteAuditLog(fmt.Sprintf("SCIM (%s): user %s removed from organization", token.Name, row.Email), nil, nil)
	return nil
}

func (s *Service) scimListGroups(orgID uuid.UUID, filter *scimFilter, start, count int) ([]*Team, int64, error) {
	query := storage.GetDb().Table("teams t").Where("t.org_id = ?", orgID)
	if filter != nil {
		query = query.Where(filter.column+" = ?", filter.value)
	}
	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	teams := []*Team{}
	if count == 0 {
		return teams, total, nil
	}
	if err := query.Select("t.*").
		Order("t.created_at, t.id").
		Offset(start - 1).
		Limit(count).
		Scan(&teams).
		Error; err != nil {
		return nil, 0, err
	}
	if err := s.repo.fillTeams(teams); err != nil {
		return nil, 0, err
	}
	return teams, total, nil
}

func (s *Service) scimGetGroup(orgID, teamID uuid.UUID) (*Team, error) {
	team, err := s.repo.team(orgID, teamID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, &scimError{404, "", "Group not found"}
		}
		return nil, err
	}
	return team, nil
}

func (s *Service) scimCreateGroup(token *SCIMToken, change scimGroupChange) (*Team, error) {
	if change.name == nil || strings.TrimSpace(*change.name) == "" {
		return nil, &scimError{400, "invalidValue", "displayName is required"}
	}
	if err := s.scimCheckMembers(token.OrgID, change.members); err != nil {
		return nil, err
	}
	team := &Team{
		ID:         uuid.New(),
		OrgID:      token.OrgID,
		Name:       strings.TrimSpace(*change.name),
		ExternalID: change.externalID,
		CreatedAt:  time.Now().UTC(),
	}
	if err := storage.GetDb().Create(team).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, &scimError{409, "uniqueness", "A team with that name exists"}
		}
		return nil, err
	}
	for _, member := range change.members {
		if err := s.joinTeam(token.OrgID, team.ID, member); err != nil {
			return nil, err
		}
	}
	s.audit.WriteAuditLog(fmt.Sprintf("SCIM (%s): team %s created", token.Name, team.Name), nil, nil)
	return s.scimGetGroup(token.OrgID, team.ID)
}

func (s *Service) scimUpdateGroup(token *SCIMToken, teamID uuid.UUID, change scimGroupChange) (*Team, error) {
	team, err := s.scimGetGroup(token.OrgID, teamID)
	if err != nil {
		return nil, err
	}
	updates := map[string]any{}
	if change.name != nil && strings.TrimSpace(*change.name) != "" && *change.name != team.Name {
		updates["name"] = strings.TrimSpace(*change.name)
	}
	if change.externalID != nil {
		updates["external_id"] = *change.externalID
	}
	if len(updates) > 0 {
		if err := storage.GetDb().Model(&Team{}).Where("id = ?", teamID).Updates(updates).Error; err != nil {
			if isUniqueViolation(err) {
				return nil, &scimError{409, "uniqueness", "A team with that name exists"}
			}
			return nil, err
		}
	}
	add, remove := change.add, change.remove
	if change.replace {
		current := map[uuid.UUID]bool{}
		for _, member := range team.Members {
			current[member.UserID] = true
		}
		wanted := map[uuid.UUID]bool{}
		for _, member := range change.members {
			wanted[member] = true
			if !current[member] {
				add = append(add, member)
			}
		}
		for member := range current {
			if !wanted[member] {
				remove = append(remove, member)
			}
		}
	}
	if err := s.scimCheckMembers(token.OrgID, add); err != nil {
		return nil, err
	}
	for _, member := range add {
		if err := s.joinTeam(token.OrgID, teamID, member); err != nil {
			return nil, err
		}
	}
	for _, member := range remove {
		if _, err := s.leaveTeam(teamID, member); err != nil {
			return nil, err
		}
	}
	if len(add)+len(remove) > 0 {
		s.audit.WriteAuditLog(fmt.Sprintf("SCIM (%s): team %s members +%d -%d", token.Name, team.Name,
			len(add), len(remove)), nil, nil)
	}
	return s.scimGetGroup(token.OrgID, teamID)
}

func (s *Service) scimDeleteGroup(token *SCIMToken, teamID uuid.UUID) error {
	team, err := s.scimGetGroup(token.OrgID, teamID)
	if err != nil {
		return err
	}
	if err := s.deleteTeam(teamID); err != nil {
		return err
	}
	s.audit.WriteAuditLog(fmt.Sprintf("SCIM (%s): team %s deleted", token.Name, team.Name), nil, nil)
	return nil
}

func (s *Service) scimSetActive(token *SCIMToken, row *scimUserRow, active bool) error {
	if row.owned(token.OrgID) {
		user := &users_models.User{ID: row.UserID, Email: row.Email}
		if row.Status == users_enums.UserStatusInactive || !active {
			if err := s.management.SetStatusByProvisioning(user, active, "SCIM ("+token.Name+")"); err != nil {
				return err
			}
		}
		if !active || !row.Suspended {
			return nil
		}
		// Suspended while it was a global admin: lift that too.
	}
	if err := storage.GetDb().Exec("UPDATE organization_memberships SET suspended = ? WHERE org_id = ? AND user_id = ?",
		!active, token.OrgID, row.UserID).Error; err != nil {
		return err
	}
	sites, err := s.userSitesInOrg(token.OrgID, row.UserID)
	if err != nil {
		return err
	}
	invalidate(map[uuid.UUID][]string{row.UserID: sites})
	verb := "suspended"
	if active {
		verb = "unsuspended"
	}
	s.audit.WriteAuditLog(fmt.Sprintf("SCIM (%s): membership of %s %s", token.Name, row.Email, verb), nil, nil)
	return nil
}

// scimCheckMembers: a group's members must be the organization's.
func (s *Service) scimCheckMembers(orgID uuid.UUID, userIDs []uuid.UUID) error {
	if len(userIDs) == 0 {
		return nil
	}
	var count int64
	if err := storage.GetDb().Table("organization_memberships").
		Where("org_id = ? AND user_id IN ?", orgID, userIDs).Count(&count).Error; err != nil {
		return err
	}
	if int(count) != len(uniqueIDs(userIDs)) {
		return &scimError{400, "invalidValue", "Group members must be users of this organization"}
	}
	return nil
}

func scimUserQuery(orgID uuid.UUID) *gorm.DB {
	return storage.GetDb().Table("organization_memberships m").
		Select("u.id AS user_id, u.email, u.name, u.status, u.role, u.is_root_admin, u.scim_org_id, u.created_at, "+
			"m.external_id, m.suspended, m.role AS org_role").
		Joins("JOIN users u ON u.id = m.user_id").
		Where("m.org_id = ?", orgID)
}

func scimDisplayName(change scimUserChange, email string) string {
	if name := scimMergeName("", change); name != "" {
		return name
	}
	return strings.SplitN(email, "@", 2)[0]
}

// scimMergeName: a formatted/display name wins; else given and family
// names replace their halves of the current name.
func scimMergeName(current string, change scimUserChange) string {
	if change.name != nil && strings.TrimSpace(*change.name) != "" {
		return strings.TrimSpace(*change.name)
	}
	given, family, _ := strings.Cut(current, " ")
	if change.givenName != nil {
		given = strings.TrimSpace(*change.givenName)
	}
	if change.familyName != nil {
		family = strings.TrimSpace(*change.familyName)
	}
	return strings.TrimSpace(given + " " + family)
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func uniqueIDs(ids []uuid.UUID) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	out := []uuid.UUID{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
