package sso_services

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	sso_dto "obzly/internal/features/users/sso/dto"
	sso_models "obzly/internal/features/users/sso/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setUpConnector creates a connector via CreateConnector (exercising its
// discovery check) pointed at a fake OIDC server.
func setUpConnector(t *testing.T, fake *fakeOIDCServer) *sso_models.Connector {
	t.Helper()

	response, err := GetConnectorService().CreateConnector(&sso_dto.CreateConnectorRequestDTO{
		Name:         "Test Connector",
		ProviderType: sso_models.ProviderTypeGenericOIDC,
		IssuerURL:    fake.Server.URL,
		ClientID:     fake.ClientID,
		ClientSecret: "fake-client-secret",
		ButtonLabel:  "Continue with Test",
		Enabled:      true,
	}, adminUserForTest())
	require.NoError(t, err)

	connector, err := connectorRepository.GetByID(response.ID)
	require.NoError(t, err)
	return connector
}

func Test_HandleCallback_ValidCodeAndState_ReturnsClaims(t *testing.T) {
	fake := newFakeOIDCServer(t, "test-client-id")
	connector := setUpConnector(t, fake)

	code := fake.IssueCode("user-123", "person@example.com", "Test Person")
	state, err := fake.signStateForTest(connector.ID)
	require.NoError(t, err)

	claims, err := GetConnectorService().HandleCallback(&sso_dto.SSOCallbackRequestDTO{
		State:       state,
		Code:        code,
		RedirectUri: "http://localhost/callback",
	})
	require.NoError(t, err)

	assert.Equal(t, connector.ID, claims.ConnectorID)
	assert.Equal(t, "user-123", claims.Subject)
	assert.Equal(t, "person@example.com", claims.Email)
	assert.Equal(t, "Test Person", claims.Name)
}

func Test_HandleCallback_WrongAudience_Rejected(t *testing.T) {
	// A token for another client ID must be refused, so one connector's tokens
	// can't be replayed against another.
	fake := newFakeOIDCServer(t, "some-other-client-id")
	connector := setUpConnectorWithClientID(t, fake, "the-real-client-id")

	code := fake.IssueCode("user-456", "person@example.com", "Test Person")
	state, err := fake.signStateForTest(connector.ID)
	require.NoError(t, err)

	_, err = GetConnectorService().HandleCallback(&sso_dto.SSOCallbackRequestDTO{
		State:       state,
		Code:        code,
		RedirectUri: "http://localhost/callback",
	})
	assert.Error(t, err)
}

func setUpConnectorWithClientID(t *testing.T, fake *fakeOIDCServer, connectorClientID string) *sso_models.Connector {
	t.Helper()

	response, err := GetConnectorService().CreateConnector(&sso_dto.CreateConnectorRequestDTO{
		Name:         "Test Connector",
		ProviderType: sso_models.ProviderTypeGenericOIDC,
		IssuerURL:    fake.Server.URL,
		ClientID:     connectorClientID,
		ClientSecret: "fake-client-secret",
		ButtonLabel:  "Continue with Test",
		Enabled:      true,
	}, adminUserForTest())
	require.NoError(t, err)

	connector, err := connectorRepository.GetByID(response.ID)
	require.NoError(t, err)
	return connector
}

func Test_HandleCallback_UnknownState_Rejected(t *testing.T) {
	fake := newFakeOIDCServer(t, "test-client-id")
	setUpConnector(t, fake)

	_, err := GetConnectorService().HandleCallback(&sso_dto.SSOCallbackRequestDTO{
		State:       "garbage",
		Code:        "irrelevant",
		RedirectUri: "http://localhost/callback",
	})
	assert.Error(t, err)
}

func Test_HandleCallback_DisabledConnector_Rejected(t *testing.T) {
	fake := newFakeOIDCServer(t, "test-client-id")
	connector := setUpConnector(t, fake)

	connector.Enabled = false
	require.NoError(t, connectorRepository.Update(connector))

	code := fake.IssueCode("user-789", "person@example.com", "Test Person")
	state, err := fake.signStateForTest(connector.ID)
	require.NoError(t, err)

	_, err = GetConnectorService().HandleCallback(&sso_dto.SSOCallbackRequestDTO{
		State:       state,
		Code:        code,
		RedirectUri: "http://localhost/callback",
	})
	assert.ErrorIs(t, err, ErrConnectorDisabled)
}

