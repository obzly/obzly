package inventory

import (
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	spans_core "obzly/internal/features/spans/core"

	"github.com/google/uuid"
)

const (
	// notReportingAfter: no data for this long and an asset is "not reporting".
	notReportingAfter = 10 * time.Minute
	// podGoneAfter / assetGoneAfter: then it's terminated (a pod or container
	// is usually replaced; a node or host may only be rebooting).
	podGoneAfter   = 30 * time.Minute
	assetGoneAfter = 24 * time.Hour
	// newFor: an asset first seen this recently is "new".
	newFor = 24 * time.Hour
	// keepTerminatedPods: terminated pods and containers are dropped after this.
	keepTerminatedPods = 7 * 24 * time.Hour
	// keepFailedRuns: a Job or CronJob run's pod is dropped once it ends,
	// unless it failed; a failed run (and a finished Job) stays this long.
	// A CronJob every 10 minutes would otherwise leave a thousand pods a week.
	keepFailedRuns = 24 * time.Hour
	// removeReplacedAfter: a node or cloud resource missing this long while
	// what reports it still does was replaced or deleted (spot capacity,
	// autoscaling), so it is dropped instead of kept for retention.
	removeReplacedAfter = time.Hour
	// minLinesForErrorShare: fewer log lines than this say nothing.
	minLinesForErrorShare = 20
	maxAttributes         = 60
)

var (
	// A Deployment's pod: <name>-<pod-template-hash>-<suffix>, both generated
	// from Kubernetes' safe alphabet (no vowels), so "otel-collector-x7k2p"
	// isn't read as Deployment "otel".
	deploymentPod = regexp.MustCompile(`^(.+)-[bcdfghjklmnpqrstvwxz2456789]{6,10}-[bcdfghjklmnpqrstvwxz2456789]{5}$`)
	replicaSet    = regexp.MustCompile(`^(.+)-[bcdfghjklmnpqrstvwxz2456789]{6,10}$`)
	statefulPod   = regexp.MustCompile(`^(.+)-[0-9]+$`)
	podPhases     = map[float64]string{1: "Pending", 2: "Running", 3: "Succeeded", 4: "Failed", 5: "Unknown"}
	healthRank    = map[string]int{
		HealthCritical: 0, HealthWarning: 1, HealthHealthy: 2, HealthUnknown: 3, HealthInactive: 4,
	}
	workloadLabels = []struct{ label, kind string }{
		{"k8s_deployment_name", "Deployment"}, {"k8s_statefulset_name", "StatefulSet"},
		{"k8s_daemonset_name", "DaemonSet"}, {"k8s_cronjob_name", "CronJob"}, {"k8s_job_name", "Job"},
	}
	// Labels that describe one data point, not the resource.
	pointLabels = map[string]bool{
		"state": true, "direction": true, "device": true, "mountpoint": true, "mode": true, "type": true,
		"cpu": true, "condition": true, "interface": true, "le": true, "quantile": true,
	}
	// A CronJob's Job is <cronjob>-<scheduled minute>, its pod <job>-<suffix>.
	cronJobRun = regexp.MustCompile(`^(.+)-[0-9]+$`)
	cronJobPod = regexp.MustCompile(`^(.+)-([0-9]+)-[bcdfghjklmnpqrstvwxz2456789]{5}$`)
)

// sample is one series of an instant query.
type sample struct {
	labels map[string]string
	value  float64
}

// firingAlert is an alert firing in the project.
type firingAlert struct {
	rule     string
	severity string
	labels   map[string]string
}

// snapshot is everything one build reads for a project.
type snapshot struct {
	now         time.Time
	series      []metrics_core.SeriesLifetimeDTO
	values      map[string][]sample
	services    []string
	serviceMeta map[string]spans_core.K8sResourceInfo
	// podLines / podErrors: log lines and error lines per pod name, last 15 min.
	podLines  map[string]float64
	podErrors map[string]float64
	alerts    []firingAlert
}

// observation collects the assets one snapshot shows.
type observation struct {
	now    time.Time
	assets map[string]*Asset
	// nodeHost maps cluster + k8s node name to the node's host name, the key
	// pod metrics (which carry host_name, not the node name) use.
	nodeHost  map[string]string
	nodeHosts map[string]bool
	// cronJobs: cluster + namespace + name of the CronJobs any series names.
	cronJobs map[string]bool
}

// observe turns a snapshot's series into assets with keys, parents, first
// and last seen, attributes and latest values; no health or state yet.
func observe(snap *snapshot) map[string]*Asset {
	o := &observation{
		now:       snap.now,
		assets:    map[string]*Asset{},
		nodeHost:  map[string]string{},
		nodeHosts: map[string]bool{},
		cronJobs:  map[string]bool{},
	}
	series := slices.Clone(snap.series)
	// Nodes first, so pods and hosts can tell which host names are nodes.
	sort.SliceStable(series, func(i, j int) bool {
		return strings.HasPrefix(series[i].MetricName, "k8s_node_") &&
			!strings.HasPrefix(series[j].MetricName, "k8s_node_")
	})
	for _, s := range series {
		l := s.Labels
		if strings.HasPrefix(s.MetricName, "k8s_node_") {
			if n, h := l["k8s_node_name"], l["host_name"]; n != "" && h != "" {
				o.nodeHost[l["k8s_cluster_name"]+"\x00"+n] = h
			}
		}
		if strings.HasPrefix(s.MetricName, "k8s_node_") || strings.HasPrefix(s.MetricName, "k8s_pod_") {
			if h := l["host_name"]; h != "" {
				o.nodeHosts[h] = true
			}
		}
		if name := l["k8s_cronjob_name"]; name != "" {
			o.cronJobs[cronJobID(l, name)] = true
		}
	}
	var cloud []metrics_core.SeriesLifetimeDTO
	for _, s := range series {
		if s.MetricName == cloudResourceMetric {
			cloud = append(cloud, s)
			continue
		}
		o.add(s)
	}
	// After the agents' assets, so a cloud resource joins the host or
	// cluster an agent already reports.
	o.addCloud(cloud, snap.values["cloud_lb_targets"])
	for _, name := range snap.services {
		o.addService(name, snap.serviceMeta[name])
	}
	applyValues(o, snap)
	return o.assets
}

