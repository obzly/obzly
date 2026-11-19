package users_controllers

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	users_dto "obzly/internal/features/users/dto"
	users_repositories "obzly/internal/features/users/repositories"
	sso_dto "obzly/internal/features/users/sso/dto"
	sso_models "obzly/internal/features/users/sso/models"
	sso_repositories "obzly/internal/features/users/sso/repositories"
	sso_services "obzly/internal/features/users/sso/services"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func connectorRequest(fake *fakeOIDCServer) sso_dto.CreateConnectorRequestDTO {
	return sso_dto.CreateConnectorRequestDTO{
		Name:         "Cognito",
		ProviderType: sso_models.ProviderTypeCognito,
		IssuerURL:    fake.Server.URL,
		ClientID:     fake.ClientID,
		ClientSecret: "shh",
		GroupsClaim:  "cognito:groups",
		ButtonLabel:  "Continue with Cognito",
		Enabled:      true,
	}
}

func updateRequestFrom(create sso_dto.CreateConnectorRequestDTO) sso_dto.UpdateConnectorRequestDTO {
	return sso_dto.UpdateConnectorRequestDTO{
		Name:         create.Name,
		ProviderType: create.ProviderType,
		IssuerURL:    create.IssuerURL,
		ClientID:     create.ClientID,
		GroupsClaim:  create.GroupsClaim,
		ButtonLabel:  create.ButtonLabel,
		Enabled:      create.Enabled,
	}
}

func createConnectorWithMode(
	t *testing.T, router *gin.Engine, fake *fakeOIDCServer, mode sso_models.MFATrustMode,
) sso_dto.ConnectorResponseDTO {
	t.Helper()

	admin := users_testing.ReacreateInitAdminAndGetAccess()
	request := connectorRequest(fake)
	request.MFATrust = &mode
	var connector sso_dto.ConnectorResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/sso/connectors", "Bearer "+admin.Token, request, http.StatusOK, &connector,
	)
	return connector
}

// Every mode against ID tokens with and without a second factor in amr, for
// a group-promoted Admin (whose MFA is otherwise mandatory).
func Test_SSOCallback_MFATrustModes_SkipLocalMFAOnlyWhenTrustIsSatisfied(t *testing.T) {
	cases := []struct {
		mode            sso_models.MFATrustMode
		amr             []string
		skipsLocalMFA   bool
		notReportedFlag bool
	}{
		{sso_models.MFATrustOff, []string{"pwd", "mfa"}, false, false},
		{sso_models.MFATrustOff, nil, false, true},
		{sso_models.MFATrustAMR, []string{"pwd", "mfa"}, true, false},
		{sso_models.MFATrustAMR, []string{"email_otp"}, true, false},
		{sso_models.MFATrustAMR, []string{"pwd"}, false, true},
		{sso_models.MFATrustAMR, nil, false, true},
		{sso_models.MFATrustAlways, []string{"fed"}, true, false},
		{sso_models.MFATrustAlways, nil, true, false},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s/amr=%s", tc.mode, strings.Join(tc.amr, "+")), func(t *testing.T) {
			router := createSSOTestRouter()
			fake := newFakeOIDCServer(t, "client-1")
			fake.amr = tc.amr
			connector := createConnectorWithMode(t, router, fake, tc.mode)
			require.Equal(t, tc.mode, connector.MFATrust)
			mapGroupToGlobalAdmin(t, connector.ID, "obzly-admins")
			email := "sso-mode-" + uuid.New().String()[:8] + "@example.com"
			subject := "mode-" + uuid.NewString()[:8]

			response := ssoSignIn(t, router, fake, connector.ID, subject, email, []string{"obzly-admins"})

			if tc.skipsLocalMFA {
				require.NotEmpty(t, response.Token, "trust is satisfied, so the Admin gets a session")
				assert.Empty(t, response.MFAToken)
				assert.False(t, response.MFASetupRequired)
				test_utils.MakeGetRequest(t, createMFAGateRouter(), "/api/v1/probe",
					"Bearer "+response.Token, http.StatusOK)
			} else {
				assert.Empty(t, response.Token)
				assert.True(t, response.MFASetupRequired)
				assert.NotEmpty(t, response.MFAToken)
			}
			assert.Equal(t, tc.notReportedFlag, response.IdPMFANotReported)
		})
	}
}

// "always" trusts the provider, not the account: a password sign-in to the
// same Admin is still sent to local MFA setup, and a local session is gated.
func Test_SSOCallback_AlwaysTrust_PasswordSignInStillRequiresLocalMFA(t *testing.T) {
	router := createSSOTestRouter()
	fake := newFakeOIDCServer(t, "client-1")
	fake.amr = []string{"fed"}
	connector := createConnectorWithMode(t, router, fake, sso_models.MFATrustAlways)
	mapGroupToGlobalAdmin(t, connector.ID, "obzly-admins")
	email := "sso-always-" + uuid.New().String()[:8] + "@example.com"

	sso := ssoSignIn(t, router, fake, connector.ID, "always-"+uuid.NewString()[:8], email,
		[]string{"obzly-admins"})
	require.NotEmpty(t, sso.Token)

	users := &users_repositories.UserRepository{}
	user, err := users.GetUserByEmail(email)
	require.NoError(t, err)
	require.False(t, user.MFAEnabled)
	hashed, err := bcrypt.GenerateFromPassword([]byte("testpassword123"), bcrypt.MinCost)
	require.NoError(t, err)
	require.NoError(t, users.UpdateUserPassword(user.ID, string(hashed)))

	var signIn users_dto.SignInResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/users/signin", "",
		users_dto.SignInRequestDTO{Email: email, Password: "testpassword123"}, http.StatusOK, &signIn)
	assert.Empty(t, signIn.Token)
	assert.True(t, signIn.MFASetupRequired)
	assert.NotEmpty(t, signIn.MFAToken)
}

