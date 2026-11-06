package attrnames

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The attribute list from the request that introduced this rule.
func Test_Normalize_UserList(t *testing.T) {
	cases := map[string]string{
		"client_ip": "client_ip", "cloud_provider": "cloud_provider", "k8s_pod_name": "k8s_pod_name",
		"log.file.path": "log_file_path", "log.iostream": "log_iostream", "logtag": "logtag",
		"message": "message", "pattern.hash": "pattern_hash",
		"resource.cloud.account.id":            "cloud_account_id",
		"resource.cloud.availability_zone":     "cloud_availability_zone",
		"resource.cloud.platform":              "cloud_platform",
		"resource.cloud.provider":              "cloud_provider",
		"resource.cloud.region":                "cloud_region",
		"resource.host.id":                     "host_id",
		"resource.host.image.id":               "host_image_id",
		"resource.host.name":                   "host_name",
		"resource.host.type":                   "host_type",
		"resource.k8s.cluster.name":            "k8s_cluster_name",
		"resource.k8s.container.name":          "k8s_container_name",
		"resource.k8s.container.restart_count": "k8s_container_restart_count",
		"resource.k8s.deployment.name":         "k8s_deployment_name",
		"resource.k8s.namespace.name":          "k8s_namespace_name",
		"resource.k8s.node.name":               "k8s_node_name",
		"resource.k8s.pod.name":                "k8s_pod_name",
		"resource.k8s.pod.uid":                 "k8s_pod_uid",
		"resource.os.type":                     "os_type",
		"service_name":                         "service_name", "span_id": "span_id", "trace_id": "trace_id",
	}
	for in, want := range cases {
		assert.Equal(t, want, Normalize(in), in)
		assert.Equal(t, want, Normalize(want), "idempotent: "+want)
	}
}

func Test_Normalize_OtherCharacters(t *testing.T) {
	assert.Equal(
		t,
		"k8s_pod_labels_app_kubernetes_io_name",
		Normalize("resource.k8s.pod.labels.app.kubernetes.io/name"),
	)
	assert.Equal(t, "scope_name", Normalize("scope.name"))
	assert.Equal(t, "_2xx_count", Normalize("2xx-count"))
	assert.Equal(t, "system_cpu_time", NormalizeMetricName("system.cpu.time"))
	assert.Equal(t, "resource_x", NormalizeMetricName("resource.x"), "metric names keep resource")
	assert.Equal(t, "", Normalize(""))
}

func Test_NormalizeMap_MostSpecificSourceWins(t *testing.T) {
	out := NormalizeMap(map[string]any{
		"resource.k8s.pod.name": "from-resource",
		"k8s.pod.name":          "from-record",
		"resource.host.name":    "h1",
		"scope.version":         "s",
		"scope_version":         "already-normal",
	})
	assert.Equal(t, map[string]any{
		"k8s_pod_name":  "from-record",
		"host_name":     "h1",
		"scope_version": "already-normal",
	}, out)

	assert.Equal(t, map[string]string{"k8s_node_name": "n"},
		NormalizeStringMap(map[string]string{"k8s.node.name": "n"}))
}

func Test_NormalizeStringMap_DropsNULBytes(t *testing.T) {
	assert.Equal(t, map[string]string{"a_b": "xy"}, NormalizeStringMap(map[string]string{"a\x00b": "x\x00y"}))
}