func setUpConnectorWithGroupsClaim(t *testing.T, fake *fakeOIDCServer, groupsClaim string) *sso_models.Connector {
	t.Helper()
	return setUpConnectorOfType(t, fake, sso_models.ProviderTypeCognito, groupsClaim)
}

func setUpConnectorOfType(
	t *testing.T, fake *fakeOIDCServer, providerType sso_models.ProviderType, groupsClaim string,
) *sso_models.Connector {
	t.Helper()

	response, err := GetConnectorService().CreateConnector(&sso_dto.CreateConnectorRequestDTO{
		Name:         "Test Connector",
		ProviderType: providerType,
		IssuerURL:    fake.Server.URL,
		ClientID:     fake.ClientID,
		ClientSecret: "fake-client-secret",
		GroupsClaim:  groupsClaim,
		ButtonLabel:  "Continue with Test",
		Enabled:      true,
	}, adminUserForTest())
	require.NoError(t, err)

	connector, err := connectorRepository.GetByID(response.ID)
	require.NoError(t, err)
	return connector
}

func callbackClaims(t *testing.T, fake *fakeOIDCServer, connector *sso_models.Connector, code string) *IdentityClaims {
	t.Helper()

	state, err := fake.signStateForTest(connector.ID)
	require.NoError(t, err)

	claims, err := GetConnectorService().HandleCallback(&sso_dto.SSOCallbackRequestDTO{
		State:       state,
		Code:        code,
		RedirectUri: "http://localhost/callback",
	})
	require.NoError(t, err)
	return claims
}

func Test_HandleCallback_GroupsClaimConfigured_ReturnsDeduplicatedGroups(t *testing.T) {
	fake := newFakeOIDCServer(t, "test-client-id")
	connector := setUpConnectorWithGroupsClaim(t, fake, "cognito:groups")

	code := fake.IssueCode("user-g1", "g1@example.com", "G One")
	fake.SetExtraClaim(code, "cognito:groups", []string{"admins", "viewers", "admins"})

	claims := callbackClaims(t, fake, connector, code)

	assert.True(t, claims.GroupsSyncEnabled)
	assert.Equal(t, []string{"admins", "viewers"}, claims.Groups)
}

func Test_HandleCallback_GroupsClaimConfiguredButAbsent_MeansNoGroups(t *testing.T) {
	// Cognito omits cognito:groups entirely for a user in no group; that
	// must read as "no groups" so their group-granted access is removed.
	fake := newFakeOIDCServer(t, "test-client-id")
	connector := setUpConnectorWithGroupsClaim(t, fake, "cognito:groups")

	code := fake.IssueCode("user-g2", "g2@example.com", "G Two")

	claims := callbackClaims(t, fake, connector, code)

	assert.True(t, claims.GroupsSyncEnabled)
	assert.Empty(t, claims.Groups)
}

func Test_HandleCallback_GroupsClaimNotConfigured_SyncDisabled(t *testing.T) {
	fake := newFakeOIDCServer(t, "test-client-id")
	connector := setUpConnectorWithGroupsClaim(t, fake, "")

	code := fake.IssueCode("user-g3", "g3@example.com", "G Three")
	fake.SetExtraClaim(code, "cognito:groups", []string{"admins"})

	claims := callbackClaims(t, fake, connector, code)

	assert.False(t, claims.GroupsSyncEnabled)
	assert.Empty(t, claims.Groups)
}

func Test_extractGroups_VariousShapes(t *testing.T) {
	assert.Equal(t, []string{"a", "b"}, extractGroups([]any{"a", "b", "a", "", 5}))
	assert.Equal(t, []string{"a", "b"}, extractGroups("[a, b]"))
	assert.Equal(t, []string{"a"}, extractGroups("a"))
	assert.Empty(t, extractGroups(nil))
	assert.Empty(t, extractGroups(42))
}

