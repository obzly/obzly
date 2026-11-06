package inventory

import (
	"slices"
	"strconv"
	"strings"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
)

// cloudResourceMetric is what a probe's cloud scan sends per resource
// (internal/cloud/discovery): value 1, its identity and attributes as labels.
const cloudResourceMetric = "cloud_resource_info"

// cloudNotReportingAfter: a resource only the cloud reports is missing
// after three of its connection's health scans (cloud_refresh_seconds;
// every 15 minutes by default), and never sooner than this.
const cloudNotReportingAfter = 50 * time.Minute

// cloudKinds maps resource types without an agent counterpart to a kind
// and the subtype shown with it.
var cloudKinds = map[string]struct{ kind, subtype string }{
	"aws_load_balancer":         {KindLoadBalancer, "Elastic Load Balancer"},
	"aws_rds_instance":          {KindDatabase, "RDS"},
	"aws_s3_bucket":             {KindBucket, "S3 bucket"},
	"aws_lambda_function":       {KindFunction, "Lambda"},
	"aws_autoscaling_group":     {KindScalingGroup, "Auto Scaling group"},
	"azure_load_balancer":       {KindLoadBalancer, "Azure Load Balancer"},
	"azure_application_gateway": {KindLoadBalancer, "Application Gateway"},
	"azure_sql_database":        {KindDatabase, "Azure SQL"},
	"azure_postgresql_server":   {KindDatabase, "PostgreSQL flexible server"},
	"azure_storage_account":     {KindBucket, "Storage account"},
	"azure_function_app":        {KindFunction, "Function app"},
	"azure_vm_scale_set":        {KindScalingGroup, "VM scale set"},
}

// Cloud states meaning a resource is deliberately not running.
var cloudStopped = map[string]bool{
	"stopped": true, "stopping": true, "shutting-down": true, "terminated": true, "deallocated": true,
	"deallocating": true, "paused": true,
}

// Cloud states meaning all is well; anything else is worth a look.
var cloudFine = map[string]bool{
	"": true, "running": true, "available": true, "active": true, "online": true, "ready": true,
	"succeeded": true, "provisioning": true,
}

// addCloud merges the cloud's resources: a VM joins the agent host with
// its instance ID, a managed cluster the cluster of its name, the rest
// are assets of their own.
func (o *observation) addCloud(series []metrics_core.SeriesLifetimeDTO, targets []sample) {
	hostsByID := map[string]*Asset{}
	for _, a := range o.assets {
		if id := a.Attributes["host_id"]; id != "" && (a.Kind == KindHost || a.Kind == KindNode) {
			hostsByID[id] = a
		}
	}
	targetCounts := map[string]map[string]float64{}
	for _, t := range targets {
		id := t.labels["cloud_resource_id"]
		if targetCounts[id] == nil {
			targetCounts[id] = map[string]float64{}
		}
		targetCounts[id][t.labels["health"]] += t.value
	}
	// Oldest first, so the latest series' state and labels win.
	slices.SortStableFunc(
		series,
		func(a, b metrics_core.SeriesLifetimeDTO) int { return a.LastSeen.Compare(b.LastSeen) },
	)
	for _, s := range series {
		l := s.Labels
		typ, id, name := l["cloud_resource_type"], l["cloud_resource_id"], firstNonEmpty(
			l["cloud_resource_name"],
			l["cloud_resource_id"],
		)
		if typ == "" || id == "" {
			continue
		}
		switch typ {
		case "aws_ec2_instance", "azure_virtual_machine":
			if host, ok := hostsByID[l["host_id"]]; ok {
				addSource(host, "cloud")
				copyCloudLabels(host, l)
				continue
			}
			host := o.ensureFrom(s, "host:cloud/"+id, KindHost, name, "cloud")
			host.Subtype = map[string]string{"aws_ec2_instance": "EC2 instance", "azure_virtual_machine": "Azure VM"}[typ]
			copyCloudLabels(host, l)
		case "aws_eks_cluster", "azure_aks_cluster":
			clusterName := firstNonEmpty(l["k8s_cluster_name"], name)
			if cluster, ok := o.assets[clusterKey(clusterName)]; ok {
				addSource(cluster, "cloud")
				copyCloudLabels(cluster, l)
				continue
			}
			cluster := o.ensureFrom(s, clusterKey(clusterName), KindCluster, clusterName, "cloud")
			cluster.Cluster = clusterName
			copyCloudLabels(cluster, l)
		default:
			kind, ok := cloudKinds[typ]
			if !ok {
				continue
			}
			asset := o.ensureFrom(s, "cloud:"+typ+"/"+id, kind.kind, name, "cloud")
			asset.Subtype = kind.subtype
			if lbType := l["lb_type"]; lbType != "" && typ == "aws_load_balancer" {
				asset.Subtype = map[string]string{
					"application": "Application Load Balancer", "network": "Network Load Balancer",
					"gateway": "Gateway Load Balancer",
				}[lbType]
			}
			copyCloudLabels(asset, l)
			if counts, ok := targetCounts[id]; ok {
				healthy, total := counts["healthy"], 0.0
				for _, count := range counts {
					total += count
				}
				asset.Attributes["targets_healthy"] = strconv.Itoa(int(healthy))
				asset.Attributes["targets_total"] = strconv.Itoa(int(total))
			}
		}
	}
}

