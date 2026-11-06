package users_controllers

import (
	"net/http"
	"testing"

	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_repositories "obzly/internal/features/users/repositories"
	sso_dto "obzly/internal/features/users/sso/dto"
	sso_models "obzly/internal/features/users/sso/models"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createConnectorWithEmailPolicy(
	t *testing.T, router *gin.Engine, fake *fakeOIDCServer, requireVerified *bool,
) sso_dto.ConnectorResponseDTO {
	t.Helper()

	admin := users_testing.ReacreateInitAdminAndGetAccess()
	var connector sso_dto.ConnectorResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/sso/connectors", "Bearer "+admin.Token,
		sso_dto.CreateConnectorRequestDTO{
			Name:                 "Email Policy",
			ProviderType:         sso_models.ProviderTypeGenericOIDC,
			IssuerURL:            fake.Server.URL,
			ClientID:             fake.ClientID,
			ClientSecret:         "shh",
			RequireVerifiedEmail: requireVerified,
			ButtonLabel:          "Continue",
			Enabled:              true,
		},
		http.StatusOK, &connector,
	)
	return connector
}

// signInWithVerification posts the callback for a token whose email_verified
// claim is set as given, expecting wantStatus.
func signInWithVerification(
	t *testing.T, router *gin.Engine, fake *fakeOIDCServer, connectorID uuid.UUID,
	subject, email string, verified bool, wantStatus int,
) (users_dto.OAuthCallbackResponseDTO, string) {
	t.Helper()

	redirectURI := "http://localhost:3000/auth/callback"
	code := fake.IssueCode(subject, email, "Email Test")
	fake.extra[code]["email_verified"] = verified

	request := sso_dto.SSOCallbackRequestDTO{
		State:       fetchStateForConnector(t, router, connectorID, redirectURI),
		Code:        code,
		RedirectUri: redirectURI,
	}
	if wantStatus != http.StatusOK {
		resp := test_utils.MakePostRequest(t, router, "/api/v1/auth/sso/callback", "", request, wantStatus)
		return users_dto.OAuthCallbackResponseDTO{}, string(resp.Body)
	}

	var response users_dto.OAuthCallbackResponseDTO
	test_utils.MakePostRequestAndUnmarshal(
		t, router, "/api/v1/auth/sso/callback", "", request, http.StatusOK, &response,
	)
	return response, ""
}

func Test_SSOCallback_UnverifiedEmail_CannotCreateAccountWhenVerificationRequired(t *testing.T) {
	router := createSSOTestRouter()
	fake := newFakeOIDCServer(t, "client-1")
	connector := createConnectorWithEmailPolicy(t, router, fake, nil)
	email := "unverified-new-" + uuid.New().String()[:8] + "@example.com"

	_, body := signInWithVerification(t, router, fake, connector.ID, "unv-new", email, false, http.StatusBadRequest)

	assert.Contains(t, body, "not verified")
	created, err := (&users_repositories.UserRepository{}).GetUserByEmail(email)
	require.NoError(t, err)
	assert.Nil(t, created, "no account may be created from an unverified email")
}

func Test_SSOCallback_UnverifiedEmail_CannotLinkToExistingAccount(t *testing.T) {
	router := createSSOTestRouter()
	fake := newFakeOIDCServer(t, "client-1")
	connector := createConnectorWithEmailPolicy(t, router, fake, nil)
	victim := users_testing.CreateTestUser(users_enums.UserRoleMember)

	_, body := signInWithVerification(t, router, fake, connector.ID, "attacker", victim.Email, false, http.StatusBadRequest)

	assert.Contains(t, body, "not verified")
}

func Test_SSOCallback_UnverifiedEmail_AllowedWhenConnectorDoesNotRequireVerification(t *testing.T) {
	router := createSSOTestRouter()
	fake := newFakeOIDCServer(t, "client-1")
	off := false
	connector := createConnectorWithEmailPolicy(t, router, fake, &off)
	email := "unverified-ok-" + uuid.New().String()[:8] + "@example.com"

	response, _ := signInWithVerification(t, router, fake, connector.ID, "unv-ok", email, false, http.StatusOK)

	assert.NotEmpty(t, response.Token)
}

func Test_SSOCallback_AlreadyLinkedIdentity_SignsInEvenIfEmailNowUnverified(t *testing.T) {
	router := createSSOTestRouter()
	fake := newFakeOIDCServer(t, "client-1")
	connector := createConnectorWithEmailPolicy(t, router, fake, nil)
	email := "linked-" + uuid.New().String()[:8] + "@example.com"

	first, _ := signInWithVerification(t, router, fake, connector.ID, "linked-subject", email, true, http.StatusOK)
	require.NotEmpty(t, first.Token)

	second, _ := signInWithVerification(t, router, fake, connector.ID, "linked-subject", email, false, http.StatusOK)

	assert.Equal(t, first.UserID, second.UserID)
	assert.NotEmpty(t, second.Token)
}
