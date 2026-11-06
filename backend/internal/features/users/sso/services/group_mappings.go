package sso_services

import (
	"errors"
	"fmt"
	"strings"

	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	sso_dto "obzly/internal/features/users/sso/dto"
	sso_models "obzly/internal/features/users/sso/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

var (
	ErrInvalidGroupMappingRole = errors.New("role must be PROJECT_ADMIN, PROJECT_MEMBER or PROJECT_VIEWER")
	ErrGroupMappingExists      = errors.New("this group is already mapped to that project")
	ErrGroupMappingProject     = errors.New("project not found")
	ErrGroupMappingNotFound    = errors.New("group mapping not found")
	ErrGlobalAdminRootOnly     = errors.New("only the root admin can manage groups that grant the global Admin role")
)

// GroupGrants is what a user's IdP groups entitle them to on this connector.
type GroupGrants struct {
	// ProjectRoles is the highest mapped role per project.
	ProjectRoles map[uuid.UUID]users_enums.ProjectRole
	// GlobalAdmin is true if any of the groups is mapped to the global Admin role.
	GlobalAdmin bool
}

// mappableRoles are the project roles a group may grant. OWNER is
// deliberately absent: ownership only changes through an explicit transfer.
var mappableRoles = map[users_enums.ProjectRole]int{
	users_enums.ProjectRoleViewer: 1,
	users_enums.ProjectRoleMember: 2,
	users_enums.ProjectRoleAdmin:  3,
}

func toGroupMappingDTO(m *sso_models.GroupMapping) *sso_dto.GroupMappingResponseDTO {
	return &sso_dto.GroupMappingResponseDTO{
		ID:          m.ID,
		ConnectorID: m.ConnectorID,
		GroupName:   m.GroupName,
		ProjectID:   m.ProjectID,
		Role:        m.Role,
		CreatedAt:   m.CreatedAt,
	}
}

func (s *ConnectorService) ListGroupMappings(
	connectorID uuid.UUID,
	user *users_models.User,
) (*sso_dto.ListGroupMappingsResponseDTO, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrInsufficientPermissions
	}
	if err := s.requireConnector(connectorID); err != nil {
		return nil, err
	}

	mappings, err := s.groupMappingRepository.ListByConnector(connectorID)
	if err != nil {
		return nil, fmt.Errorf("failed to list group mappings: %w", err)
	}

	response := &sso_dto.ListGroupMappingsResponseDTO{
		Mappings: make([]*sso_dto.GroupMappingResponseDTO, 0, len(mappings)),
	}
	for _, m := range mappings {
		response.Mappings = append(response.Mappings, toGroupMappingDTO(m))
	}
	return response, nil
}

func (s *ConnectorService) CreateGroupMapping(
	connectorID uuid.UUID,
	request *sso_dto.CreateGroupMappingRequestDTO,
	user *users_models.User,
) (*sso_dto.GroupMappingResponseDTO, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrInsufficientPermissions
	}
	if request.ProjectID == nil {
		// A group that grants the global Admin role: same rule as manually
		// promoting an admin - only the root admin may do it.
		if request.Role != string(users_enums.UserRoleAdmin) {
			return nil, ErrInvalidGroupMappingRole
		}
		if !user.IsRootAdmin {
			return nil, ErrGlobalAdminRootOnly
		}
	} else if _, ok := mappableRoles[users_enums.ProjectRole(request.Role)]; !ok {
		return nil, ErrInvalidGroupMappingRole
	}
	groupName := strings.TrimSpace(request.GroupName)
	if groupName == "" {
		return nil, errors.New("group name is required")
	}
	if err := s.requireConnector(connectorID); err != nil {
		return nil, err
	}

	mapping := &sso_models.GroupMapping{
		ConnectorID: connectorID,
		GroupName:   groupName,
		ProjectID:   request.ProjectID,
		Role:        request.Role,
	}
	if err := s.groupMappingRepository.Create(mapping); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case "23505":
				return nil, ErrGroupMappingExists
			case "23503":
				return nil, ErrGroupMappingProject
			}
		}
		return nil, fmt.Errorf("failed to create group mapping: %w", err)
	}

	if s.auditLogWriter != nil {
		s.auditLogWriter.WriteAuditLog(
			fmt.Sprintf("SSO group mapping created: %q -> %s", groupName, request.Role),
			&user.ID,
			mapping.ProjectID,
		)
	}

	return toGroupMappingDTO(mapping), nil
}

func (s *ConnectorService) DeleteGroupMapping(
	connectorID, mappingID uuid.UUID,
	user *users_models.User,
) error {
	if !user.CanUpdateSettings() {
		return ErrInsufficientPermissions
	}

	existing, err := s.groupMappingRepository.GetByID(connectorID, mappingID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrGroupMappingNotFound
		}
		return fmt.Errorf("failed to load group mapping: %w", err)
	}
	if existing.ProjectID == nil && !user.IsRootAdmin {
		return ErrGlobalAdminRootOnly
	}

	deleted, err := s.groupMappingRepository.Delete(connectorID, mappingID)
	if err != nil {
		return fmt.Errorf("failed to delete group mapping: %w", err)
	}
	if !deleted {
		return ErrGroupMappingNotFound
	}

	if s.auditLogWriter != nil {
		s.auditLogWriter.WriteAuditLog("SSO group mapping deleted", &user.ID, nil)
	}
	return nil
}

func (s *ConnectorService) requireConnector(connectorID uuid.UUID) error {
	connector, err := s.connectorRepository.GetByID(connectorID)
	if err != nil {
		return fmt.Errorf("failed to get connector: %w", err)
	}
	if connector == nil {
		return ErrConnectorNotFound
	}
	return nil
}

// ResolveGroupGrants turns a user's IdP groups into what the connector's
// mappings grant: the highest project role per project when several groups
// apply, and whether any group grants the global Admin role.
func (s *ConnectorService) ResolveGroupGrants(
	connectorID uuid.UUID,
	groups []string,
) (*GroupGrants, error) {
	mappings, err := s.groupMappingRepository.ListByConnectorAndGroups(connectorID, groups)
	if err != nil {
		return nil, fmt.Errorf("failed to load group mappings: %w", err)
	}

	grants := &GroupGrants{ProjectRoles: make(map[uuid.UUID]users_enums.ProjectRole)}
	for _, m := range mappings {
		if m.ProjectID == nil {
			if m.Role == string(users_enums.UserRoleAdmin) {
				grants.GlobalAdmin = true
			}
			continue
		}

		role := users_enums.ProjectRole(m.Role)
		rank, ok := mappableRoles[role]
		if !ok {
			continue
		}
		if current, exists := grants.ProjectRoles[*m.ProjectID]; !exists || rank > mappableRoles[current] {
			grants.ProjectRoles[*m.ProjectID] = role
		}
	}
	return grants, nil
}
