package webanalytics

import (
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const chromeUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) " +
	"Chrome/118.0.0.0 Safari/537.36"

func Test_Scan_ForEachWebServerFormat_MapsFieldsToTheNormalizedSchema(t *testing.T) {
	projectID := uuid.New()
	hour := time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour)
	at := func(minute int) time.Time { return hour.Add(time.Duration(minute) * time.Minute) }
	storeLogs(t, projectID,
		accessLog(at(1), "", map[string]any{"service_name": "nginx", "host_name": "web-1",
			"remote_addr": "198.51.100.23", "method": "GET", "path": "/catalog/42?page=2", "protocol": "HTTP/1.1",
			"status": "200", "body_bytes_sent": "512", "referer": "https://www.google.com/search?q=x",
			"user_agent": chromeUA}),
		accessLog(at(2), "", map[string]any{"service_name": "apache", "host_name": "legacy-01",
			"remote_addr": "198.51.100.91", "method": "GET", "path": "/old", "protocol": "HTTP/1.0",
			"status": "404", "bytes_sent": "-"}),
		accessLog(at(3), "", map[string]any{"obzly_source": "envoy-edge", "host_name": "edge-1",
			"downstream_remote_address": "203.0.113.77:51234", "method": "POST", "path": "/api/v1/cart",
			"response_code": "201", "bytes_sent": "90", "duration": "12", "authority": "api.example.com"}),
		accessLog(at(4), "", map[string]any{"service_name": "traefik", "host_name": "lb-1",
			"ClientHost": "192.0.2.140", "RequestMethod": "GET", "RequestPath": "/static/app.4f2a.js",
			"RequestProtocol": "HTTP/2.0", "DownstreamStatus": "200", "DownstreamContentSize": "4096",
			"Duration": "250000000", "request_User_Agent": chromeUA, "RequestHost": "shop.example.com"}),
		accessLog(at(5), "", map[string]any{"service_name": "iis", "host_name": "win-1", "c_ip": "203.0.113.12",
			"cs_method": "GET", "cs_uri_stem": "/checkout", "sc_status": "500", "time_taken": "1500",
			"cs_user_agent": "Mozilla/5.0+(Windows+NT+10.0)"}),
		accessLog(at(6), "", map[string]any{"service_name": "static-cdn", "service_namespace": "cdn",
			"client_address": "2001:db8:85a3::8a2e:370:7334", "http_request_method": "GET", "url_path": "/",
			"http_response_status_code": "304", "http_response_body_size": "0", "user_agent_original": chromeUA,
			"http_request_header_referer": "https://news.example.net/a", "http_server_request_duration": "0.05",
			"tls_protocol_version": "1.3", "server_address": "static.example.com:443",
			"network_protocol_version": "2"}),
		accessLog(at(7), `203.0.113.7 - - [18/Nov/2026:10:00:00 +0000] "GET /index.html HTTP/1.1" 404 10 "-" `+
			`"curl/8.0" 120 0.015 [shop-web-80] [] 10.0.0.1:80 10 0.014 404 abc`,
			map[string]any{"k8s_cluster_name": "prd", "k8s_namespace_name": "ingress-nginx",
				"k8s_daemonset_name": "ingress-nginx-controller"}),
		accessLog(at(8), `{"status":201,"method":"PUT","path":"/api/v1/items/550e8400-e29b-41d4-a716-446655440000",`+
			`"remote_addr":"192.0.2.10"}`, map[string]any{"service_name": "api", "host_name": "app-1"}),
		// Not access logs: no method or path, not a log line, an impossible status.
		accessLog(at(9), `{"status":"ok"}`, map[string]any{"service_name": "worker"}),
		accessLog(at(10), "hello world", map[string]any{"service_name": "worker", "status": "200"}),
		accessLog(at(11), "", map[string]any{"service_name": "worker", "status": "999", "path": "/x"}),
	)

	result := scanHour(t, projectID, hour, scanOptions{ipMode: ipModeTruncate, excludeBots: true,
		staticExtensions: defaultStaticExtensions})
	result.finish(nil, true)

	nginx := totalsFor(t, result, "service\n\nnginx\nweb-1")
	assert.Equal(t, "combined", decodeFormat(nginx.format).Dialect)
	assert.Equal(t, "remote_addr", decodeFormat(nginx.format).Fields[fieldClient])
	assert.Equal(t, int64(512), nginx.bytes)
	nginxKeys := keysFor(result, "service\n\nnginx\nweb-1")
	assert.Contains(t, nginxKeys[dimPage], "/catalog/:id", "IDs grouped, query string dropped")
	assert.Contains(t, nginxKeys[dimReferrer], "www.google.com")
	assert.Contains(t, nginxKeys[dimBrowser], "Chrome")
	assert.Contains(t, nginxKeys[dimOS], "Windows")
	assert.Contains(t, nginxKeys[dimProtocol], "HTTP/1.1")
	assert.Contains(t, nginxKeys[dimIP], "198.51.100.0/24", "truncated to /24")

	apache := totalsFor(t, result, "service\n\napache\nlegacy-01")
	assert.Equal(t, "common", decodeFormat(apache.format).Dialect)
	assert.Zero(t, apache.with[withReferer], "the common format logs no referrer")
	assert.Zero(t, apache.with[withUA])
	assert.Zero(t, apache.with[withBytes], `"-" bytes is missing, not 0`)
	assert.Equal(t, int64(1), apache.notFound)

	envoy := totalsFor(t, result, "source\nenvoy-edge")
	assert.Equal(t, "JSON (Envoy)", decodeFormat(envoy.format).Dialect)
	assert.Equal(t, int64(1), envoy.hist[4], "12 ms is in the 10-20 ms bucket")
	assert.Contains(t, keysFor(result, "source\nenvoy-edge")[dimIP], "203.0.113.0/24", "port stripped")
	_, envoyHost := result.keys[srcHost{"source\nenvoy-edge", "api.example.com"}]
	assert.True(t, envoyHost, "authority is the virtual host")

	traefikKeys := keysFor(result, "service\n\ntraefik\nlb-1")
	traefik := totalsFor(t, result, "service\n\ntraefik\nlb-1")
	assert.Equal(t, "JSON (Traefik)", decodeFormat(traefik.format).Dialect)
	assert.Equal(t, int64(1), traefik.hist[8], "250 ms (Traefik logs nanoseconds) is in the 200-300 ms bucket")
	assert.Contains(t, traefikKeys[dimStatic], "/static/app.4f2a.js")
	assert.Empty(t, traefikKeys[dimPage], "static files aren't pages")
	assert.Contains(t, traefikKeys[dimProtocol], "HTTP/2")

	iis := totalsFor(t, result, "service\n\niis\nwin-1")
	assert.Equal(t, "W3C (IIS)", decodeFormat(iis.format).Dialect)
	assert.Equal(t, int64(1), iis.s5)
	assert.Equal(t, int64(1), iis.hist[13], "1500 ms is in the 1500-2000 ms bucket")

	cdn := totalsFor(t, result, "service\ncdn\nstatic-cdn\n")
	cdnFormat := decodeFormat(cdn.format)
	assert.Equal(t, "OTel names", cdnFormat.Dialect)
	assert.Equal(t, fieldClient, cdnFormat.Fields[fieldClient])
	assert.Equal(t, int64(1), cdn.s3)
	assert.Equal(t, int64(1), cdn.hist[6], "0.05 s is 50 ms, in the 50-100 ms bucket")
	cdnKeys := result.keys[srcHost{"service\ncdn\nstatic-cdn\n", "static.example.com"}]
	require.NotNil(t, cdnKeys, "server_address without its port is the host")
	assert.Contains(t, cdnKeys[dimTLS], "TLSv1.3")
	assert.Contains(t, cdnKeys[dimIP], "2001:db8:85a3::/48", "IPv6 truncated to /48")
	assert.Contains(t, cdnKeys[dimReferrer], "news.example.net")
	assert.Contains(t, cdnKeys[dimProtocol], "HTTP/2")

	ingress := totalsFor(t, result, "k8s\nprd\ningress-nginx\ndaemonset\ningress-nginx-controller")
	assert.Equal(t, "combined (text)", decodeFormat(ingress.format).Dialect)
	assert.Equal(t, int64(1), ingress.bots, "curl is a crawler/script")
	assert.Equal(t, int64(1), ingress.hist[4], "ingress-nginx's request time, 0.015 s")
	assert.Zero(t, ingress.visitors.estimate(), "crawlers aren't visitors")
	assert.Empty(t, keysFor(result, "k8s\nprd\ningress-nginx\ndaemonset\ningress-nginx-controller")[dimNotFound],
		"crawlers are left out of top lists")
	assert.Contains(t, keysFor(result, "k8s\nprd\ningress-nginx\ndaemonset\ningress-nginx-controller")[dimStatus],
		"404", "but still count in status codes")

	api := totalsFor(t, result, "service\n\napi\napp-1")
	assert.Equal(t, "JSON in message", decodeFormat(api.format).Dialect)
	assert.Contains(t, keysFor(result, "service\n\napi\napp-1")[dimPage], "/api/v1/items/:id")

	for key := range result.totals {
		assert.NotContains(t, key.src, "worker", "logs without access-log fields don't count")
	}
}

