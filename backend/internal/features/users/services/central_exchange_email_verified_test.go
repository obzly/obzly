package users_services

import (
	"strings"
	"sync"
	"testing"
	"time"

	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	users_repositories "obzly/internal/features/users/repositories"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

// Regression: a central user who set their own email to a pending invite's
// (the control plane doesn't verify a self-service change) must not activate
// that INVITED account and take its project roles.
func Test_ExchangeCentralAssertion_InvitedUser_UnverifiedCentralEmail_NotActivatedOrLinked(t *testing.T) {
	cp := newFakeControlPlane(t)
	env := cp.Env("site-a")
	repo := &users_repositories.UserRepository{}

	previous := GetUserService().auditLogWriter
	audit := &recordingAuditWriter{}
	GetUserService().SetAuditLogWriter(audit)
	defer GetUserService().SetAuditLogWriter(previous)

	invited := createInvitedUser(t, "contoso-invited-")

	for name, opts := range map[string]centralAssertionOptions{
		"email_verified false":   {EmailUnverified: true},
		"email_verified missing": {OmitEmailVerified: true},
	} {
		t.Run(name, func(t *testing.T) {
			subject := "cp-mallory-" + uuid.NewString()
			opts.Subject, opts.Email, opts.Name = subject, invited.Email, "Mallory"
			opts.AllProjectsRole = "PROJECT_VIEWER"

			_, err := GetUserService().exchangeCentralAssertion(env, cp.Mint(t, "site-a", opts), "")
			require.ErrorIs(t, err, ErrCentralExchangeEmailNotVerified)

			user, err := repo.GetUserByID(invited.ID)
			require.NoError(t, err)
			assert.Equal(t, users_enums.UserStatusInvited, user.Status)
			assert.Equal(t, "User", user.Name)
			assert.NotNil(t, user.InviteTokenHash, "the real invitee can still redeem the invite")

			linked, err := (&users_repositories.CentralIdentityRepository{}).
				GetUserByIdentity(env.ControlPlaneIssuer, subject)
			require.NoError(t, err)
			assert.Nil(t, linked, "no link is left behind for a later attempt")
		})
	}

	assert.True(t, audit.contains("Control-plane sign-in refused"))
	assert.True(t, audit.contains(invited.Email))
}

// An account an older version linked before refusing it as INVITED is not
// activated either unless the control plane vouches for the invited email.
func Test_ExchangeCentralAssertion_InvitedUser_LinkedEarlier_UnverifiedOrOtherEmail_NotActivated(t *testing.T) {
	cp := newFakeControlPlane(t)
	env := cp.Env("site-a")
	repo := &users_repositories.UserRepository{}

	invited := createInvitedUser(t, "contoso-stale-link-")
	subject := "cp-subject-" + uuid.NewString()
	require.NoError(t, (&users_repositories.CentralIdentityRepository{}).
		LinkIdentity(invited.ID, env.ControlPlaneIssuer, subject))

	for name, opts := range map[string]centralAssertionOptions{
		"unverified":             {Email: invited.Email, EmailUnverified: true},
		"verified, other email":  {Email: "fabrikam-" + uuid.NewString() + "@example.com"},
		"claim missing entirely": {Email: invited.Email, OmitEmailVerified: true},
	} {
		t.Run(name, func(t *testing.T) {
			opts.Subject, opts.AllProjectsRole = subject, "PROJECT_VIEWER"
			_, err := GetUserService().exchangeCentralAssertion(env, cp.Mint(t, "site-a", opts), "")
			require.ErrorIs(t, err, ErrCentralExchangeEmailNotVerified)

			user, err := repo.GetUserByID(invited.ID)
			require.NoError(t, err)
			assert.Equal(t, users_enums.UserStatusInvited, user.Status)
		})
	}
}

// The same rule for an ACTIVE account with no sign-in of its own.
func Test_ExchangeCentralAssertion_ActiveAccount_UnverifiedCentralEmail_NotLinked(t *testing.T) {
	cp := newFakeControlPlane(t)
	env := cp.Env("site-a")

	email := "contoso-active-" + uuid.NewString() + "@example.com"
	existing := &users_models.User{
		ID:                   uuid.New(),
		Email:                email,
		Name:                 "Contoso Member",
		PasswordCreationTime: time.Now().UTC(),
		Role:                 users_enums.UserRoleMember,
		Status:               users_enums.UserStatusActive,
		CreatedAt:            time.Now().UTC(),
	}
	require.NoError(t, (&users_repositories.UserRepository{}).CreateUser(existing))

	subject := "cp-subject-" + uuid.NewString()
	assertion := cp.Mint(t, "site-a", centralAssertionOptions{
		Subject: subject, Email: email, EmailUnverified: true, AllProjectsRole: "PROJECT_VIEWER",
	})
	_, err := GetUserService().exchangeCentralAssertion(env, assertion, "")
	require.ErrorIs(t, err, ErrCentralExchangeEmailNotVerified)

	linked, err := (&users_repositories.CentralIdentityRepository{}).GetUserByIdentity(env.ControlPlaneIssuer, subject)
	require.NoError(t, err)
	assert.Nil(t, linked)
}

// An account already linked to one central subject is never joined by a
// second subject that later carries its email, vouched for or not.
func Test_ExchangeCentralAssertion_AccountAlreadyLinkedToAnotherSubject_NotLinkedByEmail(t *testing.T) {
	cp := newFakeControlPlane(t)
	env := cp.Env("site-a")

	email := "contoso-provisioned-" + uuid.NewString() + "@example.com"
	first := cp.Mint(t, "site-a", centralAssertionOptions{
		Subject: "cp-first-" + uuid.NewString(), Email: email, AllProjectsRole: "PROJECT_MEMBER",
	})
	provisioned, err := GetUserService().exchangeCentralAssertion(env, first, "")
	require.NoError(t, err)

	secondSubject := "cp-second-" + uuid.NewString()
	second := cp.Mint(t, "site-a", centralAssertionOptions{
		Subject: secondSubject, Email: email, AllProjectsRole: "PROJECT_VIEWER",
	})
	_, err = GetUserService().exchangeCentralAssertion(env, second, "")
	require.ErrorIs(t, err, ErrCentralExchangeAccountNotLinkable)

	linked, err := (&users_repositories.CentralIdentityRepository{}).
		GetUserByIdentity(env.ControlPlaneIssuer, secondSubject)
	require.NoError(t, err)
	assert.Nil(t, linked)

	user, err := (&users_repositories.UserRepository{}).GetUserByID(provisioned.UserID)
	require.NoError(t, err)
	assert.True(t, user.EmailVerified, "a provisioned account records the control plane's word")
}

// An unverified central email is no obstacle where no local account has it:
// the account is provisioned, and records that its email isn't vouched for.
func Test_ExchangeCentralAssertion_UnverifiedCentralEmail_NoLocalAccount_Provisions(t *testing.T) {
	cp := newFakeControlPlane(t)
	env := cp.Env("site-a")

	assertion := cp.Mint(t, "site-a", centralAssertionOptions{
		Email: "fabrikam-new-" + uuid.NewString() + "@example.com", EmailUnverified: true,
		AllProjectsRole: "PROJECT_VIEWER",
	})
	response, err := GetUserService().exchangeCentralAssertion(env, assertion, "")
	require.NoError(t, err)

	user, err := (&users_repositories.UserRepository{}).GetUserByID(response.UserID)
	require.NoError(t, err)
	assert.Equal(t, users_enums.UserStatusActive, user.Status)
	assert.False(t, user.EmailVerified)
}

func createInvitedUser(t *testing.T, emailPrefix string) *users_models.User {
	t.Helper()
	tokenHash := "hash-" + uuid.NewString()
	expires := time.Now().UTC().Add(time.Hour)
	invited := &users_models.User{
		ID:                   uuid.New(),
		Email:                emailPrefix + uuid.NewString() + "@example.com",
		Name:                 "User",
		PasswordCreationTime: time.Now().UTC(),
		Role:                 users_enums.UserRoleMember,
		Status:               users_enums.UserStatusInvited,
		CreatedAt:            time.Now().UTC(),
		InviteTokenHash:      &tokenHash,
		InviteTokenExpiresAt: &expires,
	}
	require.NoError(t, (&users_repositories.UserRepository{}).CreateUser(invited))
	return invited
}
