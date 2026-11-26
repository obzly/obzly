package spans_querier

import (
	"context"
	"database/sql"
	"fmt"

	spans_core "obzly/internal/features/spans/core"
	"obzly/internal/query"

	"github.com/google/uuid"
)

// ServiceK8sMetadata resolves each service's k8s namespace, deployment and
// node from its most recent span in the window (arg_max, not any_value: a
// service can move across a deploy), from span stats: each rollup row keeps
// its spans' latest values and when they were seen. A service without a row
// is network-only or not k8sattributes-enriched, not an error.
func (q *SpanQuerier) ServiceK8sMetadata(
	ctx context.Context,
	projectID uuid.UUID,
	timeRange *spans_core.TimeRangeDTO,
	scanBudget int64,
) (map[string]spans_core.K8sResourceInfo, error) {
	from, to := resolveTimeRange(timeRange)
	result := make(map[string]spans_core.K8sResourceInfo)

	_, err := q.runStats(ctx, projectID, statsQuery{
		scanBudget: scanBudget,
		schema:     spanStats,
		ranges:     []interval{{start: from, end: to, includeEnd: true}},
		k8s:        true,
		selectSQL: "service_name, " +
			"arg_max(k8s_namespace, k8s_namespace_at) AS namespace, max(k8s_namespace_at) AS namespace_at, " +
			"arg_max(k8s_deployment, k8s_deployment_at) AS deployment, max(k8s_deployment_at) AS deployment_at, " +
			"arg_max(k8s_node, k8s_node_at) AS node, max(k8s_node_at) AS node_at",
		groupSQL: " GROUP BY service_name",
		finalSQL: "SELECT service_name, arg_max(namespace, namespace_at), arg_max(deployment, deployment_at), " +
			"arg_max(node, node_at) FROM " + query.ResultsPlaceholder + " GROUP BY service_name",
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
