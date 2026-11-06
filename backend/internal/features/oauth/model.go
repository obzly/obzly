package oauth

import (
	"time"

	"github.com/google/uuid"
)

// RegisteredClient is a client created through dynamic registration (RFC 7591).
type RegisteredClient struct {
	ClientID     string    `gorm:"column:client_id"`
	ClientName   string    `gorm:"column:client_name"`
	ClientURI    *string   `gorm:"column:client_uri"`
	RedirectURIs string    `gorm:"column:redirect_uris"` // JSON array
	CreatedAt    time.Time `gorm:"column:created_at"`
}

func (RegisteredClient) TableName() string {
	return "oauth_clients"
}

type RefreshToken struct {
	ID              uuid.UUID  `gorm:"column:id"`
	FamilyID        uuid.UUID  `gorm:"column:family_id"`
	UserID          uuid.UUID  `gorm:"column:user_id"`
	ClientID        string     `gorm:"column:client_id"`
	ClientName      string     `gorm:"column:client_name"`
	TokenHash       string     `gorm:"column:token_hash"`
	Scopes          string     `gorm:"column:scopes"`
	Audience        string     `gorm:"column:audience"`
	FamilyExpiresAt time.Time  `gorm:"column:family_expires_at"`
	ExpiresAt       time.Time  `gorm:"column:expires_at"`
	UsedAt          *time.Time `gorm:"column:used_at"`
	RevokedAt       *time.Time `gorm:"column:revoked_at"`
	CreatedAt       time.Time  `gorm:"column:created_at"`
	MFASatisfied    bool       `gorm:"column:mfa_satisfied"`
}

func (RefreshToken) TableName() string {
	return "oauth_refresh_tokens"
}

// authorizationCode is what a code stands for; kept in Valkey for 60s, single use.
type authorizationCode struct {
	UserID        uuid.UUID `json:"userId"`
	ClientID      string    `json:"clientId"`
	ClientName    string    `json:"clientName"`
	RedirectURI   string    `json:"redirectUri"`
	CodeChallenge string    `json:"codeChallenge"`
	Scope         string    `json:"scope"`
	Resource      string    `json:"resource"`
	MFASatisfied  bool      `json:"mfaSatisfied"`
}
