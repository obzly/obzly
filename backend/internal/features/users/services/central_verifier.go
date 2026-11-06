package users_services

import (
	"context"
	"errors"
	"sync"
	"time"

	"obzly/internal/config"

	"github.com/coreos/go-oidc/v3/oidc"
)

// centralAssertionClaims is the ES256 assertion a control plane mints for one
// (user, Site) exchange (ARCHITECTURE.md "Multi-site: control plane and Sites").
type centralAssertionClaims struct {
	Subject  string                    `json:"sub"`
	Email    string                    `json:"email"`
	Name     string                    `json:"name"`
	JTI      string                    `json:"jti"`
	SiteRole string                    `json:"site_role"`
	AMR      []string                  `json:"amr"`
	Projects []centralAssertionProject `json:"projects"`
	// AllProjectsRole is a Site-wide MEMBER/VIEWER grant applied to every project,
	// including later ones (reconciled at each exchange).
	AllProjectsRole string `json:"all_projects_role,omitempty"`
	// TokenUse is set on the control plane's other documents (an organization plan),
	// never on an assertion.
	TokenUse  string `json:"token_use,omitempty"`
	IssuedAt  int64  `json:"iat"`
	NotBefore int64  `json:"nbf"`
	Expiry    int64  `json:"exp"`
}

const (
	// centralAssertionMaxSkew bounds how far in the future an assertion's
	// iat/nbf may sit relative to this Site's clock.
	centralAssertionMaxSkew = 2 * time.Minute
	// centralAssertionMaxLifetime caps exp-iat. Assertions are minted for 60s; a
	// longer one is misconfigured or forged, and go-oidc's exp check alone would accept it.
	centralAssertionMaxLifetime = 5 * time.Minute
)

// checkAssertionTimes covers what go-oidc doesn't: nbf, a required iat and a
// lifetime bound.
func checkAssertionTimes(claims *centralAssertionClaims, now time.Time) error {
	if claims.IssuedAt == 0 {
		return errors.New("assertion is missing iat")
	}
	iat := time.Unix(claims.IssuedAt, 0)
	if iat.After(now.Add(centralAssertionMaxSkew)) {
		return errors.New("assertion iat is in the future")
	}
	if claims.NotBefore != 0 && time.Unix(claims.NotBefore, 0).After(now.Add(centralAssertionMaxSkew)) {
		return errors.New("assertion is not yet valid")
	}
	if claims.Expiry != 0 && time.Unix(claims.Expiry, 0).Sub(iat) > centralAssertionMaxLifetime {
		return errors.New("assertion lifetime is too long")
	}
	return nil
}

type centralAssertionProject struct {
	ID   string `json:"id"`
	Role string `json:"role"`
}

// centralVerifierCache holds one verifier per (issuer, JWKS URL, site ID). Lazy
// because the control plane and Site start in any order; keyed so tests can use
// a fresh fake control plane each.
var centralVerifierCache sync.Map // string -> *oidc.IDTokenVerifier

func getCentralVerifier(env config.EnvVariables) *oidc.IDTokenVerifier {
	key := env.ControlPlaneIssuer + "|" + env.ControlPlaneJWKSURL + "|" + env.SiteID
	if cached, ok := centralVerifierCache.Load(key); ok {
		verifier, _ := cached.(*oidc.IDTokenVerifier)
		return verifier
	}

	// Background context: the key set outlives the request that built it and
	// refetches on an unknown kid.
	keySet := oidc.NewRemoteKeySet(context.Background(), env.ControlPlaneJWKSURL)
	verifier := oidc.NewVerifier(env.ControlPlaneIssuer, keySet, &oidc.Config{
		ClientID:             env.SiteID,
		SupportedSigningAlgs: []string{oidc.ES256},
	})

	actual, _ := centralVerifierCache.LoadOrStore(key, verifier)
	loaded, _ := actual.(*oidc.IDTokenVerifier)
	return loaded
}

// verifyCentralAssertion checks signature (ES256 only), issuer, audience (SITE_ID)
// and exp via go-oidc, then checkAssertionTimes.
func verifyCentralAssertion(ctx context.Context, env config.EnvVariables, raw string) (*centralAssertionClaims, error) {
	if !env.SiteExchangeEnabled() {
		return nil, errors.New("central token exchange is not configured")
	}

	verifier := getCentralVerifier(env)
	idToken, err := verifier.Verify(ctx, raw)
	if err != nil {
		return nil, err
	}

	var claims centralAssertionClaims
	if err := idToken.Claims(&claims); err != nil {
		return nil, err
	}
	if claims.TokenUse != "" || claims.Subject == "" {
		return nil, errors.New("not a sign-in assertion")
	}
	if err := checkAssertionTimes(&claims, time.Now()); err != nil {
		return nil, err
	}
	return &claims, nil
}
