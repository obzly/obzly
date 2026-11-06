package users_services

import (
	"testing"
	"time"

	"obzly/internal/config"
	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	users_repositories "obzly/internal/features/users/repositories"
	sso_models "obzly/internal/features/users/sso/models"
	sso_repositories "obzly/internal/features/users/sso/repositories"
	sso_services "obzly/internal/features/users/sso/services"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_ExchangeCentralAssertion_JITProvisionsNewMemberUser(t *testing.T) {
	cp := newFakeControlPlane(t)
	env := cp.Env("site-a")

	assertion := cp.Mint(t, "site-a", centralAssertionOptions{
		SiteRole: "ADMIN", // needs SOME access - ADMIN is the simplest to grant without a real project id
	})

	response, err := GetUserService().exchangeCentralAssertion(env, assertion, "203.0.113.1")
	require.NoError(t, err)
	assert.NotEmpty(t, response.Token)
	assert.NotEqual(t, uuid.Nil, response.UserID)

	claims := parseUnverified(t, response.Token)
	assert.Equal(t, AuthOriginControlPlane, claims[AuthOriginClaim])
	assert.Equal(t, string(users_enums.UserRoleAdmin), claims["role"])

	user, err := (&users_repositories.UserRepository{}).GetUserByID(response.UserID)
	require.NoError(t, err)
	assert.True(t, user.CentralAdmin)
	assert.Equal(t, users_enums.UserRoleAdmin, user.Role)
}

func Test_ExchangeCentralAssertion_SecondExchangeReusesLinkedIdentity(t *testing.T) {
	cp := newFakeControlPlane(t)
	env := cp.Env("site-a")
	subject := "cp-subject-" + uuid.NewString()

	first := cp.Mint(t, "site-a", centralAssertionOptions{Subject: subject, SiteRole: "ADMIN"})
	resp1, err := GetUserService().exchangeCentralAssertion(env, first, "")
	require.NoError(t, err)

	second := cp.Mint(t, "site-a", centralAssertionOptions{Subject: subject, SiteRole: "ADMIN"})
	resp2, err := GetUserService().exchangeCentralAssertion(env, second, "")
	require.NoError(t, err)

	assert.Equal(t, resp1.UserID, resp2.UserID, "same subject must resolve to the same local user")
}

func Test_ExchangeCentralAssertion_LinksExistingAccountByVerifiedEmail(t *testing.T) {
	cp := newFakeControlPlane(t)
	env := cp.Env("site-a")

	email := "existing-" + uuid.NewString() + "@example.com"
	existing := &users_models.User{
		ID:                   uuid.New(),
		Email:                email,
		Name:                 "Existing User",
		PasswordCreationTime: time.Now().UTC(),
		Role:                 users_enums.UserRoleMember,
		Status:               users_enums.UserStatusActive,
		CreatedAt:            time.Now().UTC(),
	}
	require.NoError(t, (&users_repositories.UserRepository{}).CreateUser(existing))

	assertion := cp.Mint(t, "site-a", centralAssertionOptions{Email: email, SiteRole: "ADMIN"})
	response, err := GetUserService().exchangeCentralAssertion(env, assertion, "")
	require.NoError(t, err)
	assert.Equal(t, existing.ID, response.UserID)
}

func Test_ExchangeCentralAssertion_RootAdminEmail_Forbidden(t *testing.T) {
	cp := newFakeControlPlane(t)
	env := cp.Env("site-a")

	repo := &users_repositories.UserRepository{}
	var root *users_models.User
	// Under test_utils.RootAdminLock, like every other test that creates or
	// retires the root admin.
	require.NoError(t, test_utils.RunExclusiveGlobal(test_utils.RootAdminLock, func() error {
		var err error
		root, err = repo.GetRootAdmin()
		if err != nil || root != nil {
			return err
		}
		// The app creates the root admin on startup, which never runs under go test.
		if _, err = GetUserService().CreateInitialAdminForTest(); err != nil {
			return err
		}
		root, err = repo.GetRootAdmin()
		return err
	}))
	require.NotNil(t, root)

	assertion := cp.Mint(t, "site-a", centralAssertionOptions{Email: root.Email, SiteRole: "ADMIN"})
	_, err := GetUserService().exchangeCentralAssertion(env, assertion, "")
	require.ErrorIs(t, err, ErrCentralExchangeAccountNotLinkable)
}

func Test_ExchangeCentralAssertion_NoGrants_Forbidden(t *testing.T) {
	cp := newFakeControlPlane(t)
	env := cp.Env("site-a")

	assertion := cp.Mint(t, "site-a", centralAssertionOptions{SiteRole: "MEMBER"}) // no projects, not ADMIN
	_, err := GetUserService().exchangeCentralAssertion(env, assertion, "")
	require.ErrorIs(t, err, ErrCentralExchangeForbidden)
}

func Test_ExchangeCentralAssertion_WrongAudience_Rejected(t *testing.T) {
	cp := newFakeControlPlane(t)
	env := cp.Env("site-a")

	assertion := cp.Mint(t, "site-a", centralAssertionOptions{SiteRole: "ADMIN", Audience: "site-b"})
	_, err := GetUserService().exchangeCentralAssertion(env, assertion, "")
	require.Error(t, err)
}

// A Site's organization plan is signed with the same keys and issued to
// the Site; it must never pass as a sign-in.
func Test_ExchangeCentralAssertion_PlanDocument_Rejected(t *testing.T) {
	cp := newFakeControlPlane(t)
	env := cp.Env("site-a")

	assertion := cp.Mint(t, "site-a", centralAssertionOptions{SiteRole: "ADMIN", TokenUse: "site_plan"})
	_, err := GetUserService().exchangeCentralAssertion(env, assertion, "")
	require.ErrorContains(t, err, "not a sign-in assertion")
}

func Test_ExchangeCentralAssertion_WrongIssuer_Rejected(t *testing.T) {
	cp := newFakeControlPlane(t)
	env := cp.Env("site-a")

	assertion := cp.Mint(
		t,
		"site-a",
		centralAssertionOptions{SiteRole: "ADMIN", Issuer: "https://not-the-real-cp.example.com"},
	)
	_, err := GetUserService().exchangeCentralAssertion(env, assertion, "")
	require.Error(t, err)
}

func Test_ExchangeCentralAssertion_Expired_Rejected(t *testing.T) {
	cp := newFakeControlPlane(t)
	env := cp.Env("site-a")

	assertion := cp.Mint(t, "site-a", centralAssertionOptions{
		SiteRole: "ADMIN",
		IssuedAt: time.Now().Add(-time.Hour),
		Expiry:   time.Now().Add(-time.Minute),
	})
	_, err := GetUserService().exchangeCentralAssertion(env, assertion, "")
	require.Error(t, err)
}

func Test_ExchangeCentralAssertion_ReplayedJTI_Rejected(t *testing.T) {
	cp := newFakeControlPlane(t)
	env := cp.Env("site-a")
	jti := "jti-" + uuid.NewString()

	assertion := cp.Mint(t, "site-a", centralAssertionOptions{SiteRole: "ADMIN", JTI: jti})
	_, err := GetUserService().exchangeCentralAssertion(env, assertion, "")
	require.NoError(t, err)

	replay := cp.Mint(t, "site-a", centralAssertionOptions{SiteRole: "ADMIN", JTI: jti})
	_, err = GetUserService().exchangeCentralAssertion(env, replay, "")
	require.ErrorIs(t, err, ErrCentralExchangeReplayed)
}

func Test_ExchangeCentralAssertion_ExchangeDisabled_WhenNoIssuerConfigured(t *testing.T) {
	cp := newFakeControlPlane(t)
	assertion := cp.Mint(t, "site-a", centralAssertionOptions{SiteRole: "ADMIN"})

	_, err := GetUserService().exchangeCentralAssertion(config.EnvVariables{SiteID: "site-a"}, assertion, "")
	require.ErrorIs(t, err, ErrCentralExchangeDisabled)
}

func Test_ExchangeCentralAssertion_DeactivatedUser_Rejected(t *testing.T) {
	cp := newFakeControlPlane(t)
	env := cp.Env("site-a")
	subject := "cp-subject-" + uuid.NewString()

	first := cp.Mint(t, "site-a", centralAssertionOptions{Subject: subject, SiteRole: "ADMIN"})
	resp, err := GetUserService().exchangeCentralAssertion(env, first, "")
	require.NoError(t, err)

	require.NoError(
		t,
		(&users_repositories.UserRepository{}).UpdateUserStatus(resp.UserID, users_enums.UserStatusInactive),
	)

	second := cp.Mint(t, "site-a", centralAssertionOptions{Subject: subject, SiteRole: "ADMIN"})
	_, err = GetUserService().exchangeCentralAssertion(env, second, "")
	require.Error(t, err)
}

func Test_ExchangeCentralAssertion_EgressCIDRRejection(t *testing.T) {
	cp := newFakeControlPlane(t)
	env := cp.Env("site-a")
	env.ControlPlaneEgressCIDRs = "10.0.0.0/8"

	assertion := cp.Mint(t, "site-a", centralAssertionOptions{SiteRole: "ADMIN"})
	_, err := GetUserService().exchangeCentralAssertion(env, assertion, "203.0.113.1")
	require.Error(t, err)

	// A caller IP inside the allowed range succeeds.
	assertion2 := cp.Mint(t, "site-a", centralAssertionOptions{SiteRole: "ADMIN"})
	_, err = GetUserService().exchangeCentralAssertion(env, assertion2, "10.1.2.3")
	require.NoError(t, err)
}

func Test_ExchangeCentralAssertion_RevokesCentralAdminWhenNoLongerGranted(t *testing.T) {
	cp := newFakeControlPlane(t)
	env := cp.Env("site-a")
	subject := "cp-subject-" + uuid.NewString()

	// First exchange grants ADMIN.
	adminAssertion := cp.Mint(t, "site-a", centralAssertionOptions{Subject: subject, SiteRole: "ADMIN"})
	resp, err := GetUserService().exchangeCentralAssertion(env, adminAssertion, "")
	require.NoError(t, err)

	// Create a second admin so the last-admin guard doesn't block the demotion.
	otherAdmin := &users_models.User{
		ID: uuid.New(), Email: "other-admin-" + uuid.NewString() + "@example.com",
		Name: "Other Admin", PasswordCreationTime: time.Now().UTC(),
		Role: users_enums.UserRoleAdmin, Status: users_enums.UserStatusActive, CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, (&users_repositories.UserRepository{}).CreateUser(otherAdmin))

	// Second exchange for the same subject grants a project role instead
	// of ADMIN - central_admin must be revoked.
	projectID := uuid.New().String()
	memberAssertion := cp.Mint(t, "site-a", centralAssertionOptions{
		Subject: subject, SiteRole: "MEMBER",
		Projects: []centralAssertionProject{{ID: projectID, Role: "PROJECT_VIEWER"}},
	})
	_, err = GetUserService().exchangeCentralAssertion(env, memberAssertion, "")
	require.NoError(t, err)

	user, err := (&users_repositories.UserRepository{}).GetUserByID(resp.UserID)
	require.NoError(t, err)
	assert.False(t, user.CentralAdmin)
	assert.Equal(t, users_enums.UserRoleMember, user.Role)
}

// A valid signature isn't enough: a day-long assertion is refused even though
// go-oidc's exp check would accept it.
func Test_ExchangeCentralAssertion_LongLivedAssertion_Rejected(t *testing.T) {
	cp := newFakeControlPlane(t)
	env := cp.Env("site-a")

	assertion := cp.Mint(t, "site-a", centralAssertionOptions{
		SiteRole: "ADMIN", IssuedAt: time.Now(), Expiry: time.Now().Add(24 * time.Hour),
	})
	_, err := GetUserService().exchangeCentralAssertion(env, assertion, "")
	require.Error(t, err)
}

// A central user with only a project grant whose email matches a local admin
// must not be linked into that admin; an ADMIN assertion may be.
func Test_ExchangeCentralAssertion_LocalAdminByEmail_RequiresAdminAssertion(t *testing.T) {
	cp := newFakeControlPlane(t)
	env := cp.Env("site-a")

	// Password-less: an account with its own sign-in is never linked at all
	// (see Test_ExchangeCentralAssertion_AccountWithOwnSignIn_NeverLinkedByEmail).
	email := "local-admin-" + uuid.NewString() + "@example.com"
	localAdmin := &users_models.User{
		ID: uuid.New(), Email: email, Name: "Local Admin",
		PasswordCreationTime: time.Now().UTC(), Role: users_enums.UserRoleAdmin,
		Status: users_enums.UserStatusActive, CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, (&users_repositories.UserRepository{}).CreateUser(localAdmin))

	viewer := cp.Mint(t, "site-a", centralAssertionOptions{
		Email: email, SiteRole: "MEMBER",
		Projects: []centralAssertionProject{{ID: uuid.NewString(), Role: "PROJECT_VIEWER"}},
	})
	_, err := GetUserService().exchangeCentralAssertion(env, viewer, "")
	require.ErrorIs(t, err, ErrCentralExchangeAccountNotLinkable)

	admin := cp.Mint(t, "site-a", centralAssertionOptions{Email: email, SiteRole: "ADMIN"})
	resp, err := GetUserService().exchangeCentralAssertion(env, admin, "")
	require.NoError(t, err)
	assert.Equal(t, localAdmin.ID, resp.UserID)
}

// The central email is unverified: an account with its own sign-in must never be
// linked by it. A password-less local account (an invite) still links.
func Test_ExchangeCentralAssertion_AccountWithOwnSignIn_NeverLinkedByEmail(t *testing.T) {
	cp := newFakeControlPlane(t)
	env := cp.Env("site-a")
	repo := &users_repositories.UserRepository{}
	hashed := "$2a$10$test"
	githubID := "gh-" + uuid.NewString()

	newLocalUser := func(prefix string, password, github *string) *users_models.User {
		user := &users_models.User{
			ID: uuid.New(), Email: prefix + "-" + uuid.NewString() + "@example.com", Name: "Local Member",
			HashedPassword: password, GitHubOAuthID: github, PasswordCreationTime: time.Now().UTC(),
			Role: users_enums.UserRoleMember, Status: users_enums.UserStatusActive, CreatedAt: time.Now().UTC(),
		}
		require.NoError(t, repo.CreateUser(user))
		return user
	}
	withPassword := newLocalUser("pw", &hashed, nil)
	withGitHub := newLocalUser("gh", nil, &githubID)
	withSSO := newLocalUser("sso", nil, nil)
	connector := &sso_models.Connector{
		ID: uuid.New(), Name: "Contoso IdP", ProviderType: sso_models.ProviderTypeGenericOIDC,
		IssuerURL: "https://idp.example.com", ClientID: "client", Scopes: "openid", ButtonLabel: "Contoso",
	}
	require.NoError(t, (&sso_repositories.ConnectorRepository{}).Create(connector))
	require.NoError(t, sso_services.GetIdentityRepository().LinkIdentity(withSSO.ID, connector.ID, "idp-sub"))

	for _, victim := range []*users_models.User{withPassword, withGitHub, withSSO} {
		for _, siteRole := range []string{"MEMBER", "ADMIN"} {
			assertion := cp.Mint(t, "site-a", centralAssertionOptions{
				Email: victim.Email, SiteRole: siteRole,
				Projects: []centralAssertionProject{{ID: uuid.NewString(), Role: "PROJECT_VIEWER"}},
			})
			_, err := GetUserService().exchangeCentralAssertion(env, assertion, "")
			require.ErrorIs(t, err, ErrCentralExchangeAccountNotLinkable, "%s as %s", victim.Email, siteRole)
		}
	}

	passwordless := newLocalUser("plain", nil, nil)
	assertion := cp.Mint(t, "site-a", centralAssertionOptions{
		Email: passwordless.Email, SiteRole: "MEMBER",
		Projects: []centralAssertionProject{{ID: uuid.NewString(), Role: "PROJECT_VIEWER"}},
	})
	resp, err := GetUserService().exchangeCentralAssertion(env, assertion, "")
	require.NoError(t, err)
	assert.Equal(t, passwordless.ID, resp.UserID)
}

// The replay guard must outlive the assertion: a jti forgotten before the
// assertion stops verifying could be replayed.
func Test_AssertionJTITTL_CoversExpiryPlusSkew(t *testing.T) {
	now := time.Now()
	claims := &centralAssertionClaims{Expiry: now.Add(5 * time.Minute).Unix()}
	assert.GreaterOrEqual(t, assertionJTITTL(claims, now), 5*time.Minute+centralAssertionMaxSkew-time.Second)

	shortLived := &centralAssertionClaims{Expiry: now.Add(10 * time.Second).Unix()}
	assert.Equal(t, centralAssertionJTIMinTTL, assertionJTITTL(shortLived, now))
}

// Every exchange comes from the control plane's one IP, so policy refusals must
// never count toward the lockout; only bad signatures and replays do.
func Test_ExchangeCentralAssertion_PolicyRefusalsDoNotTripRateLimit(t *testing.T) {
	cp := newFakeControlPlane(t)
	env := cp.Env("site-a")
	subject := "cp-subject-" + uuid.NewString()
	clientIP := "203.0.113.77"

	for i := 0; i < maxFailedLoginAttempts+2; i++ {
		assertion := cp.Mint(t, "site-a", centralAssertionOptions{Subject: subject, SiteRole: "MEMBER"})
		_, err := GetUserService().exchangeCentralAssertion(env, assertion, clientIP)
		require.ErrorIs(t, err, ErrCentralExchangeForbidden, "attempt %d", i+1)
	}
}

// A Site-wide MEMBER/VIEWER grant alone is enough access and doesn't make the user an admin.
func Test_ExchangeCentralAssertion_AllProjectsRole_IsEnoughAccess(t *testing.T) {
	cp := newFakeControlPlane(t)
	env := cp.Env("site-a")

	for _, role := range []string{"PROJECT_VIEWER", "PROJECT_MEMBER"} {
		assertion := cp.Mint(t, "site-a", centralAssertionOptions{AllProjectsRole: role})
		response, err := GetUserService().exchangeCentralAssertion(env, assertion, "")
		require.NoError(t, err, role)

		user, err := (&users_repositories.UserRepository{}).GetUserByID(response.UserID)
		require.NoError(t, err)
		assert.Equal(t, users_enums.UserRoleMember, user.Role, "a Site-wide %s grant is not Site admin", role)
		assert.False(t, user.CentralAdmin)
	}
}

// A Site-wide PROJECT_ADMIN or OWNER claim is never minted, so it means no access.
func Test_ExchangeCentralAssertion_AllProjectsRole_RejectsElevatedRoles(t *testing.T) {
	cp := newFakeControlPlane(t)
	env := cp.Env("site-a")

	for _, role := range []string{"PROJECT_ADMIN", "PROJECT_OWNER", "ADMIN", "bogus"} {
		assertion := cp.Mint(t, "site-a", centralAssertionOptions{AllProjectsRole: role})
		_, err := GetUserService().exchangeCentralAssertion(env, assertion, "")
		assert.ErrorIs(t, err, ErrCentralExchangeForbidden, role)
	}
}
