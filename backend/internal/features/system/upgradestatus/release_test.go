package system_upgradestatus

import (
	"net/http"
	"testing"

	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	"obzly/internal/storage"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A release records its schema at startup; an older binary then counts
// the releases ahead of it - distinct newer schemas, not release names.
func Test_RecordRelease_OlderBinaryCountsReleasesAhead(t *testing.T) {
	require.NoError(t, RecordRelease())
	router := createTestRouter()
	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	status := func() SchemaStatus {
		var response Response
		test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/system/upgrade-status", "Bearer "+admin.Token,
			http.StatusOK, &response)
		return response.Schema
	}
	schema := status()
	require.Zero(t, schema.ReleasesAhead, "this binary's own release isn't ahead of it")

	tag := uuid.NewString()[:8]
	db := storage.GetDb()
	t.Cleanup(func() { db.Exec("DELETE FROM schema_releases WHERE release LIKE ?", "test-"+tag+"%") })
	insert := func(release string, version int64) {
		require.NoError(t, db.Exec("INSERT INTO schema_releases (release, goose_version) VALUES (?, ?)",
			"test-"+tag+"-"+release, version).Error)
	}
	insert("next", schema.LatestAvailable+1)
	insert("next-patch", schema.LatestAvailable+1) // same schema: still one ahead
	assert.Equal(t, 1, status().ReleasesAhead)
	insert("after-next", schema.LatestAvailable+2)
	assert.Equal(t, 2, status().ReleasesAhead)
}
