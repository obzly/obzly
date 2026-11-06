package projects_services

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"obzly/internal/features/audit_logs"
	projects_models "obzly/internal/features/projects/models"
	projects_repositories "obzly/internal/features/projects/repositories"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/util/attrnames"

	"github.com/google/uuid"
)

var (
	ErrPromotedFieldForbidden = errors.New("insufficient permissions to change promoted fields")
	ErrPromotedFieldNotFound  = errors.New("field is not promoted")
)

// promotedFieldsTTL: how long ingest and queries reuse a project's promotions. A stale
// list is still correct (a row's promoted_mask says which columns it has), only slower.
const promotedFieldsTTL = 30 * time.Second

// reservedLogFields already have their own columns.
var reservedLogFields = []string{
	"id", "project_id", "timestamp", "level", "message", "client_ip", "trace_id", "span_id",
	"service_name", "service_version", "k8s_namespace_name", "k8s_pod_name", "k8s_container_name",
	"k8s_node_name", "k8s_cluster_name", "cloud_provider", "cloud_region", "fields",
}

// PromotedFieldService manages the log fields projects promote into typed columns.
type PromotedFieldService struct {
	projectService  *ProjectService
	repository      *projects_repositories.PromotedFieldRepository
	auditLogService *audit_logs.AuditLogService
	active          sync.Map // uuid.UUID -> cachedPromotedFields
}

type cachedPromotedFields struct {
	fields  []projects_models.PromotedField
	fetched time.Time
}

// ListPromotedFields is the project's active promotions; any member.
func (s *PromotedFieldService) ListPromotedFields(
	projectID uuid.UUID, user *users_models.User,
) ([]projects_models.PromotedField, error) {
	canAccess, _, err := s.projectService.CanUserAccessProject(projectID, user)
	if err != nil {
		return nil, err
	}
	if !canAccess {
		return nil, ErrRetentionPolicyNotFound
	}
	all, err := s.repository.ListByProject(projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to read promoted fields: %w", err)
	}
	return activeOnly(all), nil
}

// PromoteField gives field the next unused slot of its type; owners and
// admins only. New logs fill it within promotedFieldsTTL; older ones keep
// being read from the fields JSON.
func (s *PromotedFieldService) PromoteField(
	projectID uuid.UUID, field, fieldType string, user *users_models.User,
) (*projects_models.PromotedField, error) {
	canManage, err := s.projectService.CanUserManageProject(projectID, user)
	if err != nil {
		return nil, err
	}
	if !canManage {
		return nil, ErrPromotedFieldForbidden
	}
	field = attrnames.Normalize(field)
	switch {
	case field == "":
		return nil, errors.New("field name is required")
	case slices.Contains(reservedLogFields, field):
		return nil, fmt.Errorf("%q already has its own column", field)
	case fieldType != projects_models.PromotedTypeString && fieldType != projects_models.PromotedTypeNumber:
		return nil, errors.New("type must be string or number")
	}
	all, err := s.repository.ListByProject(projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to read promoted fields: %w", err)
	}
	used := map[string]bool{}
	for _, existing := range all {
		used[existing.Slot] = true
		if existing.RetiredAt == nil && existing.Field == field {
			return nil, fmt.Errorf("%q is already promoted", field)
		}
	}
	slot := ""
	for _, candidate := range projects_models.PromotedSlots {
		if !used[candidate] && projects_models.SlotType(candidate) == fieldType {
			slot = candidate
			break
		}
	}
	if slot == "" {
		return nil, fmt.Errorf("no %s columns left for this project", fieldType)
	}
	promoted := &projects_models.PromotedField{
		ProjectID: projectID, Slot: slot, Field: field, CreatedAt: time.Now().UTC(),
	}
	if err := s.repository.Create(promoted); err != nil {
		return nil, fmt.Errorf("failed to promote field: %w", err)
	}
	s.active.Delete(projectID)
	s.auditLogService.WriteAuditLog(fmt.Sprintf("Log field promoted: %s (%s)", field, fieldType), &user.ID, &projectID)
	return promoted, nil
}

// RetireField ends field's promotion; its column isn't reused.
func (s *PromotedFieldService) RetireField(projectID uuid.UUID, field string, user *users_models.User) error {
	canManage, err := s.projectService.CanUserManageProject(projectID, user)
	if err != nil {
		return err
	}
	if !canManage {
		return ErrPromotedFieldForbidden
	}
	retired, err := s.repository.Retire(projectID, attrnames.Normalize(field))
	if err != nil {
		return fmt.Errorf("failed to retire promoted field: %w", err)
	}
	if !retired {
		return ErrPromotedFieldNotFound
	}
	s.active.Delete(projectID)
	s.auditLogService.WriteAuditLog("Log field promotion removed: "+field, &user.ID, &projectID)
	return nil
}

// ActiveFields is projectID's promotions for ingest and queries, cached
// for promotedFieldsTTL; nil when they can't be read (everything is then
// read from the fields JSON, which is always complete).
func (s *PromotedFieldService) ActiveFields(projectID uuid.UUID) []projects_models.PromotedField {
	if value, ok := s.active.Load(projectID); ok {
		if cached, ok := value.(cachedPromotedFields); ok && time.Since(cached.fetched) < promotedFieldsTTL {
			return cached.fields
		}
	}
	all, err := s.repository.ListByProject(projectID)
	if err != nil {
		return nil
	}
	fields := activeOnly(all)
	s.active.Store(projectID, cachedPromotedFields{fields: fields, fetched: time.Now()})
	return fields
}

func activeOnly(all []projects_models.PromotedField) []projects_models.PromotedField {
	active := make([]projects_models.PromotedField, 0, len(all))
	for _, field := range all {
		if field.RetiredAt == nil {
			active = append(active, field)
		}
	}
	return active
}
