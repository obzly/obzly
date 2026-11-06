package control_plane_services

import (
	"fmt"
	"time"

	users_models "obzly/internal/features/users/models"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"
)

// assertionLifetime is short because an assertion is used once, immediately, by a
// server-to-server exchange; a leaked one shouldn't stay valid.
const assertionLifetime = 60 * time.Second

// assertionProject is one entry of the "projects" claim; must stay wire-identical to
// users_services.centralAssertionProject on the Site side.
type assertionProject struct {
	ID   string `json:"id"`
	Role string `json:"role"`
}

type AssertionService struct {
	signingKeyService *SigningKeyService
	grantService      *GrantService
}

// Mint signs a short-lived ES256 assertion for (user, siteID), exchanged at the Site's
// POST /api/v1/auth/site/exchange. amr reflects how this central session was
// authenticated; the Site trusts it for MFA enforcement.
func (s *AssertionService) Mint(
	user *users_models.User,
	siteID string,
	publicURL string,
	amr []string,
) (string, error) {
	grant, err := s.grantService.ResolveEffectiveGrant(user.ID, siteID)
	if err != nil {
		return "", fmt.Errorf("failed to resolve grant: %w", err)
	}
	if !grant.HasAnyAccess() {
		return "", fmt.Errorf("user %s has no access to site %s", user.ID, siteID)
	}

	kid, key, err := s.signingKeyService.activeSigner()
	if err != nil {
		return "", err
	}

	// site_role keeps its old meaning (ADMIN or MEMBER) for older Sites. A Site-wide
	// MEMBER/VIEWER grant travels as all_projects_role, which older Sites ignore.
	siteRole := "MEMBER"
	if grant.IsAdmin() {
		siteRole = "ADMIN"
	}
	allProjectsRole := SiteWideProjectRole(grant.SiteRole)

	projects := make([]assertionProject, 0, len(grant.Projects))
	for projectID, role := range grant.Projects {
		projects = append(projects, assertionProject{ID: projectID.String(), Role: role})
	}

	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.ES256, Key: key},
		(&jose.SignerOptions{}).WithHeader("kid", kid).WithType("JWT"),
	)
	if err != nil {
		return "", fmt.Errorf("failed to build signer: %w", err)
	}

	now := time.Now().UTC()
	claims := map[string]any{
		"iss":       publicURL,
		"aud":       siteID,
		"sub":       user.ID.String(),
		"jti":       uuid.NewString(),
		"iat":       now.Unix(),
		"nbf":       now.Add(-5 * time.Second).Unix(),
		"exp":       now.Add(assertionLifetime).Unix(),
		"email":     user.Email,
		"name":      user.Name,
		"site_role": siteRole,
		"amr":       amr,
		"projects":  projects,
	}
	if allProjectsRole != "" {
		claims["all_projects_role"] = allProjectsRole
	}

	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		return "", fmt.Errorf("failed to sign assertion: %w", err)
	}
	return raw, nil
}
