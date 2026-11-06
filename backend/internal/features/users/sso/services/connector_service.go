// Package sso_services implements portal-configured OIDC connectors (Cognito,
// Google Workspace, Entra ID or any compliant provider) via go-oidc discovery
// and ID token verification. Separate from the env-configured GitHub/Google OAuth.
package sso_services

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	users_interfaces "obzly/internal/features/users/interfaces"
	mfa_services "obzly/internal/features/users/mfa/services"
	users_models "obzly/internal/features/users/models"
	user_repositories "obzly/internal/features/users/repositories"
	sso_dto "obzly/internal/features/users/sso/dto"
	sso_models "obzly/internal/features/users/sso/models"
	sso_repositories "obzly/internal/features/users/sso/repositories"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
)

var ErrInsufficientPermissions = errors.New("insufficient permissions to manage SSO connectors")
var ErrConnectorNotFound = errors.New("SSO connector not found")

// Deleting a connector is a sensitive action: it needs a current MFA code
// from users who have MFA enabled (every admin, in practice).
var (
	ErrMFACodeRequired = errors.New("MFA code required to confirm this action")
	ErrMFACodeInvalid  = errors.New("invalid MFA code")
)

// ErrConnectorRootOnly: a connector's issuer decides who a login links to, so a
// non-root admin who could repoint it could sign in as any account, including
// other admins. Listing stays open to admins since it never exposes the secret.
var ErrConnectorRootOnly = errors.New("only the root admin can create, update, or delete SSO connectors")

const defaultScopes = "openid email profile"

type ConnectorService struct {
	connectorRepository    *sso_repositories.ConnectorRepository
	secretKeyRepository    *user_repositories.SecretKeyRepository
	auditLogWriter         users_interfaces.AuditLogWriter
	groupMappingRepository *sso_repositories.GroupMappingRepository
}

func (s *ConnectorService) SetAuditLogWriter(writer users_interfaces.AuditLogWriter) {
	s.auditLogWriter = writer
}

func (s *ConnectorService) ListConnectors(user *users_models.User) (*sso_dto.ListConnectorsResponseDTO, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrInsufficientPermissions
	}

	connectors, err := s.connectorRepository.ListAll()
	if err != nil {
		return nil, fmt.Errorf("failed to list connectors: %w", err)
	}

	response := make([]*sso_dto.ConnectorResponseDTO, len(connectors))
	for i, c := range connectors {
		response[i] = sso_dto.ToConnectorResponseDTO(c)
	}
	return &sso_dto.ListConnectorsResponseDTO{Connectors: response}, nil
}

func (s *ConnectorService) CreateConnector(
	request *sso_dto.CreateConnectorRequestDTO,
	user *users_models.User,
) (*sso_dto.ConnectorResponseDTO, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrInsufficientPermissions
	}
	if !user.IsRootAdmin {
		return nil, ErrConnectorRootOnly
	}
	if !request.ProviderType.IsValid() {
		return nil, fmt.Errorf("invalid provider type %q", request.ProviderType)
	}

	// Real discovery on save, so a bad issuer fails now rather than at first sign-in.
	if err := discoverAndCheckProvider(request.IssuerURL); err != nil {
		return nil, err
	}

	encryptedSecret, err := encryptSecret(s.secretKeyRepository, request.ClientSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt client secret: %w", err)
	}

	connector := &sso_models.Connector{
		ID:                    uuid.New(),
		Name:                  request.Name,
		ProviderType:          request.ProviderType,
		IssuerURL:             strings.TrimRight(request.IssuerURL, "/"),
		ClientID:              request.ClientID,
		ClientSecretEncrypted: encryptedSecret,
		Scopes:                effectiveScopes(request.Scopes),
		GroupsClaim:           strings.TrimSpace(request.GroupsClaim),
		RequireVerifiedEmail:  request.RequireVerifiedEmail == nil || *request.RequireVerifiedEmail,
		TrustIdPMFA:           request.TrustIdPMFA != nil && *request.TrustIdPMFA,
		ButtonLabel:           request.ButtonLabel,
		Enabled:               request.Enabled,
	}

	if err := s.connectorRepository.Create(connector); err != nil {
		return nil, fmt.Errorf("failed to create connector: %w", err)
	}

	if s.auditLogWriter != nil {
		s.auditLogWriter.WriteAuditLog(
			fmt.Sprintf("SSO connector created: %s (%s)", connector.Name, connector.ProviderType),
			&user.ID,
			nil,
		)
	}

	return sso_dto.ToConnectorResponseDTO(connector), nil
}

