package access_tokens

import (
	"time"

	"github.com/google/uuid"
)

type AccessToken struct {
	ID          uuid.UUID  `gorm:"column:id"`
	UserID      uuid.UUID  `gorm:"column:user_id"`
	Name        string     `gorm:"column:name"`
	TokenPrefix string     `gorm:"column:token_prefix"`
	TokenHash   string     `gorm:"column:token_hash"`
	Scopes      string     `gorm:"column:scopes"`
	ExpiresAt   time.Time  `gorm:"column:expires_at"`
	LastUsedAt  *time.Time `gorm:"column:last_used_at"`
	CreatedAt   time.Time  `gorm:"column:created_at"`
	RevokedAt   *time.Time `gorm:"column:revoked_at"`
	Kind        string     `gorm:"column:kind"`
	ClientID    *string    `gorm:"column:client_id"`
	Audience    *string    `gorm:"column:audience"`
	FamilyID    *uuid.UUID `gorm:"column:family_id"`
	// MFASatisfied: the session that created the token had passed MFA, so
	// MCP calls made with it may skip the local MFA requirement.
	MFASatisfied bool `gorm:"column:mfa_satisfied"`
}

const (
	KindPersonal = "pat"
	KindOAuth    = "oauth"
)

func (AccessToken) TableName() string {
	return "access_tokens"
}

func (t *AccessToken) IsActive(now time.Time) bool {
	return t.RevokedAt == nil && now.Before(t.ExpiresAt)
}
