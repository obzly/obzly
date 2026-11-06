import type { Panel, Tab, Variable } from '../../../entity/dashboards';
import type { QueryNode } from '../../../entity/query';
import { claudeCodeUsageTabs, claudeCodeUsageVariables } from './claudeCodeUsageTemplate';
import { kubernetesClusterTabs, kubernetesClusterVariables } from './kubernetesClusterTemplate';

// Ready-made dashboards, created as regular editable dashboards (unlike the backend's Overview).
// The traces template is offered even before a project has traces.
//
// Metric and label names are the ones the OTel Collector actually emits, not guessed
// semantic-convention names. Dotted names use the quoted PromQL form ({"__name__"="a.b"},
// by ("a.b")), since a bare identifier can't contain a dot.

function errorLevelsQuery(): QueryNode {
  return {
    type: 'condition',
    condition: { field: 'level', operator: 'in', value: ['error', 'fatal'] },
  };
}

function warnLevelQuery(): QueryNode {
  return {
    type: 'condition',
    condition: { field: 'level', operator: 'equals', value: 'warn' },
  };
}

function errorsAndReliabilityPanels(): Panel[] {
  return [
    {
      id: crypto.randomUUID(),
      title: 'Error rate over time',
      type: 'line',
      query: errorLevelsQuery(),
      gridPos: { x: 0, y: 0, w: 12, h: 10 },
    },
    {
      id: crypto.randomUUID(),
      title: 'Warnings over time',
      type: 'line',
      query: warnLevelQuery(),
      gridPos: { x: 0, y: 10, w: 6, h: 10 },
    },
    {
      id: crypto.randomUUID(),
      title: 'Errors by client IP',
      type: 'table',
      query: errorLevelsQuery(),
      groupField: 'client_ip',
      limit: 10,
      gridPos: { x: 6, y: 10, w: 6, h: 10 },
    },
    {
      id: crypto.randomUUID(),
      title: 'Error level breakdown',
      type: 'pie',
      query: errorLevelsQuery(),
      groupField: 'level',
      limit: 5,
      gridPos: { x: 0, y: 20, w: 6, h: 10 },
    },
    {
      id: crypto.randomUUID(),
      title: 'Top error messages',
      type: 'table',
      query: errorLevelsQuery(),
      groupField: 'message',
      limit: 20,
      gridPos: { x: 6, y: 20, w: 6, h: 10 },
    },
  ];
}

function servicePerformancePanels(): Panel[] {
  return [
    {
      id: crypto.randomUUID(),
      title: 'Requests by service',
      type: 'bar',
      source: 'spans',
      rootSpansOnly: true,
      groupField: 'service_name',
      metric: 'count',
      limit: 15,
      gridPos: { x: 0, y: 0, w: 6, h: 10 },
    },
    {
      id: crypto.randomUUID(),
      title: 'Errors by service',
      type: 'bar',
      source: 'spans',
      groupField: 'service_name',
      metric: 'error_count',
      limit: 15,
      gridPos: { x: 6, y: 0, w: 6, h: 10 },
    },
    {
      id: crypto.randomUUID(),
      title: 'p50 latency by service (ms)',
      type: 'bar',
      source: 'spans',
      groupField: 'service_name',
      metric: 'p50_duration_ms',
      limit: 15,
      gridPos: { x: 0, y: 10, w: 4, h: 10 },
    },
    {
      id: crypto.randomUUID(),
      title: 'p95 latency by service (ms)',
      type: 'bar',
      source: 'spans',
      groupField: 'service_name',
      metric: 'p95_duration_ms',
      limit: 15,
      gridPos: { x: 4, y: 10, w: 4, h: 10 },
    },
    {
      id: crypto.randomUUID(),
      title: 'p99 latency by service (ms)',
      type: 'bar',
      source: 'spans',
      groupField: 'service_name',
      metric: 'p99_duration_ms',
      limit: 15,
      gridPos: { x: 8, y: 10, w: 4, h: 10 },
    },
    {
      id: crypto.randomUUID(),
      title: 'Requests by operation',
      type: 'bar',
      source: 'spans',
      groupField: 'name',
      metric: 'count',
      limit: 15,
      gridPos: { x: 0, y: 20, w: 6, h: 10 },
    },
    {
      id: crypto.randomUUID(),
      title: 'Requests by status code',
      type: 'pie',
      source: 'spans',
      groupField: 'status_code',
      metric: 'count',
      limit: 5,
      gridPos: { x: 6, y: 20, w: 6, h: 10 },
    },
  ];
}

