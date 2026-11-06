package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	logs_core "obzly/internal/features/logs/core"
	metrics_core "obzly/internal/features/metrics/core"
	metrics_promql "obzly/internal/features/metrics/promql"
	spans_core "obzly/internal/features/spans/core"
	"obzly/internal/storage"

	"github.com/google/uuid"
	prom_labels "github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
)

// anchorMetrics are the series the inventory discovers assets from: one
// or a few per asset, so discovery reads little.
var anchorMetrics = []string{
	"k8s_node_memory_working_set", "k8s_node_condition_ready",
	"k8s_pod_memory_working_set", "k8s_pod_phase",
	"container_memory_working_set", "k8s_container_restarts",
	"k8s_deployment_desired", "k8s_statefulset_desired_pods", "k8s_daemonset_desired_scheduled_nodes",
	"k8s_job_active_pods", "k8s_cronjob_active_jobs", "k8s_namespace_phase",
	"system_memory_usage", "container_memory_usage_total",
	cloudResourceMetric,
}

const (
	cluster    = "k8s_cluster_name"
	podLabels  = cluster + ", k8s_namespace_name, k8s_pod_name, k8s_pod_uid, host_name, k8s_node_name"
	nodeLabels = cluster + ", host_name, k8s_node_name"
	// fsReal skips read-only and virtual filesystems (snap squashfs mounts
	// are always 100% full).
	fsReal = `mode!="ro", type!~"tmpfs|devtmpfs|squashfs|overlay|iso9660|nsfs|proc|sysfs"`
)

// valueQueries are the instant queries for the latest values, by the name
// applyValues reads them under. Old and new kubeletstats names both match.
var valueQueries = map[string]string{
	// Scans run every 15 min by default; two hours covers hourly ones.
	"cloud_lb_targets":        `max by (cloud_resource_id, health) (last_over_time(cloud_lb_targets[2h]))`,
	"node_cpu":                `sum by (` + nodeLabels + `) ({__name__=~"k8s_node_cpu_usage|k8s_node_cpu_utilization"})`,
	"node_allocatable_cpu":    `max by (` + nodeLabels + `) (k8s_node_allocatable_cpu)`,
	"node_memory_working_set": `sum by (` + nodeLabels + `) (k8s_node_memory_working_set)`,
	"node_memory_available":   `sum by (` + nodeLabels + `) (k8s_node_memory_available)`,
	"node_fs_usage":           `sum by (` + nodeLabels + `) (k8s_node_filesystem_usage)`,
	"node_fs_capacity":        `sum by (` + nodeLabels + `) (k8s_node_filesystem_capacity)`,
	"node_ready":              `min by (` + nodeLabels + `) (k8s_node_condition_ready)`,
	"host_cores":              `max by (host_name) (system_cpu_logical_count)`,
	"host_cpu_idle":           `sum by (host_name) (rate(system_cpu_time{state="idle"}[5m]))`,
	"host_cpu_total":          `sum by (host_name) (rate(system_cpu_time[5m]))`,
	"host_memory_used":        `sum by (host_name) (system_memory_usage{state="used"})`,
	"host_memory_total":       `sum by (host_name) (system_memory_usage)`,
	"host_fs_ratio": `max by (host_name) (sum by (host_name, mountpoint) (system_filesystem_usage{state="used", ` + fsReal +
		`}) / sum by (host_name, mountpoint) (system_filesystem_usage{` + fsReal + `}))`,
	"pod_cpu":                `sum by (` + podLabels + `) ({__name__=~"k8s_pod_cpu_usage|k8s_pod_cpu_utilization"})`,
	"pod_memory":             `sum by (` + podLabels + `) (k8s_pod_memory_working_set)`,
	"pod_cpu_limit":          `max by (` + podLabels + `) (k8s_pod_cpu_limit_utilization)`,
	"pod_memory_limit":       `max by (` + podLabels + `) (k8s_pod_memory_limit_utilization)`,
	"pod_phase":              `max by (` + podLabels + `) (k8s_pod_phase)`,
	"container_cpu":          `sum by (` + podLabels + `, k8s_container_name) ({__name__=~"container_cpu_usage|container_cpu_utilization", k8s_pod_name!=""})`,
	"container_memory":       `sum by (` + podLabels + `, k8s_container_name) (container_memory_working_set{k8s_pod_name!=""})`,
	"container_cpu_limit":    `max by (` + podLabels + `, k8s_container_name) (k8s_container_cpu_limit_utilization)`,
	"container_memory_limit": `max by (` + podLabels + `, k8s_container_name) (k8s_container_memory_limit_utilization)`,
	"container_restarts_1h":  `sum by (` + podLabels + `, k8s_container_name) (increase(k8s_container_restarts[1h]))`,
	"container_restarts_24h": `sum by (` + podLabels + `, k8s_container_name) (increase(k8s_container_restarts[24h]))`,
	"deployment_desired":     `max by (` + cluster + `, k8s_namespace_name, k8s_deployment_name) (k8s_deployment_desired)`,
	"deployment_available":   `max by (` + cluster + `, k8s_namespace_name, k8s_deployment_name) (k8s_deployment_available)`,
	"statefulset_desired":    `max by (` + cluster + `, k8s_namespace_name, k8s_statefulset_name) (k8s_statefulset_desired_pods)`,
	"statefulset_ready":      `max by (` + cluster + `, k8s_namespace_name, k8s_statefulset_name) (k8s_statefulset_ready_pods)`,
	"daemonset_desired": `max by (` + cluster + `, k8s_namespace_name, k8s_daemonset_name) ` +
		`(k8s_daemonset_desired_scheduled_nodes)`,
	"daemonset_ready":       `max by (` + cluster + `, k8s_namespace_name, k8s_daemonset_name) (k8s_daemonset_ready_nodes)`,
	"docker_cpu_percent":    `avg by (host_name, container_name, container_id) (container_cpu_utilization{k8s_pod_name=""})`,
	"docker_memory_percent": `avg by (host_name, container_name, container_id) (container_memory_percent{k8s_pod_name=""})`,
}