func Test_Scan_SourceIdentity_FirstMatchWins(t *testing.T) {
	projectID := uuid.New()
	hour := time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour)
	request := map[string]any{"method": "GET", "path": "/", "status": "200", "remote_addr": "192.0.2.1"}
	with := func(attributes map[string]any) map[string]any {
		fields := map[string]any{}
		for k, v := range request {
			fields[k] = v
		}
		for k, v := range attributes {
			fields[k] = v
		}
		return fields
	}
	storeLogs(t, projectID,
		accessLog(hour.Add(time.Minute), "", with(map[string]any{"obzly_source": "edge", "k8s_namespace_name": "web",
			"k8s_deployment_name": "nginx", "service_name": "nginx", "host_name": "node-1"})),
		accessLog(hour.Add(2*time.Minute), "", with(map[string]any{"k8s_cluster_name": "prd",
			"k8s_namespace_name": "web", "k8s_deployment_name": "nginx", "service_name": "nginx", "host_name": "node-2"})),
		accessLog(hour.Add(3*time.Minute), "", with(map[string]any{"k8s_namespace_name": "web",
			"service_name": "nginx", "service_namespace": "shop", "host_name": "node-3"})),
		accessLog(hour.Add(4*time.Minute), "", with(map[string]any{"host_name": "vm-9"})),
	)
	result := scanHour(t, projectID, hour, scanOptions{ipMode: ipModeHash, salt: "salt"})

	var identities []string
	for key := range result.totals {
		identities = append(identities, key.src)
	}
	assert.ElementsMatch(t, []string{
		"source\nedge",                     // obzly_source beats everything
		"k8s\nprd\nweb\ndeployment\nnginx", // then namespace + workload
		"service\nshop\nnginx\nnode-3",     // a namespace without a workload falls to service + host
		"host\nvm-9",                       // then the host
	}, identities)

	by, attributes, name := identityParts("k8s\nprd\nweb\ndeployment\nnginx")
	assert.Equal(t, "k8s_workload", by)
	assert.Equal(t, "nginx", name)
	assert.Equal(t, [][2]string{{"k8s_cluster_name", "prd"}, {"k8s_namespace_name", "web"},
		{"k8s_deployment_name", "nginx"}}, attributes)
	_, _, serviceName := identityParts("service\nshop\nnginx\nnode-3")
	assert.Equal(t, "nginx@node-3", serviceName)

	for _, set := range result.keys {
		for key := range set[dimIP] {
			assert.Regexp(t, `^ip#[0-9a-f]{12}$`, key, "hash mode keeps no address")
		}
	}
}

