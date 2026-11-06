package inventory

import (
	"testing"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	spans_core "obzly/internal/features/spans/core"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testNow = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

func Test_Observe_KubernetesSeries_BuildsTheHierarchy(t *testing.T) {
	snap := clusterSnapshot()

	assets := observe(snap)

	cluster := assets["cluster:prd"]
	require.NotNil(t, cluster)
	assert.Equal(t, "aws_eks", cluster.Attributes["cloud_platform"])
	node := assets["node:prd/ip-10-0-1-5.ec2.internal"]
	require.NotNil(t, node)
	assert.Equal(t, "cluster:prd", node.ParentKey)
	assert.Equal(t, "Amazon Linux 2023", node.Attributes["os_description"], "the node's own host metrics add its OS")
	assert.Nil(t, assets["host:ip-10-0-1-5.ec2.internal"], "a node's host metrics don't make a separate host")

	pod := assets["pod:prd/shop/uid-checkout-1"]
	require.NotNil(t, pod)
	assert.Equal(t, "wl:prd/shop/Deployment/checkout-api", pod.ParentKey)
	assert.Equal(t, "node:prd/ip-10-0-1-5.ec2.internal", pod.NodeKey)
	assert.Equal(t, "wl:prd/shop/StatefulSet/orders-db", assets["pod:prd/shop/uid-orders-0"].ParentKey)
	assert.Equal(t, "wl:prd/kube-system/DaemonSet/kube-proxy", assets["pod:prd/kube-system/uid-proxy"].ParentKey,
		"a DaemonSet pod is known from its label")
	assert.Equal(t, "ns:prd/shop", assets["wl:prd/shop/Deployment/checkout-api"].ParentKey)
	assert.Equal(t, "cluster:prd", assets["ns:prd/shop"].ParentKey)
	container := assets["ctr:prd/shop/uid-checkout-1/app"]
	require.NotNil(t, container)
	assert.Equal(t, pod.Key, container.ParentKey)

	agentPod := assets["pod:prd/shop/uid-pending"]
	require.NotNil(t, agentPod, "the cluster agent's pod phase alone discovers a pod")
	assert.Equal(t, "node:prd/ip-10-0-1-5.ec2.internal", agentPod.NodeKey,
		"its k8s node name maps to the node's host name")

	host := assets["host:build-01"]
	require.NotNil(t, host)
	assert.Equal(t, "EC2 instance", host.Subtype)
	docker := assets["dctr:build-01/buildkitd"]
	require.NotNil(t, docker)
	assert.Equal(t, "host:build-01", docker.ParentKey)

	service := assets["svc:checkout"]
	require.NotNil(t, service)
	assert.Equal(t, "wl:prd/shop/Deployment/checkout-api", service.ParentKey, "linked through its traces' deployment")
	assert.Equal(t, testNow.Add(-72*time.Hour), pod.FirstSeen)
}

func Test_Observe_Values_SetPercentagesOfLimitOrNode(t *testing.T) {
	assets := observe(clusterSnapshot())

	node := assets["node:prd/ip-10-0-1-5.ec2.internal"]
	assert.Equal(t, 50.0, *node.Metrics.CPUPercent, "2 of 4 cores")
	assert.Equal(t, 75.0, *node.Metrics.MemoryPercent, "6 of 8 GiB")
	checkout := assets["pod:prd/shop/uid-checkout-1"]
	assert.Equal(t, 92.0, *checkout.Metrics.MemoryPercent)
	assert.Equal(t, "limit", checkout.Metrics.MemoryBasis)
	assert.Equal(t, 12.5, *checkout.Metrics.CPUPercent, "half a core of the node's 4")
	assert.Equal(t, "node", checkout.Metrics.CPUBasis)
	assert.Equal(t, 4.0, *checkout.Metrics.Restarts1h)
	assert.Equal(t, 10.0, *checkout.Metrics.ErrorShare)
	assert.Equal(t, "Running", checkout.Metrics.Phase)
	deployment := assets["wl:prd/shop/Deployment/checkout-api"]
	assert.Equal(t, 3.0, *deployment.Metrics.Desired)
	assert.Equal(t, 0.0, *deployment.Metrics.Available)
	assert.Equal(t, 25.0, *assets["host:build-01"].Metrics.CPUPercent)
}

func Test_Reconcile_HealthRules_GiveReasonsAndRollUp(t *testing.T) {
	alerts := []firingAlert{{rule: "Orders DB down", severity: "critical",
		labels: map[string]string{"k8s_pod_name": "orders-db-0", "k8s_namespace_name": "shop"}}}

	assets, _, _ := reconcile(uuid.New(), nil, observe(clusterSnapshot()), alerts, testNow, 30*24*time.Hour)

	byKey := indexByKey(assets)
	checkout := byKey["pod:prd/shop/uid-checkout-1"]
	assert.Equal(t, HealthWarning, checkout.Health)
	assert.Contains(t, texts(checkout), "4 container restarts in the last hour")
	assert.Contains(t, texts(checkout), "Memory (of its limit) at 92%")
	assert.Contains(t, texts(checkout), "Error logs are 10.0% of its lines in the last 15 min")
	deployment := byKey["wl:prd/shop/Deployment/checkout-api"]
	assert.Equal(t, HealthCritical, deployment.Health)
	assert.Equal(t, "No replicas available (0 of 3)", deployment.Reasons[0].Text)
	orders := byKey["pod:prd/shop/uid-orders-0"]
	assert.Equal(t, HealthCritical, orders.Health)
	assert.Equal(t, `Alert "Orders DB down" is firing`, orders.Reasons[0].Text)
	namespace := byKey["ns:prd/shop"]
	assert.Equal(t, HealthWarning, namespace.Health, "a namespace rolls up its children, capped at warning")
	assert.Equal(t, HealthWarning, byKey["cluster:prd"].Health)
	assert.Equal(t, HealthHealthy, byKey["node:prd/ip-10-0-1-5.ec2.internal"].Health)
	assert.Equal(t, HealthWarning, byKey["svc:checkout"].Health, "a service follows its workload, capped")
	assert.Equal(t, HealthWarning, byKey["pod:prd/shop/uid-pending"].Health)
	assert.Contains(t, texts(byKey["pod:prd/shop/uid-pending"]), "Pending for 20 min")
}

func Test_Reconcile_Lifecycle_TracksStatesChangesAndRetention(t *testing.T) {
	projectID := uuid.New()
	first, changes, _ := reconcile(projectID, nil, observe(clusterSnapshot()), nil, testNow, 30*24*time.Hour)
	assert.Empty(t, changes, "the first build of a project records no changes")
	byKey := indexByKey(first)
	assert.Equal(t, StateActive, byKey["node:prd/ip-10-0-1-5.ec2.internal"].State)
	assert.Equal(t, StateNew, byKey["pod:prd/shop/uid-pending"].State)

	existing := make([]Asset, 0, len(first))
	for _, a := range first {
		a.ID = uuid.New()
		existing = append(existing, *a)
	}
	oldPod := Asset{ID: uuid.New(), Kind: KindPod, Key: "pod:prd/shop/uid-old", Name: "checkout-api-7d9f8b6c5d-old12",
		FirstSeen: testNow.Add(-30 * 24 * time.Hour), LastSeen: testNow.Add(-8 * 24 * time.Hour),
		State: StateTerminated, Health: HealthInactive}
	existing = append(existing, oldPod)
	// 15 minutes later the node and the build host stopped reporting and a
	// new pod appeared.
	later := testNow.Add(15 * time.Minute)
	snap := clusterSnapshot()
	snap.now = later
	var kept []metrics_core.SeriesLifetimeDTO
	for _, s := range snap.series {
		if s.Labels["host_name"] == "build-01" || s.MetricName == "k8s_node_memory_working_set" {
			continue
		}
		s.LastSeen = later
		kept = append(kept, s)
	}
	snap.series = append(kept, series("k8s_pod_memory_working_set", later.Add(-time.Minute), later, map[string]string{
		"k8s_cluster_name": "prd", "k8s_namespace_name": "shop", "k8s_pod_name": "checkout-api-7d9f8b6c5d-new99",
		"k8s_pod_uid": "uid-new", "host_name": "ip-10-0-1-5.ec2.internal",
	}))

	second, changes, stale := reconcile(projectID, existing, observe(snap), nil, later, 30*24*time.Hour)

	byKey = indexByKey(second)
	host := byKey["host:build-01"]
	assert.Equal(t, StateNotReporting, host.State)
	assert.Equal(t, HealthCritical, host.Health)
	assert.Equal(t, "No data for 16 min", host.Reasons[0].Text, "last seen a minute before the first build")
	assert.Equal(t, []uuid.UUID{oldPod.ID}, stale, "a pod terminated over 7 days ago is dropped")
	assert.Nil(t, byKey[oldPod.Key])
	assert.Equal(t, byKey["host:build-01"].ID, existingID(existing, "host:build-01"), "ids are kept")
	kinds := map[string]string{}
	for _, c := range changes {
		kinds[c.AssetKey+" "+c.Type] = c.From + "→" + c.To
	}
	assert.Equal(t, "→new", kinds["pod:prd/shop/uid-new new"])
	assert.Equal(t, "active→not_reporting", kinds["host:build-01 state"])
	assert.Equal(t, "healthy→critical", kinds["host:build-01 health"])
}

func Test_Reconcile_ReplacedNodesAndCloudResources_AreDroppedWhileTheirSourceReports(t *testing.T) {
	asset := func(kind, key, cluster string, gone time.Duration, sources Strings, attributes map[string]string) Asset {
		return Asset{
			ID: uuid.New(), Kind: kind, Key: key, Name: key, Cluster: cluster, Sources: sources,
			Attributes: attributes, FirstSeen: testNow.Add(-48 * time.Hour), LastSeen: testNow.Add(-gone),
		}
	}
	mumbai := func(state string) map[string]string {
		return map[string]string{
			"cloud_provider": "aws", "cloud_account_id": "111122223333", "cloud_region": "ap-south-1",
			"cloud_resource_state": state, "cloud_refresh_seconds": "900",
		}
	}
	existing := []Asset{
		asset(KindNode, "node:prd/spot-reclaimed", "prd", 2*time.Hour, Strings{"metrics"}, nil),
		asset(KindNode, "node:prd/ip-10-0-1-5", "prd", time.Minute, Strings{"metrics"}, nil),
		asset(KindNode, "node:prd/rebooting", "prd", 20*time.Minute, Strings{"metrics"}, nil),
		// The collector of dev stopped: its nodes stay, not reporting.
		asset(KindNode, "node:dev/ip-10-0-9-9", "dev", 2*time.Hour, Strings{"metrics"}, nil),
		asset(KindHost, "host:cloud/i-spot", "", 5*time.Minute, Strings{"cloud"}, mumbai("terminated")),
		asset(KindHost, "host:cloud/i-deleted", "", 2*time.Hour, Strings{"cloud"}, mumbai("running")),
		asset(KindDatabase, "cloud:aws_rds_instance/orders", "", 5*time.Minute, Strings{"cloud"}, mumbai("available")),
		// No scan of this account has arrived for hours: the scans stopped.
		asset(KindHost, "host:cloud/i-quiet", "", 2*time.Hour, Strings{"cloud"}, map[string]string{
			"cloud_provider": "aws", "cloud_account_id": "444455556666", "cloud_region": "eu-west-1",
			"cloud_resource_state": "running", "cloud_refresh_seconds": "900",
		}),
	}

	kept, _, stale := reconcile(uuid.New(), existing, nil, nil, testNow, 30*24*time.Hour)

	byKey := indexByKey(kept)
	for _, key := range []string{"node:prd/spot-reclaimed", "host:cloud/i-spot", "host:cloud/i-deleted"} {
		assert.Nil(t, byKey[key], key)
		assert.Contains(t, stale, existingID(existing, key), key)
	}
	for _, key := range []string{
		"node:prd/ip-10-0-1-5", "node:prd/rebooting", "node:dev/ip-10-0-9-9",
		"cloud:aws_rds_instance/orders", "host:cloud/i-quiet",
	} {
		assert.NotNil(t, byKey[key], key)
	}
	assert.Equal(t, StateNotReporting, byKey["node:dev/ip-10-0-9-9"].State)
}

func Test_Reconcile_CronJobRuns_FoldIntoTheCronJobAndLeaveOnceDone(t *testing.T) {
	projectID := uuid.New()
	shop := map[string]string{"k8s_cluster_name": "prd", "k8s_namespace_name": "shop"}
	pod := func(name, uid string) map[string]string {
		return with(with(with(shop, "k8s_pod_name", name), "k8s_pod_uid", uid), "host_name", "ip-10-0-1-5.ec2.internal")
	}
	last := testNow.Add(-time.Minute)
	running := pod("reminder-29885470-n95vm", "uid-running")
	snap := &snapshot{
		now: testNow,
		series: []metrics_core.SeriesLifetimeDTO{
			series("k8s_cronjob_active_jobs", testNow.Add(-72*time.Hour), last, with(shop, "k8s_cronjob_name", "reminder")),
			// The cluster agent names each run's Job, not its CronJob...
			series("k8s_job_active_pods", testNow.Add(-25*time.Minute), last, with(shop, "k8s_job_name", "reminder-29885450")),
			series("k8s_job_active_pods", testNow.Add(-5*time.Minute), last, with(shop, "k8s_job_name", "reminder-29885470")),
			// ...and nothing but the pod for its pod phase.
			series("k8s_pod_phase", testNow.Add(-25*time.Minute), last, pod("reminder-29885450-4p5wz", "uid-done")),
			series("k8s_pod_phase", testNow.Add(-15*time.Minute), last, pod("reminder-29885460-s8p8b", "uid-failed")),
			series("k8s_pod_phase", testNow.Add(-5*time.Minute), last, running),
			series("container_memory_working_set", testNow.Add(-5*time.Minute), last, with(with(with(running,
				"k8s_cronjob_name", "reminder"), "k8s_job_name", "reminder-29885470"), "k8s_container_name", "reminder")),
		},
		values: map[string][]sample{"pod_phase": {
			{pod("reminder-29885450-4p5wz", "uid-done"), 3},
			{pod("reminder-29885460-s8p8b", "uid-failed"), 4},
			{running, 2},
		}},
	}
	cronJob := "wl:prd/shop/CronJob/reminder"
	old := func(kind, key, name, parent, phase string, lastSeen time.Time) Asset {
		return Asset{ID: uuid.New(), Kind: kind, Key: key, Name: name, Subtype: map[string]string{
			KindWorkload: "Job"}[kind], ParentKey: parent, Cluster: "prd", Namespace: "shop",
			FirstSeen: lastSeen.Add(-time.Minute), LastSeen: lastSeen, Metrics: Metrics{Phase: phase}}
	}
	// Builds before runs were folded parented a run's pod to its Job, its
	// namespace, or a Deployment made up from its name.
	oldJob := old(KindWorkload, "wl:prd/shop/Job/reminder-29884000", "reminder-29884000", "ns:prd/shop", "",
		last)
	madeUp := old(KindWorkload, "wl:prd/shop/Deployment/reminder", "reminder", "ns:prd/shop", "",
		testNow.Add(-2*time.Hour))
	madeUp.Subtype = "Deployment"
	oldDone := old(KindPod, "pod:prd/shop/uid-old-done", "reminder-29884000-x7k2p", oldJob.Key, "Succeeded",
		testNow.Add(-2*time.Hour))
	oldDoneContainer := old(KindContainer, "ctr:prd/shop/uid-old-done/reminder", "reminder", oldDone.Key, "",
		testNow.Add(-2*time.Hour))
	unknownEnd := old(KindPod, "pod:prd/shop/uid-old-unknown", "reminder-29884010-q2w8z", "ns:prd/shop", "",
		testNow.Add(-3*time.Hour))
	unknownEndContainer := old(KindContainer, "ctr:prd/shop/uid-old-unknown/reminder", "reminder",
		unknownEnd.Key, "", testNow.Add(-3*time.Hour))
	recentFailure := old(KindPod, "pod:prd/shop/uid-old-failed", "reminder-29885299-x7k2p", madeUp.Key, "Failed",
		testNow.Add(-2*time.Hour))
	oldFailure := old(KindPod, "pod:prd/shop/uid-older-failed", "reminder-29883000-x7k2p", cronJob, "Failed",
		testNow.Add(-25*time.Hour))
	existing := []Asset{
		oldJob, madeUp, oldDone, oldDoneContainer, unknownEnd, unknownEndContainer, recentFailure, oldFailure,
	}

	assets, changes, stale := reconcile(projectID, existing, observe(snap), nil, testNow, 30*24*time.Hour)

	byKey := indexByKey(assets)
	for key, a := range byKey {
		assert.NotEqual(t, "Job", a.Subtype, "%s: a CronJob's runs aren't workloads of their own", key)
	}
	require.NotNil(t, byKey[cronJob])
	assert.Equal(t, cronJob, byKey["pod:prd/shop/uid-running"].ParentKey, "named after its CronJob")
	assert.Equal(t, StateNew, byKey["pod:prd/shop/uid-running"].State)
	assert.NotNil(t, byKey["ctr:prd/shop/uid-running/reminder"])
	assert.Nil(t, byKey["pod:prd/shop/uid-done"], "a run that succeeded leaves")
	failed := byKey["pod:prd/shop/uid-failed"]
	require.NotNil(t, failed, "a failed run stays")
	assert.Equal(t, HealthCritical, failed.Health)
	assert.Equal(t, HealthWarning, byKey[cronJob].Health)
	assert.Equal(t, StateTerminated, byKey[recentFailure.Key].State, "a failed run is kept a day")
	assert.Equal(t, cronJob, byKey[recentFailure.Key].ParentKey)
	assert.ElementsMatch(t, []uuid.UUID{
		oldJob.ID, madeUp.ID, oldDone.ID, oldDoneContainer.ID, unknownEnd.ID, unknownEndContainer.ID, oldFailure.ID,
	}, stale)
	for _, c := range changes {
		if c.Kind == KindPod || c.Kind == KindContainer {
			assert.NotEqual(t, ChangeNew, c.Type, "%s: a new run isn't news", c.AssetKey)
		}
	}
	assert.Contains(t, changes, Change{ProjectID: projectID, AssetKey: cronJob, AssetName: "reminder",
		Kind: KindWorkload, Type: ChangeNew, To: StateActive, Detail: "CronJob", At: testNow})
}

func Test_WorkloadOf_PodNames_OnlyTrustsGeneratedSuffixes(t *testing.T) {
	cases := []struct{ pod, kind, name string }{
		{"checkout-api-7d9f8b6c5d-x7k2p", "Deployment", "checkout-api"},
		{"orders-db-0", "StatefulSet", "orders-db"},
		{"otel-collector-x7k2p", "", ""},
		{"aws-node-x7k2p", "", ""},
		{"reminder-29885466-x7k2p", "", ""},
	}
	for _, c := range cases {
		kind, name := workloadOf(map[string]string{}, c.pod)
		assert.Equal(t, c.kind, kind, c.pod)
		assert.Equal(t, c.name, name, c.pod)
	}
	kind, name := workloadOf(map[string]string{"k8s_replicaset_name": "web-5f6c7d8b9c"}, "x")
	assert.Equal(t, []string{"Deployment", "web"}, []string{kind, name})
}

func clusterSnapshot() *snapshot {
	node := map[string]string{
		"k8s_cluster_name": "prd", "k8s_node_name": "ip-10-0-1-5.ec2.internal", "host_name": "ip-10-0-1-5.ec2.internal",
		"cloud_platform": "aws_eks", "cloud_region": "us-east-1",
	}
	checkout := map[string]string{
		"k8s_cluster_name": "prd", "k8s_namespace_name": "shop", "k8s_pod_name": "checkout-api-7d9f8b6c5d-x7k2p",
		"k8s_pod_uid": "uid-checkout-1", "host_name": "ip-10-0-1-5.ec2.internal",
	}
	orders := map[string]string{
		"k8s_cluster_name": "prd", "k8s_namespace_name": "shop", "k8s_pod_name": "orders-db-0",
		"k8s_pod_uid": "uid-orders-0", "host_name": "ip-10-0-1-5.ec2.internal",
	}
	proxy := map[string]string{
		"k8s_cluster_name": "prd", "k8s_namespace_name": "kube-system", "k8s_pod_name": "kube-proxy-x7k2p",
		"k8s_pod_uid": "uid-proxy", "host_name": "ip-10-0-1-5.ec2.internal", "k8s_daemonset_name": "kube-proxy",
	}
	pending := map[string]string{
		"k8s_cluster_name": "prd", "k8s_namespace_name": "shop", "k8s_pod_name": "report-job-abcde",
		"k8s_pod_uid": "uid-pending", "k8s_node_name": "ip-10-0-1-5.ec2.internal",
	}
	container := with(checkout, "k8s_container_name", "app")
	since := testNow.Add(-72 * time.Hour)
	last := testNow.Add(-time.Minute)
	return &snapshot{
		now: testNow,
		series: []metrics_core.SeriesLifetimeDTO{
			series("k8s_pod_memory_working_set", since, last, checkout),
			series("k8s_node_memory_working_set", since.Add(-24*time.Hour), last, node),
			series(
				"system_memory_usage",
				since,
				last,
				with(with(node, "state", "used"), "os_description", "Amazon Linux 2023"),
			),
			series("k8s_pod_memory_working_set", since, last, orders),
			series("k8s_pod_memory_working_set", since, last, proxy),
			series("container_memory_working_set", since, last, container),
			series("k8s_pod_phase", testNow.Add(-20*time.Minute), last, pending),
			series("system_memory_usage", since, last, map[string]string{
				"host_name": "build-01", "cloud_platform": "aws_ec2", "state": "used",
			}),
			series("container_memory_usage_total", since, last, map[string]string{
				"host_name": "build-01", "container_name": "buildkitd",
			}),
		},
		values: map[string][]sample{
			"node_cpu":                {{node, 2}},
			"host_cores":              {{map[string]string{"host_name": "ip-10-0-1-5.ec2.internal"}, 4}},
			"node_memory_working_set": {{node, 6 << 30}},
			"node_memory_available":   {{node, 2 << 30}},
			"pod_cpu":                 {{checkout, 0.5}},
			"pod_memory_limit":        {{checkout, 0.92}},
			"pod_restarts_1h":         {{checkout, 4.2}},
			"pod_phase":               {{checkout, 2}, {pending, 1}},
			"deployment_desired": {{map[string]string{"k8s_cluster_name": "prd", "k8s_namespace_name": "shop",
				"k8s_deployment_name": "checkout-api"}, 3}},
			"deployment_available": {{map[string]string{"k8s_cluster_name": "prd", "k8s_namespace_name": "shop",
				"k8s_deployment_name": "checkout-api"}, 0}},
			"host_cpu_idle":  {{map[string]string{"host_name": "build-01"}, 3}},
			"host_cpu_total": {{map[string]string{"host_name": "build-01"}, 4}},
		},
		services:    []string{"checkout"},
		serviceMeta: map[string]spans_core.K8sResourceInfo{"checkout": {Namespace: "shop", Deployment: "checkout-api"}},
		podLines:    map[string]float64{"checkout-api-7d9f8b6c5d-x7k2p": 200},
		podErrors:   map[string]float64{"checkout-api-7d9f8b6c5d-x7k2p": 20},
	}
}

func series(name string, first, last time.Time, labels map[string]string) metrics_core.SeriesLifetimeDTO {
	return metrics_core.SeriesLifetimeDTO{MetricName: name, Labels: labels, FirstSeen: first, LastSeen: last}
}

func with(labels map[string]string, key, value string) map[string]string {
	copied := map[string]string{key: value}
	for k, v := range labels {
		if k != key {
			copied[k] = v
		}
	}
	return copied
}

func indexByKey(assets []*Asset) map[string]*Asset {
	byKey := map[string]*Asset{}
	for _, a := range assets {
		byKey[a.Key] = a
	}
	return byKey
}

func texts(a *Asset) []string {
	var result []string
	for _, r := range a.Reasons {
		result = append(result, r.Text)
	}
	return result
}

func existingID(assets []Asset, key string) uuid.UUID {
	for _, a := range assets {
		if a.Key == key {
			return a.ID
		}
	}
	return uuid.Nil
}