func (o *observation) add(s metrics_core.SeriesLifetimeDTO) {
	l := s.Labels
	cluster := l["k8s_cluster_name"]
	switch {
	case strings.HasPrefix(s.MetricName, "k8s_node_"):
		node := o.ensure(s, o.nodeKey(cluster, l), KindNode, firstNonEmpty(l["k8s_node_name"], l["host_name"]))
		node.ParentKey = o.ensureCluster(s, cluster).Key
		node.Cluster = cluster
		node.Node = node.Name
	case s.MetricName == "k8s_namespace_phase":
		o.ensureNamespace(s, cluster, l["k8s_namespace_name"])
	case s.MetricName == "k8s_deployment_desired", s.MetricName == "k8s_statefulset_desired_pods",
		s.MetricName == "k8s_daemonset_desired_scheduled_nodes", s.MetricName == "k8s_job_active_pods",
		s.MetricName == "k8s_cronjob_active_jobs":
		if kind, name := o.workloadOf(l, ""); name != "" {
			o.ensureWorkload(s, cluster, l["k8s_namespace_name"], kind, name)
		}
	case strings.HasPrefix(s.MetricName, "k8s_pod_"):
		o.ensurePod(s)
	case s.MetricName == "k8s_container_restarts",
		s.MetricName == "container_memory_working_set" && l["k8s_pod_name"] != "":
		pod := o.ensurePod(s)
		if pod == nil || l["k8s_container_name"] == "" {
			return
		}
		container := o.ensure(s, containerKey(pod.Key, l["k8s_container_name"]), KindContainer, l["k8s_container_name"])
		container.ParentKey = pod.Key
		container.NodeKey = pod.NodeKey
		container.Cluster, container.Namespace, container.Node = pod.Cluster, pod.Namespace, pod.Node
	case s.MetricName == "system_memory_usage":
		host := l["host_name"]
		if host == "" {
			return
		}
		if o.nodeHosts[host] {
			// The node's own host metrics: its OS and cloud details.
			if node, ok := o.assets[nodeKey(cluster, host)]; ok {
				mergeAttributes(node, l)
			}
			return
		}
		asset := o.ensure(s, hostKey(host), KindHost, host)
		asset.Node = host
		asset.Subtype = hostSubtype(l["cloud_platform"])
	case s.MetricName == "container_memory_usage_total" && l["k8s_pod_name"] == "":
		name := firstNonEmpty(l["container_name"], l["container_id"])
		if name == "" {
			return
		}
		host := l["host_name"]
		asset := o.ensure(s, dockerKey(host, name), KindDockerContainer, name)
		asset.Node = host
		if host != "" && !o.nodeHosts[host] {
			asset.ParentKey = hostKey(host)
			if _, ok := o.assets[asset.ParentKey]; !ok {
				parent := o.ensure(s, asset.ParentKey, KindHost, host)
				parent.Node = host
				parent.Attributes = Attributes{"host_name": host}
			}
		}
	}
}

func (o *observation) addService(name string, meta spans_core.K8sResourceInfo) {
	key := serviceKey(name)
	asset, ok := o.assets[key]
	if !ok {
		asset = &Asset{
			Kind: KindService, Key: key, Name: name, FirstSeen: o.now, LastSeen: o.now,
			Attributes: Attributes{"service_name": name},
		}
		o.assets[key] = asset
	}
	asset.LastSeen = o.now
	addSource(asset, "traces")
	asset.Namespace = meta.Namespace
	if meta.Namespace != "" {
		asset.Attributes["k8s_namespace_name"] = meta.Namespace
	}
	if meta.Deployment == "" {
		return
	}
	asset.Attributes["k8s_deployment_name"] = meta.Deployment
	// Link to the workload when exactly one cluster runs it.
	var match *Asset
	for _, a := range o.assets {
		if a.Kind == KindWorkload && a.Name == meta.Deployment && a.Namespace == meta.Namespace {
			if match != nil {
				return
			}
			match = a
		}
	}
	if match != nil {
		asset.ParentKey = match.Key
		asset.Cluster = match.Cluster
	}
}

func (o *observation) ensurePod(s metrics_core.SeriesLifetimeDTO) *Asset {
	l := s.Labels
	pod, ns, cluster := l["k8s_pod_name"], l["k8s_namespace_name"], l["k8s_cluster_name"]
	if pod == "" {
		return nil
	}
	asset := o.ensure(s, podKey(cluster, ns, firstNonEmpty(l["k8s_pod_uid"], pod)), KindPod, pod)
	asset.Cluster, asset.Namespace = cluster, ns
	namespace := o.ensureNamespace(s, cluster, ns)
	// A series that doesn't name the workload (the cluster agent's) keeps
	// the one another series of the pod named.
	if kind, name := o.workloadOf(l, pod); name != "" {
		asset.ParentKey = o.ensureWorkload(s, cluster, ns, kind, name).Key
	} else if asset.ParentKey == "" {
		asset.ParentKey = namespace.Key
	}
	if node := o.nodeName(cluster, l); node != "" {
		asset.Node = node
		asset.NodeKey = nodeKey(cluster, node)
	}
	return asset
}

