// Package orgplan is an organization's plan: project quotas a Site enforces (the
// stricter of the project's own limit and the organization's) and Site and
// member counts the control plane enforces. A Site gets it from a document its
// control plane signs; a standalone install uses its Default organization's.
package orgplan

import (
	"errors"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// Limits are a plan's quotas; 0 is unlimited.
type Limits struct {
	LogsPerSecondLimit int   `json:"logsPerSecondLimit" gorm:"column:logs_per_second_limit"`
	MaxLogsAmount      int64 `json:"maxLogsAmount"      gorm:"column:max_logs_amount"`
	MaxLogsSizeMB      int   `json:"maxLogsSizeMb"      gorm:"column:max_logs_size_mb"`
	MaxLogsLifeDays    int   `json:"maxLogsLifeDays"    gorm:"column:max_logs_life_days"`
	MaxLogSizeKB       int   `json:"maxLogSizeKb"       gorm:"column:max_log_size_kb"`
	// MaxProjects is per Site.
	MaxProjects int `json:"maxProjects" gorm:"column:max_projects"`
	// MaxSites and MaxMembers are enforced by the control plane.
	MaxSites   int `json:"maxSites"   gorm:"column:max_sites"`
	MaxMembers int `json:"maxMembers" gorm:"column:max_members"`
}

// OrgPlan is an organization's plan as the control plane stores it.
type OrgPlan struct {
	OrgID uuid.UUID `json:"orgId"     gorm:"column:org_id;primaryKey"`
	Limits
	UpdatedBy *uuid.UUID `json:"updatedBy" gorm:"column:updated_by"`
	UpdatedAt time.Time  `json:"updatedAt" gorm:"column:updated_at"`
}

const (
	maxLimit    = 1_000_000_000
	maxLifeDays = 3650
)

var (
	current atomic.Pointer[Limits]

	errLimitNegative = errors.New("limits can't be negative (0 is unlimited)")
	errLifeDays      = errors.New("retention is at most 3650 days")
	errLimitTooLarge = errors.New("a limit is at most 1,000,000,000")
)

func (OrgPlan) TableName() string { return "org_plans" }

// Validate refuses negative or absurd values.
func (l Limits) Validate() error {
	for _, value := range []int64{int64(l.LogsPerSecondLimit), l.MaxLogsAmount, int64(l.MaxLogsSizeMB),
		int64(l.MaxLogsLifeDays), int64(l.MaxLogSizeKB), int64(l.MaxProjects), int64(l.MaxSites),
		int64(l.MaxMembers)} {
		if value < 0 {
			return errLimitNegative
		}
	}
	if l.MaxLogsLifeDays > maxLifeDays {
		return errLifeDays
	}
	for _, value := range []int{l.LogsPerSecondLimit, l.MaxLogsSizeMB, l.MaxLogSizeKB, l.MaxProjects,
		l.MaxSites, l.MaxMembers} {
		if value > maxLimit {
			return errLimitTooLarge
		}
	}
	return nil
}

// Current is the plan this process enforces; zero Limits without one.
func Current() Limits {
	if limits := current.Load(); limits != nil {
		return *limits
	}
	return Limits{}
}

// Set replaces Current (the loader, and tests).
func Set(limits Limits) {
	current.Store(&limits)
}

// Stricter is the smaller of two limits where 0 is unlimited.
func Stricter(a, b int) int {
	if a == 0 || (b != 0 && b < a) {
		return b
	}
	return a
}

// Stricter64 is Stricter for int64 limits.
func Stricter64(a, b int64) int64 {
	if a == 0 || (b != 0 && b < a) {
		return b
	}
	return a
}