func Test_CreateConnector_WithInvalidMFATrust_ReturnsBadRequest(t *testing.T) {
	router := createSSOTestRouter()
	fake := newFakeOIDCServer(t, "client-1")
	admin := users_testing.ReacreateInitAdminAndGetAccess()

	for _, value := range []string{"sometimes", "", "ALWAYS"} {
		request := connectorRequest(fake)
		mode := sso_models.MFATrustMode(value)
		request.MFATrust = &mode
		response := test_utils.MakePostRequest(t, router, "/api/v1/sso/connectors", "Bearer "+admin.Token,
			request, http.StatusBadRequest)
		assert.Contains(t, string(response.Body), "invalid mfaTrust", value)
	}
}

func Test_UpdateConnector_WithInvalidMFATrust_ReturnsBadRequestAndKeepsMode(t *testing.T) {
	router := createSSOTestRouter()
	fake := newFakeOIDCServer(t, "client-1")
	connector := createConnectorWithMode(t, router, fake, sso_models.MFATrustAMR)
	admin := users_testing.ReacreateInitAdminAndGetAccess()

	update := updateRequestFrom(connectorRequest(fake))
	bad := sso_models.MFATrustMode("everyone")
	update.MFATrust = &bad
	test_utils.MakePutRequest(t, router, "/api/v1/sso/connectors/"+connector.ID.String(), "Bearer "+admin.Token,
		update, http.StatusBadRequest)

	stored, err := (&sso_repositories.ConnectorRepository{}).GetByID(connector.ID)
	require.NoError(t, err)
	assert.Equal(t, sso_models.MFATrustAMR, stored.MFATrust)
}

// Clients that only know trustIdpMfa keep working: true means "amr" on
// create, and sending true back for an "always" connector doesn't weaken it.
func Test_Connector_LegacyTrustIdPMFA_MapsToMFATrust(t *testing.T) {
	router := createSSOTestRouter()
	fake := newFakeOIDCServer(t, "client-1")
	trust := true
	connector := createCognitoConnectorWithMFATrust(t, router, fake, &trust)
	assert.Equal(t, sso_models.MFATrustAMR, connector.MFATrust)
	assert.True(t, connector.TrustIdPMFA)

	admin := users_testing.ReacreateInitAdminAndGetAccess()
	url := "/api/v1/sso/connectors/" + connector.ID.String()
	put := func(mfaTrust *sso_models.MFATrustMode, trustIdPMFA *bool) sso_dto.ConnectorResponseDTO {
		update := updateRequestFrom(connectorRequest(fake))
		update.MFATrust = mfaTrust
		update.TrustIdPMFA = trustIdPMFA
		var response sso_dto.ConnectorResponseDTO
		test_utils.MakePutRequestAndUnmarshal(t, router, url, "Bearer "+admin.Token, update, http.StatusOK, &response)
		return response
	}
	repo := &sso_repositories.ConnectorRepository{}

	always := sso_models.MFATrustAlways
	updated := put(&always, nil)
	assert.Equal(t, sso_models.MFATrustAlways, updated.MFATrust)
	assert.True(t, updated.TrustIdPMFA, "legacy field reports any trust as true")

	updated = put(nil, &trust)
	assert.Equal(t, sso_models.MFATrustAlways, updated.MFATrust, "legacy true keeps always")

	updated = put(nil, nil)
	assert.Equal(t, sso_models.MFATrustAlways, updated.MFATrust, "omitted keeps the current mode")

	off := false
	updated = put(nil, &off)
	assert.Equal(t, sso_models.MFATrustOff, updated.MFATrust)
	assert.False(t, updated.TrustIdPMFA)
	stored, err := repo.GetByID(connector.ID)
	require.NoError(t, err)
	assert.False(t, stored.TrustIdPMFA, "legacy column follows for older pods")

	// mfaTrust wins when both are sent.
	updated = put(&always, &off)
	assert.Equal(t, sso_models.MFATrustAlways, updated.MFATrust)
	stored, err = repo.GetByID(connector.ID)
	require.NoError(t, err)
	assert.True(t, stored.TrustIdPMFA)
}

type recordingAuditLogWriter struct {
	mu       sync.Mutex
	messages []string
}

func (r *recordingAuditLogWriter) WriteAuditLog(message string, _ *uuid.UUID, _ *uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, message)
}

func Test_UpdateConnector_ChangingMFATrust_WritesAuditLog(t *testing.T) {
	router := createSSOTestRouter()
	fake := newFakeOIDCServer(t, "client-1")
	recorder := &recordingAuditLogWriter{}
	sso_services.GetConnectorService().SetAuditLogWriter(recorder)
	defer sso_services.GetConnectorService().SetAuditLogWriter(&AuditLogWriterStub{})

	connector := createConnectorWithMode(t, router, fake, sso_models.MFATrustOff)
	admin := users_testing.ReacreateInitAdminAndGetAccess()
	update := updateRequestFrom(connectorRequest(fake))
	always := sso_models.MFATrustAlways
	update.MFATrust = &always
	test_utils.MakePutRequest(t, router, "/api/v1/sso/connectors/"+connector.ID.String(), "Bearer "+admin.Token,
		update, http.StatusOK)

	assert.Contains(t, recorder.messages, "SSO connector Cognito: provider MFA trust changed from off to always")
}
