package users_services

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"obzly/internal/config"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// fakeControlPlane serves a JWKS (no discovery document, as when
// CONTROL_PLANE_JWKS_URL differs from the issuer) and signs ES256 assertions.
type fakeControlPlane struct {
	Server *httptest.Server
	key    *ecdsa.PrivateKey
	keyID  string
}

func newFakeControlPlane(t *testing.T) *fakeControlPlane {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	f := &fakeControlPlane{key: key, keyID: "cp-key-1"}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/jwks.json", f.handleJWKS)
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Server.Close)

	return f
}

func (f *fakeControlPlane) handleJWKS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	jwk := jose.JSONWebKey{Key: &f.key.PublicKey, KeyID: f.keyID, Algorithm: "ES256", Use: "sig"}
	_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}})
}

func (f *fakeControlPlane) Issuer() string  { return f.Server.URL }
func (f *fakeControlPlane) JWKSURL() string { return f.Server.URL + "/.well-known/jwks.json" }

// Env returns EnvVariables for this fake, for exchangeCentralAssertion (never
// config.GetEnv()).
func (f *fakeControlPlane) Env(siteID string) config.EnvVariables {
	return config.EnvVariables{
		SiteID:              siteID,
		ControlPlaneIssuer:  f.Issuer(),
		ControlPlaneJWKSURL: f.JWKSURL(),
	}
}

// centralAssertionOptions overrides Mint's default: a valid 60-second assertion
// for a MEMBER of one project. Zero values keep the default.
type centralAssertionOptions struct {
	Subject  string
	Email    string
	Name     string
	SiteRole string
	Projects []centralAssertionProject
	// AllProjectsRole sets the all_projects_role claim (a Site-wide
	// MEMBER/VIEWER grant) - omitted when empty, like the real minter.
	AllProjectsRole string
	AMR             []string
	JTI             string
	IssuedAt        time.Time
	NotBefore       time.Time
	Expiry          time.Time
	Issuer          string // overrides f.Issuer() - for a wrong-iss test
	Audience        string // overrides siteID - for a wrong-aud test
	TokenUse        string // set on the control plane's other documents
}

// Mint signs an ES256 assertion for siteID, like the control plane's AssertionService.Mint.
func (f *fakeControlPlane) Mint(t *testing.T, siteID string, opts centralAssertionOptions) string {
	t.Helper()

	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.ES256, Key: f.key},
		(&jose.SignerOptions{}).WithHeader("kid", f.keyID).WithType("JWT"),
	)
	require.NoError(t, err)

	now := time.Now()
	issuer := opts.Issuer
	if issuer == "" {
		issuer = f.Issuer()
	}
	audience := opts.Audience
	if audience == "" {
		audience = siteID
	}
	subject := opts.Subject
	if subject == "" {
		subject = "cp-subject-" + uuid.NewString()
	}
	email := opts.Email
	if email == "" {
		email = subject + "@example.com"
	}
	siteRole := opts.SiteRole
	if siteRole == "" {
		siteRole = "MEMBER"
	}
	jti := opts.JTI
	if jti == "" {
		jti = uuid.NewString()
	}
	iat := opts.IssuedAt
	if iat.IsZero() {
		iat = now
	}
	nbf := opts.NotBefore
	if nbf.IsZero() {
		nbf = now.Add(-5 * time.Second)
	}
	exp := opts.Expiry
	if exp.IsZero() {
		exp = now.Add(60 * time.Second)
	}

	claims := map[string]any{
		"iss":       issuer,
		"aud":       audience,
		"sub":       subject,
		"jti":       jti,
		"iat":       iat.Unix(),
		"nbf":       nbf.Unix(),
		"exp":       exp.Unix(),
		"email":     email,
		"name":      opts.Name,
		"site_role": siteRole,
		"amr":       opts.AMR,
		"projects":  opts.Projects,
	}
	if opts.AllProjectsRole != "" {
		claims["all_projects_role"] = opts.AllProjectsRole
	}
	if opts.TokenUse != "" {
		claims["token_use"] = opts.TokenUse
	}

	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	require.NoError(t, err)
	return raw
}
