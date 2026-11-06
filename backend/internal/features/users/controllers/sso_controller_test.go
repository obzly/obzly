package users_controllers

import (
	"net/http"
	"net/url"
	"testing"

	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	sso_dto "obzly/internal/features/users/sso/dto"
	sso_models "obzly/internal/features/users/sso/models"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -- Admin CRUD -------------------------------------------------------

func Test_CreateConnector_WhenRootAdmin_ConnectorCreated(t *testing.T) {
	router := createSSOTestRouter()
	admin := users_testing.ReacreateInitAdminAndGetAccess()
	fake := newFakeOIDCServer(t, "client-1")

	request := sso_dto.CreateConnectorRequestDTO{
		Name:         "Cognito",
		ProviderType: sso_models.ProviderTypeCognito,
		IssuerURL:    fake.Server.URL,
		ClientID:     fake.ClientID,
		ClientSecret: "shh",
		ButtonLabel:  "Continue with Cognito",
		Enabled:      true,
	}

	var response sso_dto.ConnectorResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/sso/connectors", "Bearer "+admin.Token, request, http.StatusOK, &response,
	)

	assert.Equal(t, "Cognito", response.Name)
	assert.True(t, response.HasClientSecret)
	assert.True(t, response.Enabled)
}

func Test_CreateConnector_WhenNotAdmin_Forbidden(t *testing.T) {
	router := createSSOTestRouter()
	member := users_testing.CreateTestUser(users_enums.UserRoleMember)
	fake := newFakeOIDCServer(t, "client-1")

	request := sso_dto.CreateConnectorRequestDTO{
		Name:         "Cognito",
		ProviderType: sso_models.ProviderTypeCognito,
		IssuerURL:    fake.Server.URL,
		ClientID:     fake.ClientID,
		ClientSecret: "shh",
		ButtonLabel:  "Continue with Cognito",
		Enabled:      true,
	}

	test_utils.MakePostRequest(
		t, router, "/api/v1/sso/connectors", "Bearer "+member.Token, request, http.StatusForbidden,
	)
}

func Test_CreateConnector_WithBadIssuer_ReturnsBadRequest(t *testing.T) {
	router := createSSOTestRouter()
	admin := users_testing.ReacreateInitAdminAndGetAccess()

	request := sso_dto.CreateConnectorRequestDTO{
		Name:         "Broken",
		ProviderType: sso_models.ProviderTypeGenericOIDC,
		IssuerURL:    "http://127.0.0.1:1/does-not-exist",
		ClientID:     "whatever",
		ClientSecret: "shh",
		ButtonLabel:  "Continue",
		Enabled:      true,
	}

	test_utils.MakePostRequest(
		t, router, "/api/v1/sso/connectors", "Bearer "+admin.Token, request, http.StatusBadRequest,
	)
}

