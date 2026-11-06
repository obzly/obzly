import { type ObiTracePermissions, buildObiKubernetesConfig } from '../obiKubernetesConfig';
import type { DataSourceDefinition } from './types';

const YES_NO_OPTIONS = [
  { value: 'no', label: 'No' },
  { value: 'yes', label: 'Yes' },
];

export const obiDataSource: DataSourceDefinition = {
  id: 'obi-network',
  name: 'eBPF Network Map (OBI)',
  category: 'Infrastructure',
  description: 'OpenTelemetry eBPF agent: service-to-service network edges for the Service Map.',
  fields: [
    {
      key: 'namespace',
      label: 'Kubernetes namespace',
      defaultValue: 'obzly-obi',
    },
    {
      key: 'clusterName',
      label: 'Cluster name (optional)',
      defaultValue: '',
      placeholder: 'e.g. prod-eks-01',
      helpText:
        "Sets k8s.cluster.name on network metrics. OBI often can't auto-detect it (it logs a " +
        'warning), so setting it is recommended.',
    },
    {
      key: 'enableTraces',
      label: 'Trace applications',
      defaultValue: 'no',
      options: YES_NO_OPTIONS,
      helpText:
        'Also generate application traces with no code changes (HTTP, gRPC, SQL, Redis, Kafka ' +
        'calls) for the namespaces below, plus request-rate/latency metrics. Extra CPU on each ' +
        'node; leave "No" for the network map only.',
    },
    {
      key: 'traceNamespaces',
      label: 'Namespaces to trace',
      defaultValue: '',
      placeholder: 'e.g. prod-*, checkout (blank = all except system namespaces)',
      helpText:
        'Only used when "Trace applications" is Yes. Comma-separated, wildcards allowed. ' +
        'This is where your APPLICATIONS run, not the namespace above where OBI itself is ' +
        "being deployed - pointing it at OBI's own namespace finds nothing to instrument " +
        '(no error, just no traces). Leave blank to trace everything except system namespaces.',
    },
    {
      key: 'tracePermissions',
      label: 'Trace permissions',
      defaultValue: 'capabilities',
      options: [
        { value: 'capabilities', label: 'Capabilities only (narrowest)' },
        { value: 'selinux', label: 'SELinux label (Bottlerocket)' },
        { value: 'selinux-sysadmin', label: 'SELinux label + SYS_ADMIN' },
        { value: 'privileged', label: 'Privileged' },
      ],
      helpText:
        'Only used when "Trace applications" is Yes. Capabilities only: works where SELinux ' +
        'isn\'t enforcing; on Bottlerocket OBI hangs at "attaching uprobes". SELinux label: ' +
        "adds Bottlerocket's super_t label, traces work but no Node.js runtime metrics. " +
        '+ SYS_ADMIN: Node.js too. Privileged: everything, with full host access.',
    },
    {
      key: 'enableK8sCache',
      label: 'Deploy Kubernetes metadata cache',
      defaultValue: 'no',
      options: YES_NO_OPTIONS,
      helpText:
        "Adds OBI's k8s-cache service: one API-server list/watch connection shared by all " +
        'nodes instead of one per node. Recommended for large clusters; leave "No" for small ones.',
    },
  ],
  build: ({ baseUrl, apiKey, fieldValues }) => {
    const namespace = fieldValues.namespace.trim() || 'obzly-obi';
    return {
      filename: 'obzly-obi.yaml',
      language: 'yaml',
      content: buildObiKubernetesConfig({
        baseUrl,
        apiKey,
        namespace,
        clusterName: fieldValues.clusterName?.trim() || undefined,
        enableK8sCache: fieldValues.enableK8sCache === 'yes',
        enableTraces: fieldValues.enableTraces === 'yes',
        traceNamespaces: fieldValues.traceNamespaces?.trim() || undefined,
        tracePermissions: fieldValues.tracePermissions as ObiTracePermissions,
      }),
      instructions:
        `Apply with: kubectl apply -f obzly-obi.yaml. Runs a privileged-ish eBPF DaemonSet ` +
        `(hostPID, hostNetwork, BPF/PERFMON/NET_ADMIN) - review before applying. Network ` +
        `edges appear on the Service Map once obi.network.flow.bytes metrics arrive.`,
    };
  },
};
