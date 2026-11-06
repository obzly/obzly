import { buildKubernetesOtelConfig } from '../kubernetesOtelConfig';
import type { DataSourceDefinition } from './types';

export const kubernetesDataSource: DataSourceDefinition = {
  id: 'kubernetes',
  name: 'Kubernetes',
  category: 'Infrastructure',
  description: 'DaemonSet collector: container logs, node metrics, and OTLP from your apps.',
  fields: [
    {
      key: 'namespace',
      label: 'Kubernetes namespace',
      defaultValue: 'obzly-otel',
    },
    {
      key: 'clusterName',
      label: 'Cluster name (optional)',
      defaultValue: '',
      placeholder: 'e.g. prod-eks-01',
      helpText:
        'Tagged onto every log/metric as k8s.cluster.name. Nothing in a cluster can report ' +
        'its own name automatically, so this is only set if you provide it. Set it when one ' +
        'project has several clusters: Infrastructure tells clusters apart by it.',
    },
    {
      key: 'excludeNamespaces',
      label: 'Exclude namespaces (optional)',
      defaultValue: '',
      placeholder: 'e.g. kube-system, kube-node-lease, monitoring-*',
      helpText:
        'Logs and pod metrics from these namespaces are not collected. Comma-separated, ' +
        'wildcards (* ?) allowed.',
    },
    {
      key: 'includeNamespaces',
      label: 'Only these namespaces (optional)',
      defaultValue: '',
      placeholder: 'e.g. prod-*, checkout (blank = all namespaces)',
      helpText:
        'If set, only these namespaces are collected (minus any excluded above). Node-level ' +
        'metrics are always collected.',
    },
    {
      key: 'excludePods',
      label: 'Exclude pods (optional)',
      defaultValue: '',
      placeholder: 'e.g. noisy-batch-*, *-cronjob-*',
      helpText: 'Skip logs and metrics from pods whose name matches, in any namespace.',
    },
    {
      key: 'clusterAgent',
      label: 'Cluster agent',
      type: 'checkbox',
      defaultValue: 'yes',
      helpText:
        'Adds one small Deployment that reads pod phase, restarts, replica counts and node ' +
        'readiness from the Kubernetes API (read-only). Infrastructure uses them for health.',
    },
    {
      key: 'limitUtilization',
      label: 'Limit utilization metrics',
      type: 'checkbox',
      defaultValue: 'no',
      helpText:
        'Adds pod and container CPU/memory as a share of their limits (Infrastructure shows ' +
        'a share of the node otherwise). Needs the nodes/proxy permission, which also reaches ' +
        "the kubelet's exec API on every node, so it is off by default.",
    },
    {
      key: 'hostNetwork',
      label: 'Use host network',
      type: 'checkbox',
      defaultValue: 'no',
      helpText:
        "Runs the collector on each node's network. Needed on EKS Auto Mode (and any node with " +
        'an IMDS hop limit of 1) for cloud/host resource attributes - from pod networking the ' +
        "metadata request is one hop too many. Also makes node network metrics report the node's " +
        'interfaces. Ports 4317, 4318 and 13133 (health check) must be free on every node. OTLP ' +
        'then listens on the node IP (instead of the pod IP), still without authentication.',
    },
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => ({
    filename: 'obzly-otel-collector.yaml',
    language: 'yaml',
    content: buildKubernetesOtelConfig({
      baseUrl,
      apiKey,
      namespace: fieldValues.namespace.trim() || 'obzly-otel',
      clusterName: fieldValues.clusterName?.trim() || undefined,
      includeNamespaces: fieldValues.includeNamespaces,
      excludeNamespaces: fieldValues.excludeNamespaces,
      excludePods: fieldValues.excludePods,
      hostNetwork: fieldValues.hostNetwork === 'yes',
      clusterAgent: fieldValues.clusterAgent !== 'no',
      limitUtilization: fieldValues.limitUtilization === 'yes',
    }),
    instructions:
      `Apply with: kubectl apply -f obzly-otel-collector.yaml. Point in-cluster apps at ` +
      `obzly-otel-collector.${fieldValues.namespace.trim() || 'obzly-otel'}.svc.cluster.local:4317 ` +
      `for OTLP gRPC (or :4318 for HTTP). That in-cluster OTLP endpoint has no authentication: ` +
      `any pod can send to it, so restrict it with a NetworkPolicy if that matters.`,
  }),
};
