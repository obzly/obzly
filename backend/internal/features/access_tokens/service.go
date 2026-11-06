package access_tokens

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	audit_logs "obzly/internal/features/audit_logs"
	users_enums "obzly/internal/features/users/enums"
	mfa_services "obzly/internal/features/users/mfa/services"
	users_models "obzly/internal/features/users/models"
	users_repositories "obzly/internal/features/users/repositories"
	cache_utils "obzly/internal/util/cache"

	"github.com/google/uuid"
)

// AccessTokenService manages personal access tokens, which authenticate a user
// on the MCP endpoint only.
type AccessTokenService struct {
	repository                *AccessTokenRepository
	userRepository            *users_repositories.UserRepository
	auditLogService           *audit_logs.AuditLogService
	mfaService                *mfa_services.Service
	lastUsedThrottleCacheUtil *cache_utils.CacheUtil[bool]
}

const (
	TokenPrefix      = "obz_pat_"
	OAuthTokenPrefix = "obz_oat_"
	ScopeMCPRead     = "mcp:read"
	maxActivePerUser = 20
	tokenRandomBytes = 32
)

var (
	ErrInvalidToken    = errors.New("invalid or expired access token")
	ErrMFACodeRequired = errors.New("MFA code required to create an access token")
	ErrMFACodeInvalid  = errors.New("invalid MFA code")
	ErrTooManyTokens   = fmt.Errorf("at most %d active access tokens per user - revoke one first", maxActivePerUser)
	ErrTokenNotFound   = errors.New("access token not found")
)

// Create issues a personal access token. sessionSatisfiesMFA is whether the
// creating session passed MFA elsewhere (control plane, trusted IdP).
func (s *AccessTokenService) Create(
	user *users_models.User,
	request *CreateAccessTokenRequestDTO,
	sessionSatisfiesMFA bool,
) (*CreateAccessTokenResponseDTO, error) {
	if err := s.requireMFAIfEnabled(user, request.MFACode); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	active, err := s.repository.ListActiveByUser(user.ID, now)
	if err != nil {
		return nil, fmt.Errorf("failed to list access tokens: %w", err)
	}
	if len(active) >= maxActivePerUser {
		return nil, ErrTooManyTokens
	}

	fullToken, err := generateToken(TokenPrefix)
	if err != nil {
		return nil, fmt.Errorf("failed to generate token: %w", err)
	}

	token := &AccessToken{
		ID:          uuid.New(),
		UserID:      user.ID,
		Name:        strings.TrimSpace(request.Name),
		TokenPrefix: fullToken[:len(TokenPrefix)+6] + "...",
		TokenHash:   hashToken(fullToken),
		Scopes:      ScopeMCPRead,
		ExpiresAt:   now.AddDate(0, 0, request.ExpiresInDays),
		CreatedAt:   now,
		Kind:        KindPersonal,
		// A Member's token must not later stand in for MFA once they
		// are promoted to Admin.
		MFASatisfied: user.MFAEnabled || sessionSatisfiesMFA,
	}
	if err := s.repository.Create(token); err != nil {
		return nil, fmt.Errorf("failed to create access token: %w", err)
	}

	s.auditLogService.WriteAuditLog(
		fmt.Sprintf("Access token created: %s (%s)", token.Name, token.TokenPrefix), &user.ID, nil,
	)

	return &CreateAccessTokenResponseDTO{Token: fullToken, AccessToken: toDTO(token)}, nil
}

func (s *AccessTokenService) List(user *users_models.User) (*ListAccessTokensResponseDTO, error) {
	tokens, err := s.repository.ListActiveByUser(user.ID, time.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("failed to list access tokens: %w", err)
	}

	response := &ListAccessTokensResponseDTO{AccessTokens: make([]AccessTokenDTO, 0, len(tokens))}
	for _, token := range tokens {
		response.AccessTokens = append(response.AccessTokens, toDTO(token))
	}
	return response, nil
}

func (s *AccessTokenService) Revoke(user *users_models.User, tokenID uuid.UUID) error {
	revoked, err := s.repository.Revoke(user.ID, tokenID, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("failed to revoke access token: %w", err)
	}
	if !revoked {
		return ErrTokenNotFound
	}

	s.auditLogService.WriteAuditLog(fmt.Sprintf("Access token revoked: %s", tokenID), &user.ID, nil)
	return nil
}

