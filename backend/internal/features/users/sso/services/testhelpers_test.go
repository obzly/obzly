package sso_services

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// adminUserForTest is an unpersisted root admin (connector changes are root-only).
func adminUserForTest() *users_models.User {
	return &users_models.User{ID: uuid.New(), Role: users_enums.UserRoleAdmin, IsRootAdmin: true}
}

// fakeOIDCServer is a real loopback OIDC provider (discovery, token endpoint,
// JWKS, RS256 ID tokens), so HandleCallback runs against real wire formats.
type fakeOIDCServer struct {
	Server   *httptest.Server
	ClientID string
	key      *rsa.PrivateKey
	keyID    string
	// codes maps an issued code to the claims for its ID token (see IssueCode).
	codes map[string]jwt.Claims
	extra map[string]map[string]any

	// Token endpoint observations, for tests about how the code is redeemed.
	tokenRequests  int
	lastUsedBasic  bool   // client secret sent in an HTTP Basic header
	lastBodySecret string // client_secret sent in the form body
	// authMethods, when set, is advertised as token_endpoint_auth_methods_supported.
	authMethods []string
	// nonce is echoed in every ID token, as a real provider echoes the authorize nonce.
	nonce string
}

// signStateForTest signs a state for connectorID and makes the fake echo
// its nonce, as if the browser had gone through /authorize with it.
func (f *fakeOIDCServer) signStateForTest(connectorID uuid.UUID) (string, error) {
	state, err := signState(secretKeyRepository, connectorID)
	if err != nil {
		return "", err
	}
	f.nonce = NonceForState(state)
	return state, nil
}

func newFakeOIDCServer(t *testing.T, clientID string) *fakeOIDCServer {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	f := &fakeOIDCServer{
		ClientID: clientID,
		key:      key,
		keyID:    "test-key-1",
		codes:    map[string]jwt.Claims{},
		extra:    map[string]map[string]any{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", f.handleDiscovery)
	mux.HandleFunc("/token", f.handleToken)
	mux.HandleFunc("/jwks", f.handleJWKS)

	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Server.Close)

	return f
}

func (f *fakeOIDCServer) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	discovery := map[string]any{
		"issuer":                                f.Server.URL,
		"authorization_endpoint":                f.Server.URL + "/authorize",
		"token_endpoint":                        f.Server.URL + "/token",
		"jwks_uri":                              f.Server.URL + "/jwks",
		"userinfo_endpoint":                     f.Server.URL + "/userinfo",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	}
	if len(f.authMethods) > 0 {
		discovery["token_endpoint_auth_methods_supported"] = f.authMethods
	}
	_ = json.NewEncoder(w).Encode(discovery)
}

func (f *fakeOIDCServer) handleJWKS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	jwk := jose.JSONWebKey{
		Key:       &f.key.PublicKey,
		KeyID:     f.keyID,
		Algorithm: "RS256",
		Use:       "sig",
	}
	_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}})
}

func (f *fakeOIDCServer) handleToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	_ = r.ParseForm()
	code := r.FormValue("code")
	f.tokenRequests++
	_, _, f.lastUsedBasic = r.BasicAuth()
	f.lastBodySecret = r.PostFormValue("client_secret")

	claims, ok := f.codes[code]
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":             "invalid_grant",
			"error_description": "Invalid authorization code",
		})
		return
	}

	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: f.key},
		(&jose.SignerOptions{}).WithHeader("kid", f.keyID).WithType("JWT"),
	)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	builder := jwt.Signed(signer).Claims(claims)
	if f.nonce != "" {
		builder = builder.Claims(map[string]any{"nonce": f.nonce})
	}
	if extra := f.extra[code]; len(extra) > 0 {
		builder = builder.Claims(extra)
	}
	rawIDToken, err := builder.Serialize()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "fake-access-token",
		"token_type":   "Bearer",
		"id_token":     rawIDToken,
		"expires_in":   3600,
	})
}

// IssueCode registers a one-time code whose ID token carries subject/email/name.
func (f *fakeOIDCServer) IssueCode(subject, email, name string) string {
	code := "code-" + subject
	f.codes[code] = jwt.Claims{
		Issuer:   f.Server.URL,
		Subject:  subject,
		Audience: jwt.Audience{f.ClientID},
		Expiry:   jwt.NewNumericDate(time.Now().Add(time.Hour)),
		IssuedAt: jwt.NewNumericDate(time.Now()),
	}
	f.extra[code] = map[string]any{
		"email":          email,
		"email_verified": true,
		"name":           name,
	}
	return code
}

// SetExtraClaim adds (or overrides) a claim on the ID token issued for code.
func (f *fakeOIDCServer) SetExtraClaim(code, key string, value any) {
	f.extra[code][key] = value
}
