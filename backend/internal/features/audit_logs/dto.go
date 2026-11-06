package audit_logs

import (
	"time"

	"github.com/google/uuid"
)

type GetAuditLogsRequest struct {
	Limit      int        `form:"limit"      json:"limit"`
	Offset     int        `form:"offset"     json:"offset"`
	BeforeDate *time.Time `form:"beforeDate" json:"beforeDate"`

	// Global and project logs: case-insensitive substring filters, and sorting.
	User      string `form:"user"      json:"user"`      // user name or email
	Action    string `form:"action"    json:"action"`    // the log message
	SortBy    string `form:"sortBy"    json:"sortBy"`    // createdAt (default) or user
	SortOrder string `form:"sortOrder" json:"sortOrder"` // desc (default) or asc
}

type GetAuditLogsResponse struct {
	AuditLogs []*AuditLogDTO `json:"auditLogs"`
	Total     int64          `json:"total"`
	Limit     int            `json:"limit"`
	Offset    int            `json:"offset"`
}

type AuditLogDTO struct {
	ID          uuid.UUID  `json:"id"          gorm:"column:id"`
	UserID      *uuid.UUID `json:"userId"      gorm:"column:user_id"`
	ProjectID   *uuid.UUID `json:"projectId"   gorm:"column:project_id"`
	Message     string     `json:"message"     gorm:"column:message"`
	CreatedAt   time.Time  `json:"createdAt"   gorm:"column:created_at"`
	UserEmail   *string    `json:"userEmail"   gorm:"column:user_email"`
	UserName    *string    `json:"userName"    gorm:"column:user_name"`
	ProjectName *string    `json:"projectName" gorm:"column:project_name"`
}
