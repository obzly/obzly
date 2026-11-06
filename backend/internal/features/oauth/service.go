package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	access_tokens "obzly/internal/features/access_tokens"
	audit_logs "obzly/internal/features/audit_logs"
	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	users_repositories "obzly/internal/features/users/repositories"
	cache_utils "obzly/internal/util/cache"

	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"
)

const (
	accessTokenTTL    = time.Hour
	refreshTokenTTL   = 30 * 24 * time.Hour
	familyLifetime    = 90 * 24 * time.Hour
	codeTTL           = 60 * time.Second
	refreshPrefix     = "obz_ort_"
	codeKeyPrefix     = "oauth_code:"
	supportedScope    = access_tokens.ScopeMCPRead
	registeredIDBytes = 16
)

var codeVerifierPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

type OAuthService struct {
	repository         *OAuthRepository
	userRepository     *users_repositories.UserRepository
	accessTokenService *access_tokens.AccessTokenService
	auditLogService    *audit_logs.AuditLogService
	valkey             valkey.Client
	metadataCache      *cache_utils.CacheUtil[client]
}

// AuthorizeRequest is the authorization request the consent page forwards.
type AuthorizeRequest struct {
	ResponseType        string `json:"response_type"`
	ClientID            string `json:"client_id"`
	RedirectURI         string `json:"redirect_uri"`
	Scope               string `json:"scope"`
	State               string `json:"state"`
	CodeChallenge       string `json:"code_challenge"`
	CodeChallengeMethod string `json:"code_challenge_method"`
	Resource            string `json:"resource"`
}

// AuthorizeError: Fatal errors are shown to the user (the redirect target
// can't be trusted); the others are sent back to the client's redirect_uri.
type AuthorizeError struct {
	Code        string
	Description string
	Fatal       bool
}

func (e *AuthorizeError) Error() string {
	return e.Description
}

type ConsentInfo struct {
	ClientID         string `json:"clientId"`
	ClientName       string `json:"clientName"`
	ClientURI        string `json:"clientUri,omitempty"`
	RedirectHost     string `json:"redirectHost"`
	Scope            string `json:"scope"`
	VerifiedIdentity bool   `json:"verifiedIdentity"`
}

// ValidateAuthorize checks an authorization request for the consent page.
func (s *OAuthService) ValidateAuthorize(
	ctx context.Context, request *AuthorizeRequest, issuer, resource string,
) (*ConsentInfo, *client, *AuthorizeError) {
	if request.ClientID == "" {
		return nil, nil, &AuthorizeError{"invalid_request", "client_id is missing", true}
	}
	c, err := s.resolveClient(ctx, request.ClientID)
	if err != nil {
		return nil, nil, &AuthorizeError{"invalid_client", "Unknown or invalid client: " + err.Error(), true}
	}
	if request.RedirectURI == "" || !redirectMatches(c.RedirectURIs, request.RedirectURI) {
		return nil, nil, &AuthorizeError{"invalid_request", "redirect_uri is not registered for this client", true}
	}

	switch {
	case request.ResponseType != "code":
		return nil, c, &AuthorizeError{"unsupported_response_type", "only response_type=code is supported", false}
	case request.CodeChallenge == "" || request.CodeChallengeMethod != "S256":
		return nil, c, &AuthorizeError{"invalid_request", "PKCE with code_challenge_method=S256 is required", false}
	case request.Resource != "" && !strings.EqualFold(strings.TrimRight(request.Resource, "/"), resource):
		return nil, c, &AuthorizeError{"invalid_target", "resource must be " + resource, false}
	}
	if _, ok := normalizeScope(request.Scope); !ok {
		return nil, c, &AuthorizeError{"invalid_scope", "the only supported scope is " + supportedScope, false}
	}

	redirect, _ := url.Parse(request.RedirectURI)
	return &ConsentInfo{
		ClientID: c.ID, ClientName: c.Name, ClientURI: c.URI, RedirectHost: redirect.Host,
		Scope: supportedScope, VerifiedIdentity: c.MetadataDocument,
	}, c, nil
}

