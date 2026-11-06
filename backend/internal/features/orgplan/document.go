package orgplan

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"obzly/internal/config"

	"github.com/coreos/go-oidc/v3/oidc"
)

// Document is the plan a control plane publishes for one Site: an ES256
// JWT with the assertion signing keys, issued to the Site (aud = SITE_ID).
// TokenUse keeps it from ever passing as a sign-in assertion, and an
// assertion from passing as a plan.
type Document struct {
	TokenUse  string  `json:"token_use"`
	OrgID     string  `json:"org"`
	Limits    *Limits `json:"limits"`
	IssuedAt  int64   `json:"iat"`
	NotBefore int64   `json:"nbf"`
	Expiry    int64   `json:"exp"`
}

const (
	// TokenUse is a plan document's token_use claim.
	TokenUse = "site_plan"
	// DocumentLifetime is how long a document is accepted after issue; a
	// Site keeps a verified plan for good, so this only bounds replay.
	DocumentLifetime = 24 * time.Hour

	maxClockSkew = 2 * time.Minute
)

var verifiers sync.Map // issuer|jwks|site -> *oidc.IDTokenVerifier

// VerifyDocument checks raw against the control plane's JWKS, issuer and
// this Site's id, then that it is a plan document issued within its
// lifetime.
func VerifyDocument(ctx context.Context, env config.EnvVariables, raw string, now time.Time) (*Document, error) {
	token, err := verifier(env).Verify(ctx, raw)
	if err != nil {
		return nil, err
	}
	var doc Document
	if err := token.Claims(&doc); err != nil {
		return nil, err
	}
	switch {
	case doc.TokenUse != TokenUse:
		return nil, errors.New("not a plan document")
	case doc.Limits == nil:
		return nil, errors.New("plan document has no limits")
	case doc.IssuedAt == 0:
		return nil, errors.New("plan document is missing iat")
	case time.Unix(doc.IssuedAt, 0).After(now.Add(maxClockSkew)):
		return nil, errors.New("plan document is issued in the future")
	case doc.Expiry-doc.IssuedAt > int64((DocumentLifetime + maxClockSkew).Seconds()):
		return nil, fmt.Errorf("plan document lifetime exceeds %s", DocumentLifetime)
	}
	return &doc, nil
}

func verifier(env config.EnvVariables) *oidc.IDTokenVerifier {
	key := env.ControlPlaneIssuer + "|" + env.ControlPlaneJWKSURL + "|" + env.SiteID
	if cached, ok := verifiers.Load(key); ok {
		v, _ := cached.(*oidc.IDTokenVerifier)
		return v
	}
	keySet := oidc.NewRemoteKeySet(context.Background(), env.ControlPlaneJWKSURL)
	v := oidc.NewVerifier(env.ControlPlaneIssuer, keySet, &oidc.Config{
		ClientID:             env.SiteID,
		SupportedSigningAlgs: []string{oidc.ES256},
	})
	actual, _ := verifiers.LoadOrStore(key, v)
	loaded, _ := actual.(*oidc.IDTokenVerifier)
	return loaded
}
