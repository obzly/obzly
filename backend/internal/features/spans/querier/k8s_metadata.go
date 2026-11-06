package spans_querier

import (
	"database/sql"
	"fmt"
	"strings"

	spans_core "obzly/internal/features/spans/core"
	"obzly/internal/query"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

// k8sNamespacePath/k8sDeploymentPath/k8sNodePath address the resource
// attributes the k8sattributes Collector processor sets (k8s.namespace.name,
// k8s.deployment.name, k8s.node.name), as normalized at ingest.
const (
	k8sNamespacePath  = `$.k8s_namespace_name`
	k8sDeploymentPath = `$.k8s_deployment_name`
	k8sNodePath       = `$.k8s_node_name`
)

// ServiceK8sMetadata resolves each service's k8s namespace, deployment and
// node from its most recent span in the window (arg_max, not any_value: a
// service can move across a deploy). A service without a row is
// network-only or not k8sattributes-enriched, not an error.
func (q *SpanQuerier) ServiceK8sMetadata(
	projectID uuid.UUID,
	timeRange *spans_core.TimeRangeDTO,
) (map[string]spans_core.K8sResourceInfo, error) {
	from, to := resolveTimeRange(timeRange)
	tail, records, err := q.inputs(projectID, from, to, segments.PruningHints{})
	if err != nil {
		return nil, err
	}
	result := make(map[string]spans_core.K8sResourceInfo)

	// Each fragment returns its latest non-null value of each attribute and
	// when it was seen; the merge keeps the latest of those.
	paths := []any{k8sNamespacePath, k8sDeploymentPath, k8sNodePath}
	var partialColumns []string
	var partialValues []any
	for i, name := range []string{"namespace", "deployment", "node"} {
		extract := "json_extract_string(resource_attributes, ?)"
		partialColumns = append(partialColumns,
			"arg_max("+extract+", start_time) AS "+name,
			"max(start_time) FILTER (WHERE "+extract+" IS NOT NULL) AS "+name+"_at")
		partialValues = append(partialValues, paths[i], paths[i])
	}
	where := " WHERE project_id = ? AND start_time >= ? AND start_time <= ? GROUP BY service_name"
	whereArgs := []any{projectID.String(), from, to}
	single := "SELECT service_name, " +
		"arg_max(json_extract_string(resource_attributes, ?), start_time) AS namespace, " +
		"arg_max(json_extract_string(resource_attributes, ?), start_time) AS deployment, " +
		"arg_max(json_extract_string(resource_attributes, ?), start_time) AS node " +
		"FROM " + query.SourcePlaceholder + where
	_, err = q.run(query.Request{
		Segments:   records,
		LocalFiles: tail,
		FragmentSQL: "SELECT service_name, " + strings.Join(
			partialColumns,
			", ",
		) + " FROM " + query.SourcePlaceholder + where,
		FragmentArgs: append(partialValues, whereArgs...),
		FinalSQL: "SELECT service_name, arg_max(namespace, namespace_at), arg_max(deployment, deployment_at), " +
			"arg_max(node, node_at) FROM " + query.ResultsPlaceholder + " GROUP BY service_name",
		SingleSQL:  single,
		SingleArgs: append(append([]any{}, paths...), whereArgs...),
	}, func(rows *sql.Rows) error {
		for rows.Next() {
			var serviceName string
			var info spans_core.K8sResourceInfo
			var namespace, deployment, node *string
			if err := rows.Scan(&serviceName, &namespace, &deployment, &node); err != nil {
				return fmt.Errorf("failed to scan service-k8s-metadata row: %w", err)
			}
			if namespace == nil && deployment == nil && node == nil {
				continue
			}
			if namespace != nil {
				info.Namespace = *namespace
			}
			if deployment != nil {
				info.Deployment = *deployment
			}
			if node != nil {
				info.Node = *node
			}
			result[serviceName] = info
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to execute service-k8s-metadata query: %w", err)
	}

	return result, nil
}