// Decide records the decision; sessionSatisfiesMFA is whether the approving
// session passed MFA elsewhere (see access_tokens.AccessToken.MFASatisfied).
func (s *OAuthService) Decide(
	ctx context.Context, user *users_models.User, request *AuthorizeRequest, approve bool, issuer, resource string,
	sessionSatisfiesMFA bool,
) (string, *AuthorizeError) {
	_, c, authErr := s.ValidateAuthorize(ctx, request, issuer, resource)
	if authErr != nil {
		if authErr.Fatal {
			return "", authErr
		}
		return errorRedirect(request, issuer, authErr.Code, authErr.Description), nil
	}
	if !approve {
		return errorRedirect(request, issuer, "access_denied", "the user denied access"), nil
	}

	code, err := randomToken(32)
	if err != nil {
		return "", &AuthorizeError{"server_error", "failed to create code", true}
	}
	payload, _ := json.Marshal(authorizationCode{
		UserID: user.ID, ClientID: c.ID, ClientName: c.Name, RedirectURI: request.RedirectURI,
		CodeChallenge: request.CodeChallenge, Scope: supportedScope, Resource: resource,
		MFASatisfied: user.MFAEnabled || sessionSatisfiesMFA,
	})
	if err := s.valkey.Do(ctx, s.valkey.B().Set().Key(codeKeyPrefix+hashValue(code)).
		Value(string(payload)).Ex(codeTTL).Build()).Error(); err != nil {
		return "", &AuthorizeError{"server_error", "failed to store code", true}
	}

	s.auditLogService.WriteAuditLog(fmt.Sprintf("OAuth app authorized for MCP: %s", c.Name), &user.ID, nil)

	query := url.Values{"code": {code}, "iss": {issuer}}
	if request.State != "" {
		query.Set("state", request.State)
	}
	return appendQuery(request.RedirectURI, query), nil
}

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

type TokenError struct {
	Code        string `json:"error"`
	Description string `json:"error_description"`
}

func (e *TokenError) Error() string {
	return e.Description
}

// ExchangeCode implements grant_type=authorization_code with PKCE.
func (s *OAuthService) ExchangeCode(
	ctx context.Context, clientID, code, redirectURI, verifier, resource string,
) (*TokenResponse, *TokenError) {
	if code == "" || clientID == "" || redirectURI == "" {
		return nil, &TokenError{"invalid_request", "code, client_id and redirect_uri are required"}
	}
	raw, err := s.valkey.Do(ctx, s.valkey.B().Getdel().Key(codeKeyPrefix+hashValue(code)).Build()).ToString()
	if err != nil {
		return nil, &TokenError{"invalid_grant", "the authorization code is invalid, expired or already used"}
	}
	var grant authorizationCode
	if json.Unmarshal([]byte(raw), &grant) != nil {
		return nil, &TokenError{"invalid_grant", "the authorization code is invalid"}
	}

	switch {
	case grant.ClientID != clientID:
		return nil, &TokenError{"invalid_grant", "the code was issued to another client"}
	case grant.RedirectURI != redirectURI:
		return nil, &TokenError{"invalid_grant", "redirect_uri does not match the authorization request"}
	case !codeVerifierPattern.MatchString(verifier) || pkceChallenge(verifier) != grant.CodeChallenge:
		return nil, &TokenError{"invalid_grant", "code_verifier does not match the code challenge"}
	case resource != "" && !strings.EqualFold(strings.TrimRight(resource, "/"), grant.Resource):
		return nil, &TokenError{"invalid_target", "resource does not match the authorization request"}
	}

	user, err := s.userRepository.GetUserByID(grant.UserID)
	if err != nil || user == nil || user.Status != users_enums.UserStatusActive {
		return nil, &TokenError{"invalid_grant", "the account is not active"}
	}

	now := time.Now().UTC()
	return s.issue(user.ID, grant.ClientID, grant.ClientName, grant.Resource, grant.Scope,
		uuid.New(), now.Add(familyLifetime), grant.MFASatisfied)
}