// workloadOf is the workload of a series' pod or Job. Each CronJob run is
// a new Job, so a Job or pod named after a known CronJob belongs to it.
func (o *observation) workloadOf(l map[string]string, pod string) (string, string) {
	if l["k8s_cronjob_name"] == "" {
		if job := l["k8s_job_name"]; job != "" {
			if m := cronJobRun.FindStringSubmatch(job); m != nil && o.cronJobs[cronJobID(l, m[1])] {
				return "CronJob", m[1]
			}
		} else if m := cronJobPod.FindStringSubmatch(pod); m != nil && o.cronJobs[cronJobID(l, m[1])] {
			return "CronJob", m[1]
		}
	}
	return workloadOf(l, pod)
}

func (o *observation) ensureWorkload(s metrics_core.SeriesLifetimeDTO, cluster, ns, kind, name string) *Asset {
	asset := o.ensure(s, workloadKey(cluster, ns, kind, name), KindWorkload, name)
	asset.Subtype = kind
	asset.Cluster, asset.Namespace = cluster, ns
	asset.ParentKey = o.ensureNamespace(s, cluster, ns).Key
	asset.Attributes = Attributes{
		"k8s_cluster_name": cluster, "k8s_namespace_name": ns, "k8s_" + strings.ToLower(kind) + "_name": name,
	}
	if image := s.Labels["container_image_name"]; image != "" {
		asset.Attributes["container_image_name"] = image
		asset.Attributes["container_image_tag"] = s.Labels["container_image_tag"]
	}
	return asset
}

func (o *observation) ensureNamespace(s metrics_core.SeriesLifetimeDTO, cluster, ns string) *Asset {
	asset := o.ensure(s, namespaceKey(cluster, ns), KindNamespace, firstNonEmpty(ns, "(no namespace)"))
	asset.Cluster, asset.Namespace = cluster, ns
	asset.ParentKey = o.ensureCluster(s, cluster).Key
	asset.Attributes = Attributes{"k8s_cluster_name": cluster, "k8s_namespace_name": ns}
	return asset
}

func (o *observation) ensureCluster(s metrics_core.SeriesLifetimeDTO, cluster string) *Asset {
	asset := o.ensure(s, clusterKey(cluster), KindCluster, firstNonEmpty(cluster, "(unnamed cluster)"))
	asset.Cluster = cluster
	for _, key := range []string{"cloud_provider", "cloud_platform", "cloud_region", "cloud_account_id"} {
		if v := s.Labels[key]; v != "" {
			asset.Attributes[key] = v
		}
	}
	asset.Attributes["k8s_cluster_name"] = cluster
	return asset
}

// ensure is the asset at key, created from s if new; its first and last
// seen widen to s's, and its attributes take s's resource labels when s is
// its latest series.
func (o *observation) ensure(s metrics_core.SeriesLifetimeDTO, key, kind, name string) *Asset {
	return o.ensureFrom(s, key, kind, name, "metrics")
}

func (o *observation) ensureFrom(s metrics_core.SeriesLifetimeDTO, key, kind, name, source string) *Asset {
	asset, ok := o.assets[key]
	if !ok {
		asset = &Asset{
			Kind:       kind,
			Key:        key,
			Name:       name,
			FirstSeen:  s.FirstSeen,
			LastSeen:   s.LastSeen,
			Attributes: Attributes{},
		}
		o.assets[key] = asset
		if kind != KindCluster && kind != KindNamespace && kind != KindWorkload {
			mergeAttributes(asset, s.Labels)
		}
	}
	if s.FirstSeen.Before(asset.FirstSeen) {
		asset.FirstSeen = s.FirstSeen
	}
	if s.LastSeen.After(asset.LastSeen) {
		asset.LastSeen = s.LastSeen
		if kind != KindCluster && kind != KindNamespace && kind != KindWorkload {
			mergeAttributes(asset, s.Labels)
		}
	}
	addSource(asset, source)
	return asset
}

// nodeName is the node a pod's series runs on: its host name, or its k8s
// node name translated through the node metrics.
func (o *observation) nodeName(cluster string, l map[string]string) string {
	if h := l["host_name"]; h != "" && o.nodeHosts[h] {
		return h
	}
	if n := l["k8s_node_name"]; n != "" {
		if h, ok := o.nodeHost[cluster+"\x00"+n]; ok {
			return h
		}
		return n
	}
	return l["host_name"]
}

func (o *observation) nodeKey(cluster string, l map[string]string) string {
	return nodeKey(cluster, o.nodeName(cluster, l))
}

