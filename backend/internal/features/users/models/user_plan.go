package users_models

import (
	users_enums "obzly/internal/features/users/enums"

	"github.com/google/uuid"
)

type UserPlan struct {
	ID                   uuid.UUID                `json:"id"                   gorm:"column:id"`
	Name                 string                   `json:"name"                 gorm:"column:name"`
	Type                 users_enums.UserPlanType `json:"type"                 gorm:"column:type"`
	IsPublic             bool                     `json:"isPublic"             gorm:"column:is_public"`
	AllowedProjectsCount int                      `json:"allowedProjectsCount" gorm:"column:allowed_projects_count"`
	WarningText          string                   `json:"warningText"          gorm:"column:warning_text"`
	UpgradeText          string                   `json:"upgradeText"          gorm:"column:upgrade_text"`

	// Rate Limiting & Quotas. If some value is 0, it means unlimited
	LogsPerSecondLimit int   `json:"logsPerSecondLimit" gorm:"column:logs_per_second_limit"`
	MaxLogsAmount      int64 `json:"maxLogsAmount"      gorm:"column:max_logs_amount"`
	MaxLogsSizeMB      int   `json:"maxLogsSizeMb"      gorm:"column:max_logs_size_mb"`
	MaxLogsLifeDays    int   `json:"maxLogsLifeDays"    gorm:"column:max_logs_life_days"`
	MaxLogSizeKB       int   `json:"maxLogSizeKb"       gorm:"column:max_log_size_kb"`
}

func (UserPlan) TableName() string {
	return "user_plans"
}
