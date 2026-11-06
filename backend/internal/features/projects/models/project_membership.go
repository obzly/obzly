package projects_models

import (
	"time"

	users_enums "obzly/internal/features/users/enums"

	"github.com/google/uuid"
)

type ProjectMembership struct {
	ID        uuid.UUID               `json:"id"        gorm:"column:id"`
	UserID    uuid.UUID               `json:"userId"    gorm:"column:user_id"`
	ProjectID uuid.UUID               `json:"projectId" gorm:"column:project_id"`
	Role      users_enums.ProjectRole `json:"role"      gorm:"column:role"`
	CreatedAt time.Time               `json:"createdAt" gorm:"column:created_at"`

	// SSOConnectorID is set only on memberships created by SSO group sync;
	// a later sync updates or removes only those. NULL = added manually.
	SSOConnectorID *uuid.UUID `json:"-" gorm:"column:sso_connector_id"`

	// CentralManaged is SSOConnectorID's twin for the control plane's token exchange:
	// set only on memberships SyncCentralMemberships created/updated, and the only
	// ones a later exchange changes. Manual members and OWNER rows are never touched.
	CentralManaged bool `json:"-" gorm:"column:central_managed"`
}

func (ProjectMembership) TableName() string {
	return "project_memberships"
}