// applyValues sets each asset's latest values from the instant queries.
func applyValues(o *observation, snap *snapshot) {
	byHost := map[string]*Asset{}
	for _, a := range o.assets {
		if a.Kind == KindNode || a.Kind == KindHost {
			byHost[a.Node] = a
		}
	}
	lookup := func(key string) *Asset { return o.assets[key] }
	values := func(name string, key func(map[string]string) string) map[string]float64 {
		result := map[string]float64{}
		for _, s := range snap.values[name] {
			if k := key(s.labels); k != "" && !math.IsNaN(s.value) && !math.IsInf(s.value, 0) {
				result[k] += s.value
			}
		}
		return result
	}
	nodeOf := func(l map[string]string) string { return o.nodeKey(l["k8s_cluster_name"], l) }
	hostOf := func(l map[string]string) string { return l["host_name"] }
	podOf := func(l map[string]string) string {
		if l["k8s_pod_name"] == "" {
			return ""
		}
		return podKey(
			l["k8s_cluster_name"],
			l["k8s_namespace_name"],
			firstNonEmpty(l["k8s_pod_uid"], l["k8s_pod_name"]),
		)
	}
	containerOf := func(l map[string]string) string {
		if pod := podOf(l); pod != "" && l["k8s_container_name"] != "" {
			return containerKey(pod, l["k8s_container_name"])
		}
		return ""
	}
	workloadOfKind := func(kind string) func(map[string]string) string {
		return func(l map[string]string) string {
			name := l["k8s_"+strings.ToLower(kind)+"_name"]
			if name == "" {
				return ""
			}
			return workloadKey(l["k8s_cluster_name"], l["k8s_namespace_name"], kind, name)
		}
	}
	dockerOf := func(l map[string]string) string {
		name := firstNonEmpty(l["container_name"], l["container_id"])
		if name == "" || l["k8s_pod_name"] != "" {
			return ""
		}
		return dockerKey(l["host_name"], name)
	}

	// Node and host capacity, by host name.
	cores := values("host_cores", hostOf)
	allocatable := values("node_allocatable_cpu", nodeOf)
	memWorkingSet := values("node_memory_working_set", nodeOf)
	memAvailable := values("node_memory_available", nodeOf)
	nodeCores := map[string]float64{}
	nodeMemory := map[string]float64{}
	for key, a := range o.assets {
		if a.Kind != KindNode {
			continue
		}
		if c := cores[a.Node]; c > 0 {
			nodeCores[key] = c
		} else if c := allocatable[key]; c > 0 {
			nodeCores[key] = c
		}
		if total := memWorkingSet[key] + memAvailable[key]; total > 0 {
			nodeMemory[key] = total
		}
	}

	for key, cpu := range values("node_cpu", nodeOf) {
		if a := lookup(key); a != nil && nodeCores[key] > 0 {
			a.Metrics.CPUPercent = percent(cpu / nodeCores[key])
		}
	}
	for key, ws := range memWorkingSet {
		if a := lookup(key); a != nil && nodeMemory[key] > 0 {
			a.Metrics.MemoryPercent = percent(ws / nodeMemory[key])
		}
	}
	fsCapacity := values("node_fs_capacity", nodeOf)
	for key, used := range values("node_fs_usage", nodeOf) {
		if a := lookup(key); a != nil && fsCapacity[key] > 0 {
			a.Metrics.DiskPercent = percent(used / fsCapacity[key])
		}
	}
	for key, ready := range values("node_ready", nodeOf) {
		if a := lookup(key); a != nil {
			isReady := ready > 0
			a.Metrics.Ready = &isReady
		}
	}

	cpuTotal := values("host_cpu_total", hostOf)
	for host, idle := range values("host_cpu_idle", hostOf) {
		if a := byHost[host]; a != nil && a.Kind == KindHost && cpuTotal[host] > 0 {
			a.Metrics.CPUPercent = percent(1 - idle/cpuTotal[host])
		}
	}
	memTotal := values("host_memory_total", hostOf)
	for host, used := range values("host_memory_used", hostOf) {
		if a := byHost[host]; a != nil && a.Kind == KindHost && memTotal[host] > 0 {
			a.Metrics.MemoryPercent = percent(used / memTotal[host])
		}
	}
	for host, ratio := range values("host_fs_ratio", hostOf) {
		if a := byHost[host]; a != nil && a.Metrics.DiskPercent == nil {
			a.Metrics.DiskPercent = percent(ratio)
		}
	}

	// Pods and containers: a share of their limit when they have one, else
	// of their node.
	setUsage := func(a *Asset, cpu, memory, cpuLimit, memoryLimit float64, hasCPU, hasMemory, hasCPULimit, hasMemoryLimit bool) {
		if hasCPULimit {
			a.Metrics.CPUPercent, a.Metrics.CPUBasis = percent(cpuLimit), "limit"
		} else if hasCPU && nodeCores[a.NodeKey] > 0 {
			a.Metrics.CPUPercent, a.Metrics.CPUBasis = percent(cpu/nodeCores[a.NodeKey]), "node"
		}
		if hasMemoryLimit {
			a.Metrics.MemoryPercent, a.Metrics.MemoryBasis = percent(memoryLimit), "limit"
		} else if hasMemory && nodeMemory[a.NodeKey] > 0 {
			a.Metrics.MemoryPercent, a.Metrics.MemoryBasis = percent(memory/nodeMemory[a.NodeKey]), "node"
		}
	}
	for _, level := range []struct {
		prefix string
		key    func(map[string]string) string
	}{{"pod", podOf}, {"container", containerOf}} {
		cpu := values(level.prefix+"_cpu", level.key)
		memory := values(level.prefix+"_memory", level.key)
		cpuLimit := values(level.prefix+"_cpu_limit", level.key)
		memoryLimit := values(level.prefix+"_memory_limit", level.key)
		restarts1h := values(level.prefix+"_restarts_1h", level.key)
		restarts24h := values(level.prefix+"_restarts_24h", level.key)
		for key, a := range o.assets {
			if (level.prefix == "pod" && a.Kind != KindPod) ||
				(level.prefix == "container" && a.Kind != KindContainer) {
				continue
			}
			c, hasCPU := cpu[key]
			m, hasMemory := memory[key]
			cl, hasCPULimit := cpuLimit[key]
			ml, hasMemoryLimit := memoryLimit[key]
			setUsage(a, c, m, cl, ml, hasCPU, hasMemory, hasCPULimit, hasMemoryLimit)
			// increase() extrapolates to the window's edges; restarts are whole.
			if v, ok := restarts1h[key]; ok {
				a.Metrics.Restarts1h = whole(v)
			}
			if v, ok := restarts24h[key]; ok {
				a.Metrics.Restarts24h = whole(v)
			}
		}
	}
	for key, phase := range values("pod_phase", podOf) {
		if a := lookup(key); a != nil {
			a.Metrics.Phase = podPhases[phase]
		}
	}
	for _, a := range o.assets {
		if a.Kind != KindPod {
			continue
		}
		lines := snap.podLines[a.Name]
		if lines >= minLinesForErrorShare {
			a.Metrics.ErrorShare = percent(snap.podErrors[a.Name] / lines)
			addSource(a, "logs")
		}
	}

	for _, w := range []struct{ kind, desired, available string }{
		{"Deployment", "deployment_desired", "deployment_available"},
		{"StatefulSet", "statefulset_desired", "statefulset_ready"},
		{"DaemonSet", "daemonset_desired", "daemonset_ready"},
	} {
		available := values(w.available, workloadOfKind(w.kind))
		for key, desired := range values(w.desired, workloadOfKind(w.kind)) {
			if a := lookup(key); a != nil {
				a.Metrics.Desired = round(desired)
				a.Metrics.Available = round(available[key])
			}
		}
	}

	dockerMemory := values("docker_memory_percent", dockerOf)
	for key, cpu := range values("docker_cpu_percent", dockerOf) {
		if a := lookup(key); a != nil {
			a.Metrics.CPUPercent = round(cpu)
		}
	}
	for key, memory := range dockerMemory {
		if a := lookup(key); a != nil {
			a.Metrics.MemoryPercent = round(memory)
		}
	}
}

