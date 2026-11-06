package metrics_promql

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_NormalizePromQL(t *testing.T) {
	cases := map[string]string{
		`sum(rate({"__name__"="system.cpu.time"}[5m])) by ("state")`:                                 `sum by (state) (rate(system_cpu_time[5m]))`,
		`sum({"__name__"="container.memory.usage.total"}) by ("container.name")`:                     `sum by (container_name) (container_memory_usage_total)`,
		`{"__name__"=~"system\\.cpu\\.load_average\\.(1m|5m|15m)"}`:                                  `{__name__=~"system_cpu_load_average_(1m|5m|15m)"}`,
		`sum({"__name__"="k8s.node.memory.working_set", "k8s.node.name"="n1"}) by ("k8s.node.name")`: `sum by (k8s_node_name) (k8s_node_memory_working_set{k8s_node_name="n1"})`,
		`a / on("k8s.pod.name") group_left("service.name") b`:                                        `a / on (k8s_pod_name) group_left (service_name) b`,
		`label_replace(up, "k8s.pod", "$1", "resource.k8s.pod.name", "(.*)")`:                        `label_replace(up, "k8s_pod", "$1", "k8s_pod_name", "(.*)")`,
	}
	for in, want := range cases {
		assert.Equal(t, want, NormalizePromQL(in), in)
	}

	for _, unchanged := range []string{`sum(rate(http_requests_total[5m])) by (job)`, `rate(foo[5m`} {
		assert.Equal(t, unchanged, NormalizePromQL(unchanged), "untouched: "+unchanged)
	}
}
