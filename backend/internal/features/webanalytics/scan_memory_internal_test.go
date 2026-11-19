package webanalytics

import (
	"fmt"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	logs_parquetstore "obzly/internal/features/logs/parquetstore"
	"obzly/internal/storage/segments"

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

func Test_Scan_WhenAnHourIsMostlyHostLogs_CountsTheAccessLinesExactlyWithinATinySandbox(t *testing.T) {
	// The production failure scaled down: an hour of mostly plain host log
	// lines (and JSON app logs that aren't access logs) with a few access
	// logs, on a sandbox far smaller than the hour's raw rows.
	segments.SetMaxSpillSize("48MB")
	t.Cleanup(func() { segments.SetMaxSpillSize(segments.DefaultMaxSpillSize) })
	repository, err := logs_parquetstore.New(t.TempDir(),
		logs_parquetstore.WithQuerierMemoryLimit("64MB"), logs_parquetstore.WithQuerierThreads(2))
	require.NoError(t, err)

	projectID := uuid.New()
	hour := time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour)
	const batches, perBatch = 10, 24_000
	const total = batches * perBatch
	var fieldsAccess, textAccess, jsonAccess int64
	for batch := range batches {
		items := make([]*logs_core.LogItem, 0, perBatch)
		for n := range perBatch {
			i := batch*perBatch + n
			at := hour.Add(time.Duration(i) * (time.Hour / total))
			hostFields := map[string]any{
				"host_name": fmt.Sprintf("vm-%d", i%40), "log_file_path": "/var/log/app/service.log",
				"log_file_name": "service.log", "os_type": "linux", "service_name": fmt.Sprintf("batch-%d", i%9),
				"process_pid": fmt.Sprintf("%d", 1000+i%5000), "trace_hint": uuid.NewString(),
			}
			var item *logs_core.LogItem
			switch {
			case i%100 == 0:
				fieldsAccess++
				item = accessLog(at, "", nginxRequest(fmt.Sprintf("198.51.100.%d", i%250), "/catalog", "200", "web-1"))
			case i%100 == 1:
				textAccess++
				item = accessLog(at, fmt.Sprintf(`203.0.113.%d - - [18/Nov/2026:10:00:00 +0000] "GET /p/%d HTTP/1.1" `+
					`404 10 "-" "%s"`, i%250, i%50, chromeUA), map[string]any{"k8s_cluster_name": "prd",
					"k8s_namespace_name": "ingress-nginx", "k8s_daemonset_name": "ingress-nginx-controller"})
			case i%100 == 2:
				jsonAccess++
				item = accessLog(at, fmt.Sprintf(`{"status":201,"method":"POST","path":"/api/v1/items/%d",`+
					`"remote_addr":"192.0.2.%d"}`, i, i%250), map[string]any{"service_name": "api", "host_name": "app-1"})
			case i%10 == 3:
				// JSON app logs that aren't access logs, one with a status but no request.
				item = accessLog(at, fmt.Sprintf(`{"level":"info","msg":"job %d done","status":"ok","attempt":%d}`,
					i, i%3), hostFields)
			case i%10 == 4:
				item = accessLog(at, fmt.Sprintf("[worker-%d] %s queue drained after %d ms, next poll in 5 s", i%12,
					uuid.NewString(), i%997), hostFields)
			default:
				item = accessLog(at, fmt.Sprintf("Nov 18 10:%02d:%02d vm-%d systemd[1]: Started session %d of user "+
					"deploy; %s; cgroup /user.slice/user-1000.slice/session-%d.scope; unit state active (running)",
					(i/60)%60, i%60, i%40, i, uuid.NewString(), i), hostFields)
			}
			items = append(items, item)
		}
		require.NoError(t, repository.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: items}))
		require.NoError(t, repository.ForceFlush())
	}

	result, err := runScan(repository, scanRequest{projectID: projectID, from: hour, to: hour.Add(time.Hour),
		keysFrom: hour, bucket: time.Hour, options: scanOptions{ipMode: ipModeTruncate,
			staticExtensions: defaultStaticExtensions}, timeout: 5 * time.Minute})
	require.NoError(t, err)
	result.finish(nil, false)

	assert.Equal(t, fieldsAccess+textAccess+jsonAccess, result.rowsRead, "every access line, nothing else")
	assert.Equal(t, fieldsAccess, totalsFor(t, result, "service\n\nnginx\nweb-1").requests)
	ingress := totalsFor(t, result, "k8s\nprd\ningress-nginx\ndaemonset\ningress-nginx-controller")
	assert.Equal(t, textAccess, ingress.requests)
	assert.Equal(t, textAccess, ingress.notFound)
	assert.Equal(t, jsonAccess, totalsFor(t, result, "service\n\napi\napp-1").s2)
	assert.Contains(t, keysFor(result, "service\n\napi\napp-1")[dimPage], "/api/v1/items/:id")
}
