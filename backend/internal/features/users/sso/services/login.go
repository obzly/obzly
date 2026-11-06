package sso_services

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	sso_dto "obzly/internal/features/users/sso/dto"
	sso_models "obzly/internal/features/users/sso/models"
	"obzly/internal/util/logger"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

var ErrConnectorDisabled = errors.New("SSO connector is disabled")

// IdentityClaims is what HandleCallback extracts from a verified ID token,
// provider-neutral so account linking needs no OIDC knowledge.
type IdentityClaims struct {
	ConnectorID uuid.UUID
	Subject     string
	Email       string
	Name        string

	// EmailVerified: the provider vouches for Email. RequireVerifiedEmail is
	// the connector's setting; when both apply the caller must not link or
	// create an account by this email unless EmailVerified.
	EmailVerified        bool
	RequireVerifiedEmail bool

	// IdPMFAVerified: the connector trusts the provider's MFA and amr shows a second
	// factor, so local TOTP is skipped.
	IdPMFAVerified bool

	// GroupsSyncEnabled is true when a groups claim is configured. A missing claim
	// means "no groups" (Cognito omits it for a user in none), not "unknown".
	GroupsSyncEnabled bool
	Groups            []string
	// GroupsUnknown is true when the groups could not be determined (an Entra
	// ID group-overage lookup failed): the sync must then change nothing
	// rather than treat the user as having no groups.
	GroupsUnknown bool
}

// oidcClaims is the standard OIDC claim subset (openid-connect-core-1_0 StandardClaims).
type oidcClaims struct {
	Subject string `json:"sub"`
	Email   string `json:"email"`
	// email_verified is a JSON boolean at most providers but the string
	// "true" at a few; xms_edov is Entra ID's optional "email domain owner
	// verified" claim.
	EmailVerified            any    `json:"email_verified"`
	EmailDomainOwnerVerified any    `json:"xms_edov"`
	Name                     string `json:"name"`
	GivenName                string `json:"given_name"`
	FamilyName               string `json:"family_name"`
	// AMR is RFC 8176's authentication methods; some providers send a string.
	AMR any `json:"amr"`
}

// mfaAMRValues are RFC 8176 methods that mean a second factor was used.
var mfaAMRValues = []string{"mfa", "otp", "hwk", "swk", "sms", "fido"}

func amrShowsMFA(amr any) bool {
	var methods []string
	switch v := amr.(type) {
	case []any:
		for _, item := range v {
			if method, ok := item.(string); ok {
				methods = append(methods, method)
			}
		}
	case string:
		methods = strings.Fields(strings.ReplaceAll(v, ",", " "))
	}
	for _, method := range methods {
		if slices.Contains(mfaAMRValues, strings.ToLower(method)) {
			return true
		}
	}
	return false
}

// HandleCallback verifies the signed state, exchanges the code with the
// connector's secret and verifies the ID token via the provider's JWKS (go-oidc).
func (s *ConnectorService) HandleCallback(request *sso_dto.SSOCallbackRequestDTO) (*IdentityClaims, error) {
	connectorID, err := verifyState(s.secretKeyRepository, request.State)
	if err != nil {
		return nil, err
	}

	connector, err := s.connectorRepository.GetByID(connectorID)
	if err != nil {
		return nil, fmt.Errorf("failed to load connector: %w", err)
	}
	if connector == nil {
		return nil, ErrConnectorNotFound
	}
	if !connector.Enabled {
		return nil, ErrConnectorDisabled
	}

	clientSecret, err := decryptSecret(s.secretKeyRepository, connector.ClientSecretEncrypted)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt client secret: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	provider, err := discoverProviderCached(connector.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("failed to discover OIDC configuration: %w", err)
	}

	oauthConfig := &oauth2.Config{
		ClientID:     connector.ClientID,
		ClientSecret: clientSecret,
		RedirectURL:  request.RedirectUri,
		Endpoint:     tokenEndpointFor(provider),
		Scopes:       []string{"openid"},
	}

	token, err := oauthConfig.Exchange(ctx, request.Code)
	if err != nil {
		return nil, describeExchangeError(connector, err)
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return nil, errors.New("token response did not include an id_token")
	}

	verifier := provider.Verifier(&oidc.Config{ClientID: connector.ClientID})
	idToken, err := verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("failed to verify id_token: %w", err)
	}
	// Binds the ID token to this sign-in, so one issued for another request
	// can't be injected (go-oidc leaves the nonce to the caller).
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(NonceForState(request.State))) != 1 {
		return nil, errors.New("the identity provider's token doesn't belong to this sign-in - try signing in again")
	}

	var claims oidcClaims
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("failed to decode id_token claims: %w", err)
	}

	if claims.Email == "" {
		return nil, fmt.Errorf("%s account has no email claim", connector.Name)
	}

	emailVerified := isTruthyClaim(claims.EmailVerified) ||
		(connector.ProviderType == sso_models.ProviderTypeEntraID && isTruthyClaim(claims.EmailDomainOwnerVerified))

	var groups []string
	groupsUnknown := false
	if connector.GroupsClaim != "" {
		var raw map[string]any
		if err := idToken.Claims(&raw); err != nil {
			return nil, fmt.Errorf("failed to decode id_token claims: %w", err)
		}
		groups = extractGroups(raw[connector.GroupsClaim])

		if len(groups) == 0 && connector.ProviderType == sso_models.ProviderTypeEntraID &&
			hasGroupsOverage(raw, connector.GroupsClaim) {
			resolved, err := fetchEntraGroupIDs(ctx, token.AccessToken)
			if err != nil {
				logger.GetLogger().Warn(
					"SSO group sync skipped: could not resolve Entra ID group overage",
					"connector", connector.Name, "error", err,
				)
				groupsUnknown = true
			} else {
				groups = extractGroups(resolved)
			}
		}
	}

	name := claims.Name
	if name == "" {
		name = firstNonEmpty(claims.GivenName+" "+claims.FamilyName, claims.Email)
	}

	return &IdentityClaims{
		ConnectorID: connectorID,
		Subject:     claims.Subject,
		Email:       claims.Email,
		Name:        name,

		EmailVerified:        emailVerified,
		RequireVerifiedEmail: connector.RequireVerifiedEmail,
		IdPMFAVerified:       connector.TrustIdPMFA && amrShowsMFA(claims.AMR),

		GroupsSyncEnabled: connector.GroupsClaim != "",
		Groups:            groups,
		GroupsUnknown:     groupsUnknown,
	}, nil
}

