package users_controllers

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	users_dto "obzly/internal/features/users/dto"
	users_enums "obzly/internal/features/users/enums"
	users_repositories "obzly/internal/features/users/repositories"
	users_services "obzly/internal/features/users/services"
	sso_dto "obzly/internal/features/users/sso/dto"
	sso_models "obzly/internal/features/users/sso/models"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingAuditWriter keeps every audit message, for tests about what is
// (and isn't) audited.
type recordingAuditWriter struct {
	mu       sync.Mutex
	messages []string
}

func (w *recordingAuditWriter) WriteAuditLog(message string, _ *uuid.UUID, _ *uuid.UUID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.messages = append(w.messages, message)
}

func (w *recordingAuditWriter) contains(substring string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, message := range w.messages {
		if strings.Contains(message, substring) {
			return true
		}
	}
	return false
}

// The lookup ignores case and surrounding spaces, so the lockout counter
// must too - otherwise every variant gets ten fresh guesses.
func Test_SignIn_EmailCaseAndSpaceVariants_ShareOneLockout(t *testing.T) {
	router := createUserTestRouter()
	email := "case-lockout-" + uuid.New().String() + "@example.com"
	test_utils.MakePostRequest(t, router, "/api/v1/users/signup", "",
		users_dto.SignUpRequestDTO{Email: email, Password: "testpassword123", Name: "Case"}, http.StatusOK)

	variants := []string{strings.ToUpper(email), " " + email, "Case-" + email[len("case-"):], email + " "}
	for i := range 9 {
		test_utils.MakePostRequest(t, router, "/api/v1/users/signin", "",
			users_dto.SignInRequestDTO{Email: variants[i%len(variants)], Password: "wrong"}, http.StatusBadRequest)
	}
	test_utils.MakePostRequest(t, router, "/api/v1/users/signin", "",
		users_dto.SignInRequestDTO{Email: strings.ToUpper(email), Password: "wrong"}, http.StatusTooManyRequests)
	test_utils.MakePostRequest(t, router, "/api/v1/users/signin", "",
		users_dto.SignInRequestDTO{Email: email, Password: "testpassword123"}, http.StatusTooManyRequests)
}

func Test_SignIn_WhenAccountLocksOut_WritesAuditLog(t *testing.T) {
	router := createUserTestRouter()
	audit := &recordingAuditWriter{}
	users_services.GetUserService().SetAuditLogWriter(audit)
	defer users_services.GetUserService().SetAuditLogWriter(&AuditLogWriterStub{})

	email := "audited-lockout-" + uuid.New().String() + "@example.com"
	for range 10 {
		test_utils.MakeRequest(t, router, test_utils.RequestOptions{
			Method: "POST", URL: "/api/v1/users/signin",
			Body: users_dto.SignInRequestDTO{Email: email, Password: "wrong"},
		})
	}
	assert.True(t, audit.contains("Sign-in locked"), "lockout audited")
	assert.True(t, audit.contains(email))
}

// With registration closed, an existing email and a new one get the same
// answer, so sign-up can't be used to find out who has an account.
func Test_SignUp_WhenRegistrationClosed_ExistingAndNewEmailsGetSameAnswer(t *testing.T) {
	router := createUserTestRouter()
	existing := "existing-" + uuid.New().String() + "@example.com"
	test_utils.MakePostRequest(t, router, "/api/v1/users/signup", "",
		users_dto.SignUpRequestDTO{Email: existing, Password: "testpassword123", Name: "Existing"}, http.StatusOK)

	users_testing.DisableExternalRegistrations()
	defer users_testing.EnableExternalRegistrations()

	forExisting := test_utils.MakePostRequest(t, router, "/api/v1/users/signup", "",
		users_dto.SignUpRequestDTO{Email: existing, Password: "testpassword123", Name: "Again"}, http.StatusBadRequest)
	forNew := test_utils.MakePostRequest(t, router, "/api/v1/users/signup", "",
		users_dto.SignUpRequestDTO{Email: "new-" + uuid.New().String() + "@example.com", Password: "testpassword123",
			Name: "New"}, http.StatusBadRequest)
	assert.Equal(t, string(forNew.Body), string(forExisting.Body))
}

