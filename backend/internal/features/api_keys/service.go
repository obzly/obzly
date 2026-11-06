package api_keys

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	audit_logs "obzly/internal/features/audit_logs"
	projects_services "obzly/internal/features/projects/services"
	mfa_services "obzly/internal/features/users/mfa/services"
	users_models "obzly/internal/features/users/models"
	cache_utils "obzly/internal/util/cache"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
)

type ApiKeyService struct {
	apiKeyRepository *ApiKeyRepository
	projectService   *projects_services.ProjectService
	auditLogService  *audit_logs.AuditLogService
	mfaService       *mfa_services.Service

	apiKeyCacheUtil           *cache_utils.CacheUtil[CachedApiKey]
	lastUsedThrottleCacheUtil *cache_utils.CacheUtil[bool]
	// negativeCacheUtil shares apiKeyCacheUtil's key prefix with a much shorter TTL,
	// for unknown token hashes only: those are attacker-chosen, so a flood of bogus
	// keys must not accumulate for the full default TTL.
	negativeCacheUtil *cache_utils.CacheUtil[CachedApiKey]
	singleflight      singleflight.Group // Prevents thundering herd on DB calls
}

const (
	TokenPrefix = "lb_"
	TokenLength = 32
)

var (
	ErrMFACodeRequired = errors.New("MFA code required to confirm this action")
	ErrMFACodeInvalid  = errors.New("invalid MFA code")
)

func (s *ApiKeyService) CreateApiKey(
	projectID uuid.UUID,
	request *CreateApiKeyRequestDTO,
	creator *users_models.User,
) (*ApiKey, error) {
	canManage, err := s.projectService.CanUserManageProject(projectID, creator)
	if err != nil {
		return nil, err
	}
	if !canManage {
		return nil, errors.New("insufficient permissions to create API keys")
	}

	fullToken, tokenPrefix, tokenHash, err := s.generateSecureToken()
	if err != nil {
		return nil, fmt.Errorf("failed to generate token: %w", err)
	}

	apiKey := &ApiKey{
		ID:          uuid.New(),
		Name:        request.Name,
		ProjectID:   projectID,
		TokenPrefix: tokenPrefix,
		TokenHash:   tokenHash,
		Status:      ApiKeyStatusActive,
	}

	if err := s.apiKeyRepository.CreateApiKey(apiKey); err != nil {
		return nil, fmt.Errorf("failed to create API key: %w", err)
	}

	// Pre-warm the cache so the new key works immediately.
	cachedKey := &CachedApiKey{
		ID:        apiKey.ID,
		ProjectID: apiKey.ProjectID,
		Status:    apiKey.Status,
	}
	s.apiKeyCacheUtil.Set(tokenHash, cachedKey)

	s.auditLogService.WriteAuditLog(
		fmt.Sprintf("API key created: %s (%s)", request.Name, tokenPrefix),
		&creator.ID,
		&projectID,
	)

	apiKey.Token = fullToken

	return apiKey, nil
}

func (s *ApiKeyService) GetProjectApiKeys(
	projectID uuid.UUID,
	user *users_models.User,
) (*GetApiKeysResponseDTO, error) {
	canAccess, _, err := s.projectService.CanUserAccessProject(projectID, user)
	if err != nil {
		return nil, err
	}
	if !canAccess {
		return nil, errors.New("insufficient permissions to view API keys")
	}

	apiKeys, err := s.apiKeyRepository.GetApiKeysByProjectID(projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to get API keys: %w", err)
	}

	return &GetApiKeysResponseDTO{
		ApiKeys: apiKeys,
	}, nil
}

func (s *ApiKeyService) UpdateApiKey(
	projectID uuid.UUID,
	apiKeyID uuid.UUID,
	request *UpdateApiKeyRequestDTO,
	updater *users_models.User,
) error {
	canManage, err := s.projectService.CanUserManageProject(projectID, updater)
	if err != nil {
		return err
	}
	if !canManage {
		return errors.New("insufficient permissions to update API keys")
	}

	apiKey, err := s.apiKeyRepository.GetApiKeyByID(apiKeyID)
	if err != nil {
		return errors.New("API key not found")
	}

	if apiKey.ProjectID != projectID {
		return errors.New("API key does not belong to this project")
	}

	if request.Name != nil {
		apiKey.Name = *request.Name
	}

	if request.Status != nil {
		apiKey.Status = *request.Status
	}

	if err := s.apiKeyRepository.UpdateApiKey(apiKey); err != nil {
		return fmt.Errorf("failed to update API key: %w", err)
	}

	s.apiKeyCacheUtil.Invalidate(apiKey.TokenHash)

	s.auditLogService.WriteAuditLog(
		fmt.Sprintf("API key updated: %s (%s)", apiKey.Name, apiKey.TokenPrefix),
		&updater.ID,
		&projectID,
	)

	return nil
}