// Refresh implements grant_type=refresh_token with rotation. Presenting an
// already-rotated token revokes the whole family (stolen token defence).
func (s *OAuthService) Refresh(clientID, refreshToken, resource string) (*TokenResponse, *TokenError) {
	invalid := &TokenError{"invalid_grant", "the refresh token is invalid, expired or revoked"}
	if refreshToken == "" || clientID == "" {
		return nil, &TokenError{"invalid_request", "refresh_token and client_id are required"}
	}

	token, err := s.repository.GetRefreshTokenByHash(hashValue(refreshToken))
	if err != nil || token == nil || token.ClientID != clientID {
		return nil, invalid
	}
	now := time.Now().UTC()
	if token.RevokedAt != nil || !now.Before(token.ExpiresAt) {
		return nil, invalid
	}
	if resource != "" && !strings.EqualFold(strings.TrimRight(resource, "/"), token.Audience) {
		return nil, &TokenError{"invalid_target", "resource does not match the original grant"}
	}

	consumed, err := s.repository.MarkUsed(token.ID, now)
	if err != nil {
		return nil, &TokenError{"server_error", "failed to rotate the refresh token"}
	}
	if !consumed {
		s.revokeFamily(token.FamilyID)
		return nil, invalid
	}

	user, err := s.userRepository.GetUserByID(token.UserID)
	if err != nil || user == nil || user.Status != users_enums.UserStatusActive {
		s.revokeFamily(token.FamilyID)
		return nil, &TokenError{"invalid_grant", "the account is not active"}
	}
	if token.CreatedAt.Before(user.PasswordCreationTime) {
		s.revokeFamily(token.FamilyID)
		return nil, invalid
	}

	return s.issue(user.ID, token.ClientID, token.ClientName, token.Audience, token.Scopes,
		token.FamilyID, token.FamilyExpiresAt, token.MFASatisfied)
}

type RegisterRequest struct {
	RedirectURIs            []string `json:"redirect_uris"`
	ClientName              string   `json:"client_name"`
	ClientURI               string   `json:"client_uri"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

type RegisterResponse struct {
	ClientID                string   `json:"client_id"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	ClientIDIssuedAt        int64    `json:"client_id_issued_at"`
}

// Register implements RFC 7591 for public clients (no client secret).
func (s *OAuthService) Register(request *RegisterRequest) (*RegisterResponse, *TokenError) {
	if err := validateRedirectURIs(request.RedirectURIs); err != nil {
		return nil, &TokenError{"invalid_redirect_uri", err.Error()}
	}
	if method := request.TokenEndpointAuthMethod; method != "" && method != "none" {
		return nil, &TokenError{
			"invalid_client_metadata",
			"only public clients (token_endpoint_auth_method=none) are supported",
		}
	}

	suffix, err := randomToken(registeredIDBytes)
	if err != nil {
		return nil, &TokenError{"server_error", "failed to register client"}
	}
	name := truncate(strings.TrimSpace(request.ClientName), maxClientNameChars)
	if name == "" {
		name = "Unnamed MCP client"
	}
	redirects, _ := json.Marshal(request.RedirectURIs)
	registered := &RegisteredClient{
		ClientID: "obz_dcr_" + suffix, ClientName: name, RedirectURIs: string(redirects),
		CreatedAt: time.Now().UTC(),
	}
	if request.ClientURI != "" {
		registered.ClientURI = &request.ClientURI
	}
	if err := s.repository.CreateClient(registered); err != nil {
		return nil, &TokenError{"server_error", "failed to register client"}
	}

	return &RegisterResponse{
		ClientID: registered.ClientID, ClientName: name, RedirectURIs: request.RedirectURIs,
		TokenEndpointAuthMethod: "none", GrantTypes: []string{"authorization_code", "refresh_token"},
		ResponseTypes: []string{"code"}, ClientIDIssuedAt: registered.CreatedAt.Unix(),
	}, nil
}