func (s *ConnectorService) UpdateConnector(
	id uuid.UUID,
	request *sso_dto.UpdateConnectorRequestDTO,
	user *users_models.User,
) (*sso_dto.ConnectorResponseDTO, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrInsufficientPermissions
	}
	if !user.IsRootAdmin {
		return nil, ErrConnectorRootOnly
	}
	if !request.ProviderType.IsValid() {
		return nil, fmt.Errorf("invalid provider type %q", request.ProviderType)
	}

	connector, err := s.connectorRepository.GetByID(id)
	if err != nil {
		return nil, fmt.Errorf("failed to get connector: %w", err)
	}
	if connector == nil {
		return nil, ErrConnectorNotFound
	}

	if err := discoverAndCheckProvider(request.IssuerURL); err != nil {
		return nil, err
	}

	connector.Name = request.Name
	connector.ProviderType = request.ProviderType
	connector.IssuerURL = strings.TrimRight(request.IssuerURL, "/")
	connector.ClientID = request.ClientID
	connector.Scopes = effectiveScopes(request.Scopes)
	connector.GroupsClaim = strings.TrimSpace(request.GroupsClaim)
	if request.RequireVerifiedEmail != nil {
		connector.RequireVerifiedEmail = *request.RequireVerifiedEmail
	}
	if request.TrustIdPMFA != nil {
		connector.TrustIdPMFA = *request.TrustIdPMFA
	}
	connector.ButtonLabel = request.ButtonLabel
	connector.Enabled = request.Enabled

	if request.ClientSecret != "" {
		encryptedSecret, err := encryptSecret(s.secretKeyRepository, request.ClientSecret)
		if err != nil {
			return nil, fmt.Errorf("failed to encrypt client secret: %w", err)
		}
		connector.ClientSecretEncrypted = encryptedSecret
	}

	if err := s.connectorRepository.Update(connector); err != nil {
		return nil, fmt.Errorf("failed to update connector: %w", err)
	}

	if s.auditLogWriter != nil {
		s.auditLogWriter.WriteAuditLog(
			fmt.Sprintf("SSO connector updated: %s (%s)", connector.Name, connector.ProviderType),
			&user.ID,
			nil,
		)
	}

	return sso_dto.ToConnectorResponseDTO(connector), nil
}

func (s *ConnectorService) DeleteConnector(id uuid.UUID, user *users_models.User, mfaCode *string) error {
	if !user.CanUpdateSettings() {
		return ErrInsufficientPermissions
	}
	if !user.IsRootAdmin {
		return ErrConnectorRootOnly
	}
	if err := requireMFAIfEnabled(user, mfaCode); err != nil {
		return err
	}

	connector, err := s.connectorRepository.GetByID(id)
	if err != nil {
		return fmt.Errorf("failed to get connector: %w", err)
	}
	if connector == nil {
		return ErrConnectorNotFound
	}

	if err := s.connectorRepository.Delete(id); err != nil {
		return fmt.Errorf("failed to delete connector: %w", err)
	}

	if s.auditLogWriter != nil {
		s.auditLogWriter.WriteAuditLog(
			fmt.Sprintf("SSO connector deleted: %s (%s)", connector.Name, connector.ProviderType),
			&user.ID,
			nil,
		)
	}

	return nil
}

// ListPublicConnectors serves the unauthenticated login page: enabled connectors
// with an AuthorizeURL, discovered concurrently.
func (s *ConnectorService) ListPublicConnectors(redirectUri string) (*sso_dto.ListPublicConnectorsResponseDTO, error) {
	connectors, err := s.connectorRepository.ListEnabled()
	if err != nil {
		return nil, fmt.Errorf("failed to list connectors: %w", err)
	}

	results := make([]*sso_dto.PublicConnectorDTO, len(connectors))
	errs := make([]error, len(connectors))

	var wg sync.WaitGroup
	for i, connector := range connectors {
		wg.Add(1)
		go func(i int, connector *sso_models.Connector) {
			defer wg.Done()
			dto, err := s.buildPublicConnectorDTO(connector, redirectUri)
			if err != nil {
				errs[i] = err
				return
			}
			results[i] = dto
		}(i, connector)
	}
	wg.Wait()

	publicConnectors := make([]*sso_dto.PublicConnectorDTO, 0, len(results))
	for i, dto := range results {
		if errs[i] != nil {
			// An unreachable IdP mustn't take down the login page; skip it.
			continue
		}
		publicConnectors = append(publicConnectors, dto)
	}

	return &sso_dto.ListPublicConnectorsResponseDTO{Connectors: publicConnectors}, nil
}