// hourlyQueries change slowly and read a day of data: run at most hourly
// per project.
var hourlyQueries = map[string]bool{"container_restarts_24h": true}

// podFromContainer: a pod's value is its containers' summed (applyValues
// sums the samples of each pod), so one query serves both levels.
var podFromContainer = map[string]string{
	"pod_restarts_1h":  "container_restarts_1h",
	"pod_restarts_24h": "container_restarts_24h",
}

// errorLevels are the log levels that count as errors.
var errorLevels = []any{"error", "fatal", "critical", "panic"}

// collector reads a project's snapshot; a fake one drives the tests.
type collector interface {
	collect(ctx context.Context, projectID uuid.UUID, now time.Time) (*snapshot, error)
}

type storageCollector struct {
	log    *slog.Logger
	hourly *valueCache
}

// valueCache keeps hourlyQueries' results per project and query.
type valueCache struct {
	mu      sync.Mutex
	entries map[string]cachedValues
}

type cachedValues struct {
	at      time.Time
	samples []sample
}

// collect reads everything one build needs. Only discovery failing fails
// the build; a missing value source (no traces, no logs) leaves its values
// out.
func (c *storageCollector) collect(ctx context.Context, projectID uuid.UUID, now time.Time) (*snapshot, error) {
	snap := &snapshot{now: now, values: map[string][]sample{}}
	series, err := metrics_core.GetMetricCoreRepository().ListSeriesLifetimes(ctx, projectID,
		&metrics_core.SeriesLifetimesRequestDTO{
			MetricNames: anchorMetrics, ActiveSince: now.Add(-time.Hour), FirstSeenSince: now.Add(-30 * 24 * time.Hour),
		})
	if err != nil {
		return nil, err
	}
	snap.series = series
	if len(series) > 0 {
		for name, expression := range valueQueries {
			if samples, ok := c.hourly.get(projectID, name, now); ok && hourlyQueries[name] {
				snap.values[name] = samples
				continue
			}
			samples, err := instant(ctx, projectID, expression, now)
			if err != nil {
				c.log.Debug("inventory value query failed", "projectId", projectID, "query", name, "error", err)
				continue
			}
			snap.values[name] = samples
			if hourlyQueries[name] {
				c.hourly.put(projectID, name, now, samples)
			}
		}
		addPodValuesFromContainers(snap.values)
	}

	window := &spans_core.TimeRangeDTO{From: ptr(now.Add(-15 * time.Minute)), To: &now}
	if services, err := spans_core.GetSpanCoreRepository().ListServices(projectID, window); err == nil {
		snap.services = services.Services
		if len(services.Services) > 0 {
			hour := &spans_core.TimeRangeDTO{From: ptr(now.Add(-time.Hour)), To: &now}
			if meta, err := spans_core.GetSpanCoreRepository().ServiceK8sMetadata(projectID, hour); err == nil {
				snap.serviceMeta = meta
			}
		}
	}

	// Nothing reporting (no anchor series, no services): nothing to build.
	if len(series) == 0 && len(snap.services) == 0 {
		return snap, nil
	}
	if len(series) > 0 {
		snap.podLines, snap.podErrors = c.podLogCounts(ctx, projectID, now)
	}
	snap.alerts, err = firingAlerts(projectID)
	if err != nil {
		c.log.Warn("inventory: can't read firing alerts", "projectId", projectID, "error", err)
	}
	return snap, nil
}