type ConnectedAppDTO struct {
	ID         uuid.UUID `json:"id"`
	ClientName string    `json:"clientName"`
	ClientID   string    `json:"clientId"`
	LastUsedAt time.Time `json:"lastUsedAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

func (s *OAuthService) ListConnectedApps(user *users_models.User) ([]ConnectedAppDTO, error) {
	tokens, err := s.repository.ListActiveByUser(user.ID, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	apps := make([]ConnectedAppDTO, 0, len(tokens))
	for _, t := range tokens {
		apps = append(apps, ConnectedAppDTO{
			ID: t.FamilyID, ClientName: t.ClientName, ClientID: t.ClientID,
			LastUsedAt: t.CreatedAt, ExpiresAt: t.FamilyExpiresAt,
		})
	}
	return apps, nil
}

var ErrAppNotFound = errors.New("connected app not found")

func (s *OAuthService) RevokeConnectedApp(user *users_models.User, familyID uuid.UUID) error {
	revoked, err := s.repository.RevokeUserFamily(user.ID, familyID, time.Now().UTC())
	if err != nil {
		return err
	}
	if !revoked {
		return ErrAppNotFound
	}
	_ = s.accessTokenService.RevokeFamily(familyID)
	s.auditLogService.WriteAuditLog(fmt.Sprintf("OAuth app access revoked: %s", familyID), &user.ID, nil)
	return nil
}

func (s *OAuthService) issue(
	userID uuid.UUID, clientID, clientName, audience, scopes string, familyID uuid.UUID, familyExpires time.Time,
	mfaSatisfied bool,
) (*TokenResponse, *TokenError) {
	now := time.Now().UTC()
	_ = s.repository.DeleteExpired(userID, now)

	accessToken, _, err := s.accessTokenService.IssueOAuth(
		userID, clientID, clientName, audience, scopes, familyID, accessTokenTTL, mfaSatisfied,
	)
	if err != nil {
		return nil, &TokenError{"server_error", "failed to issue access token"}
	}

	suffix, err := randomToken(32)
	if err != nil {
		return nil, &TokenError{"server_error", "failed to issue refresh token"}
	}
	refreshToken := refreshPrefix + suffix
	expires := now.Add(refreshTokenTTL)
	if familyExpires.Before(expires) {
		expires = familyExpires
	}
	if err := s.repository.CreateRefreshToken(&RefreshToken{
		ID: uuid.New(), FamilyID: familyID, UserID: userID, ClientID: clientID, ClientName: clientName,
		TokenHash: hashValue(refreshToken), Scopes: scopes, Audience: audience,
		FamilyExpiresAt: familyExpires, ExpiresAt: expires, CreatedAt: now, MFASatisfied: mfaSatisfied,
	}); err != nil {
		return nil, &TokenError{"server_error", "failed to issue refresh token"}
	}

	return &TokenResponse{
		AccessToken: accessToken, TokenType: "Bearer", ExpiresIn: int(accessTokenTTL.Seconds()),
		RefreshToken: refreshToken, Scope: scopes,
	}, nil
}

func (s *OAuthService) revokeFamily(familyID uuid.UUID) {
	now := time.Now().UTC()
	_ = s.repository.RevokeFamily(familyID, now)
	_ = s.accessTokenService.RevokeFamily(familyID)
}

func (s *OAuthService) resolveClient(ctx context.Context, clientID string) (*client, error) {
	if isMetadataDocumentClientID(clientID) {
		if cached := s.metadataCache.Get(clientID); cached != nil {
			return cached, nil
		}
		c, err := fetchMetadataDocument(ctx, clientID)
		if err != nil {
			return nil, err
		}
		s.metadataCache.Set(clientID, c)
		return c, nil
	}

	registered, err := s.repository.GetClient(clientID)
	if err != nil {
		return nil, err
	}
	if registered == nil {
		return nil, errUnknownClient
	}
	var redirects []string
	_ = json.Unmarshal([]byte(registered.RedirectURIs), &redirects)
	c := &client{ID: registered.ClientID, Name: registered.ClientName, RedirectURIs: redirects}
	if registered.ClientURI != nil {
		c.URI = *registered.ClientURI
	}
	return c, nil
}

// normalizeScope accepts an empty scope or one that is exactly mcp:read.
func normalizeScope(scope string) (string, bool) {
	fields := strings.Fields(scope)
	if len(fields) == 0 {
		return supportedScope, true
	}
	for _, field := range fields {
		if field != supportedScope {
			return "", false
		}
	}
	return supportedScope, true
}

func errorRedirect(request *AuthorizeRequest, issuer, code, description string) string {
	query := url.Values{"error": {code}, "error_description": {description}, "iss": {issuer}}
	if request.State != "" {
		query.Set("state", request.State)
	}
	return appendQuery(request.RedirectURI, query)
}

func appendQuery(rawURL string, query url.Values) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	existing := parsed.Query()
	for key, values := range query {
		existing[key] = values
	}
	parsed.RawQuery = existing.Encode()
	return parsed.String()
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomToken(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func hashValue(value string) string {
	return access_tokens.HashToken(value)
}
