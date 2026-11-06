// Builds the manifests to run OBI (OpenTelemetry eBPF Instrumentation) as a
// per-node DaemonSet; its network flow metrics give the Service Map edges for
// services with no tracing SDK. Separate from the Collector manifest because
// it needs host PID access and eBPF capabilities. Keep in sync with
// deploy/kubernetes/obi/.

/**
 * Host access OBI gets for application tracing (network-only mode needs only
 * the base capabilities). Tested on Bottlerocket:
 * - capabilities: base + DAC_OVERRIDE; uprobe attach hangs under SELinux.
 * - selinux: + the privileged SELinux label (super_t); the Node.js injector
 *   can't join netns.
 * - selinux-sysadmin: + SYS_ADMIN; the Node.js injector works too.
 * - privileged: full host access.
 */
export type ObiTracePermissions = 'capabilities' | 'selinux' | 'selinux-sysadmin' | 'privileged';

export interface ObiKubernetesConfigOptions {
  baseUrl: string;
  apiKey: string;
  namespace: string;
  clusterName?: string;
  // Deploys OBI's k8s-cache companion (one list/watch connection to the API
  // server instead of one per node) and points the DaemonSet at it.
  enableK8sCache?: boolean;
  // Also generate application traces (HTTP/gRPC/SQL/Redis/Kafka spans, no code
  // changes) for workloads in these namespaces (glob patterns; blank = every
  // namespace except OBI's built-in system exclusions).
  enableTraces?: boolean;
  traceNamespaces?: string;
  tracePermissions?: ObiTracePermissions;
}

// Pinned: main moves, and one cluster's pods ended up on different builds.
// v0.14.0 is the first release with the tracefs uprobe fallback (PR #3490).
// The cache must match OBI's version.
const OBI_VERSION = 'v0.14.0';
const OBI_IMAGE = `otel/ebpf-instrument:${OBI_VERSION}`;
const K8S_CACHE_IMAGE = `otel/opentelemetry-ebpf-k8s-cache:${OBI_VERSION}`;
const K8S_CACHE_PORT = 50055;

const yamlQuote = (value: string): string =>
  `"${value.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`;