// reconcile merges what this build observed into what the project's
// inventory held: lifecycle states, health with reasons, and the changes
// between the two. It returns the assets to save, the changes, and the ids
// of assets past retention.
func reconcile(
	projectID uuid.UUID,
	existing []Asset,
	observed map[string]*Asset,
	alerts []firingAlert,
	now time.Time,
	retention time.Duration,
) ([]*Asset, []Change, []uuid.UUID) {
	previous := map[string]Asset{}
	all := map[string]*Asset{}
	for _, a := range existing {
		previous[a.Key] = a
		copied := a
		// The last phase known says how a run ended after its pod is gone.
		copied.Metrics = Metrics{Phase: a.Metrics.Phase}
		all[a.Key] = &copied
	}
	for key, obs := range observed {
		obs.ProjectID = projectID
		if before, ok := previous[key]; ok {
			obs.ID = before.ID
			if before.FirstSeen.Before(obs.FirstSeen) {
				obs.FirstSeen = before.FirstSeen
			}
			if before.LastSeen.After(obs.LastSeen) {
				obs.LastSeen = before.LastSeen
			}
			if len(obs.Attributes) == 0 {
				obs.Attributes = before.Attributes
			}
		}
		all[key] = obs
	}

	for _, a := range all {
		if cronJob := runCronJob(a, all); cronJob != nil {
			a.ParentKey = cronJob.Key
		}
	}
	runs := map[string]bool{}
	for key, a := range all {
		a.State = lifecycle(a, now)
		if isRun(a, all) {
			runs[key] = true
		}
	}
	reporting := reportingScopes(all)
	var stale []uuid.UUID
	drop := func(key string) {
		if id := all[key].ID; id != uuid.Nil {
			stale = append(stale, id)
		}
		delete(all, key)
	}
	for key, a := range all {
		gone := now.Sub(a.LastSeen)
		switch {
		case gone > retention,
			a.State == StateTerminated && (a.Kind == KindPod || a.Kind == KindContainer) && gone > keepTerminatedPods,
			runs[key] && a.Kind == KindPod && a.State == StateTerminated &&
				(a.Metrics.Phase != "Failed" || gone > keepFailedRuns),
			isJob(a) && (a.State == StateTerminated && gone > keepFailedRuns || cronJobOf(a, all) != nil),
			observed[key] == nil && isCronJobAsDeployment(a, all),
			replaced(a, reporting, now):
			drop(key)
		}
	}
	// A run's containers go with its pod.
	for key, a := range all {
		if _, ok := all[a.ParentKey]; runs[key] && a.Kind == KindContainer && !ok {
			drop(key)
		}
	}
	assessHealth(all, alerts, now)

	firstBuild := len(existing) == 0
	var changes []Change
	result := make([]*Asset, 0, len(all))
	for _, key := range slices.Sorted(maps.Keys(all)) {
		a := all[key]
		a.UpdatedAt = now
		result = append(result, a)
		before, ok := previous[key]
		change := func(kind, from, to, detail string) {
			changes = append(changes, Change{
				ProjectID: projectID, AssetKey: key, AssetName: a.Name, Kind: a.Kind,
				Type: kind, From: from, To: to, Detail: detail, At: now,
			})
		}
		switch {
		case !ok:
			// Every run's pod is new; a failed one shows as a health change.
			if !firstBuild && !runs[key] {
				change(ChangeNew, "", a.State, describeNew(a))
			}
		default:
			if before.State != a.State && (before.State != StateNew || a.State != StateActive) && !runs[key] {
				change(ChangeState, before.State, a.State, "")
			}
			if before.Health != a.Health {
				change(ChangeHealth, before.Health, a.Health, firstReason(a))
			}
		}
	}
	return result, changes, stale
}