// IssueOAuth creates a short-lived OAuth access token bound to audience.
func (s *AccessTokenService) IssueOAuth(
	userID uuid.UUID, clientID, clientName, audience, scopes string, familyID uuid.UUID, ttl time.Duration,
	mfaSatisfied bool,
) (string, time.Time, error) {
	fullToken, err := generateToken(OAuthTokenPrefix)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to generate token: %w", err)
	}

	now := time.Now().UTC()
	_ = s.repository.DeleteExpiredOAuth(userID, now)

	token := &AccessToken{
		ID:           uuid.New(),
		UserID:       userID,
		Name:         clientName,
		TokenPrefix:  fullToken[:len(OAuthTokenPrefix)+6] + "...",
		TokenHash:    hashToken(fullToken),
		Scopes:       scopes,
		ExpiresAt:    now.Add(ttl),
		CreatedAt:    now,
		Kind:         KindOAuth,
		ClientID:     &clientID,
		Audience:     &audience,
		FamilyID:     &familyID,
		MFASatisfied: mfaSatisfied,
	}
	if err := s.repository.Create(token); err != nil {
		return "", time.Time{}, fmt.Errorf("failed to store access token: %w", err)
	}
	return fullToken, token.ExpiresAt, nil
}

func (s *AccessTokenService) RevokeFamily(familyID uuid.UUID) error {
	return s.repository.RevokeFamily(familyID, time.Now().UTC())
}

// Authenticate resolves a token to its active user. Revocation, expiry, password
// changes and deactivation apply immediately (no cache); OAuth tokens must match audience.
func (s *AccessTokenService) Authenticate(rawToken, audience string) (*users_models.User, *AccessToken, error) {
	if !strings.HasPrefix(rawToken, TokenPrefix) && !strings.HasPrefix(rawToken, OAuthTokenPrefix) {
		return nil, nil, ErrInvalidToken
	}

	token, err := s.repository.GetByHash(hashToken(rawToken))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to look up access token: %w", err)
	}
	now := time.Now().UTC()
	if token == nil || !token.IsActive(now) {
		return nil, nil, ErrInvalidToken
	}
	if token.Kind == KindOAuth && (token.Audience == nil || !strings.EqualFold(*token.Audience, audience)) {
		return nil, nil, ErrInvalidToken
	}

	user, err := s.userRepository.GetUserByID(token.UserID)
	if err != nil || user == nil || user.Status != users_enums.UserStatusActive {
		return nil, nil, ErrInvalidToken
	}
	// Password changes revoke tokens; this also covers ones that predate it.
	if token.CreatedAt.Before(user.PasswordCreationTime) {
		return nil, nil, ErrInvalidToken
	}

	s.recordLastUsed(token.ID)
	return user, token, nil
}

func (s *AccessTokenService) requireMFAIfEnabled(user *users_models.User, code *string) error {
	if !user.MFAEnabled {
		return nil
	}
	if code == nil || strings.TrimSpace(*code) == "" {
		return ErrMFACodeRequired
	}
	if err := s.mfaService.VerifyCurrentUserCode(user, strings.TrimSpace(*code)); err != nil {
		if errors.Is(err, mfa_services.ErrTooManyAttempts) {
			return err
		}
		return ErrMFACodeInvalid
	}
	return nil
}

// recordLastUsed writes last_used_at at most once per throttle window.
func (s *AccessTokenService) recordLastUsed(tokenID uuid.UUID) {
	key := tokenID.String()
	if s.lastUsedThrottleCacheUtil.Get(key) != nil {
		return
	}
	marker := true
	s.lastUsedThrottleCacheUtil.Set(key, &marker)

	go func() {
		_ = s.repository.UpdateLastUsedAt(tokenID, time.Now().UTC())
	}()
}

func generateToken(prefix string) (string, error) {
	random := make([]byte, tokenRandomBytes)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(random), nil
}

// HashToken is the stored form of any opaque token (SHA-256, hex).
func HashToken(token string) string {
	return hashToken(token)
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func splitScopes(scopes string) []string {
	return strings.Fields(scopes)
}
