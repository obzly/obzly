package control_plane_services

import (
	"fmt"
	"time"

	"obzly/internal/config"
	"obzly/internal/features/orgplan"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"
)

// SignSitePlan signs the plan document for siteID (orgplan.Document) with
// the assertion keys; orgID is nil for a Site in no organization.
func (s *SigningKeyService) SignSitePlan(siteID string, orgID *uuid.UUID, limits orgplan.Limits) (string, error) {
	kid, key, err := s.activeSigner()
	if err != nil {
		return "", err
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.ES256, Key: key},
		(&jose.SignerOptions{}).WithHeader("kid", kid).WithType("JWT"),
	)
	if err != nil {
		return "", fmt.Errorf("failed to build signer: %w", err)
	}
	org := ""
	if orgID != nil {
		org = orgID.String()
	}
	now := time.Now().UTC()
	raw, err := jwt.Signed(signer).Claims(map[string]any{
		"iss":       config.GetEnv().PublicURL,
		"aud":       siteID,
		"jti":       uuid.NewString(),
		"iat":       now.Unix(),
		"nbf":       now.Add(-5 * time.Second).Unix(),
		"exp":       now.Add(orgplan.DocumentLifetime).Unix(),
		"token_use": orgplan.TokenUse,
		"org":       org,
		"limits":    limits,
	}).Serialize()
	if err != nil {
		return "", fmt.Errorf("failed to sign site plan: %w", err)
	}
	return raw, nil
}

// SignSiteDocument signs claims for siteID with iss/aud/jti/iat/nbf/exp and token_use,
// so a Site can't take one kind of document for another.
func (s *SigningKeyService) SignSiteDocument(
	siteID, tokenUse string, lifetime time.Duration, claims map[string]any,
) (string, error) {
	kid, key, err := s.activeSigner()
	if err != nil {
		return "", err
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.ES256, Key: key},
		(&jose.SignerOptions{}).WithHeader("kid", kid).WithType("JWT"),
	)
	if err != nil {
		return "", fmt.Errorf("failed to build signer: %w", err)
	}
	now := time.Now().UTC()
	all := map[string]any{
		"iss": config.GetEnv().PublicURL, "aud": siteID, "jti": uuid.NewString(),
		"iat": now.Unix(), "nbf": now.Add(-5 * time.Second).Unix(), "exp": now.Add(lifetime).Unix(),
		"token_use": tokenUse,
	}
	for name, value := range claims {
		if _, reserved := all[name]; !reserved {
			all[name] = value
		}
	}
	raw, err := jwt.Signed(signer).Claims(all).Serialize()
	if err != nil {
		return "", fmt.Errorf("failed to sign the document: %w", err)
	}
	return raw, nil
}