func Test_SignUp_TooManyFromOneIP_ReturnsTooManyRequests(t *testing.T) {
	router := createUserTestRouter()
	remoteAddr := test_utils.RandomTestRemoteAddr()
	email := "flood-" + uuid.New().String() + "@example.com"

	signUp := func(status int) {
		test_utils.MakeRequest(t, router, test_utils.RequestOptions{
			Method: "POST", URL: "/api/v1/users/signup", RemoteAddr: remoteAddr,
			Body:           users_dto.SignUpRequestDTO{Email: email, Password: "testpassword123", Name: "Flood"},
			ExpectedStatus: status,
		})
	}
	signUp(http.StatusOK)
	for range 19 {
		signUp(http.StatusBadRequest) // already exists - still counted
	}
	signUp(http.StatusTooManyRequests)
}

// Each SSO callback costs an outbound call to the provider, so one source
// can only make so many a minute.
func Test_SSOCallback_TooManyFromOneIP_ReturnsTooManyRequests(t *testing.T) {
	router := createSSOTestRouter()
	remoteAddr := test_utils.RandomTestRemoteAddr()
	callback := func(status int) {
		test_utils.MakeRequest(t, router, test_utils.RequestOptions{
			Method: "POST", URL: "/api/v1/auth/sso/callback", RemoteAddr: remoteAddr,
			Body: sso_dto.SSOCallbackRequestDTO{
				State: "garbage", Code: "whatever", RedirectUri: "http://localhost:3000/auth/callback",
			},
			ExpectedStatus: status,
		})
	}
	for range 120 {
		callback(http.StatusBadRequest)
	}
	callback(http.StatusTooManyRequests)
}

func Test_UpdateUserInfo_EmailChange_MFAEnabled_RequiresCode(t *testing.T) {
	router := createUserTestRouter()
	token, password, userID := signUpAndSignInWithPassword(t, router)
	code := users_testing.EnableRealMFAForTestUser(userID, "irrelevant@example.com")

	newEmail := "mfa-email-" + uuid.New().String() + "@example.com"
	resp := test_utils.MakePutRequest(t, router, "/api/v1/users/me", "Bearer "+token,
		users_dto.UpdateUserInfoRequestDTO{Email: &newEmail, CurrentPassword: password},
		http.StatusUnprocessableEntity)
	assert.Contains(t, string(resp.Body), "MFA code required")

	test_utils.MakePutRequest(t, router, "/api/v1/users/me", "Bearer "+token,
		users_dto.UpdateUserInfoRequestDTO{Email: &newEmail, CurrentPassword: password, MFACode: &code},
		http.StatusOK)
}

// A deactivated user signing in through SSO gets no token, and the audit
// log doesn't claim they signed in.
func Test_SSOCallback_DeactivatedUser_GetsNoTokenAndNoSignInAudit(t *testing.T) {
	router := createSSOTestRouter()
	fake := newFakeOIDCServer(t, "client-1")
	connector := createGenericConnector(t, router, fake)
	email := "sso-deactivated-" + uuid.New().String()[:8] + "@example.com"
	subject := "deactivated-" + uuid.NewString()[:8]

	first := ssoSignIn(t, router, fake, connector.ID, subject, email, nil)
	require.NotEmpty(t, first.Token)
	require.NoError(t, (&users_repositories.UserRepository{}).
		UpdateUserStatus(first.UserID, users_enums.UserStatusInactive))

	audit := &recordingAuditWriter{}
	users_services.GetUserService().SetAuditLogWriter(audit)
	defer users_services.GetUserService().SetAuditLogWriter(&AuditLogWriterStub{})

	redirectURI := "http://localhost:3000/auth/callback"
	resp := test_utils.MakePostRequest(t, router, "/api/v1/auth/sso/callback", "",
		sso_dto.SSOCallbackRequestDTO{
			State: fetchStateForConnector(t, router, connector.ID, redirectURI),
			Code:  fake.IssueCode(subject, email, "Deactivated"), RedirectUri: redirectURI,
		}, http.StatusBadRequest)
	assert.Contains(t, string(resp.Body), "deactivated")
	assert.NotContains(t, string(resp.Body), `"token"`)
	assert.False(t, audit.contains("signed in"))
}