func (s *ApiKeyService) DeleteApiKey(
	projectID uuid.UUID,
	apiKeyID uuid.UUID,
	deleter *users_models.User,
	mfaCode *string,
) error {
	canManage, err := s.projectService.CanUserManageProject(projectID, deleter)
	if err != nil {
		return err
	}
	if !canManage {
		return errors.New("insufficient permissions to delete API keys")
	}

	if err := s.requireMFAIfEnabled(deleter, mfaCode); err != nil {
		return err
	}

	apiKey, err := s.apiKeyRepository.GetApiKeyByID(apiKeyID)
	if err != nil {
		return errors.New("API key not found")
	}

	if apiKey.ProjectID != projectID {
		return errors.New("API key does not belong to this project")
	}

	if err := s.apiKeyRepository.DeleteApiKey(apiKeyID); err != nil {
		return fmt.Errorf("failed to delete API key: %w", err)
	}

	s.apiKeyCacheUtil.Invalidate(apiKey.TokenHash)

	s.auditLogService.WriteAuditLog(
		fmt.Sprintf("API key deleted: %s (%s)", apiKey.Name, apiKey.TokenPrefix),
		&deleter.ID,
		&projectID,
	)

	return nil
}

// BulkDeleteApiKeys deletes several keys under one MFA check.
func (s *ApiKeyService) BulkDeleteApiKeys(
	projectID uuid.UUID,
	apiKeyIDs []uuid.UUID,
	deleter *users_models.User,
	mfaCode *string,
) (int, error) {
	canManage, err := s.projectService.CanUserManageProject(projectID, deleter)
	if err != nil {
		return 0, err
	}
	if !canManage {
		return 0, errors.New("insufficient permissions to delete API keys")
	}

	if err := s.requireMFAIfEnabled(deleter, mfaCode); err != nil {
		return 0, err
	}

	apiKeys, err := s.apiKeyRepository.GetApiKeysByIDs(apiKeyIDs)
	if err != nil {
		return 0, fmt.Errorf("failed to look up API keys: %w", err)
	}

	ownedIDs := make([]uuid.UUID, 0, len(apiKeys))
	for _, apiKey := range apiKeys {
		if apiKey.ProjectID != projectID {
			return 0, errors.New("API key does not belong to this project")
		}
		ownedIDs = append(ownedIDs, apiKey.ID)
	}

	if len(ownedIDs) == 0 {
		return 0, nil
	}

	if err := s.apiKeyRepository.DeleteApiKeys(ownedIDs); err != nil {
		return 0, fmt.Errorf("failed to delete API keys: %w", err)
	}

	for _, apiKey := range apiKeys {
		s.apiKeyCacheUtil.Invalidate(apiKey.TokenHash)
	}

	s.auditLogService.WriteAuditLog(
		fmt.Sprintf("%d inactive API key(s) bulk-deleted", len(ownedIDs)),
		&deleter.ID,
		&projectID,
	)

	return len(ownedIDs), nil
}

