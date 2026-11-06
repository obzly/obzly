package orgplan

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"obzly/internal/config"
	"obzly/internal/storage"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type signer struct {
	key    *ecdsa.PrivateKey
	server *httptest.Server
}

func Test_Stricter_ZeroIsUnlimited(t *testing.T) {
	assert.Equal(t, 0, Stricter(0, 0))
	assert.Equal(t, 5, Stricter(0, 5))
	assert.Equal(t, 5, Stricter(5, 0))
	assert.Equal(t, 3, Stricter(5, 3))
	assert.Equal(t, 3, Stricter(3, 5))
	assert.Equal(t, int64(7), Stricter64(0, 7))
	assert.Equal(t, int64(2), Stricter64(2, 7))
}

func Test_Validate_RefusesNegativeAndAbsurdLimits(t *testing.T) {
	require.NoError(t, Limits{}.Validate())
	require.NoError(t, Limits{LogsPerSecondLimit: 1000, MaxLogsLifeDays: 3650, MaxLogsAmount: 1 << 40}.Validate())
	require.Error(t, Limits{MaxProjects: -1}.Validate())
	require.Error(t, Limits{MaxLogsAmount: -1}.Validate())
	require.Error(t, Limits{MaxLogsLifeDays: 3651}.Validate())
	require.Error(t, Limits{MaxMembers: 1_000_000_001}.Validate())
}

func Test_DocumentURL_NextToTheJWKS(t *testing.T) {
	env := config.EnvVariables{SiteID: "eu 1", ControlPlaneIssuer: "https://cp.example.com",
		ControlPlaneJWKSURL: "http://cp.internal:4005/.well-known/jwks.json"}
	assert.Equal(t, "http://cp.internal:4005/api/v1/site-plans/eu%201", DocumentURL(env))
	env.ControlPlaneJWKSURL = "https://keys.example.com/jwks"
	assert.Equal(t, "https://cp.example.com/api/v1/site-plans/eu%201", DocumentURL(env))
}

func Test_VerifyDocument_OnlyPlanDocumentsForThisSite(t *testing.T) {
	s := newSigner(t)
	env := config.EnvVariables{SiteID: "site-a", ControlPlaneIssuer: s.server.URL,
		ControlPlaneJWKSURL: s.server.URL + "/.well-known/jwks.json"}
	now := time.Now().UTC()
	limits := Limits{MaxProjects: 2}

	doc, err := VerifyDocument(context.Background(), env, s.sign(t, s.claims("site-a", now, limits)), now)
	require.NoError(t, err)
	assert.Equal(t, limits, *doc.Limits)

	for name, mutate := range map[string]func(map[string]any){
		"assertion":     func(c map[string]any) { delete(c, "token_use"); c["sub"] = "user" },
		"no limits":     func(c map[string]any) { delete(c, "limits") },
		"other site":    func(c map[string]any) { c["aud"] = "site-b" },
		"long lifetime": func(c map[string]any) { c["exp"] = now.Add(72 * time.Hour).Unix() },
		"issued later":  func(c map[string]any) { c["iat"] = now.Add(time.Hour).Unix() },
		"expired": func(c map[string]any) {
			c["iat"], c["exp"] = now.Add(-48*time.Hour).Unix(), now.Add(-24*time.Hour).Unix()
		},
		"other issuer":    func(c map[string]any) { c["iss"] = "https://elsewhere.example.com" },
		"wrong token_use": func(c map[string]any) { c["token_use"] = "something_else" },
	} {
		claims := s.claims("site-a", now, limits)
		mutate(claims)
		_, err := VerifyDocument(context.Background(), env, s.sign(t, claims), now)
		assert.Error(t, err, name)
	}
}

// Replicas race; a document older than the held one never replaces it.
func Test_Apply_KeepsTheNewestDocument(t *testing.T) {
	require.NoError(t, storage.GetDb().Exec("DELETE FROM site_org_plan").Error)
	t.Cleanup(func() {
		Set(Limits{})
		storage.GetDb().Exec("DELETE FROM site_org_plan")
		stateMu.Lock()
		lastIssued = time.Time{}
		stateMu.Unlock()
	})
	now := time.Now().UTC()
	newer := &Document{TokenUse: TokenUse, Limits: &Limits{MaxProjects: 5}, IssuedAt: now.Unix()}
	older := &Document{TokenUse: TokenUse, Limits: &Limits{MaxProjects: 50}, IssuedAt: now.Add(-time.Minute).Unix()}

	require.NoError(t, apply(newer, now))
	require.NoError(t, apply(older, now))
	assert.Equal(t, 5, Current().MaxProjects)

	Set(Limits{})
	stateMu.Lock()
	lastIssued = time.Time{}
	stateMu.Unlock()
	require.NoError(t, loadCached())
	assert.Equal(t, 5, Current().MaxProjects, "survives a restart")
}

// A failed refresh warns only once failures have lasted an hour - not at
// start - and a control plane without plans is noted once, not warned.
func Test_LogStale_WarnsAfterAnHourOfFailures(t *testing.T) {
	var out bytes.Buffer
	log := slog.New(slog.NewTextHandler(&out, nil))
	stateMu.Lock()
	savedStart, savedFetched, savedLog := startedAt, lastFetched, lastStaleLog
	startedAt, lastFetched, lastStaleLog = time.Now().UTC(), time.Time{}, time.Time{}
	stateMu.Unlock()
	t.Cleanup(func() {
		stateMu.Lock()
		startedAt, lastFetched, lastStaleLog = savedStart, savedFetched, savedLog
		stateMu.Unlock()
	})

	logStale(log, errors.New("dial tcp: no such host"))
	assert.Empty(t, out.String(), "just started")
	logStale(log, ErrNotPublished)
	logStale(log, ErrNotPublished)
	assert.Equal(t, 1, strings.Count(out.String(), "publishes no organization plan"))
	assert.NotContains(t, out.String(), "level=WARN")

	stateMu.Lock()
	startedAt = time.Now().UTC().Add(-2 * time.Hour)
	stateMu.Unlock()
	logStale(log, errors.New("dial tcp: no such host"))
	logStale(log, errors.New("dial tcp: no such host"))
	assert.Equal(t, 1, strings.Count(out.String(), "level=WARN"))
}

func newSigner(t *testing.T) *signer {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	s := &signer{key: key}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
			{Key: &key.PublicKey, KeyID: "k1", Algorithm: string(jose.ES256), Use: "sig"},
		}})
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *signer) claims(site string, now time.Time, limits Limits) map[string]any {
	return map[string]any{
		"iss": s.server.URL, "aud": site, "iat": now.Unix(), "nbf": now.Add(-5 * time.Second).Unix(),
		"exp": now.Add(DocumentLifetime).Unix(), "token_use": TokenUse, "org": "", "limits": limits,
	}
}

func (s *signer) sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	jwtSigner, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: s.key},
		(&jose.SignerOptions{}).WithHeader("kid", "k1").WithType("JWT"))
	require.NoError(t, err)
	raw, err := jwt.Signed(jwtSigner).Claims(claims).Serialize()
	require.NoError(t, err)
	return raw
}
