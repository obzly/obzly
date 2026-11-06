import type { Panel, Tab, Variable } from '../../../entity/dashboards';

// Converted from Grafana dashboard 15661 "K8S Dashboard" (rev 2, starsliao).
// The original reads kube-state-metrics + cAdvisor; this maps each panel to
// the OTel kubeletstats/hostmetrics series kubernetesOtelConfig.ts collects.
// Rows become tabs, $origin_prometheus becomes k8s_cluster_name and $Node
// becomes host_name (kubeletstats pod series carry no k8s_node_name).
// Dropped: panels that need requests/limits, restarts, PVC or object counts
// (kube-state-metrics only) and the Grafana Cassandra JVM panel.

export const kubernetesClusterVariables: Variable[] = [
  { key: 'cluster', label: 'Cluster', source: 'metricLabel', metricLabelName: 'k8s_cluster_name' },
  { key: 'node', label: 'Node', source: 'metricLabel', metricLabelName: 'host_name' },
  {
    key: 'namespace',
    label: 'Namespace',
    source: 'metricLabel',
    metricLabelName: 'k8s_namespace_name',
  },
  {
    key: 'container',
    label: 'Container',
    source: 'metricLabel',
    metricLabelName: 'k8s_container_name',
  },
  { key: 'pod', label: 'Pod', source: 'metricLabel', metricLabelName: 'k8s_pod_name' },
];

// Unset variables become .* (see dashboardVariables.ts), so every matcher is =~.
const NODE = 'k8s_cluster_name=~"${cluster}", host_name=~"${node}"';
const POD = `${NODE}, k8s_namespace_name=~"\${namespace}", k8s_pod_name=~"\${pod}"`;
const CONTAINER = `${POD}, k8s_container_name!="", k8s_container_name=~"\${container}"`;

const nodeMemTotal = `(k8s_node_memory_working_set{${NODE}} + k8s_node_memory_available{${NODE}})`;

function metricPanel(
  title: string,
  type: Panel['type'],
  promQuery: string,
  gridPos: Panel['gridPos'],
  unit?: Panel['unit'],
): Panel {
  return { id: crypto.randomUUID(), title, type, source: 'metrics', promQuery, unit, gridPos };
}

function nodeTab(): Tab {
  return {
    id: crypto.randomUUID(),
    title: 'Node Resource Overview',
    panels: [
      metricPanel(
        'Node memory ratio',
        'stat',
        `sum(k8s_node_memory_working_set{${NODE}}) / sum(${nodeMemTotal})`,
        { x: 0, y: 0, w: 2, h: 4 },
        'percent',
      ),
      metricPanel(
        'Node CPU ratio',
        'stat',
        `sum(k8s_node_cpu_usage{${NODE}}) / sum(system_cpu_logical_count{${NODE}})`,
        { x: 2, y: 0, w: 2, h: 4 },
        'percent',
      ),
      metricPanel(
        'Nodes',
        'stat',
        `count(count by (host_name) (k8s_node_memory_working_set{${NODE}}))`,
        { x: 4, y: 0, w: 2, h: 4 },
      ),
      metricPanel(
        'Pods',
        'stat',
        `count(count by (k8s_pod_uid) (k8s_pod_memory_working_set{${POD}}))`,
        { x: 6, y: 0, w: 2, h: 3 },
      ),
      metricPanel(
        'Node memory total',
        'stat',
        `sum(${nodeMemTotal})`,
        { x: 0, y: 4, w: 2, h: 4 },
        'bytes',
      ),
      metricPanel('Node CPU cores', 'stat', `sum(system_cpu_logical_count{${NODE}})`, {
        x: 2,
        y: 4,
        w: 2,
        h: 4,
      }),
      metricPanel(
        'Node storage',
        'stat',
        `sum(k8s_node_filesystem_capacity{${NODE}})`,
        { x: 4, y: 4, w: 2, h: 4 },
        'bytes',
      ),
      metricPanel(
        'Pods per namespace',
        'table',
        `count by (k8s_namespace_name) (k8s_pod_memory_working_set{${POD}})`,
        { x: 6, y: 3, w: 2, h: 5 },
      ),
      metricPanel(
        'Network by namespace (bytes/s)',
        'line',
        `sum by (k8s_namespace_name, direction) (rate(k8s_pod_network_io{${POD}}[5m]))`,
        { x: 8, y: 0, w: 4, h: 8 },
        'bytes',
      ),
      metricPanel(
        'Memory usage by node',
        'line',
        `sum by (host_name) (k8s_node_memory_working_set{${NODE}})`,
        { x: 0, y: 8, w: 4, h: 7 },
        'bytes',
      ),
      metricPanel(
        'CPU cores used by node',
        'line',
        `sum by (host_name) (k8s_node_cpu_usage{${NODE}})`,
        { x: 4, y: 8, w: 4, h: 7 },
      ),
      metricPanel(
        'Pods by node',
        'line',
        `count by (host_name) (k8s_pod_memory_working_set{${POD}})`,
        { x: 8, y: 8, w: 4, h: 7 },
      ),
      metricPanel(
        'Node CPU %',
        'line',
        `sum by (host_name) (k8s_node_cpu_usage{${NODE}}) / sum by (host_name) (system_cpu_logical_count{${NODE}})`,
        { x: 0, y: 15, w: 4, h: 8 },
        'percent',
      ),
      metricPanel(
        'Node memory %',
        'line',
        `sum by (host_name) (k8s_node_memory_working_set{${NODE}}) / sum by (host_name) (${nodeMemTotal})`,
        { x: 4, y: 15, w: 4, h: 8 },
        'percent',
      ),
      metricPanel(
        'Node network (bytes/s)',
        'line',
        `sum by (host_name, direction) (rate(k8s_node_network_io{${NODE}}[5m]))`,
        { x: 8, y: 15, w: 4, h: 8 },
        'bytes',
      ),
      metricPanel(
        'Node filesystem used %',
        'table',
        `sum by (host_name) (k8s_node_filesystem_usage{${NODE}}) / sum by (host_name) (k8s_node_filesystem_capacity{${NODE}})`,
        { x: 0, y: 23, w: 4, h: 8 },
        'percent',
      ),
      metricPanel(
        'CPU cores by namespace',
        'line',
        `sum by (k8s_namespace_name) (k8s_pod_cpu_usage{${POD}})`,
        { x: 4, y: 23, w: 4, h: 8 },
      ),
      metricPanel(
        'Working set memory by namespace',
        'line',
        `sum by (k8s_namespace_name) (k8s_pod_memory_working_set{${POD}})`,
        { x: 8, y: 23, w: 4, h: 8 },
        'bytes',
      ),
    ],
  };
}

