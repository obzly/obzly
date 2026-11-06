package users_controllers

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/require"
)

// lastAuthorizeNonce is the nonce of the authorize URL fetchStateForConnector read last.
var lastAuthorizeNonce string

// fakeOIDCServer is a local copy of sso/services' test provider (test
// helpers can't be imported across packages).
type fakeOIDCServer struct {
	Server   *httptest.Server
	ClientID string
	key      *rsa.PrivateKey
	keyID    string
	codes    map[string]jwt.Claims
	extra    map[string]map[string]any
	// amr, when set, is the authentication methods claim of every ID token.
	amr []string
	// authorizeEndpoint, when set, replaces the advertised authorization_endpoint.
	authorizeEndpoint string
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
	authorizeEndpoint := f.Server.URL + "/authorize"
	if f.authorizeEndpoint != "" {
		authorizeEndpoint = f.authorizeEndpoint
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuer":                                f.Server.URL,
		"authorization_endpoint":                authorizeEndpoint,
		"token_endpoint":                        f.Server.URL + "/token",
		"jwks_uri":                              f.Server.URL + "/jwks",
		"userinfo_endpoint":                     f.Server.URL + "/userinfo",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
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

	claims, ok := f.codes[code]
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
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
	// A real provider echoes the authorize request's nonce; the tests'
	// "authorize request" is fetchStateForConnector.
	if lastAuthorizeNonce != "" {
		builder = builder.Claims(map[string]any{"nonce": lastAuthorizeNonce})
	}
	if f.amr != nil {
		builder = builder.Claims(map[string]any{"amr": f.amr})
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

// IssueCode registers a one-time authorization code that redeems to an ID
// token carrying the given subject/email/name.
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
