package users_repositories

import (
	"time"

	"gorm.io/gorm"
)

// UserListFilter narrows the Users list; empty fields don't filter.
type UserListFilter struct {
	BeforeCreatedAt *time.Time
	// Query matches email or name, case-insensitively.
	Query string
	// Role is ADMIN or MEMBER.
	Role string
	// MFA is "on" or "off".
	MFA string
	// Status is the account's: ACTIVE, INVITED or INACTIVE.
	Status string
	// Activity "inactive" is an account that can't sign in (not ACTIVE) or
	// wasn't seen for InactiveAfter; "active" is the rest.
	Activity string
	// Now anchors Activity; zero means time.Now().UTC().
	Now time.Time
}

// UsersSummary is the Users page's counts (users_dto.UsersSummaryResponseDTO).
type UsersSummary struct {
	Total            int64 `gorm:"column:total"`
	ActiveAccounts   int64 `gorm:"column:active_accounts"`
	Invited          int64 `gorm:"column:invited"`
	Deactivated      int64 `gorm:"column:deactivated"`
	ActiveLast7Days  int64 `gorm:"column:active_last7_days"`
	Inactive         int64 `gorm:"column:inactive"`
	MFAEnabled       int64 `gorm:"column:mfa_enabled"`
	Admins           int64 `gorm:"column:admins"`
	AdminsWithoutMFA int64 `gorm:"column:admins_without_mfa"`
}

// InactiveAfter: no sign-in or request for this long makes a user inactive.
const InactiveAfter = 30 * 24 * time.Hour

const (
	// lastSeenSQL is the latest sign-in or request; NULL when never seen.
	lastSeenSQL = "GREATEST(last_active_at, last_login_at)"
	// inactiveSQL takes the InactiveAfter cutoff as its one argument.
	inactiveSQL = "(status <> 'ACTIVE' OR COALESCE(" + lastSeenSQL + ", '-infinity'::timestamptz) < ?)"
)

func (f UserListFilter) apply(query *gorm.DB) *gorm.DB {
	if f.BeforeCreatedAt != nil {
		query = query.Where("created_at < ?", *f.BeforeCreatedAt)
	}
	if f.Query != "" {
		pattern := "%" + f.Query + "%"
		query = query.Where("(email ILIKE ? OR name ILIKE ?)", pattern, pattern)
	}
	if f.Role != "" {
		query = query.Where("role = ?", f.Role)
	}
	switch f.MFA {
	case "on":
		query = query.Where("mfa_enabled")
	case "off":
		query = query.Where("NOT mfa_enabled")
	}
	if f.Status != "" {
		query = query.Where("status = ?", f.Status)
	}
	now := f.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	switch f.Activity {
	case "inactive":
		query = query.Where(inactiveSQL, now.Add(-InactiveAfter))
	case "active":
		query = query.Where("NOT "+inactiveSQL, now.Add(-InactiveAfter))
	}
	return query
}