// requireMFAIfEnabled is the step-up check for sensitive key actions, enforced
// only when the user has MFA enabled (always, for admins).
func (s *ApiKeyService) requireMFAIfEnabled(user *users_models.User, code *string) error {
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

// VerifyApiKeyValue checks token is the full value of an active key in projectID
// (keys are stored hashed, so users paste it to reuse one). It reads the DB
// directly so verifying doesn't count as a use or touch the auth cache.
func (s *ApiKeyService) VerifyApiKeyValue(
	projectID uuid.UUID,
	token string,
	user *users_models.User,
) (*VerifyApiKeyResponseDTO, error) {
	canAccess, _, err := s.projectService.CanUserAccessProject(projectID, user)
	if err != nil {
		return nil, err
	}
	if !canAccess {
		return nil, errors.New("insufficient permissions to view API keys")
	}

	if !strings.HasPrefix(token, TokenPrefix) {
		return &VerifyApiKeyResponseDTO{IsValid: false}, nil
	}

	apiKey, err := s.apiKeyRepository.GetApiKeyByTokenHash(s.hashToken(token))
	if err != nil || apiKey.ProjectID != projectID {
		return &VerifyApiKeyResponseDTO{IsValid: false}, nil
	}

	return &VerifyApiKeyResponseDTO{IsValid: true, ApiKeyID: apiKey.ID}, nil
}

func (s *ApiKeyService) ValidateApiKey(token string, projectID uuid.UUID) (*ValidateTokenResponse, error) {
	resolved, err := s.ResolveProjectFromApiKey(token)
	if err != nil {
		return nil, err
	}
	if !resolved.IsValid || resolved.ProjectID != projectID {
		return &ValidateTokenResponse{IsValid: false}, nil
	}
	return resolved, nil
}

// ResolveProjectFromApiKey finds the project for a bare token, for OTLP ingestion
// where the token is the only project identifier. Use ValidateApiKey when the
// project is already known.
func (s *ApiKeyService) ResolveProjectFromApiKey(token string) (*ValidateTokenResponse, error) {
	if !strings.HasPrefix(token, TokenPrefix) {
		return &ValidateTokenResponse{IsValid: false}, nil
	}

	tokenHash := s.hashToken(token)

	if cachedKey := s.apiKeyCacheUtil.Get(tokenHash); cachedKey != nil {
		if cachedKey.Status != ApiKeyStatusActive {
			return &ValidateTokenResponse{IsValid: false}, nil
		}

		s.recordLastUsed(cachedKey.ID)

		return &ValidateTokenResponse{
			IsValid:   true,
			ApiKeyID:  cachedKey.ID,
			ProjectID: cachedKey.ProjectID,
		}, nil
	}

	// Tier 2: Database lookup with singleflight protection (prevents thundering herd)
	result, err, _ := s.singleflight.Do(tokenHash, func() (any, error) {
		return s.apiKeyRepository.GetApiKeyByTokenHash(tokenHash)
	})

	if err != nil {
		invalidCachedKey := &CachedApiKey{
			ID:        uuid.Nil,
			ProjectID: uuid.Nil,
			Status:    ApiKeyStatusNotFound,
		}

		s.negativeCacheUtil.Set(tokenHash, invalidCachedKey)
		return &ValidateTokenResponse{IsValid: false}, nil
	}

	apiKey, ok := result.(*ApiKey)
	if !ok {
		return &ValidateTokenResponse{IsValid: false}, fmt.Errorf("failed to cast result to ApiKey")
	}

	if apiKey.Status != ApiKeyStatusActive {
		return &ValidateTokenResponse{IsValid: false}, nil
	}

	cachedKey := &CachedApiKey{
		ID:        apiKey.ID,
		ProjectID: apiKey.ProjectID,
		Status:    apiKey.Status,
	}
	s.apiKeyCacheUtil.Set(tokenHash, cachedKey)

	s.recordLastUsed(apiKey.ID)

	return &ValidateTokenResponse{
		IsValid:   true,
		ApiKeyID:  apiKey.ID,
		ProjectID: apiKey.ProjectID,
	}, nil
}

// recordLastUsed writes the last-used time at most once per cache window per key
// (this runs on every ingested request), in a goroutine off the request path.
func (s *ApiKeyService) recordLastUsed(apiKeyID uuid.UUID) {
	throttleKey := apiKeyID.String()
	if s.lastUsedThrottleCacheUtil.Get(throttleKey) != nil {
		return
	}

	marker := true
	s.lastUsedThrottleCacheUtil.Set(throttleKey, &marker)

	go func() {
		_ = s.apiKeyRepository.UpdateLastUsedAt(apiKeyID, time.Now().UTC())
	}()
}

func (s *ApiKeyService) generateSecureToken() (fullToken, prefix, hash string, err error) {
	tokenBytes := make([]byte, TokenLength/2) // hex encoding doubles the length
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", "", "", err
	}

	tokenSuffix := hex.EncodeToString(tokenBytes)
	fullToken = TokenPrefix + tokenSuffix
	prefix = TokenPrefix + tokenSuffix[:6] + "..."
	hash = s.hashToken(fullToken)

	return fullToken, prefix, hash, nil
}

func (s *ApiKeyService) hashToken(token string) string {
	hasher := sha256.New()
	hasher.Write([]byte(token))
	return hex.EncodeToString(hasher.Sum(nil))
}
