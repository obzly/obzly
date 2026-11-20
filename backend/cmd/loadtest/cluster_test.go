package main

import (
	"math/rand"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
)

func Test_ClusterProfile_PodNames_FollowEachWorkloadKind(t *testing.T) {
	byName := map[string]*clusterWorkload{}
	for _, w := range clusterWorkloads(80) {
		byName[w.name] = w
	}
	at := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)

	orders := byName["orders-api"]
	pod := orders.podAt(0, at.Truncate(orders.churn))
	assert.Regexp(t, `^orders-api-[a-z2-9]{10}-[bcdfghjklmnpqrstvwxz2456789]{5}$`, pod.name)
	assert.NotEqual(t, pod.name, orders.podAt(0, at.Truncate(orders.churn).Add(orders.churn)).name,
		"a Deployment's pods change with each churn period")

	postgres := byName["postgres"]
	assert.Equal(t, "postgres-1", postgres.podAt(1, at).name)
	assert.Equal(t, "postgres-1", postgres.podAt(1, at.Add(72*time.Hour)).name, "a StatefulSet keeps its ordinals")

	kubeProxy := byName["kube-proxy"]
	assert.Equal(t, clusterNodes, kubeProxy.replicas, "a DaemonSet has a pod per node")
	first, second := kubeProxy.podAt(0, at), kubeProxy.podAt(1, at)
	assert.Regexp(t, `^kube-proxy-[bcdfghjklmnpqrstvwxz2456789]{5}$`, first.name)
	assert.Equal(t, "contoso-node-01", first.node)
	assert.Equal(t, "contoso-node-02", second.node)
	assert.Equal(t, first, kubeProxy.podAt(0, at.Add(time.Hour)))

	assert.Equal(t, "contoso-legacy-erp-02", byName["contoso-legacy-erp"].podAt(1, at).name)
}

func Test_ClusterProfile_Resources_DescribeEachWorkloadKind(t *testing.T) {
	clock := &simClock{start: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), perLine: float64(time.Millisecond)}
	workloads := clusterWorkloads(80)
	rng := rand.New(rand.NewSource(3))
	keys := map[string]bool{}
	containers := map[string]bool{}
	hosts := 0
	for i := 0; i < 300; i++ {
		request := buildClusterLogsRequest(rng, workloads, clock, clock.reserve(200), 200, 6)
		for _, rl := range request.ResourceLogs {
			attrs := attrMap(rl.Resource.Attributes)
			for key := range attrs {
				keys[key] = true
			}
			if _, ok := attrs["host.name"]; ok {
				hosts++
				assert.NotContains(t, attrs, "k8s.pod.name", "a VM has no Kubernetes attributes")
				continue
			}
			containers[attrs["k8s.namespace.name"]+"/"+attrs["k8s.container.name"]] = true
		}
	}

	for _, key := range []string{"k8s.deployment.name", "k8s.statefulset.name", "k8s.daemonset.name", "host.name"} {
		assert.True(t, keys[key], key)
	}
	assert.Positive(t, hosts)
	assert.True(t, containers["shop/istio-proxy"], "meshed workloads have a sidecar")
	assert.True(t, containers["shop/orders-api"])
	assert.False(t, containers["edge/istio-proxy"], "the edge isn't meshed")
}

func Test_ClusterProfile_Lines_EveryFormatAppearsWithMadeUpNamesOnly(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	workloads := clusterWorkloads(80)
	at := time.Date(2026, 11, 2, 3, 4, 5, 0, time.UTC)
	ipPattern := regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	emailPattern := regexp.MustCompile(`[a-z0-9.+]+@([a-z0-9-]+(?:\.[a-z0-9-]+)*\.[a-z]{2,})\b`)
	seen := map[uintptr]bool{}
	all := map[uintptr]bool{}

	for _, w := range workloads {
		for _, c := range w.containers {
			for _, l := range c.lines {
				all[reflect.ValueOf(l.line).Pointer()] = true
			}
		}
	}
	for i := 0; i < 60_000; i++ {
		w := pickWorkload(rng, workloads)
		c := w.containers[pickContainer(rng, w.containers)]
		line := pickLine(rng, c.lines)
		seen[reflect.ValueOf(line).Pointer()] = true
		level, body := line(rng, w.podAt(rng.Intn(w.replicas), at), at)

		require.Contains(t, otlpSeverities, level)
		require.NotEmpty(t, body)
		for _, ip := range ipPattern.FindAllString(body, -1) {
			assert.Regexp(t, `^(192\.0\.2|198\.51\.100|203\.0\.113|10\.42|127\.0\.0)\.`, ip, body)
		}
		for _, m := range emailPattern.FindAllStringSubmatch(body, -1) {
			assert.Contains(t, []string{"example.com", "example.net", "example.org"}, m[1], body)
		}
	}

	// fromK8sKind closures share one function, so compare the distinct
	// generators: all of them were drawn.
	assert.Len(t, seen, len(all), "every line format appears")
}

func attrMap(attrs []*commonv1.KeyValue) map[string]string {
	result := map[string]string{}
	for _, a := range attrs {
		result[a.Key] = a.Value.GetStringValue()
	}
	return result
}