// reportingScopes is when each source last reported anything: a cluster's
// nodes (by cluster) and a cloud account's region (by cloudScope).
func reportingScopes(all map[string]*Asset) map[string]time.Time {
	latest := map[string]time.Time{}
	note := func(scope string, at time.Time) {
		if at.After(latest[scope]) {
			latest[scope] = at
		}
	}
	for _, a := range all {
		if a.Kind == KindNode && a.Cluster != "" {
			note("nodes:"+a.Cluster, a.LastSeen)
		}
		if slices.Contains(a.Sources, "cloud") {
			note(cloudScope(a), a.LastSeen)
		}
	}
	return latest
}

// replaced: the cloud says a resource is terminated, or a node or cloud
// resource has been missing past removeReplacedAfter while its cluster's
// other nodes, or its cloud account and region, still report. If the
// collector or the scans stop instead, everything stays, not reporting.
func replaced(a *Asset, reporting map[string]time.Time, now time.Time) bool {
	gone := now.Sub(a.LastSeen)
	if slices.Contains(a.Sources, "cloud") {
		if a.Attributes["cloud_resource_state"] == "terminated" {
			return true
		}
		stale := cloudStaleAfter(a)
		if gone > max(removeReplacedAfter, stale) && now.Sub(reporting[cloudScope(a)]) < stale {
			return true
		}
	}
	return a.Kind == KindNode && a.Cluster != "" && gone > removeReplacedAfter &&
		now.Sub(reporting["nodes:"+a.Cluster]) < notReportingAfter
}

// lifecycle is an asset's state from when it was first and last seen.
func lifecycle(a *Asset, now time.Time) string {
	gone := now.Sub(a.LastSeen)
	transient := a.Kind == KindPod || a.Kind == KindContainer || a.Kind == KindDockerContainer || isJob(a)
	switch {
	case a.Kind == KindPod && a.Metrics.Phase == "Succeeded":
		return StateTerminated
	case transient && gone >= podGoneAfter, !transient && gone >= assetGoneAfter:
		return StateTerminated
	case gone >= notReportingAfter && !cloudOnly(a), gone >= cloudStaleAfter(a):
		return StateNotReporting
	case now.Sub(a.FirstSeen) < newFor:
		return StateNew
	default:
		return StateActive
	}
}

// assessHealth sets every asset's health and reasons: leaves from their
// own values, then workloads, namespaces and clusters from their children.
func assessHealth(all map[string]*Asset, alerts []firingAlert, now time.Time) {
	children := map[string][]*Asset{}
	for _, a := range all {
		a.Reasons = nil
		if a.ParentKey != "" {
			children[a.ParentKey] = append(children[a.ParentKey], a)
		}
	}
	for _, kinds := range [][]string{
		{
			KindNode, KindHost, KindPod, KindContainer, KindDockerContainer,
			KindLoadBalancer, KindDatabase, KindBucket, KindFunction, KindScalingGroup,
		},
		{KindWorkload},
		{KindNamespace, KindService},
		{KindCluster},
	} {
		for _, a := range all {
			if slices.Contains(kinds, a.Kind) {
				assess(a, children[a.Key], all, now)
				for _, alert := range alerts {
					if alertMatches(a, alert.labels) {
						health := HealthWarning
						if strings.EqualFold(alert.severity, "critical") {
							health = HealthCritical
						}
						a.Reasons = append(a.Reasons, Reason{health, "Alert \"" + alert.rule + "\" is firing"})
					}
				}
				a.Health = worstOf(a)
			}
		}
	}
}

// assess appends a's own reasons: its state, its values against the
// thresholds, and for a parent its children's.
func assess(a *Asset, kids []*Asset, all map[string]*Asset, now time.Time) {
	m := a.Metrics
	add := func(health, format string, args ...any) {
		a.Reasons = append(a.Reasons, Reason{health, fmt.Sprintf(format, args...)})
	}
	if a.State == StateTerminated {
		add(HealthInactive, "%s", terminatedReason(a))
		return
	}
	if a.State == StateNotReporting {
		minutes := int(now.Sub(a.LastSeen).Minutes())
		switch a.Kind {
		case KindNode, KindHost, KindCluster:
			add(HealthCritical, "No data for %d min", minutes)
		default:
			add(HealthUnknown, "No data for %d min", minutes)
		}
		return
	}
	if assessCloud(a, add) {
		return
	}
	switch a.Kind {
	case KindNode, KindHost:
		if m.Ready != nil && !*m.Ready {
			add(HealthCritical, "Node is NotReady")
		}
		threshold(a, m.CPUPercent, "CPU", 90, 0)
		threshold(a, m.MemoryPercent, "Memory", 90, 97)
		threshold(a, m.DiskPercent, "Filesystem", 85, 95)
	case KindPod, KindContainer:
		switch m.Phase {
		case "Failed":
			add(HealthCritical, "Pod phase is Failed")
		case "Pending":
			if now.Sub(a.FirstSeen) > notReportingAfter {
				add(HealthWarning, "Pending for %d min", int(now.Sub(a.FirstSeen).Minutes()))
			}
		case "Unknown":
			add(HealthWarning, "Pod phase is Unknown")
		}
		if m.Restarts1h != nil && *m.Restarts1h >= 10 {
			add(HealthCritical, "%.0f container restarts in the last hour (crash looping)", *m.Restarts1h)
		} else if m.Restarts1h != nil && *m.Restarts1h >= 3 {
			add(HealthWarning, "%.0f container restarts in the last hour", *m.Restarts1h)
		}
		if m.MemoryBasis == "limit" {
			threshold(a, m.MemoryPercent, "Memory (of its limit)", 90, 0)
		}
		if m.ErrorShare != nil && *m.ErrorShare > 5 {
			add(HealthWarning, "Error logs are %.1f%% of its lines in the last 15 min", *m.ErrorShare)
		}
	case KindDockerContainer:
		threshold(a, m.MemoryPercent, "Memory", 90, 0)
	case KindWorkload:
		if m.Desired != nil && *m.Desired > 0 && m.Available != nil {
			switch {
			case *m.Available == 0:
				add(HealthCritical, "No replicas available (0 of %.0f)", *m.Desired)
			case *m.Available < *m.Desired:
				add(HealthWarning, "%.0f of %.0f replicas available", *m.Available, *m.Desired)
			}
		}
		rollUp(a, kids, HealthWarning)
	case KindNamespace:
		rollUp(a, kids, HealthWarning)
	case KindCluster:
		reporting, down := 0, 0
		var namespaces []*Asset
		for _, n := range kids {
			if n.Kind != KindNode {
				namespaces = append(namespaces, n)
				continue
			}
			if n.State == StateTerminated {
				continue
			}
			reporting++
			if n.Health == HealthCritical {
				down++
			}
		}
		if reporting > 0 && down == reporting {
			add(HealthCritical, "No node is healthy")
		} else if down > 0 {
			add(HealthWarning, "%d of %d nodes critical", down, reporting)
		}
		rollUp(a, namespaces, HealthWarning)
	case KindService:
		if parent, ok := all[a.ParentKey]; ok && parent.Health != "" && parent.Health != HealthHealthy &&
			parent.Health != HealthInactive {
			add(capHealth(parent.Health, HealthWarning), "Workload %s: %s", parent.Name, firstReason(parent))
		}
		if a.ParentKey == "" && len(a.Reasons) == 0 {
			add(HealthUnknown, "Seen only in traces")
		}
	}
}

