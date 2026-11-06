package system_usage

import (
	"net/http"
	"os"
	"testing"
	"time"

	logs_testsupport "obzly/internal/features/logs/testsupport"
	users_enums "obzly/internal/features/users/enums"
	users_middleware "obzly/internal/features/users/middleware"
	users_services "obzly/internal/features/users/services"
	users_testing "obzly/internal/features/users/testing"
	"obzly/internal/storage"
	"obzly/internal/storage/segments"
	test_utils "obzly/internal/util/testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	logs_testsupport.SetupRepository()
	os.Exit(m.Run())
}

func Test_GetUsage_CountsSegmentsByHourAndSignal_AdminOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	protected := router.Group("/api/v1")
	protected.Use(users_middleware.AuthMiddleware(users_services.GetUserService()))
	(&Controller{&Service{}}).RegisterRoutes(protected)

	projectID := uuid.New()
	require.NoError(t, storage.GetDb().Exec("INSERT INTO projects (id, name) VALUES (?, ?)",
		projectID, "usage "+projectID.String()[:8]).Error)
	t.Cleanup(func() {
		storage.GetDb().Exec("DELETE FROM stream_segments WHERE project_id = ?", projectID)
		storage.GetDb().Exec("DELETE FROM projects WHERE id = ?", projectID)
	})
	hour := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Hour)
	repository := &segments.SegmentRepository{}
	for _, record := range []*segments.SegmentRecord{
		{Stream: "logs", RawSizeBytes: 7_000_000, SizeBytes: 700_000, RowCount: 70},
		{Stream: "ai_logs", RawSizeBytes: 3_000_000, SizeBytes: 300_000, RowCount: 30},
		{Stream: "logs_1m", RawSizeBytes: 9_000_000, SizeBytes: 900_000, RowCount: 90},
		{Stream: "spans", RawSizeBytes: 5_000_000, SizeBytes: 500_000, RowCount: 50},
	} {
		record.ProjectID, record.Path = projectID, "usage/"+uuid.NewString()+".parquet"
		record.MinTime, record.MaxTime = hour.Add(time.Minute), hour.Add(10*time.Minute)
		require.NoError(t, repository.Create(record))
	}

	member := users_testing.CreateTestUser(users_enums.UserRoleMember)
	test_utils.MakeGetRequest(t, router, "/api/v1/system/usage", "Bearer "+member.Token, http.StatusForbidden)

	admin := users_testing.CreateTestUser(users_enums.UserRoleAdmin)
	var response Response
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/system/usage", "Bearer "+admin.Token,
		http.StatusOK, &response)
	at := map[string]Hour{}
	for _, item := range response.Hours {
		if item.Hour.Equal(hour) {
			at[item.Signal] = item
		}
	}
	require.GreaterOrEqual(t, at["logs"].RawBytes, int64(10_000_000), "logs and AI logs, not the rollup")
	require.GreaterOrEqual(t, at["traces"].Rows, int64(50))
	signals := map[string]Storage{}
	for _, item := range response.Storage {
		signals[item.Signal] = item
	}
	require.NotContains(t, signals, "logs_1m")
	require.GreaterOrEqual(t, signals["logs"].Bytes, int64(1_000_000))
	require.NotNil(t, signals["logs"].Oldest)
}