// Host metrics from the Collector's hostmetrics receiver (cpu, memory, disk, filesystem,
// network and load scrapers).
function hostSystemPanels(): Panel[] {
  return [
    {
      id: crypto.randomUUID(),
      title: 'CPU time by state',
      type: 'line',
      source: 'metrics',
      promQuery: 'sum by (state) (rate(system_cpu_time[5m]))',
      gridPos: { x: 0, y: 0, w: 6, h: 10 },
    },
    {
      id: crypto.randomUUID(),
      title: 'Load average (1m / 5m / 15m)',
      type: 'line',
      source: 'metrics',
      // PromQL strings take Go escapes: a regex "\." must reach the parser as "\\." or the tile fails
      // with "unknown escape sequence". Same for the container network tiles.
      promQuery: '{__name__=~"system_cpu_load_average_(1m|5m|15m)"}',
      gridPos: { x: 6, y: 0, w: 6, h: 10 },
    },
    {
      id: crypto.randomUUID(),
      title: 'Memory usage by state',
      type: 'line',
      source: 'metrics',
      promQuery: 'sum by (state) (system_memory_usage)',
      unit: 'bytes',
      gridPos: { x: 0, y: 10, w: 6, h: 10 },
    },
    {
      id: crypto.randomUUID(),
      title: 'Disk I/O by device',
      type: 'line',
      source: 'metrics',
      promQuery: 'sum by (device, direction) (rate(system_disk_io[5m]))',
      unit: 'bytes',
      gridPos: { x: 6, y: 10, w: 6, h: 10 },
    },
    {
      id: crypto.randomUUID(),
      title: 'Network I/O by device',
      type: 'line',
      source: 'metrics',
      promQuery: 'sum by (device, direction) (rate(system_network_io[5m]))',
      unit: 'bytes',
      gridPos: { x: 0, y: 20, w: 6, h: 10 },
    },
    {
      id: crypto.randomUUID(),
      title: 'Filesystem usage by mountpoint',
      type: 'line',
      source: 'metrics',
      promQuery: 'sum by (mountpoint, state) (system_filesystem_usage)',
      unit: 'bytes',
      gridPos: { x: 6, y: 20, w: 6, h: 10 },
    },
  ];
}

// Per-container metrics from a docker_stats-style receiver (container-level, not pod-level).
function containerResourcesPanels(): Panel[] {
  return [
    {
      id: crypto.randomUUID(),
      title: 'Container CPU usage',
      type: 'line',
      source: 'metrics',
      promQuery: 'sum by (container_name) (rate(container_cpu_usage_total[5m]))',
      gridPos: { x: 0, y: 0, w: 6, h: 10 },
    },
    {
      id: crypto.randomUUID(),
      title: 'Container CPU usage',
      type: 'line',
      source: 'metrics',
      // Current kubeletstats/docker_stats receivers emit container.cpu.usage
      // (cores); older ones emitted container.cpu.utilization (ratio). Match
      // either - a project only ever has one of them live at a time.
      promQuery: 'avg by (container_name) ({__name__=~"container_cpu_(usage|utilization)"})',
      gridPos: { x: 6, y: 0, w: 6, h: 10 },
    },
    {
      id: crypto.randomUUID(),
      title: 'Container memory usage',
      type: 'line',
      source: 'metrics',
      promQuery: 'sum by (container_name) (container_memory_usage_total)',
      unit: 'bytes',
      gridPos: { x: 0, y: 10, w: 6, h: 10 },
    },
    {
      id: crypto.randomUUID(),
      title: 'Container memory percent',
      type: 'line',
      source: 'metrics',
      promQuery: 'avg by (container_name) (container_memory_percent)',
      unit: 'percent',
      gridPos: { x: 6, y: 10, w: 6, h: 10 },
    },
    {
      id: crypto.randomUUID(),
      title: 'Container network I/O (rx/tx bytes)',
      type: 'line',
      source: 'metrics',
      promQuery:
        'sum by (container_name, __name__) (rate({__name__=~"container_network_io_usage_(rx|tx)_bytes"}[5m]))',
      unit: 'bytes',
      gridPos: { x: 0, y: 20, w: 6, h: 10 },
    },
    {
      id: crypto.randomUUID(),
      title: 'Container network drops',
      type: 'line',
      source: 'metrics',
      promQuery:
        'sum by (container_name, __name__) (rate({__name__=~"container_network_io_usage_(rx|tx)_dropped"}[5m]))',
      gridPos: { x: 6, y: 20, w: 6, h: 10 },
    },
  ];
}

