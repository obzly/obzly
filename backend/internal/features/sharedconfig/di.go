package sharedconfig

import (
	"github.com/google/uuid"

	"obzly/internal/util/rate_limit"
)

var controller = &Controller{rate_limit.NewRateLimiter()}

var auditLogWriter interface {
	WriteAuditLog(message string, userID *uuid.UUID, projectID *uuid.UUID)
}

func GetController() *Controller { return controller }

// SetAuditLogWriter is set from audit_logs.SetupDependencies (audit_logs
// can't be imported here without a cycle).
func SetAuditLogWriter(writer interface {
	WriteAuditLog(message string, userID *uuid.UUID, projectID *uuid.UUID)
},
) {
	auditLogWriter = writer
}