func storeLogs(t *testing.T, projectID uuid.UUID, items ...*logs_core.LogItem) {
	t.Helper()
	for _, item := range items {
		item.ProjectID = projectID
	}
	repository := logs_core.GetLogCoreRepository()
	require.NoError(t, repository.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: items}))
	require.NoError(t, repository.ForceFlush())
}

func accessLog(at time.Time, message string, fields map[string]any) *logs_core.LogItem {
	if message == "" {
		message = "request"
	}
	return &logs_core.LogItem{ID: uuid.New(), Timestamp: at, Level: logs_core.LogLevelInfo, Message: message,
		Fields: fields}
}

func scanHour(t *testing.T, projectID uuid.UUID, hour time.Time, options scanOptions) *rollup {
	t.Helper()
	result, err := runScan(logs_core.GetLogCoreRepository(), scanRequest{projectID: projectID, from: hour,
		to: hour.Add(time.Hour), keysFrom: hour, bucket: time.Hour, options: options, timeout: time.Minute})
	require.NoError(t, err)
	return result
}

func totalsFor(t *testing.T, result *rollup, identity string) *totalsAgg {
	t.Helper()
	merged := newTotals()
	found := false
	for key, agg := range result.totals {
		if key.src == identity {
			merged.merge(agg)
			found = true
		}
	}
	require.True(t, found, "no requests from %q", identity)
	return merged
}

func keysFor(result *rollup, identity string) keySet {
	merged := keySet{}
	for sh, set := range result.keys {
		if sh.src != identity {
			continue
		}
		for dim, list := range set {
			for key, agg := range list {
				merged.add(dim, key, agg)
			}
		}
	}
	return merged
}

func Test_Scan_FieldsTheGeneratedEnvoyAndHAProxyConfigsWrite_AreReadAsTheyAre(t *testing.T) {
	projectID := uuid.New()
	hour := time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour)
	storeLogs(t, projectID,
		// envoyDataSource's documented json_format: time, method, path, status, duration_ms.
		accessLog(hour.Add(time.Minute), "", map[string]any{"service_name": "envoy", "host_name": "edge-1",
			"time": hour.Format(time.RFC3339), "method": "GET", "path": "/health", "status": "200",
			"duration_ms": "7"}),
		// haproxyDataSource's regex captures only the status: no method or path, not an access log.
		accessLog(hour.Add(2*time.Minute), "haproxy[1]: 10.0.0.1:5000 ... 200 512", map[string]any{
			"service_name": "haproxy", "host_name": "lb-1", "time": "Nov 18 10:00:00", "status": "200"}),
	)
	result := scanHour(t, projectID, hour, scanOptions{ipMode: ipModeTruncate})
	result.finish(nil, true)

	envoy := totalsFor(t, result, "service\n\nenvoy\nedge-1")
	assert.Equal(t, int64(1), envoy.s2)
	assert.Equal(t, int64(1), envoy.hist[3], "duration_ms 7 is in the 5-10 ms bucket")
	assert.Equal(t, "duration_ms", decodeFormat(envoy.format).Fields[fieldDuration])
	assert.Contains(t, keysFor(result, "service\n\nenvoy\nedge-1")[dimPage], "/health")
	for key := range result.totals {
		assert.NotContains(t, key.src, "haproxy")
	}
}
