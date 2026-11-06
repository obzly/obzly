package logs_receiving_tests

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"testing"
	"time"

	"obzly/internal/broker"
	logs_core "obzly/internal/features/logs/core"
	logs_jetstream "obzly/internal/features/logs/jetstream"
	logs_parquetstore "obzly/internal/features/logs/parquetstore"
	logs_receiving "obzly/internal/features/logs/receiving"
	projects_testing "obzly/internal/features/projects/testing"
	users_enums "obzly/internal/features/users/enums"
	users_testing "obzly/internal/features/users/testing"
	"obzly/internal/ingest"
	"obzly/internal/ingest/ingesttest"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func Test_SubmitLogs_WithJetStream_LogsAreStoredOnlyAfterAWorkerWritesThem(t *testing.T) {
	test_utils.UseIsolatedSegmentWriter(t)
	client := ingesttest.StartBroker(t, 64<<20)
	router := CreateLogsTestRouter()
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.NewString()
	project := projects_testing.CreateTestProject("JetStream "+uniqueID[:8], user, router)

	var response logs_receiving.SubmitLogsResponseDTO
	test_utils.MakePostRequestAndUnmarshal(t, router,
		fmt.Sprintf("/api/v1/logs/receiving/%s", project.ID), "",
		&logs_receiving.SubmitLogsRequestDTO{Logs: CreateValidLogItems(5, uniqueID)},
		http.StatusAccepted, &response)
	require.Equal(t, 5, response.Accepted)

	require.EqualValues(
		t,
		1,
		ingesttest.StreamMessages(t, client, broker.SignalLogs),
		"the accepted batch is held by JetStream",
	)
	require.Zero(t, storedLogs(t, project.ID), "nothing is written before a worker consumes it")

	logsWriter, err := logs_core.GetLogCoreRepository().(*logs_parquetstore.LogParquetStore).NewDurableWriter()
	require.NoError(t, err)
	aiWriter, err := logs_core.GetAIRepository().(*logs_parquetstore.LogParquetStore).NewDurableWriter()
	require.NoError(t, err)
	ingesttest.StartConsumer(t, client, broker.SignalLogs,
		logs_jetstream.NewSink(logsWriter, aiWriter, 64<<20, ingesttest.MaxAge, ingesttest.Logger()))

	require.Eventually(t, func() bool {
		return storedLogs(t, project.ID) == 5 && ingesttest.StreamMessages(t, client, broker.SignalLogs) == 0
	}, 20*time.Second, 100*time.Millisecond, "the worker writes the rows, then acks the batch")
}

func Test_SubmitLogs_WhenIngestBufferFull_Returns429WithRetryAfter(t *testing.T) {
	ingesttest.StartBroker(t, 2048)
	router := CreateLogsTestRouter()
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.NewString()
	project := projects_testing.CreateTestProject("JetStream full "+uniqueID[:8], user, router)

	var last *test_utils.TestResponse
	for i := 0; i < 20; i++ {
		last = test_utils.MakeRequest(t, router, test_utils.RequestOptions{
			Method: http.MethodPost,
			URL:    fmt.Sprintf("/api/v1/logs/receiving/%s", project.ID),
			Body:   &logs_receiving.SubmitLogsRequestDTO{Logs: CreateValidLogItems(5, uniqueID)},
		})
		if last.StatusCode != http.StatusAccepted {
			break
		}
	}

	require.Equal(t, http.StatusTooManyRequests, last.StatusCode)
	require.Contains(t, string(last.Body), logs_core.ErrorIngestBufferFull)
	require.Equal(t, strconv.Itoa(ingest.RetryAfterSeconds), last.Headers.Get("Retry-After"))
}

func Test_SubmitLogs_WhenBrokerIsDown_Returns503WithRetryAfter(t *testing.T) {
	srv, err := broker.StartServer(broker.ServerConfig{Name: "down", StoreDir: t.TempDir(), InProcess: true},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	client, err := broker.ConnectInProcess(srv, "test")
	require.NoError(t, err)
	require.NoError(t, client.EnsureIngestStreams(context.Background(),
		broker.StreamSettings{Replicas: 1, MaxBytes: 64 << 20}))
	broker.SetDefault(client)
	t.Cleanup(broker.ShutdownDefault)
	router := CreateLogsTestRouter()
	user := users_testing.CreateTestUser(users_enums.UserRoleMember)
	uniqueID := uuid.NewString()
	project := projects_testing.CreateTestProject("JetStream down "+uniqueID[:8], user, router)
	srv.Shutdown()
	srv.WaitForShutdown()

	response := test_utils.MakeRequest(t, router, test_utils.RequestOptions{
		Method: http.MethodPost,
		URL:    fmt.Sprintf("/api/v1/logs/receiving/%s", project.ID),
		Body:   &logs_receiving.SubmitLogsRequestDTO{Logs: CreateValidLogItems(2, uniqueID)},
	})

	require.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
	require.Contains(t, string(response.Body), logs_core.ErrorIngestUnavailable)
	require.Equal(t, strconv.Itoa(ingest.RetryAfterSeconds), response.Headers.Get("Retry-After"))
}

func storedLogs(t *testing.T, projectID uuid.UUID) int64 {
	t.Helper()
	stats, err := logs_core.GetLogCoreRepository().GetProjectLogStats(projectID)
	require.NoError(t, err)
	return stats.TotalLogs
}