// fakeGraph serves Microsoft Graph's getMemberObjects, records the calls it
// gets, and points the package at itself for the test's duration.
type fakeGraph struct {
	calls         atomic.Int32
	lastAuthorize atomic.Value
}

func newFakeGraph(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, serverURL string)) *fakeGraph {
	t.Helper()

	graph := &fakeGraph{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		graph.calls.Add(1)
		graph.lastAuthorize.Store(r.Header.Get("Authorization"))
		handler(w, r, server.URL)
	}))
	previous := entraGraphMemberObjectsURL
	entraGraphMemberObjectsURL = server.URL + "/v1.0/me/getMemberObjects"
	t.Cleanup(func() {
		entraGraphMemberObjectsURL = previous
		server.Close()
	})
	return graph
}

func issueOverageCode(fake *fakeOIDCServer, subject string) string {
	code := fake.IssueCode(subject, subject+"@example.com", "Overage User")
	fake.SetExtraClaim(code, "_claim_names", map[string]string{"groups": "src1"})
	fake.SetExtraClaim(
		code,
		"_claim_sources",
		map[string]any{"src1": map[string]string{"endpoint": "https://example.invalid"}},
	)
	return code
}

func Test_HandleCallback_EntraGroupsClaimWithObjectIDs_ReturnedAsIs(t *testing.T) {
	fake := newFakeOIDCServer(t, "test-client-id")
	connector := setUpConnectorOfType(t, fake, sso_models.ProviderTypeEntraID, "groups")

	code := fake.IssueCode("entra-1", "entra1@example.com", "Entra One")
	fake.SetExtraClaim(code, "groups", []string{"11111111-1111-1111-1111-111111111111"})

	claims := callbackClaims(t, fake, connector, code)

	assert.Equal(t, []string{"11111111-1111-1111-1111-111111111111"}, claims.Groups)
	assert.False(t, claims.GroupsUnknown)
}

func Test_HandleCallback_EntraGroupOverage_ResolvedFromGraphAcrossPages(t *testing.T) {
	fake := newFakeOIDCServer(t, "test-client-id")
	connector := setUpConnectorOfType(t, fake, sso_models.ProviderTypeEntraID, "groups")
	graph := newFakeGraph(t, func(w http.ResponseWriter, r *http.Request, serverURL string) {
		if r.URL.Query().Get("page") == "2" {
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []string{"group-c"}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"value":           []string{"group-a", "group-b"},
			"@odata.nextLink": serverURL + "/v1.0/me/getMemberObjects?page=2",
		})
	})

	claims := callbackClaims(t, fake, connector, issueOverageCode(fake, "entra-2"))

	assert.Equal(t, []string{"group-a", "group-b", "group-c"}, claims.Groups)
	assert.False(t, claims.GroupsUnknown)
	assert.Equal(t, int32(2), graph.calls.Load())
	assert.Equal(t, "Bearer fake-access-token", graph.lastAuthorize.Load())
}

func Test_HandleCallback_EntraGroupOverageGraphFails_GroupsUnknownSoSyncChangesNothing(t *testing.T) {
	fake := newFakeOIDCServer(t, "test-client-id")
	connector := setUpConnectorOfType(t, fake, sso_models.ProviderTypeEntraID, "groups")
	newFakeGraph(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.WriteHeader(http.StatusForbidden)
	})

	claims := callbackClaims(t, fake, connector, issueOverageCode(fake, "entra-3"))

	assert.True(t, claims.GroupsSyncEnabled)
	assert.True(t, claims.GroupsUnknown)
	assert.Empty(t, claims.Groups)
}

func Test_HandleCallback_OverageClaimOnNonEntraProvider_GraphNeverCalled(t *testing.T) {
	fake := newFakeOIDCServer(t, "test-client-id")
	connector := setUpConnectorOfType(t, fake, sso_models.ProviderTypeGenericOIDC, "groups")
	graph := newFakeGraph(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		_ = json.NewEncoder(w).Encode(map[string]any{"value": []string{"should-not-be-used"}})
	})

	claims := callbackClaims(t, fake, connector, issueOverageCode(fake, "generic-1"))

	assert.Empty(t, claims.Groups)
	assert.False(t, claims.GroupsUnknown)
	assert.Equal(t, int32(0), graph.calls.Load())
}