// copyCloudLabels takes the cloud's identity and attributes, without
// overwriting what an agent reports. Health comes only from the latest
// series: a resource without status checks now has none.
func copyCloudLabels(a *Asset, labels map[string]string) {
	delete(a.Attributes, "cloud_health")
	for key, value := range labels {
		if value == "" || key == "cloud_connection_id" {
			continue
		}
		if _, taken := a.Attributes[key]; taken && !strings.HasPrefix(key, "cloud_") {
			continue
		}
		if len(a.Attributes) >= maxAttributes && a.Attributes[key] == "" {
			continue
		}
		a.Attributes[key] = value
	}
}

// assessCloud adds reasons from what the cloud says; true when the
// resource is stopped, so its other values don't matter.
func assessCloud(a *Asset, add func(health, format string, args ...any)) bool {
	state, ok := a.Attributes["cloud_resource_state"]
	if !ok {
		return false
	}
	provider := map[string]string{"aws": "AWS", "azure": "Azure"}[a.Attributes["cloud_provider"]]
	if cloudStopped[state] {
		add(HealthInactive, "%s in %s", strings.ToUpper(state[:1])+state[1:], provider)
		return true
	}
	if !cloudFine[state] {
		add(HealthWarning, "%s reports %q", provider, state)
	}
	switch a.Attributes["cloud_health"] {
	case "unhealthy":
		add(HealthCritical, "%s reports it unhealthy (failed status checks or unavailable)", provider)
	case "degraded":
		add(HealthWarning, "%s reports it degraded", provider)
	}
	if total, err := strconv.Atoi(a.Attributes["targets_total"]); err == nil && total > 0 {
		healthy, _ := strconv.Atoi(a.Attributes["targets_healthy"])
		switch {
		case healthy == 0:
			add(HealthCritical, "No healthy targets (%d registered)", total)
		case healthy < total:
			add(HealthWarning, "%d of %d targets unhealthy", total-healthy, total)
		}
	}
	if desired, err := strconv.Atoi(a.Attributes["desired"]); err == nil && a.Kind == KindScalingGroup {
		if healthy, err := strconv.Atoi(a.Attributes["healthy"]); err == nil && healthy < desired {
			add(HealthWarning, "%d of %d desired instances healthy", healthy, desired)
		}
	}
	return false
}

// cloudScope is the cloud account and region a resource was listed in.
func cloudScope(a *Asset) string {
	return "cloud:" + a.Attributes["cloud_provider"] + "/" + a.Attributes["cloud_account_id"] + "/" +
		a.Attributes["cloud_region"]
}

// cloudOnly: only a cloud scan reports it, so it's seen every scan, not
// every minute.
func cloudOnly(a *Asset) bool {
	return len(a.Sources) == 1 && a.Sources[0] == "cloud"
}

// cloudStaleAfter is how long a cloud-only asset can go unseen before it's
// not reporting: three of its connection's refreshes and a little slack.
func cloudStaleAfter(a *Asset) time.Duration {
	refresh, err := strconv.Atoi(a.Attributes["cloud_refresh_seconds"])
	if err != nil || refresh <= 0 {
		return cloudNotReportingAfter
	}
	return max(cloudNotReportingAfter, 3*time.Duration(refresh)*time.Second+5*time.Minute)
}
