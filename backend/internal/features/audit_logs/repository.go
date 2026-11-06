package audit_logs

import (
	"fmt"
	"obzly/internal/storage"
	"strings"
	"time"

	"github.com/google/uuid"
)

type AuditLogRepository struct{}

func (r *AuditLogRepository) Create(auditLog *AuditLog) error {
	if auditLog.ID == uuid.Nil {
		auditLog.ID = uuid.New()
	}

	return storage.GetDb().Create(auditLog).Error
}

// AuditLogFilter narrows and orders the global or a project's audit log.
type AuditLogFilter struct {
	ProjectID  *uuid.UUID // nil = every project (the global log)
	BeforeDate *time.Time
	User       string // matches user name or email
	Action     string // matches the message
	SortBy     string // "createdAt" (default) or "user"
	SortOrder  string // "desc" (default) or "asc"
}

// likePattern turns a search term into a case-insensitive substring pattern,
// escaping LIKE's own wildcards so they match literally.
func likePattern(term string) string {
	escaped := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.TrimSpace(term))
	return "%" + escaped + "%"
}

func (f AuditLogFilter) where() (string, []any) {
	clauses := []string{}
	args := []any{}
	if f.ProjectID != nil {
		clauses = append(clauses, "al.project_id = ?")
		args = append(args, *f.ProjectID)
	}
	if f.BeforeDate != nil {
		clauses = append(clauses, "al.created_at < ?")
		args = append(args, *f.BeforeDate)
	}
	if strings.TrimSpace(f.User) != "" {
		clauses = append(clauses, `(u.name ILIKE ? ESCAPE '\' OR u.email ILIKE ? ESCAPE '\')`)
		args = append(args, likePattern(f.User), likePattern(f.User))
	}
	if strings.TrimSpace(f.Action) != "" {
		clauses = append(clauses, `al.message ILIKE ? ESCAPE '\'`)
		args = append(args, likePattern(f.Action))
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// orderBy is built only from fixed strings, never from the request text.
func (f AuditLogFilter) orderBy() string {
	direction := "DESC"
	if strings.EqualFold(f.SortOrder, "asc") {
		direction = "ASC"
	}
	if f.SortBy == "user" {
		// System entries (no user) sort after named users either way.
		return fmt.Sprintf(" ORDER BY COALESCE(u.name, u.email) %s NULLS LAST, al.created_at DESC", direction)
	}
	return " ORDER BY al.created_at " + direction
}

const globalAuditLogFrom = `
		FROM audit_logs al
		LEFT JOIN users u ON al.user_id = u.id
		LEFT JOIN projects p ON al.project_id = p.id`

func (r *AuditLogRepository) List(limit, offset int, filter AuditLogFilter) ([]*AuditLogDTO, error) {
	var auditLogs = make([]*AuditLogDTO, 0)

	where, args := filter.where()
	sql := `
		SELECT
			al.id,
			al.user_id,
			al.project_id,
			al.message,
			al.created_at,
			u.email as user_email,
			u.name as user_name,
			p.name as project_name` + globalAuditLogFrom + where + filter.orderBy() + " LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	err := storage.GetDb().Raw(sql, args...).Scan(&auditLogs).Error

	return auditLogs, err
}

func (r *AuditLogRepository) GetByUser(
	userID uuid.UUID,
	limit, offset int,
	beforeDate *time.Time,
) ([]*AuditLogDTO, error) {
	var auditLogs = make([]*AuditLogDTO, 0)

	sql := `
		SELECT 
			al.id,
			al.user_id,
			al.project_id,
			al.message,
			al.created_at,
			u.email as user_email,
			u.name as user_name,
			p.name as project_name
		FROM audit_logs al
		LEFT JOIN users u ON al.user_id = u.id
		LEFT JOIN projects p ON al.project_id = p.id
		WHERE al.user_id = ?`

	args := []interface{}{userID}

	if beforeDate != nil {
		sql += " AND al.created_at < ?"
		args = append(args, *beforeDate)
	}

	sql += " ORDER BY al.created_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	err := storage.GetDb().Raw(sql, args...).Scan(&auditLogs).Error

	return auditLogs, err
}

func (r *AuditLogRepository) Count(filter AuditLogFilter) (int64, error) {
	var count int64
	where, args := filter.where()
	err := storage.GetDb().Raw("SELECT COUNT(*)"+globalAuditLogFrom+where, args...).Scan(&count).Error
	return count, err
}

func (r *AuditLogRepository) CountByUser(userID uuid.UUID, beforeDate *time.Time) (int64, error) {
	var count int64
	query := storage.GetDb().Model(&AuditLog{}).Where("user_id = ?", userID)

	if beforeDate != nil {
		query = query.Where("created_at < ?", *beforeDate)
	}

	err := query.Count(&count).Error
	return count, err
}
