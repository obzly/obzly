package logs_core

import (
	"fmt"
	"slices"
	"time"
)

// PromotedResourceColumns are the log fields copied into their own Parquet
// columns. Fields are normalized at ingest (resource.k8s.pod.name ->
// k8s_pod_name), so each column reads the key of the same name. Shared by the
// write path and Live Tail so they can't drift.
var PromotedResourceColumns = []struct{ Column, OtelKey string }{
	{"service_name", "service_name"},
	{"service_version", "service_version"},
	{"k8s_namespace_name", "k8s_namespace_name"},
	{"k8s_pod_name", "k8s_pod_name"},
	{"k8s_container_name", "k8s_container_name"},
	{"k8s_node_name", "k8s_node_name"},
	{"k8s_cluster_name", "k8s_cluster_name"},
	{"cloud_provider", "cloud_provider"},
	{"cloud_region", "cloud_region"},
}

// SourceIndexFields are the resource attributes log_source_index records per
// log source: what the Logs sidebar can offer when a source didn't log in the
// search window.
var SourceIndexFields = []string{
	"service_name",
	"k8s_cluster_name",
	"k8s_namespace_name",
	"k8s_deployment_name",
	"k8s_statefulset_name",
	"k8s_daemonset_name",
	"k8s_job_name",
	"k8s_cronjob_name",
	"k8s_container_name",
	"k8s_pod_name",
	"k8s_node_name",
	"host_name",
}

// RecentValuesWindow: how far back the Logs sidebar offers values whose
// sources didn't log in the search window.
const RecentValuesWindow = 36 * time.Hour

// IsSourceIndexField reports whether field (normalized) is in SourceIndexFields.
func IsSourceIndexField(field string) bool {
	return slices.Contains(SourceIndexFields, field)
}

// PromotedColumnValue reads one promoted column's value from a log's
// normalized Fields; "" when absent (every consumer treats a missing system
// field as empty, like trace_id/span_id).
func PromotedColumnValue(fields map[string]any, key string) string {
	if value, ok := fields[key]; ok {
		return StringifyFieldValue(value)
	}
	return ""
}

// StringifyFieldValue renders a JSON-decoded field value as the plain string
// every consumer stores or compares (fields column, promoted columns, Live Tail).
func StringifyFieldValue(value any) string {
	switch typed := value.(type) {
	case float64:
		if typed == float64(int64(typed)) {
			return fmt.Sprintf("%.0f", typed)
		}
		return fmt.Sprintf("%f", typed)
	case float32:
		if typed == float32(int32(typed)) {
			return fmt.Sprintf("%.0f", typed)
		}
		return fmt.Sprintf("%f", typed)
	default:
		return fmt.Sprintf("%v", typed)
	}
}