// The authorize URL carries a nonce, and an ID token issued for another
// sign-in (another nonce) is refused.
func Test_SSOCallback_IDTokenWithAnotherNonce_Rejected(t *testing.T) {
	router := createSSOTestRouter()
	fake := newFakeOIDCServer(t, "client-1")
	connector := createGenericConnector(t, router, fake)
	redirectURI := "http://localhost:3000/auth/callback"

	state := fetchStateForConnector(t, router, connector.ID, redirectURI)
	require.NotEmpty(t, lastAuthorizeNonce)
	code := fake.IssueCode("nonce-"+uuid.NewString()[:8], "nonce-"+uuid.NewString()[:8]+"@example.com", "Nonce")
	fake.extra[code]["nonce"] = "issued-for-a-different-sign-in"

	resp := test_utils.MakePostRequest(t, router, "/api/v1/auth/sso/callback", "",
		sso_dto.SSOCallbackRequestDTO{State: state, Code: code, RedirectUri: redirectURI}, http.StatusBadRequest)
	assert.Contains(t, string(resp.Body), "doesn't belong to this sign-in")
}

// Every state is unique, so the SPA's sessionStorage check can tell its
// own sign-in from one an attacker started.
func Test_ListSSOConnectors_StateIsUniquePerRequest(t *testing.T) {
	router := createSSOTestRouter()
	fake := newFakeOIDCServer(t, "client-1")
	connector := createGenericConnector(t, router, fake)
	redirectURI := "http://localhost:3000/auth/callback"

	first := fetchStateForConnector(t, router, connector.ID, redirectURI)
	second := fetchStateForConnector(t, router, connector.ID, redirectURI)
	assert.NotEqual(t, first, second)
}

// The login page sends browsers to the discovered authorize URL, so a
// provider naming a non-https one (javascript:, plain http to a remote
// host) can't be saved.
func Test_CreateConnector_NonHTTPSAuthorizeEndpoint_Rejected(t *testing.T) {
	router := createSSOTestRouter()
	admin := users_testing.ReacreateInitAdminAndGetAccess()

	for _, endpoint := range []string{"javascript:alert(1)", "http://idp.example.com/authorize"} {
		fake := newFakeOIDCServer(t, "client-1")
		fake.authorizeEndpoint = endpoint
		resp := test_utils.MakePostRequest(t, router, "/api/v1/sso/connectors", "Bearer "+admin.Token,
			sso_dto.CreateConnectorRequestDTO{
				Name: "Insecure", ProviderType: sso_models.ProviderTypeGenericOIDC, IssuerURL: fake.Server.URL,
				ClientID: fake.ClientID, ClientSecret: "shh", ButtonLabel: "Continue", Enabled: true,
			}, http.StatusBadRequest)
		assert.Contains(t, string(resp.Body), "https", endpoint)
	}
}

func createGenericConnector(t *testing.T, router *gin.Engine, fake *fakeOIDCServer) sso_dto.ConnectorResponseDTO {
	t.Helper()
	admin := users_testing.ReacreateInitAdminAndGetAccess()
	var connector sso_dto.ConnectorResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/sso/connectors", "Bearer "+admin.Token,
		sso_dto.CreateConnectorRequestDTO{
			Name: "Contoso", ProviderType: sso_models.ProviderTypeGenericOIDC, IssuerURL: fake.Server.URL,
			ClientID: fake.ClientID, ClientSecret: "shh", ButtonLabel: "Continue with Contoso", Enabled: true,
		}, http.StatusOK, &connector)
	return connector
}
