package users_repositories

import (
	"strings"
	"testing"
	"time"

	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/storage"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clearRootAdminFlag un-flags every root admin so the test starts from none.
// It holds test_utils.RootAdminLock for the rest of the test, since other
// packages recreate the root admin in parallel.
func clearRootAdminFlag(t *testing.T, repo *UserRepository) {
	t.Helper()
	test_utils.AcquireExclusiveGlobalQueue(t, test_utils.RootAdminLock)
	admin, err := repo.GetRootAdmin()
	require.NoError(t, err)
	if admin != nil {
		require.NoError(t, repo.RetireRootAdminForTests("retired-"+uuid.New().String()))
	}
}

func Test_CreateInitialAdmin_WhenNoneExists_CreatesOneWithGivenEmail(t *testing.T) {
	repo := &UserRepository{}
	clearRootAdminFlag(t, repo)

	email := "root-admin-" + uuid.New().String()[:8]
	created, err := repo.CreateInitialAdmin(email, "test-hash")
	require.NoError(t, err)
	assert.True(t, created)

	admin, err := repo.GetRootAdmin()
	require.NoError(t, err)
	require.NotNil(t, admin)
	assert.Equal(t, email, admin.Email)
	assert.True(t, admin.IsRootAdmin)
	assert.Equal(t, users_enums.UserRoleAdmin, admin.Role)
	require.NotNil(t, admin.BootstrapTokenHash)
	assert.Equal(t, "test-hash", *admin.BootstrapTokenHash)
}

func Test_CreateInitialAdmin_WhenOneAlreadyExists_IsANoOp(t *testing.T) {
	repo := &UserRepository{}
	clearRootAdminFlag(t, repo)

	firstEmail := "root-admin-" + uuid.New().String()[:8]
	created, err := repo.CreateInitialAdmin(firstEmail, "test-hash")
	require.NoError(t, err)
	assert.True(t, created)

	// A different email must neither create a second root admin nor rename it.
	created, err = repo.CreateInitialAdmin("some-other-email", "test-hash-2")
	require.NoError(t, err)
	assert.False(t, created)

	admin, err := repo.GetRootAdmin()
	require.NoError(t, err)
	require.NotNil(t, admin)
	assert.Equal(t, firstEmail, admin.Email)
}

func Test_SyncRootAdminEmail_RenamesWhenDifferent(t *testing.T) {
	repo := &UserRepository{}
	clearRootAdminFlag(t, repo)

	_, err := repo.CreateInitialAdmin("old-"+uuid.New().String()[:8], "test-hash")
	require.NoError(t, err)

	newEmail := "new-" + uuid.New().String()[:8]
	changed, err := repo.SyncRootAdminEmail(newEmail)
	require.NoError(t, err)
	assert.True(t, changed)

	admin, err := repo.GetRootAdmin()
	require.NoError(t, err)
	assert.Equal(t, newEmail, admin.Email)
}

func Test_SyncRootAdminEmail_SameEmail_IsANoOp(t *testing.T) {
	repo := &UserRepository{}
	clearRootAdminFlag(t, repo)

	email := "same-" + uuid.New().String()[:8]
	_, createErr := repo.CreateInitialAdmin(email, "test-hash")
	require.NoError(t, createErr)

	changed, err := repo.SyncRootAdminEmail(email)
	require.NoError(t, err)
	assert.False(t, changed)
}

func Test_SyncRootAdminEmail_BlankEmail_IsANoOp(t *testing.T) {
	repo := &UserRepository{}
	clearRootAdminFlag(t, repo)
	_, err := repo.CreateInitialAdmin("blank-test-"+uuid.New().String()[:8], "test-hash")
	require.NoError(t, err)

	changed, err := repo.SyncRootAdminEmail("")
	require.NoError(t, err)
	assert.False(t, changed)
}

// ADMIN_EMAIL already used by another account must be a clear startup error.
func Test_SyncRootAdminEmail_AlreadyTaken_ReturnsClearError(t *testing.T) {
	repo := &UserRepository{}
	clearRootAdminFlag(t, repo)
	_, err := repo.CreateInitialAdmin("taker-test-"+uuid.New().String()[:8], "test-hash")
	require.NoError(t, err)

	takenEmail := "taken-" + uuid.New().String()[:8] + "@example.com"
	hashedPassword := "$2a$10$test"
	require.NoError(t, repo.CreateUser(&users_models.User{
		ID:                   uuid.New(),
		Email:                takenEmail,
		Name:                 "Someone Else",
		HashedPassword:       &hashedPassword,
		PasswordCreationTime: time.Now().UTC(),
		Role:                 users_enums.UserRoleMember,
		Status:               users_enums.UserStatusActive,
		CreatedAt:            time.Now().UTC(),
	}))

	_, err = repo.SyncRootAdminEmail(takenEmail)
	assert.Error(t, err)
}

// Replicas booting together all see "no root admin"; with the same email, either
// unique constraint may fire first, and the outcome must be one admin and no errors.
func Test_CreateInitialAdmin_ConcurrentReplicas_CreatesExactlyOneAdmin(t *testing.T) {
	repo := &UserRepository{}
	clearRootAdminFlag(t, repo)

	email := "concurrent-admin-" + uuid.New().String()[:8]
	const replicas = 8

	type result struct {
		created bool
		err     error
	}
	results := make(chan result, replicas)
	for i := 0; i < replicas; i++ {
		go func() {
			created, err := repo.CreateInitialAdmin(email, "test-hash")
			results <- result{created, err}
		}()
	}

	createdCount := 0
	for i := 0; i < replicas; i++ {
		r := <-results
		require.NoError(
			t,
			r.err,
			"every concurrent replica's CreateInitialAdmin call must succeed, not just the winner's",
		)
		if r.created {
			createdCount++
		}
	}
	assert.Equal(t, 1, createdCount, "exactly one replica should report having created the admin")

	admin, err := repo.GetRootAdmin()
	require.NoError(t, err)
	require.NotNil(t, admin)
	assert.Equal(t, email, admin.Email)

	var totalAdmins int64
	require.NoError(
		t,
		storage.GetDb().Model(&users_models.User{}).Where("is_root_admin = ?", true).Count(&totalAdmins).Error,
	)
	assert.Equal(
		t,
		int64(1),
		totalAdmins,
		"exactly one root admin row must exist after N concurrent CreateInitialAdmin calls",
	)
}

func newTestUser(email string) *users_models.User {
	hashedPassword := "$2a$10$test"
	return &users_models.User{
		ID:                   uuid.New(),
		Email:                email,
		Name:                 "Test User",
		HashedPassword:       &hashedPassword,
		PasswordCreationTime: time.Now().UTC(),
		Role:                 users_enums.UserRoleMember,
		Status:               users_enums.UserStatusActive,
		CreatedAt:            time.Now().UTC(),
	}
}

func Test_CreateUser_NormalizesEmailToLowercase(t *testing.T) {
	repo := &UserRepository{}
	mixedCase := "MixedCase-" + uuid.New().String()[:8] + "@Example.COM"

	user := newTestUser(mixedCase)
	require.NoError(t, repo.CreateUser(user))

	assert.Equal(
		t,
		strings.ToLower(mixedCase),
		user.Email,
		"CreateUser should normalize the in-memory struct too, not just what's persisted",
	)

	fetched, err := repo.GetUserByEmail(mixedCase)
	require.NoError(t, err)
	require.NotNil(t, fetched)
	assert.Equal(t, strings.ToLower(mixedCase), fetched.Email)
}

// Lookups must cooperate with rows stored in another casing.
func Test_GetUserByEmail_IsCaseInsensitive(t *testing.T) {
	repo := &UserRepository{}
	base := "lookup-" + uuid.New().String()[:8] + "@example.com"

	require.NoError(t, repo.CreateUser(newTestUser(base)))

	for _, variant := range []string{
		strings.ToUpper(base),
		strings.ToUpper(base[:1]) + base[1:],
		"  " + base + "  ",
	} {
		fetched, err := repo.GetUserByEmail(variant)
		require.NoError(t, err, "variant %q", variant)
		require.NotNil(t, fetched, "variant %q should still resolve to the stored user", variant)
		assert.Equal(t, strings.ToLower(base), fetched.Email)
	}
}

// idx_users_email_lower must reject a case-only duplicate even without the
// application's normalization.
func Test_CreateUser_DuplicateEmailDifferentCase_RejectedByUniqueConstraint(t *testing.T) {
	repo := &UserRepository{}
	base := "dupe-" + uuid.New().String()[:8] + "@example.com"

	require.NoError(t, repo.CreateUser(newTestUser(base)))

	// Bypass CreateUser's normalization so the constraint itself is what's tested.
	second := newTestUser(strings.ToUpper(base))
	err := storage.GetDb().Create(second).Error
	require.Error(t, err, "a case-variant duplicate email must be rejected at the database level")
}

// Two requests racing with the same TOTP code: only the first may consume
// its step.
func Test_UpdateMFALastUsedStep_SameStepTwice_SecondIsRefused(t *testing.T) {
	repo := &UserRepository{}
	user := &users_models.User{
		ID: uuid.New(), Email: "totp-race-" + uuid.NewString() + "@example.com", Name: "TOTP",
		PasswordCreationTime: time.Now().UTC(), Role: users_enums.UserRoleMember,
		Status: users_enums.UserStatusActive, CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, repo.CreateUser(user))

	consumed, err := repo.UpdateMFALastUsedStep(user.ID, 1000)
	require.NoError(t, err)
	assert.True(t, consumed)

	consumed, err = repo.UpdateMFALastUsedStep(user.ID, 1000)
	require.NoError(t, err)
	assert.False(t, consumed, "the same step can't be consumed twice")

	consumed, err = repo.UpdateMFALastUsedStep(user.ID, 999)
	require.NoError(t, err)
	assert.False(t, consumed, "nor an older one")
}
