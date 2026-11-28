package api_keys

import (
	"testing"

	projects_controllers "obzly/internal/features/projects/controllers"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	test_utils "obzly/internal/util/testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_ResolveProjectFromApiKey_WhenDatabaseFails_ReturnsErrorAndCachesNothing(t *testing.T) {
	users_testing.CleanupPlans()
	router := CreateApiKeyTestRouter(projects_controllers.GetProjectController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectViaAPI("Key Lookup Outage", owner, router)
	apiKey := CreateTestApiKey("outage", project.ID, owner.Token, router)
	service := GetApiKeyService()
	service.apiKeyCacheUtil.Invalidate(service.hashToken(apiKey.Token))

	stop := test_utils.FailQueriesOn(t, "api_keys")
	_, err := service.ResolveProjectFromApiKey(apiKey.Token)
	require.ErrorIs(t, err, ErrLookupUnavailable)
	stop()

	// The failure was not cached as "invalid": the key works at once.
	resolved, err := service.ResolveProjectFromApiKey(apiKey.Token)
	require.NoError(t, err)
	assert.True(t, resolved.IsValid)
	assert.Equal(t, project.ID, resolved.ProjectID)
}

func Test_ResolveProjectFromApiKey_WhenKeyUnknown_IsInvalidWithoutError(t *testing.T) {
	resolved, err := GetApiKeyService().ResolveProjectFromApiKey(TokenPrefix + "unknown0000000000000000000000000")
	require.NoError(t, err)
	assert.False(t, resolved.IsValid)
}

// While Valkey is unreachable a key this replica resolved lately still works
// from its own memory, even with the database down too; one it never saw is
// a retryable error, not invalid.
func Test_ResolveProjectFromApiKey_WhenValkeyAndDatabaseFail_KnownKeyStillResolves(t *testing.T) {
	users_testing.CleanupPlans()
	router := CreateApiKeyTestRouter(projects_controllers.GetProjectController())
	owner := users_testing.CreateTestUser(users_enums.UserRoleMember)
	project, _ := projects_testing.CreateTestProjectViaAPI("Key Lookup Valkey Outage", owner, router)
	known := CreateTestApiKey("known", project.ID, owner.Token, router)
	unseen := CreateTestApiKey("unseen", project.ID, owner.Token, router)
	service := GetApiKeyService()
	resolved, err := service.ResolveProjectFromApiKey(known.Token)
	require.NoError(t, err)
	require.True(t, resolved.IsValid)

	test_utils.ValkeyUnreachable(t)
	test_utils.FailQueriesOn(t, "api_keys")

	resolved, err = service.ResolveProjectFromApiKey(known.Token)
	require.NoError(t, err)
	assert.True(t, resolved.IsValid)
	assert.Equal(t, project.ID, resolved.ProjectID)

	_, err = service.ResolveProjectFromApiKey(unseen.Token)
	require.ErrorIs(t, err, ErrLookupUnavailable)
}