// podLogCounts is each pod's log lines and error lines in the last 15
// minutes (the per-minute rollups answer these).
func (c *storageCollector) podLogCounts(
	ctx context.Context,
	projectID uuid.UUID,
	now time.Time,
) (map[string]float64, map[string]float64) {
	window := &logs_core.TimeRangeDTO{From: ptr(now.Add(-15 * time.Minute)), To: &now}
	count := func(query *logs_core.QueryNode) map[string]float64 {
		response, err := logs_core.GetLogCoreRepository().ExecuteAggregation(ctx, projectID,
			&logs_core.AggregationRequestDTO{Query: query, TimeRange: window, GroupField: "k8s_pod_name", Limit: 5000})
		if err != nil {
			c.log.Debug("inventory log counts failed", "projectId", projectID, "error", err)
			return nil
		}
		result := map[string]float64{}
		for _, bucket := range response.Buckets {
			if bucket.GroupKey != "" {
				result[bucket.GroupKey] += float64(bucket.Count)
			}
		}
		return result
	}
	lines := count(nil)
	if len(lines) == 0 {
		return nil, nil
	}
	return lines, count(&logs_core.QueryNode{
		Type: logs_core.QueryNodeTypeCondition,
		Condition: &logs_core.ConditionNode{
			Field: "level", Operator: logs_core.ConditionOperatorIn, Value: errorLevels,
		},
	})
}

func firingAlerts(projectID uuid.UUID) ([]firingAlert, error) {
	var rows []struct {
		Name        string
		RuleLabels  string
		StateLabels string
	}
	err := storage.GetDb().Raw(`SELECT r.name, r.labels::text AS rule_labels, s.labels::text AS state_labels
		FROM alert_states s JOIN alert_rules r ON r.id = s.rule_id
		WHERE r.project_id = ? AND s.state = 'firing'`, projectID).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	alerts := make([]firingAlert, 0, len(rows))
	for _, row := range rows {
		var ruleLabels, stateLabels map[string]string
		_ = json.Unmarshal([]byte(row.RuleLabels), &ruleLabels)
		_ = json.Unmarshal([]byte(row.StateLabels), &stateLabels)
		severity := stateLabels["severity"]
		if severity == "" {
			severity = ruleLabels["severity"]
		}
		alerts = append(alerts, firingAlert{rule: row.Name, severity: severity, labels: stateLabels})
	}
	return alerts, nil
}

func instant(ctx context.Context, projectID uuid.UUID, expression string, now time.Time) ([]sample, error) {
	query, err := metrics_promql.NewInstantQuery(ctx, projectID, expression, now)
	if err != nil {
		return nil, err
	}
	defer query.Close()
	result := query.Exec(ctx)
	if result.Err != nil {
		return nil, result.Err
	}
	vector, ok := result.Value.(promql.Vector)
	if !ok {
		return nil, errors.New("not an instant vector")
	}
	samples := make([]sample, 0, len(vector))
	for _, point := range vector {
		labels := map[string]string{}
		point.Metric.Range(func(label prom_labels.Label) {
			if label.Name != "__name__" {
				labels[label.Name] = label.Value
			}
		})
		samples = append(samples, sample{labels: labels, value: point.F})
	}
	return samples, nil
}

func addPodValuesFromContainers(values map[string][]sample) {
	for pod, container := range podFromContainer {
		if samples, ok := values[container]; ok {
			values[pod] = samples
		}
	}
}

func (c *valueCache) get(projectID uuid.UUID, name string, now time.Time) ([]sample, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[projectID.String()+"/"+name]
	return entry.samples, ok && !now.Before(entry.at) && now.Sub(entry.at) < time.Hour
}

func (c *valueCache) put(projectID uuid.UUID, name string, now time.Time, samples []sample) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]cachedValues{}
	}
	c.entries[projectID.String()+"/"+name] = cachedValues{now, samples}
}

func ptr[T any](v T) *T { return &v }
