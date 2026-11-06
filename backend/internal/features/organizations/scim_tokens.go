package organizations

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	users_models "obzly/internal/features/users/models"
	"obzly/internal/storage"

	"github.com/google/uuid"
)

// SCIMToken authenticates an IdP's SCIM client for one organization. Only
// a SHA-256 of the token is stored; the token is shown once.
type SCIMToken struct {
	ID         uuid.UUID  `json:"id"         gorm:"column:id;primaryKey"`
	OrgID      uuid.UUID  `json:"orgId"      gorm:"column:org_id"`
	Name       string     `json:"name"       gorm:"column:name"`
	Prefix     string     `json:"prefix"     gorm:"column:token_prefix"`
	Hash       string     `json:"-"          gorm:"column:token_hash"`
	CreatedBy  *uuid.UUID `json:"createdBy"  gorm:"column:created_by"`
	CreatedAt  time.Time  `json:"createdAt"  gorm:"column:created_at"`
	LastUsedAt *time.Time `json:"lastUsedAt" gorm:"column:last_used_at"`
}

// NewSCIMToken is a just-created token, with its secret.
type NewSCIMToken struct {
	SCIMToken
	Token string `json:"token"`
}

const (
	scimTokenScheme   = "obzscim_"
	scimPrefixLength  = 12
	maxSCIMTokensOrg  = 10
	scimTouchInterval = time.Minute
)

var errSCIMUnauthorized = errors.New("invalid SCIM token")

func (SCIMToken) TableName() string { return "scim_tokens" }

// ListSCIMTokens, CreateSCIMToken and DeleteSCIMToken are for owners and
// global admins: a token can create accounts.
func (s *Service) ListSCIMTokens(orgID uuid.UUID, user *users_models.User) ([]SCIMToken, error) {
	if err := s.ownerAccess(orgID, user); err != nil {
		return nil, err
	}
	tokens := []SCIMToken{}
	err := storage.GetDb().Where("org_id = ?", orgID).Order("created_at").Find(&tokens).Error
	return tokens, err
}

func (s *Service) CreateSCIMToken(orgID uuid.UUID, name string, user *users_models.User) (*NewSCIMToken, error) {
	if err := s.ownerAccess(orgID, user); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return nil, errors.New("a token needs a name of up to 100 characters")
	}
	var count int64
	if err := storage.GetDb().Model(&SCIMToken{}).Where("org_id = ?", orgID).Count(&count).Error; err != nil {
		return nil, err
	}
	if count >= maxSCIMTokensOrg {
		return nil, errors.New("an organization has at most 10 SCIM tokens")
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	body := base64.RawURLEncoding.EncodeToString(secret)
	token := &NewSCIMToken{
		SCIMToken: SCIMToken{
			ID:        uuid.New(),
			OrgID:     orgID,
			Name:      name,
			Prefix:    body[:scimPrefixLength],
			Hash:      hashSCIMToken(scimTokenScheme + body),
			CreatedBy: &user.ID,
			CreatedAt: time.Now().UTC(),
		},
		Token: scimTokenScheme + body,
	}
	if err := storage.GetDb().Create(&token.SCIMToken).Error; err != nil {
		return nil, err
	}
	s.audit.WriteAuditLog("SCIM token created: "+name+" for organization "+orgID.String(), &user.ID, nil)
	return token, nil
}

func (s *Service) DeleteSCIMToken(orgID, tokenID uuid.UUID, user *users_models.User) error {
	if err := s.ownerAccess(orgID, user); err != nil {
		return err
	}
	var token SCIMToken
	if err := storage.GetDb().Where("id = ? AND org_id = ?", tokenID, orgID).First(&token).Error; err != nil {
		return notFound(err)
	}
	if err := storage.GetDb().Delete(&token).Error; err != nil {
		return err
	}
	s.audit.WriteAuditLog("SCIM token revoked: "+token.Name+" for organization "+orgID.String(), &user.ID, nil)
	return nil
}

// authenticateSCIM checks an Authorization header against the stored
// tokens: looked up by prefix, compared by hash in constant time.
func (s *Service) authenticateSCIM(header string) (*SCIMToken, error) {
	raw, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		return nil, errSCIMUnauthorized
	}
	raw = strings.TrimSpace(raw)
	body, ok := strings.CutPrefix(raw, scimTokenScheme)
	if !ok || len(body) <= scimPrefixLength {
		return nil, errSCIMUnauthorized
	}
	var token SCIMToken
	if err := storage.GetDb().Where("token_prefix = ?", body[:scimPrefixLength]).First(&token).Error; err != nil {
		return nil, errSCIMUnauthorized
	}
	if subtle.ConstantTimeCompare([]byte(token.Hash), []byte(hashSCIMToken(raw))) != 1 {
		return nil, errSCIMUnauthorized
	}
	now := time.Now().UTC()
	storage.GetDb().Model(&SCIMToken{}).
		Where("id = ? AND (last_used_at IS NULL OR last_used_at < ?)", token.ID, now.Add(-scimTouchInterval)).
		Update("last_used_at", now)
	return &token, nil
}

func (s *Service) ownerAccess(orgID uuid.UUID, user *users_models.User) error {
	if _, err := s.repo.org(orgID); err != nil {
		return notFound(err)
	}
	role, err := s.access(orgID, user, true)
	if err != nil {
		return err
	}
	if role != RoleOwner && !isAdmin(user) {
		return ErrForbidden
	}
	return nil
}

func hashSCIMToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