func Test_ListUpdateDeleteConnector_FullLifecycle(t *testing.T) {
	router := createSSOTestRouter()
	admin := users_testing.ReacreateInitAdminAndGetAccess()
	fake := newFakeOIDCServer(t, "client-1")

	createRequest := sso_dto.CreateConnectorRequestDTO{
		Name:         "Entra",
		ProviderType: sso_models.ProviderTypeEntraID,
		IssuerURL:    fake.Server.URL,
		ClientID:     fake.ClientID,
		ClientSecret: "shh",
		ButtonLabel:  "Continue with Entra",
		Enabled:      true,
	}
	var created sso_dto.ConnectorResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/sso/connectors", "Bearer "+admin.Token, createRequest, http.StatusOK, &created,
	)

	var listResponse sso_dto.ListConnectorsResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t, router, "/api/v1/sso/connectors", "Bearer "+admin.Token, http.StatusOK, &listResponse,
	)
	found := false
	for _, c := range listResponse.Connectors {
		if c.ID == created.ID {
			found = true
		}
	}
	assert.True(t, found)

	// Disable it and leave the secret blank (kept).
	updateRequest := sso_dto.UpdateConnectorRequestDTO{
		Name:         "Entra Renamed",
		ProviderType: sso_models.ProviderTypeEntraID,
		IssuerURL:    fake.Server.URL,
		ClientID:     fake.ClientID,
		ButtonLabel:  "Continue with Entra",
		Enabled:      false,
	}
	var updated sso_dto.ConnectorResponseDTO
	test_utils.MakePutRequestAndUnmarshal(
		t, router, "/api/v1/sso/connectors/"+created.ID.String(), "Bearer "+admin.Token,
		updateRequest, http.StatusOK, &updated,
	)
	assert.Equal(t, "Entra Renamed", updated.Name)
	assert.False(t, updated.Enabled)
	assert.True(t, updated.HasClientSecret, "blank ClientSecret on update must keep the existing secret")

	// Delete is MFA step-up protected: no code, then a wrong code, are refused.
	deleteURL := "/api/v1/sso/connectors/" + created.ID.String()
	validCode := users_testing.EnableRealMFAForTestUser(admin.UserID, "root-admin")
	test_utils.MakeDeleteRequest(t, router, deleteURL, "Bearer "+admin.Token, http.StatusUnprocessableEntity)
	test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: http.MethodDelete, URL: deleteURL, AuthToken: "Bearer " + admin.Token,
		Body: map[string]string{"mfaCode": "000000"}, ExpectedStatus: http.StatusUnprocessableEntity,
	})
	test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: http.MethodDelete, URL: deleteURL, AuthToken: "Bearer " + admin.Token,
		Body: map[string]string{"mfaCode": validCode}, ExpectedStatus: http.StatusOK,
	})

	var listAfterDelete sso_dto.ListConnectorsResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t, router, "/api/v1/sso/connectors", "Bearer "+admin.Token, http.StatusOK, &listAfterDelete,
	)
	for _, c := range listAfterDelete.Connectors {
		assert.NotEqual(t, created.ID, c.ID)
	}
}

// -- Public list + callback -------------------------------------------

func Test_ListPublicConnectors_OnlyIncludesEnabledConnectors(t *testing.T) {
	router := createSSOTestRouter()
	admin := users_testing.ReacreateInitAdminAndGetAccess()
	fake := newFakeOIDCServer(t, "client-1")

	enabledRequest := sso_dto.CreateConnectorRequestDTO{
		Name:         "Enabled Connector",
		ProviderType: sso_models.ProviderTypeGoogleWorkspace,
		IssuerURL:    fake.Server.URL,
		ClientID:     fake.ClientID,
		ClientSecret: "shh",
		ButtonLabel:  "Continue with Google Workspace",
		Enabled:      true,
	}
	var enabled sso_dto.ConnectorResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/sso/connectors", "Bearer "+admin.Token, enabledRequest, http.StatusOK, &enabled,
	)

	disabledRequest := enabledRequest
	disabledRequest.Name = "Disabled Connector"
	disabledRequest.Enabled = false
	var disabled sso_dto.ConnectorResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/sso/connectors", "Bearer "+admin.Token, disabledRequest, http.StatusOK, &disabled,
	)

	redirectURI := url.QueryEscape("http://localhost:3000/auth/callback")
	var publicList sso_dto.ListPublicConnectorsResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t, router, "/api/v1/auth/sso/connectors?redirectUri="+redirectURI, "", http.StatusOK, &publicList,
	)

	var foundEnabled, foundDisabled bool
	for _, c := range publicList.Connectors {
		if c.ID == enabled.ID {
			foundEnabled = true
			assert.NotEmpty(t, c.AuthorizeURL)
		}
		if c.ID == disabled.ID {
			foundDisabled = true
		}
	}
	assert.True(t, foundEnabled)
	assert.False(t, foundDisabled)
}