func Test_HandleCallback_EmailVerifiedClaimShapes(t *testing.T) {
	cases := []struct {
		name         string
		providerType sso_models.ProviderType
		claims       map[string]any
		wantVerified bool
	}{
		{"boolean true", sso_models.ProviderTypeCognito, map[string]any{"email_verified": true}, true},
		{"boolean false", sso_models.ProviderTypeCognito, map[string]any{"email_verified": false}, false},
		{"string true", sso_models.ProviderTypeGenericOIDC, map[string]any{"email_verified": "true"}, true},
		{"string false", sso_models.ProviderTypeGenericOIDC, map[string]any{"email_verified": "false"}, false},
		{"claim absent", sso_models.ProviderTypeGenericOIDC, map[string]any{"email_verified": nil}, false},
		{
			"entra domain owner verified",
			sso_models.ProviderTypeEntraID,
			map[string]any{"email_verified": nil, "xms_edov": true},
			true,
		},
		{
			"xms_edov ignored outside entra",
			sso_models.ProviderTypeGenericOIDC,
			map[string]any{"email_verified": false, "xms_edov": true},
			false,
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeOIDCServer(t, "test-client-id")
			connector := setUpConnectorOfType(t, fake, tc.providerType, "")
			code := fake.IssueCode("ev-"+string(rune('a'+i)), "ev@example.com", "EV")
			for key, value := range tc.claims {
				fake.SetExtraClaim(code, key, value)
			}

			claims := callbackClaims(t, fake, connector, code)

			assert.Equal(t, tc.wantVerified, claims.EmailVerified)
			assert.True(t, claims.RequireVerifiedEmail, "new connectors require a verified email by default")
		})
	}
}

// A refused code is redeemed exactly once and the provider's reason is reported
// (x/oauth2's "auto" mode used to retry and surface only invalid_grant).
func Test_HandleCallback_RefusedCode_RedeemedOnceWithProviderReason(t *testing.T) {
	fake := newFakeOIDCServer(t, "test-client-id")
	connector := setUpConnector(t, fake)
	state, err := fake.signStateForTest(connector.ID)
	require.NoError(t, err)

	_, err = GetConnectorService().HandleCallback(&sso_dto.SSOCallbackRequestDTO{
		State: state, Code: "not-a-real-code", RedirectUri: "http://localhost/callback",
	})
	require.Error(t, err)
	assert.Equal(t, 1, fake.tokenRequests, "the code must be sent to the token endpoint only once")
	assert.Contains(t, err.Error(), "invalid_grant (Invalid authorization code)")
}

// The client secret goes in a Basic header by default (OIDC's default),
// and in the form body when discovery lists only client_secret_post.
func Test_HandleCallback_ClientAuthFollowsDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name      string
		methods   []string
		wantBasic bool
	}{
		{"no methods advertised", nil, true},
		{"basic and post (Cognito)", []string{"client_secret_basic", "client_secret_post"}, true},
		{"post only", []string{"client_secret_post"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeOIDCServer(t, "test-client-id")
			fake.authMethods = tc.methods
			connector := setUpConnector(t, fake)
			code := fake.IssueCode("user-auth-style", "auth-style@example.com", "Auth Style")
			state, err := fake.signStateForTest(connector.ID)
			require.NoError(t, err)

			_, err = GetConnectorService().HandleCallback(&sso_dto.SSOCallbackRequestDTO{
				State: state, Code: code, RedirectUri: "http://localhost/callback",
			})
			require.NoError(t, err)
			assert.Equal(t, 1, fake.tokenRequests)
			assert.Equal(t, tc.wantBasic, fake.lastUsedBasic)
			assert.Equal(t, !tc.wantBasic, fake.lastBodySecret != "")
		})
	}
}
