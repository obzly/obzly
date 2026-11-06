package logs_core

import (
	"slices"
	"strings"
)

// Log context directions (LogContextRequestDTO.Direction).
const (
	ContextBefore = "before"
	ContextAfter  = "after"
)

// The context's default scope is the anchor's workload (every replica of its
// deployment, statefulset, daemonset or job), not one pod; every such field
// the anchor has must match. host_name only scopes logs from outside Kubernetes.
var (
	contextSourceFields = []string{"k8s_cluster_name", "k8s_namespace_name"}
	// contextWorkloadFields are OTel k8sattributes' workload names; a pod
	// has one kind (a cronjob's pods have job and cronjob). The replicaset
	// is left out: its pod-template hash would narrow to one rollout.
	contextWorkloadFields = []string{
		"k8s_deployment_name", "k8s_statefulset_name", "k8s_daemonset_name", "k8s_job_name", "k8s_cronjob_name",
	}
	contextTrailingFields = []string{"k8s_container_name", "service_name"}
)

// AutoContextScope is the default scope for anchor: the source and
// workload fields it has, then extra (LOG_CONTEXT_DEFAULT_FIELDS). A pod
// with no workload is scoped to the pod and a log from outside Kubernetes
// to its host, so the default never widens to a whole namespace.
func AutoContextScope(anchor *LogItemDTO, extra []string) []string {
	var fields []string
	present := func(field string) bool {
		_, ok := ContextValue(anchor, field)
		return ok
	}
	add := func(candidates ...string) bool {
		added := false
		for _, field := range candidates {
			if present(field) && !slices.Contains(fields, field) {
				fields = append(fields, field)
				added = true
			}
		}
		return added
	}
	add(contextSourceFields...)
	hasWorkload := add(contextWorkloadFields...)
	add(contextTrailingFields...)
	if !hasWorkload {
		add("k8s_pod_name")
	}
	if !slices.ContainsFunc(fields, func(field string) bool { return strings.HasPrefix(field, "k8s_") }) {
		add("host_name")
	}
	add(extra...)
	return fields
}

// ContextExtraFields parses LOG_CONTEXT_DEFAULT_FIELDS (comma-separated).
func ContextExtraFields(raw string) []string {
	var fields []string
	for _, field := range strings.Split(raw, ",") {
		if field = strings.TrimSpace(field); field != "" && !slices.Contains(fields, field) {
			fields = append(fields, field)
		}
	}
	return fields
}

// ContextValue is the anchor's value for field as a scope filter compares
// it; false when the anchor doesn't have it.
func ContextValue(item *LogItemDTO, field string) (string, bool) {
	var value string
	switch field {
	case "level":
		value = item.Level
	case "trace_id":
		value = item.TraceID
	case "span_id":
		value = item.SpanID
	case "client_ip":
		value = item.ClientIP
	default:
		raw, ok := item.Fields[field]
		if !ok || raw == nil {
			return "", false
		}
		value = StringifyFieldValue(raw)
	}
	return value, value != ""
}