func (s *ConnectorService) buildPublicConnectorDTO(
	connector *sso_models.Connector,
	redirectUri string,
) (*sso_dto.PublicConnectorDTO, error) {
	provider, err := discoverProviderCached(connector.IssuerURL)
	if err != nil {
		return nil, err
	}

	state, err := signState(s.secretKeyRepository, connector.ID)
	if err != nil {
		return nil, err
	}

	if err := checkAuthorizeURL(provider.Endpoint().AuthURL); err != nil {
		return nil, err
	}

	params := url.Values{
		"client_id":     {connector.ClientID},
		"redirect_uri":  {redirectUri},
		"response_type": {"code"},
		"scope":         {connector.Scopes},
		"state":         {state},
		"nonce":         {NonceForState(state)},
	}

	authorizeURL := provider.Endpoint().AuthURL + "?" + params.Encode()

	return &sso_dto.PublicConnectorDTO{
		ID:           connector.ID,
		Name:         connector.Name,
		ProviderType: connector.ProviderType,
		ButtonLabel:  connector.ButtonLabel,
		AuthorizeURL: authorizeURL,
	}, nil
}

func discoverProvider(issuerURL string) (*oidc.Provider, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return oidc.NewProvider(ctx, strings.TrimRight(issuerURL, "/"))
}

// discoverAndCheckProvider is discoverProvider for saving a connector: the
// discovered authorize URL is what the login page sends browsers to.
func discoverAndCheckProvider(issuerURL string) error {
	provider, err := discoverProvider(issuerURL)
	if err != nil {
		return fmt.Errorf("failed to discover OIDC configuration at %q: %w", issuerURL, err)
	}
	return checkAuthorizeURL(provider.Endpoint().AuthURL)
}

// ErrInsecureAuthorizeURL: browsers are only sent to https (or loopback http for
// local development), never another scheme a discovery document could name.
var ErrInsecureAuthorizeURL = errors.New("the provider's authorization endpoint must be an https URL")

func checkAuthorizeURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return ErrInsecureAuthorizeURL
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(parsed.Hostname()) {
			return nil
		}
	}
	return ErrInsecureAuthorizeURL
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Discovery is cached in process (*oidc.Provider isn't serializable) for the
// unauthenticated login page and callback, so each page load doesn't fan out
// to every IdP. Saving a connector stays uncached to test the real IdP.
const (
	discoveryCacheTTL      = 1 * time.Hour
	discoveryErrorCacheTTL = 1 * time.Minute
)

type discoveryCacheEntry struct {
	provider  *oidc.Provider
	err       error
	expiresAt time.Time
}

var (
	discoveryCacheMu sync.Mutex
	discoveryCache   = map[string]discoveryCacheEntry{}
)

func discoverProviderCached(issuerURL string) (*oidc.Provider, error) {
	discoveryCacheMu.Lock()
	if entry, ok := discoveryCache[issuerURL]; ok && time.Now().Before(entry.expiresAt) {
		discoveryCacheMu.Unlock()
		return entry.provider, entry.err
	}
	discoveryCacheMu.Unlock()

	provider, err := discoverProvider(issuerURL)

	// The callback uses this cache too: a brief IdP outage must not block
	// sign-in for the whole hour.
	ttl := discoveryCacheTTL
	if err != nil {
		ttl = discoveryErrorCacheTTL
	}
	discoveryCacheMu.Lock()
	discoveryCache[issuerURL] = discoveryCacheEntry{
		provider:  provider,
		err:       err,
		expiresAt: time.Now().Add(ttl),
	}
	discoveryCacheMu.Unlock()

	return provider, err
}

func effectiveScopes(scopes string) string {
	if strings.TrimSpace(scopes) == "" {
		return defaultScopes
	}
	return scopes
}

// requireMFAIfEnabled mirrors api_keys.ApiKeyService.requireMFAIfEnabled.
func requireMFAIfEnabled(user *users_models.User, code *string) error {
	if !user.MFAEnabled {
		return nil
	}
	if code == nil || strings.TrimSpace(*code) == "" {
		return ErrMFACodeRequired
	}
	if err := mfa_services.GetMFAService().VerifyCurrentUserCode(user, strings.TrimSpace(*code)); err != nil {
		if errors.Is(err, mfa_services.ErrTooManyAttempts) {
			return err
		}
		return ErrMFACodeInvalid
	}
	return nil
}