function podTab(): Tab {
  return {
    id: crypto.randomUUID(),
    title: 'Pod Resource Overview',
    panels: [
      metricPanel(
        'Pod memory (working set)',
        'table',
        `sum by (k8s_namespace_name, k8s_pod_name) (k8s_pod_memory_working_set{${POD}})`,
        { x: 0, y: 0, w: 6, h: 8 },
        'bytes',
      ),
      metricPanel(
        'Pod CPU cores',
        'table',
        `sum by (k8s_namespace_name, k8s_pod_name) (k8s_pod_cpu_usage{${POD}})`,
        { x: 6, y: 0, w: 6, h: 8 },
      ),
      metricPanel(
        'Container CPU cores',
        'line',
        `sum by (k8s_pod_name, k8s_container_name) (container_cpu_usage{${CONTAINER}})`,
        { x: 0, y: 8, w: 4, h: 9 },
      ),
      metricPanel(
        'Container working set memory',
        'line',
        `sum by (k8s_pod_name, k8s_container_name) (container_memory_working_set{${CONTAINER}})`,
        { x: 4, y: 8, w: 4, h: 9 },
        'bytes',
      ),
      metricPanel(
        'Pod network (bytes/s)',
        'line',
        `sum by (k8s_pod_name, direction) (rate(k8s_pod_network_io{${POD}}[5m]))`,
        { x: 8, y: 8, w: 4, h: 9 },
        'bytes',
      ),
      metricPanel(
        'Container RSS memory',
        'line',
        `sum by (k8s_pod_name, k8s_container_name) (container_memory_rss{${CONTAINER}})`,
        { x: 0, y: 17, w: 6, h: 9 },
        'bytes',
      ),
      metricPanel(
        'Pod filesystem usage',
        'line',
        `sum by (k8s_pod_name) (k8s_pod_filesystem_usage{${POD}})`,
        { x: 6, y: 17, w: 6, h: 9 },
        'bytes',
      ),
    ],
  };
}

function containerTab(): Tab {
  return {
    id: crypto.randomUUID(),
    title: 'Microservices (Container Name)',
    panels: [
      metricPanel(
        'Pods per container name',
        'table',
        `count by (k8s_namespace_name, k8s_container_name) (container_memory_working_set{${CONTAINER}})`,
        { x: 0, y: 0, w: 4, h: 9 },
      ),
      metricPanel(
        'Memory per container name',
        'table',
        `sum by (k8s_namespace_name, k8s_container_name) (container_memory_working_set{${CONTAINER}})`,
        { x: 4, y: 0, w: 4, h: 9 },
        'bytes',
      ),
      metricPanel(
        'CPU cores per container name',
        'table',
        `sum by (k8s_namespace_name, k8s_container_name) (container_cpu_usage{${CONTAINER}})`,
        { x: 8, y: 0, w: 4, h: 9 },
      ),
      metricPanel(
        'Average CPU cores per pod',
        'line',
        `avg by (k8s_container_name) (container_cpu_usage{${CONTAINER}})`,
        { x: 0, y: 9, w: 6, h: 8 },
      ),
      metricPanel(
        'Average memory per pod',
        'line',
        `avg by (k8s_container_name) (container_memory_working_set{${CONTAINER}})`,
        { x: 6, y: 9, w: 6, h: 8 },
        'bytes',
      ),
      metricPanel(
        'Total CPU cores',
        'line',
        `sum by (k8s_container_name) (container_cpu_usage{${CONTAINER}})`,
        { x: 0, y: 17, w: 4, h: 8 },
      ),
      metricPanel(
        'Total memory',
        'line',
        `sum by (k8s_container_name) (container_memory_working_set{${CONTAINER}})`,
        { x: 4, y: 17, w: 4, h: 8 },
        'bytes',
      ),
      metricPanel(
        'Pod count',
        'line',
        `count by (k8s_container_name) (container_memory_working_set{${CONTAINER}})`,
        { x: 8, y: 17, w: 4, h: 8 },
      ),
    ],
  };
}

export function kubernetesClusterTabs(): Tab[] {
  return [nodeTab(), podTab(), containerTab()];
}