func firstNonEmpty(candidates ...string) string {
	for _, c := range candidates {
		if c != "" && c != " " {
			return c
		}
	}
	return ""
}

// extractGroups normalizes a groups claim into a de-duplicated list. Cognito
// and most providers send a JSON array; some federated setups send a single
// string, possibly comma separated or bracketed like "[a, b]".
func extractGroups(value any) []string {
	var candidates []string
	switch v := value.(type) {
	case []string:
		candidates = v
	case []any:
		for _, item := range v {
			if str, ok := item.(string); ok {
				candidates = append(candidates, str)
			}
		}
	case string:
		trimmed := strings.TrimSpace(v)
		trimmed = strings.TrimPrefix(trimmed, "[")
		trimmed = strings.TrimSuffix(trimmed, "]")
		candidates = strings.Split(trimmed, ",")
	}

	seen := make(map[string]bool, len(candidates))
	groups := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		group := strings.Trim(strings.TrimSpace(candidate), `"'`)
		if group == "" || seen[group] {
			continue
		}
		seen[group] = true
		groups = append(groups, group)
	}
	return groups
}

func isTruthyClaim(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	default:
		return false
	}
}

// tokenEndpointFor sets an explicit auth style. With "auto", x/oauth2 retries a
// failed exchange with the same single-use code, so the real error is hidden
// behind invalid_grant.
func tokenEndpointFor(provider *oidc.Provider) oauth2.Endpoint {
	endpoint := provider.Endpoint()
	endpoint.AuthStyle = oauth2.AuthStyleInHeader

	var discovery struct {
		AuthMethods []string `json:"token_endpoint_auth_methods_supported"`
	}
	if err := provider.Claims(&discovery); err == nil && len(discovery.AuthMethods) > 0 &&
		!slices.Contains(discovery.AuthMethods, "client_secret_basic") &&
		slices.Contains(discovery.AuthMethods, "client_secret_post") {
		endpoint.AuthStyle = oauth2.AuthStyleInParams
	}
	return endpoint
}

// describeExchangeError logs the provider's full refusal and returns a message
// naming what it objected to.
func describeExchangeError(connector *sso_models.Connector, err error) error {
	var retrieveErr *oauth2.RetrieveError
	if !errors.As(err, &retrieveErr) {
		logger.GetLogger().Warn("SSO code exchange failed", "connector", connector.Name, "error", err)
		return fmt.Errorf("failed to exchange authorization code: %w", err)
	}

	status := 0
	if retrieveErr.Response != nil {
		status = retrieveErr.Response.StatusCode
	}
	logger.GetLogger().Warn("SSO code exchange refused by the identity provider",
		"connector", connector.Name,
		"status", status,
		"error", retrieveErr.ErrorCode,
		"errorDescription", retrieveErr.ErrorDescription,
		"body", string(retrieveErr.Body),
	)

	detail := retrieveErr.ErrorCode
	if retrieveErr.ErrorDescription != "" {
		detail += " (" + retrieveErr.ErrorDescription + ")"
	}
	switch retrieveErr.ErrorCode {
	case "invalid_client":
		return fmt.Errorf("the identity provider rejected this connector's client credentials: %s - "+
			"check the connector's Client ID and Client secret", detail)
	case "invalid_grant":
		return fmt.Errorf("the identity provider rejected the sign-in code: %s - it was already used, "+
			"expired, or the app client's callback URL doesn't match this page; try signing in again", detail)
	default:
		return fmt.Errorf("the identity provider refused the sign-in: %s", detail)
	}
}