// rollUp gives a the worst health among its live children, capped.
func rollUp(a *Asset, kids []*Asset, limit string) {
	var worst *Asset
	count := 0
	for _, k := range kids {
		if k.Health != HealthWarning && k.Health != HealthCritical {
			continue
		}
		count++
		if worst == nil || healthRank[k.Health] < healthRank[worst.Health] {
			worst = k
		}
	}
	if worst == nil {
		return
	}
	text := fmt.Sprintf("%s %s: %s", kindLabel(worst), worst.Name, firstReason(worst))
	if count > 1 {
		text += fmt.Sprintf(" (and %d more)", count-1)
	}
	a.Reasons = append(a.Reasons, Reason{capHealth(worst.Health, limit), text})
}

func threshold(a *Asset, value *float64, what string, warnAt, criticalAt float64) {
	if value == nil {
		return
	}
	switch {
	case criticalAt > 0 && *value >= criticalAt:
		a.Reasons = append(a.Reasons, Reason{HealthCritical, fmt.Sprintf("%s at %.0f%%", what, *value)})
	case *value >= warnAt:
		a.Reasons = append(a.Reasons, Reason{HealthWarning, fmt.Sprintf("%s at %.0f%%", what, *value)})
	}
}

func worstOf(a *Asset) string {
	if len(a.Reasons) == 0 {
		if a.Kind == KindService {
			return HealthUnknown
		}
		return HealthHealthy
	}
	worst := a.Reasons[0].Health
	for _, r := range a.Reasons[1:] {
		if healthRank[r.Health] < healthRank[worst] {
			worst = r.Health
		}
	}
	// Reasons sorted worst first, so the first one explains the colour.
	sort.SliceStable(
		a.Reasons,
		func(i, j int) bool { return healthRank[a.Reasons[i].Health] < healthRank[a.Reasons[j].Health] },
	)
	return worst
}

// alertMatches: the alert's labels name this asset (its identifying label)
// and agree with every other identifying label the asset has.
func alertMatches(a *Asset, labels map[string]string) bool {
	identity := map[string]string{}
	switch a.Kind {
	case KindPod:
		identity["k8s_pod_name"] = a.Name
	case KindContainer:
		identity["k8s_container_name"] = a.Name
		identity["k8s_pod_name"] = a.Attributes["k8s_pod_name"]
	case KindNode:
		if labels["k8s_node_name"] != "" {
			identity["k8s_node_name"] = a.Attributes["k8s_node_name"]
		} else {
			identity["host_name"] = a.Node
		}
	case KindHost:
		identity["host_name"] = a.Node
	case KindWorkload:
		identity["k8s_"+strings.ToLower(a.Subtype)+"_name"] = a.Name
	case KindNamespace:
		if labels["k8s_pod_name"] != "" || labels["k8s_deployment_name"] != "" {
			return false
		}
		identity["k8s_namespace_name"] = a.Namespace
	case KindCluster:
		if labels["k8s_namespace_name"] != "" || labels["host_name"] != "" || labels["k8s_node_name"] != "" {
			return false
		}
		identity["k8s_cluster_name"] = a.Cluster
	case KindService:
		identity["service_name"] = a.Name
	default:
		return false
	}
	for key, want := range identity {
		if want == "" || labels[key] != want {
			return false
		}
	}
	for key, have := range map[string]string{"k8s_namespace_name": a.Namespace, "k8s_cluster_name": a.Cluster} {
		if got := labels[key]; got != "" && have != "" && got != have {
			return false
		}
	}
	return true
}