// Kubernetes pod/node/volume metrics from the kubeletstats receiver in kubernetesOtelConfig.ts.
function kubernetesPanels(): Panel[] {
  return [
    {
      id: crypto.randomUUID(),
      title: 'Pod CPU usage',
      type: 'line',
      source: 'metrics',
      // kubeletstats renamed k8s.pod.cpu.utilization -> k8s.pod.cpu.usage
      // (cores); match either so old and new collectors both fill this tile.
      promQuery:
        'avg by (k8s_namespace_name, k8s_pod_name) ({__name__=~"k8s_pod_cpu_(usage|utilization)"})',
      gridPos: { x: 0, y: 0, w: 6, h: 10 },
    },
    {
      id: crypto.randomUUID(),
      title: 'Pod memory working set',
      type: 'line',
      source: 'metrics',
      promQuery: 'sum by (k8s_namespace_name, k8s_pod_name) (k8s_pod_memory_working_set)',
      unit: 'bytes',
      gridPos: { x: 6, y: 0, w: 6, h: 10 },
    },
    {
      id: crypto.randomUUID(),
      title: 'Pod filesystem usage',
      type: 'line',
      source: 'metrics',
      promQuery: 'sum by (k8s_namespace_name, k8s_pod_name) (k8s_pod_filesystem_usage)',
      unit: 'bytes',
      gridPos: { x: 0, y: 10, w: 6, h: 10 },
    },
    {
      id: crypto.randomUUID(),
      title: 'Pod network I/O (rx/tx bytes)',
      type: 'line',
      source: 'metrics',
      promQuery: 'sum by (k8s_pod_name, direction) (rate(k8s_pod_network_io[5m]))',
      unit: 'bytes',
      gridPos: { x: 6, y: 10, w: 6, h: 10 },
    },
    {
      id: crypto.randomUUID(),
      title: 'Node CPU usage',
      type: 'line',
      source: 'metrics',
      // Same rename as the pod tile: k8s.node.cpu.utilization -> k8s.node.cpu.usage.
      promQuery: 'avg by (k8s_node_name) ({__name__=~"k8s_node_cpu_(usage|utilization)"})',
      gridPos: { x: 0, y: 20, w: 6, h: 10 },
    },
    {
      id: crypto.randomUUID(),
      title: 'Node memory working set',
      type: 'line',
      source: 'metrics',
      promQuery: 'sum by (k8s_node_name) (k8s_node_memory_working_set)',
      unit: 'bytes',
      gridPos: { x: 6, y: 20, w: 6, h: 10 },
    },
    {
      id: crypto.randomUUID(),
      title: 'Node network I/O (rx/tx bytes)',
      type: 'line',
      source: 'metrics',
      promQuery: 'sum by (k8s_node_name, direction) (rate(k8s_node_network_io[5m]))',
      unit: 'bytes',
      gridPos: { x: 0, y: 30, w: 6, h: 10 },
    },
    {
      id: crypto.randomUUID(),
      title: 'Volume available space',
      type: 'line',
      source: 'metrics',
      promQuery: 'sum by (k8s_namespace_name, k8s_volume_name) (k8s_volume_available)',
      unit: 'bytes',
      gridPos: { x: 6, y: 30, w: 6, h: 10 },
    },
  ];
}

export interface DashboardTemplate {
  key: string;
  title: string;
  description: string;
  buildTabs: () => Tab[];
  variables?: Variable[];
}

const singleTab = (buildPanels: () => Panel[]) => (): Tab[] => [
  { id: crypto.randomUUID(), title: 'Default', panels: buildPanels() },
];

export const dashboardTemplates: DashboardTemplate[] = [
  {
    key: 'errors-reliability',
    title: 'Errors & Reliability',
    description: 'Error/warning rates, error breakdown by level, offending IPs and messages.',
    buildTabs: singleTab(errorsAndReliabilityPanels),
  },
  {
    key: 'service-performance',
    title: 'Service Performance (Traces)',
    description:
      'RED-method view of traced services: request rate, error rate and latency percentiles.',
    buildTabs: singleTab(servicePerformancePanels),
  },
  {
    key: 'host-system',
    title: 'Host & System Resources',
    description: 'CPU, memory, disk and network metrics for the machines running your workloads.',
    buildTabs: singleTab(hostSystemPanels),
  },
  {
    key: 'container-resources',
    title: 'Container Resources',
    description:
      'Per-container CPU, memory and network usage (Docker-level, not Kubernetes-pod-level).',
    buildTabs: singleTab(containerResourcesPanels),
  },
  {
    key: 'kubernetes',
    title: 'Kubernetes Pods & Nodes',
    description:
      'Pod and node CPU/memory/network/filesystem usage, from the kubeletstats receiver.',
    buildTabs: singleTab(kubernetesPanels),
  },
  {
    key: 'k8s-cluster-grafana-15661',
    title: 'K8S Cluster Overview (Grafana 15661)',
    description:
      'Nodes, pods and microservices by container name, with cluster/node/namespace/container/pod variables. Converted from Grafana dashboard 15661 to kubeletstats metrics.',
    buildTabs: kubernetesClusterTabs,
    variables: kubernetesClusterVariables,
  },
  {
    key: 'claude-code-usage',
    title: 'Claude Code usage',
    description:
      "Cost, tokens, cache hits, sessions, active time, lines of code, commits and edit acceptance from Claude Code's metrics, with user, model, terminal and version variables. Needs Claude Code's metrics exporter (Data Sources, Claude Code).",
    buildTabs: claudeCodeUsageTabs,
    variables: claudeCodeUsageVariables,
  },
];