export function buildObiKubernetesConfig({
  baseUrl,
  apiKey,
  namespace,
  clusterName,
  enableK8sCache,
  enableTraces,
  traceNamespaces,
  tracePermissions = 'capabilities',
}: ObiKubernetesConfigOptions): string {
  const cluster = clusterName?.trim() ?? '';
  const clusterEnv = cluster
    ? `            - name: OTEL_EBPF_KUBE_CLUSTER_NAME
              value: ${yamlQuote(cluster)}
`
    : '';

  const namespaces = (traceNamespaces ?? '').split(/[\s,]+/).filter((entry) => entry.length > 0);
  const instrumentEntries = (namespaces.length > 0 ? namespaces : ['*'])
    .map((ns) => `        - k8s_namespace: ${yamlQuote(ns)}`)
    .join('\n');

  // Application tracing needs a discovery config (OBI instruments nothing by
  // default) and the application metrics feature next to the network ones.
  const tracesConfigMap = enableTraces
    ? `---
apiVersion: v1
kind: ConfigMap
metadata:
  name: obi-config
  namespace: ${namespace}
data:
  obi-config.yml: |
    discovery:
      instrument:
${instrumentEntries}
`
    : '';
  const tracesEnv = enableTraces
    ? `            - name: OTEL_EBPF_CONFIG_PATH
              value: "/config/obi-config.yml"
`
    : '';
  const tracesMount = enableTraces
    ? `            - name: obi-config
              mountPath: /config
`
    : '';
  const tracesVolume = enableTraces
    ? `        - name: obi-config
          configMap:
            name: obi-config
`
    : '';
  const metricsFeatures = enableTraces
    ? 'application,network,network_flow_packets'
    : 'network,network_flow_packets';

  // Application tracing needs CAP_DAC_OVERRIDE: on kernels where
  // CVE-2025-38466 made CAP_SYS_ADMIN a requirement for PMU-based uprobe
  // attachment, OBI (v0.14.0+, PR #3490) falls back to tracefs, whose control
  // files need write access. Without it OBI hangs right after "attaching
  // uprobes as uprobe_multi links". DAC_READ_SEARCH doesn't cover this.
  const permissions: ObiTracePermissions | 'network' = enableTraces ? tracePermissions : 'network';
  const securityContext = buildSecurityContext(permissions);

  const cacheEnv = enableK8sCache
    ? `            - name: OTEL_EBPF_KUBE_META_CACHE_ADDRESS
              value: "obi-k8s-cache.${namespace}.svc:${K8S_CACHE_PORT}"
`
    : '';

  const cacheManifests = enableK8sCache
    ? `---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: obi-k8s-cache
  namespace: ${namespace}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: obi-k8s-cache-${namespace}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: obi-${namespace}
subjects:
  - kind: ServiceAccount
    name: obi-k8s-cache
    namespace: ${namespace}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: obi-k8s-cache
  namespace: ${namespace}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: obi-k8s-cache
  template:
    metadata:
      labels:
        app: obi-k8s-cache
    spec:
      serviceAccountName: obi-k8s-cache
      containers:
        - name: k8s-cache
          image: ${K8S_CACHE_IMAGE}
          ports:
            - containerPort: ${K8S_CACHE_PORT}
---
apiVersion: v1
kind: Service
metadata:
  name: obi-k8s-cache
  namespace: ${namespace}
spec:
  selector:
    app: obi-k8s-cache
  ports:
    - port: ${K8S_CACHE_PORT}
      targetPort: ${K8S_CACHE_PORT}
`
    : '';

  return `apiVersion: v1
kind: Namespace
metadata:
  name: ${namespace}
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: obi
  namespace: ${namespace}
---
# Read-only, cluster-scoped: only what OBI needs to label flows with
# Kubernetes owner/namespace metadata.
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: obi-${namespace}
rules:
  - apiGroups: [""]
    resources: ["pods", "services", "nodes"]
    verbs: ["list", "watch"]
  - apiGroups: ["apps"]
    resources: ["replicasets"]
    verbs: ["list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: obi-${namespace}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: obi-${namespace}
subjects:
  - kind: ServiceAccount
    name: obi
    namespace: ${namespace}
---
apiVersion: v1
kind: Secret
metadata:
  name: obzly-api-key
  namespace: ${namespace}
type: Opaque
stringData:
  OTEL_EXPORTER_OTLP_HEADERS: ${yamlQuote(`X-API-Key=${apiKey}`)}
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: obi
  namespace: ${namespace}
spec:
  selector:
    matchLabels:
      app: obi
  template:
    metadata:
      labels:
        app: obi
    spec:
      serviceAccountName: obi
      hostPID: true
      hostNetwork: true
      dnsPolicy: ClusterFirstWithHostNet
      containers:
        - name: obi
          image: ${OBI_IMAGE}
          env:
            - name: OTEL_EBPF_METRICS_FEATURES
              value: ${yamlQuote(metricsFeatures)}
            - name: OTEL_EBPF_KUBE_METADATA_ENABLE
              value: "true"
            - name: OTEL_EXPORTER_OTLP_ENDPOINT
              value: ${yamlQuote(baseUrl)}
            - name: OTEL_EXPORTER_OTLP_PROTOCOL
              value: "http/protobuf"
${clusterEnv}${cacheEnv}${tracesEnv}            - name: OTEL_EXPORTER_OTLP_HEADERS
              valueFrom:
                secretKeyRef:
                  name: obzly-api-key
                  key: OTEL_EXPORTER_OTLP_HEADERS
${securityContext}          volumeMounts:
            - name: tracefs
              mountPath: /sys/kernel/tracing
            - name: var-run-obi
              mountPath: /var/run/obi
${tracesMount}      volumes:
        - name: tracefs
          hostPath:
            path: /sys/kernel/tracing
            type: Directory
        - name: var-run-obi
          emptyDir: {}
${tracesVolume}${tracesConfigMap}${cacheManifests}`;
}

const BASE_CAPABILITIES = [
  'BPF',
  'PERFMON',
  'NET_ADMIN',
  'NET_RAW',
  'SYS_PTRACE',
  'CHECKPOINT_RESTORE',
  'DAC_READ_SEARCH',
];

function buildSecurityContext(permissions: ObiTracePermissions | 'network'): string {
  if (permissions === 'privileged') {
    return `          # Full host access: required by nothing else in this bundle, chosen for
          # application tracing.
          securityContext:
            privileged: true
            runAsUser: 0
            readOnlyRootFilesystem: true
`;
  }

  const capabilities = [...BASE_CAPABILITIES];
  if (permissions !== 'network') capabilities.push('DAC_OVERRIDE');
  if (permissions === 'selinux-sysadmin') capabilities.push('SYS_ADMIN');
  const selinux =
    permissions === 'selinux' || permissions === 'selinux-sysadmin'
      ? `            # Bottlerocket's SELinux label for privileged containers; without it
            # uprobe attachment hangs on SELinux-enforcing nodes.
            seLinuxOptions:
              type: super_t
`
      : '';
  const comment =
    permissions === 'network'
      ? `          # Narrow capability set; if OBI fails to load its eBPF programs on
          # your kernel, replace this block with "privileged: true".
`
      : '';

  return `${comment}          securityContext:
            runAsUser: 0
            readOnlyRootFilesystem: true
${selinux}            capabilities:
              add:
${capabilities.map((c) => `                - ${c}`).join('\n')}
              drop:
                - ALL
`;
}