func Test_SSOCallback_NewUser_CreatesAndSignsIn(t *testing.T) {
	router := createSSOTestRouter()
	admin := users_testing.ReacreateInitAdminAndGetAccess()
	fake := newFakeOIDCServer(t, "client-1")

	connectorRequest := sso_dto.CreateConnectorRequestDTO{
		Name:         "Test SSO",
		ProviderType: sso_models.ProviderTypeGenericOIDC,
		IssuerURL:    fake.Server.URL,
		ClientID:     fake.ClientID,
		ClientSecret: "shh",
		ButtonLabel:  "Continue with Test SSO",
		Enabled:      true,
	}
	var connector sso_dto.ConnectorResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/sso/connectors", "Bearer "+admin.Token, connectorRequest, http.StatusOK, &connector,
	)

	redirectURI := "http://localhost:3000/auth/callback"
	state := fetchStateForConnector(t, router, connector.ID, redirectURI)

	email := "sso-new-" + uuid.New().String()[:8] + "@example.com"
	code := fake.IssueCode("subject-1", email, "SSO New User")

	callbackRequest := sso_dto.SSOCallbackRequestDTO{
		State:       state,
		Code:        code,
		RedirectUri: redirectURI,
	}
	var callbackResponse users_dto.OAuthCallbackResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/auth/sso/callback", "", callbackRequest, http.StatusOK, &callbackResponse,
	)

	assert.NotEmpty(t, callbackResponse.Token)
	assert.Equal(t, email, callbackResponse.Email)
	assert.True(t, callbackResponse.IsNewUser)
}

// An email match to a password account must not be auto-linked.
func Test_SSOCallback_ExistingPasswordAccount_RefusesAutoLink(t *testing.T) {
	router := createSSOTestRouter()
	rootAdmin := users_testing.ReacreateInitAdminAndGetAccess()
	fake := newFakeOIDCServer(t, "client-1")

	email := "sso-existing-" + uuid.New().String()[:8] + "@example.com"
	signupRequest := users_dto.SignUpRequestDTO{
		Email:    email,
		Password: "testpassword123",
		Name:     "Existing SSO User",
	}
	test_utils.MakePostRequest(t, router, "/api/v1/users/signup", "", signupRequest, http.StatusOK)

	connectorRequest := sso_dto.CreateConnectorRequestDTO{
		Name:         "Test SSO",
		ProviderType: sso_models.ProviderTypeGenericOIDC,
		IssuerURL:    fake.Server.URL,
		ClientID:     fake.ClientID,
		ClientSecret: "shh",
		ButtonLabel:  "Continue with Test SSO",
		Enabled:      true,
	}
	var connector sso_dto.ConnectorResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/sso/connectors", "Bearer "+rootAdmin.Token, connectorRequest, http.StatusOK, &connector,
	)

	redirectURI := "http://localhost:3000/auth/callback"
	state := fetchStateForConnector(t, router, connector.ID, redirectURI)
	code := fake.IssueCode("subject-existing", email, "Existing SSO User")

	callbackRequest := sso_dto.SSOCallbackRequestDTO{
		State:       state,
		Code:        code,
		RedirectUri: redirectURI,
	}
	resp := test_utils.MakePostRequest(
		t, router, "/api/v1/auth/sso/callback", "", callbackRequest, http.StatusBadRequest,
	)
	assert.Contains(t, string(resp.Body), "already exists")
}

func Test_CreateConnector_WhenNonRootAdmin_ReturnsForbidden(t *testing.T) {
	router := createSSOTestRouter()
	nonRootAdmin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	fake := newFakeOIDCServer(t, "client-2")

	connectorRequest := sso_dto.CreateConnectorRequestDTO{
		Name:         "Rogue SSO",
		ProviderType: sso_models.ProviderTypeGenericOIDC,
		IssuerURL:    fake.Server.URL,
		ClientID:     fake.ClientID,
		ClientSecret: "shh",
		ButtonLabel:  "Continue",
		Enabled:      true,
	}
	resp := test_utils.MakePostRequest(
		t, router, "/api/v1/sso/connectors", "Bearer "+nonRootAdmin.Token, connectorRequest, http.StatusForbidden,
	)
	assert.Contains(t, string(resp.Body), "root admin")
}

