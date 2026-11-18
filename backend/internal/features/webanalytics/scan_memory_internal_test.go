package webanalytics

import (
	"fmt"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	logs_parquetstore "obzly/internal/features/logs/parquetstore"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_Scan_WhenTheSandboxHasAFewTensOfMB_BuildsTheHourWithoutRunningOutOfMemory(t *testing.T) {
	// A worker's sandbox, scaled down: the hour's access logs alone fit easily.
	repository, err := logs_parquetstore.New(t.TempDir(),
		logs_parquetstore.WithQuerierMemoryLimit("64MB"), logs_parquetstore.WithQuerierThreads(2))
	require.NoError(t, err)

	projectID := uuid.New()
	hour := time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour)
	const batches, perBatch = 6, 5000
	statuses := []int{200, 304, 404, 500, 201}
	for batch := range batches {
		items := make([]*logs_core.LogItem, 0, perBatch)
		for n := range perBatch {
			i := batch*perBatch + n
			message := fmt.Sprintf(`198.51.%d.%d - - [18/Nov/2026:10:00:00 +0000] "GET /catalog/%d?page=%d HTTP/1.1" `+
				`%d %d "https://www.example.com/p/%d" "%s" 512 0.012 [shop-web-80] [] 10.0.%d.%d:80 1024 0.01 200 %s`,
				i%256, i%251, i%5000, i%9, statuses[i%len(statuses)], 1000+i%7000, i%997, chromeUA, i%10, i%200,
				uuid.NewString())
			items = append(items, &logs_core.LogItem{ID: uuid.New(), ProjectID: projectID,
				Timestamp: hour.Add(time.Duration(i) * (time.Hour / (batches * perBatch))), Level: logs_core.LogLevelInfo,
				Message: message, Fields: map[string]any{
					"k8s_cluster_name": "prd", "k8s_namespace_name": "ingress-nginx",
					"k8s_daemonset_name": "ingress-nginx-controller", "k8s_pod_name": fmt.Sprintf("ingress-%d", i%7),
					"k8s_node_name": fmt.Sprintf("node-%d", i%13), "k8s_pod_uid": uuid.NewString(), "stream": "stdout",
				}})
		}
		require.NoError(t, repository.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: items}))
		require.NoError(t, repository.ForceFlush())
	}

	result, err := runScan(repository, scanRequest{projectID: projectID, from: hour, to: hour.Add(time.Hour),
		keysFrom: hour, bucket: time.Hour, options: scanOptions{ipMode: ipModeTruncate,
			staticExtensions: defaultStaticExtensions}, timeout: 5 * time.Minute})
	require.NoError(t, err)
	result.finish(nil, true)

	ingress := totalsFor(t, result, "k8s\nprd\ningress-nginx\ndaemonset\ningress-nginx-controller")
	assert.Equal(t, int64(batches*perBatch), ingress.requests)
	assert.Equal(t, int64(batches*perBatch/len(statuses)), ingress.notFound)
}