func workloadOf(l map[string]string, pod string) (string, string) {
	for _, w := range workloadLabels {
		if v := l[w.label]; v != "" {
			return w.kind, v
		}
	}
	if rs := l["k8s_replicaset_name"]; rs != "" {
		if m := replicaSet.FindStringSubmatch(rs); m != nil {
			return "Deployment", m[1]
		}
		return "ReplicaSet", rs
	}
	if pod == "" {
		return "", ""
	}
	// A CronJob run's pod whose CronJob isn't known; its all-digit middle
	// part would read as a Deployment's pod-template hash.
	if m := cronJobPod.FindStringSubmatch(pod); m != nil && len(m[2]) >= 8 {
		return "", ""
	}
	if m := deploymentPod.FindStringSubmatch(pod); m != nil {
		return "Deployment", m[1]
	}
	if m := statefulPod.FindStringSubmatch(pod); m != nil {
		return "StatefulSet", m[1]
	}
	return "", ""
}

func mergeAttributes(a *Asset, labels map[string]string) {
	for key, value := range labels {
		if value == "" || pointLabels[key] || len(a.Attributes) >= maxAttributes && a.Attributes[key] == "" {
			continue
		}
		if a.Kind == KindPod && strings.HasPrefix(key, "k8s_container_") {
			continue
		}
		a.Attributes[key] = value
	}
}

// isRun: a pod or container of a Job or CronJob, which comes and goes with
// every run.
func isRun(a *Asset, all map[string]*Asset) bool {
	parent := a.ParentKey
	switch a.Kind {
	case KindPod:
	case KindContainer:
		pod, ok := all[a.ParentKey]
		if !ok {
			return false
		}
		parent = pod.ParentKey
	default:
		return false
	}
	return strings.Contains(parent, "/CronJob/") || strings.Contains(parent, "/Job/")
}

func isJob(a *Asset) bool { return a.Kind == KindWorkload && a.Subtype == "Job" }

// cronJobOf is the CronJob a Job workload is one run of, if known; such a
// Job was stored by an earlier build that didn't fold runs into their CronJob.
func cronJobOf(a *Asset, all map[string]*Asset) *Asset {
	m := cronJobRun.FindStringSubmatch(a.Name)
	if m == nil || !isJob(a) {
		return nil
	}
	return all[workloadKey(a.Cluster, a.Namespace, "CronJob", m[1])]
}

// runCronJob is the CronJob a stored pod is a run of when an earlier build
// parented it elsewhere: to its namespace (no series named its workload),
// to its own Job, or to a Deployment its scheduled minute looked like the
// pod-template hash of.
func runCronJob(a *Asset, all map[string]*Asset) *Asset {
	if a.Kind != KindPod || strings.Contains(a.ParentKey, "/CronJob/") {
		return nil
	}
	m := cronJobPod.FindStringSubmatch(a.Name)
	if m == nil || len(m[2]) < 8 {
		return nil
	}
	return all[workloadKey(a.Cluster, a.Namespace, "CronJob", m[1])]
}

// isCronJobAsDeployment: a Deployment an earlier build made up from a
// CronJob's run pods.
func isCronJobAsDeployment(a *Asset, all map[string]*Asset) bool {
	return a.Kind == KindWorkload && a.Subtype == "Deployment" &&
		all[workloadKey(a.Cluster, a.Namespace, "CronJob", a.Name)] != nil
}

func cronJobID(l map[string]string, name string) string {
	return l["k8s_cluster_name"] + "\x00" + l["k8s_namespace_name"] + "\x00" + name
}

func addSource(a *Asset, source string) {
	if !slices.Contains(a.Sources, source) {
		a.Sources = append(a.Sources, source)
	}
}

func hostSubtype(platform string) string {
	switch platform {
	case "aws_ec2":
		return "EC2 instance"
	case "azure_vm":
		return "Azure VM"
	case "gcp_compute_engine":
		return "GCE instance"
	}
	return ""
}

func terminatedReason(a *Asset) string {
	if a.Kind == KindPod && a.Metrics.Phase == "Succeeded" {
		return "Completed"
	}
	return "No data since " + a.LastSeen.Format("2006-01-02 15:04 UTC")
}

func describeNew(a *Asset) string {
	if a.Subtype != "" {
		return a.Subtype
	}
	return kindLabel(a)
}

func kindLabel(a *Asset) string {
	switch a.Kind {
	case KindWorkload:
		return firstNonEmpty(a.Subtype, "Workload")
	case KindDockerContainer:
		return "Docker container"
	case KindHost:
		return firstNonEmpty(a.Subtype, "Host")
	}
	return strings.ToUpper(a.Kind[:1]) + a.Kind[1:]
}

func firstReason(a *Asset) string {
	if len(a.Reasons) == 0 {
		return ""
	}
	return a.Reasons[0].Text
}

func capHealth(health, limit string) string {
	if healthRank[health] < healthRank[limit] {
		return limit
	}
	return health
}

func percent(ratio float64) *float64 { return round(ratio * 100) }

func whole(v float64) *float64 {
	r := math.Round(v)
	return &r
}

func round(v float64) *float64 {
	r := math.Round(v*10) / 10
	return &r
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func clusterKey(cluster string) string { return "cluster:" + cluster }

func namespaceKey(cluster, ns string) string { return "ns:" + cluster + "/" + ns }

func nodeKey(cluster, node string) string { return "node:" + cluster + "/" + node }

func hostKey(host string) string { return "host:" + host }

func podKey(cluster, ns, id string) string { return "pod:" + cluster + "/" + ns + "/" + id }

func workloadKey(cluster, ns, kind, name string) string {
	return "wl:" + cluster + "/" + ns + "/" + kind + "/" + name
}

func containerKey(podKey, name string) string {
	return "ctr:" + strings.TrimPrefix(podKey, "pod:") + "/" + name
}

func dockerKey(host, name string) string { return "dctr:" + host + "/" + name }

func serviceKey(name string) string { return "svc:" + name }