// A second login with an already-linked identity reuses that account.
func Test_SSOCallback_SameIdentityTwice_ReusesLinkedAccount(t *testing.T) {
	router := createSSOTestRouter()
	rootAdmin := users_testing.ReacreateInitAdminAndGetAccess()
	fake := newFakeOIDCServer(t, "client-3")

	connectorRequest := sso_dto.CreateConnectorRequestDTO{
		Name:         "Test SSO 2",
		ProviderType: sso_models.ProviderTypeGenericOIDC,
		IssuerURL:    fake.Server.URL,
		ClientID:     fake.ClientID,
		ClientSecret: "shh",
		ButtonLabel:  "Continue with Test SSO",
		Enabled:      true,
	}
	var connector sso_dto.ConnectorResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/sso/connectors", "Bearer "+rootAdmin.Token, connectorRequest, http.StatusOK, &connector,
	)

	email := "sso-new-twice-" + uuid.New().String()[:8] + "@example.com"
	redirectURI := "http://localhost:3000/auth/callback"

	state := fetchStateForConnector(t, router, connector.ID, redirectURI)
	code := fake.IssueCode("subject-reused", email, "New SSO User")
	var first users_dto.OAuthCallbackResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/auth/sso/callback", "",
		sso_dto.SSOCallbackRequestDTO{State: state, Code: code, RedirectUri: redirectURI},
		http.StatusOK, &first,
	)
	assert.True(t, first.IsNewUser)

	state2 := fetchStateForConnector(t, router, connector.ID, redirectURI)
	code2 := fake.IssueCode("subject-reused", email, "New SSO User")
	var second users_dto.OAuthCallbackResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/auth/sso/callback", "",
		sso_dto.SSOCallbackRequestDTO{State: state2, Code: code2, RedirectUri: redirectURI},
		http.StatusOK, &second,
	)
	assert.False(t, second.IsNewUser)
	assert.Equal(t, first.UserID, second.UserID)
}

func Test_SSOCallback_WithBadState_ReturnsBadRequest(t *testing.T) {
	router := createSSOTestRouter()

	callbackRequest := sso_dto.SSOCallbackRequestDTO{
		State:       "garbage",
		Code:        "whatever",
		RedirectUri: "http://localhost:3000/auth/callback",
	}
	test_utils.MakePostRequest(t, router, "/api/v1/auth/sso/callback", "", callbackRequest, http.StatusBadRequest)
}

// fetchStateForConnector reads the signed state from the public connector list's
// AuthorizeURL, as the frontend would.
func fetchStateForConnector(
	t *testing.T,
	router *gin.Engine,
	connectorID uuid.UUID,
	redirectURI string,
) string {
	t.Helper()

	var publicList sso_dto.ListPublicConnectorsResponseDTO
	test_utils.MakeGetRequestAndUnmarshal(
		t, router, "/api/v1/auth/sso/connectors?redirectUri="+url.QueryEscape(redirectURI), "",
		http.StatusOK, &publicList,
	)

	for _, c := range publicList.Connectors {
		if c.ID == connectorID {
			parsed, err := url.Parse(c.AuthorizeURL)
			require.NoError(t, err)
			lastAuthorizeNonce = parsed.Query().Get("nonce")
			return parsed.Query().Get("state")
		}
	}

	t.Fatalf("connector %s not found in public connector list", connectorID)
	return ""
}

func Test_DeleteConnector_WhenNonRootAdmin_ReturnsForbidden(t *testing.T) {
	router := createSSOTestRouter()
	root := users_testing.ReacreateInitAdminAndGetAccess()
	fake := newFakeOIDCServer(t, "client-1")
	var created sso_dto.ConnectorResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/sso/connectors", "Bearer "+root.Token, sso_dto.CreateConnectorRequestDTO{
			Name: "Delete guard", ProviderType: sso_models.ProviderTypeGenericOIDC, IssuerURL: fake.Server.URL,
			ClientID: fake.ClientID, ClientSecret: "shh", ButtonLabel: "Continue", Enabled: true,
		}, http.StatusOK, &created,
	)

	nonRootAdmin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	validCode := users_testing.EnableRealMFAForTestUser(nonRootAdmin.UserID, nonRootAdmin.Email)
	test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: http.MethodDelete, URL: "/api/v1/sso/connectors/" + created.ID.String(),
		AuthToken: "Bearer " + nonRootAdmin.Token, Body: map[string]string{"mfaCode": validCode},
		ExpectedStatus: http.StatusForbidden,
	})
}
