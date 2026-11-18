package ingestion

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_Identify_FirstMatchingRuleWins(t *testing.T) {
	cases := []struct {
		name       string
		attributes map[string]string
		by         string
	}{
		{"explicit source beats everything", map[string]string{"obzly_source": "edge", "service_name": "nginx",
			"k8s_namespace_name": "shop", "k8s_deployment_name": "web"}, IdentifiedByExplicit},
		{"workload beats service", map[string]string{"service_name": "checkout", "host_name": "node-1",
			"k8s_namespace_name": "shop", "k8s_deployment_name": "checkout"}, IdentifiedByWorkload},
		{"namespace without workload is not a workload", map[string]string{"service_name": "checkout",
			"k8s_namespace_name": "shop"}, IdentifiedByService},
		{"cluster-level data", map[string]string{"k8s_cluster_name": "prd", "k8s_node_name": "n1"},
			IdentifiedByCluster},
		{"host only", map[string]string{"host_name": "vm-1"}, IdentifiedByHost},
		{"nothing to go on", map[string]string{"foo": "bar"}, IdentifiedByUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.by, identify(getter(c.attributes)).by)
		})
	}
}

func Test_Identify_CronJobRunsAndPodsOfOneWorkload_ShareOneSource(t *testing.T) {
	run1 := identify(getter(map[string]string{"k8s_namespace_name": "ops", "k8s_cronjob_name": "backup",
		"k8s_job_name": "backup-29001", "k8s_pod_name": "backup-29001-x"}))
	run2 := identify(getter(map[string]string{"k8s_namespace_name": "ops", "k8s_cronjob_name": "backup",
		"k8s_job_name": "backup-29002", "k8s_pod_name": "backup-29002-y"}))
	assert.Equal(t, run1.key, run2.key)

	otherHost := identify(getter(map[string]string{"service_name": "api", "host_name": "vm-2"}))
	host := identify(getter(map[string]string{"service_name": "api", "host_name": "vm-1"}))
	assert.NotEqual(t, host.key, otherHost.key, "service + host: one source per host")
}

func Test_DetectKind_FromWhatGeneratedConfigsAndSDKsSet(t *testing.T) {
	cases := []struct {
		name       string
		attributes map[string]string
		scope      string
		metric     string
		kind       string
	}{
		{"explicit marker", map[string]string{"obzly_data_source": "custom-otlp", "service_name": "nginx"}, "", "",
			"custom-otlp"},
		{"service.name a collector config stamps", map[string]string{"service_name": "postgresql"}, "", "", "postgresql"},
		{"Claude Code", map[string]string{"service_name": "claude-code", "telemetry_sdk_language": "nodejs"}, "", "",
			"claude-code"},
		{"receiver scope", map[string]string{}, "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/" +
			"dockerstatsreceiver", "container_cpu_usage_total", "docker"},
		{"host metrics on windows", map[string]string{"os_type": "windows"}, "otelcol/hostmetricsreceiver/cpu",
			"system_cpu_time", "windows"},
		{"SDK language beats the platform", map[string]string{"telemetry_sdk_language": "go",
			"k8s_namespace_name": "shop"}, "", "", "lang-go"},
		{"Java SDK's JVM metrics stay the Java SDK", map[string]string{"telemetry_sdk_language": "java"}, "",
			"jvm_memory_used", "lang-java"},
		{"metric prefix", map[string]string{"service_name": "cache-01"}, "", "redis_memory_used", "redis"},
		{"kubernetes pod", map[string]string{"k8s_pod_name": "p"}, "", "", "kubernetes"},
		{"unknown", map[string]string{"service_name": "my-app"}, "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.kind, detectKind(getter(c.attributes), c.scope, c.metric).kind)
		})
	}
}

func getter(attributes map[string]string) attributeGetter {
	return func(name string) string { return attributes[name] }
}
