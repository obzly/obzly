package users_services

import (
	users_models "obzly/internal/features/users/models"
	"obzly/internal/storage"
	test_utils "obzly/internal/util/testing"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_ResetBootstrapToken_WhenRootAdminHasNoPassword_ReplacesTheToken(t *testing.T) {
	admin := rootAdminWithoutPassword(t)
	oldHash := *admin.BootstrapTokenHash

	token, err := GetUserService().ResetBootstrapToken()
	require.NoError(t, err)

	reloaded, err := GetUserService().userRepository.GetRootAdmin()
	require.NoError(t, err)
	require.NotNil(t, reloaded.BootstrapTokenHash)
	require.Equal(t, hashBootstrapToken(token), *reloaded.BootstrapTokenHash)
	require.NotEqual(t, oldHash, *reloaded.BootstrapTokenHash)
}

func Test_ResetBootstrapToken_WhenRootAdminHasAPassword_Refuses(t *testing.T) {
	admin := rootAdminWithoutPassword(t)
	hashed := "$2a$10$abcdefghijklmnopqrstuuJmQ3KkB0k3s3o6Y2b1kQvB6m9p1l2Ce"
	require.NoError(t, storage.GetDb().Model(admin).Update("hashed_password", hashed).Error)

	_, err := GetUserService().ResetBootstrapToken()

	require.ErrorIs(t, err, ErrRootAdminAlreadySetUp)
}

// rootAdminWithoutPassword makes sure the shared test database has a root
// admin with no password and a bootstrap token, and restores whatever was
// there afterwards. Holds test_utils.RootAdminLock for the test, like every
// test that changes the root admin.
func rootAdminWithoutPassword(t *testing.T) *users_models.User {
	t.Helper()
	test_utils.AcquireExclusiveGlobalQueue(t, test_utils.RootAdminLock)
	repo := GetUserService().userRepository
	_, err := GetUserService().CreateInitialAdminForTest()
	require.NoError(t, err)
	admin, err := repo.GetRootAdmin()
	require.NoError(t, err)
	require.NotNil(t, admin)

	previousPassword, previousToken := admin.HashedPassword, admin.BootstrapTokenHash
	t.Cleanup(func() {
		require.NoError(t, storage.GetDb().Model(&users_models.User{}).Where("id = ?", admin.ID).
			Updates(map[string]any{"hashed_password": previousPassword, "bootstrap_token_hash": previousToken}).Error)
	})

	placeholder := hashBootstrapToken("placeholder")
	require.NoError(t, storage.GetDb().Model(&users_models.User{}).Where("id = ?", admin.ID).
		Updates(map[string]any{"hashed_password": nil, "bootstrap_token_hash": placeholder}).Error)
	admin.HashedPassword, admin.BootstrapTokenHash = nil, &placeholder
	return admin
}
