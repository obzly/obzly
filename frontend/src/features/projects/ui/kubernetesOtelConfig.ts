// Builds a ready-to-apply OpenTelemetry Collector manifest for running as a
// per-node "agent" DaemonSet in a Kubernetes cluster: it tails every pod's
// container logs (the file_log receiver, the same approach the official
// OpenTelemetry Collector Helm chart's "daemonset" mode uses), scrapes
// node-level resource metrics (the host_metrics receiver) and per-pod/
// per-container resource metrics from the node's own kubelet (the
// kubelet_stats receiver - what actually backs `kubectl top pod`), tags
// every log/metric with cloud and host resource attributes (the
// resource_detection processor - cloud.provider, cloud.region, host.name,
// etc.) plus an optional static cluster name, and also accepts OTLP
// (logs, traces, and metrics) pushed directly from in-cluster app SDKs,
// forwarding all of it to this Obzly project over OTLP/HTTP.
//
// OTLP/HTTP (not gRPC) is the default exporter here deliberately: it shares
// the exact same host/port the viewer is already using to reach the UI (so
// whatever TLS termination/reverse-proxy already fronts that URL just
// works), where OTLP gRPC's own port (4317) may or may not be separately
// exposed/TLS-terminated depending on how this instance is deployed - see
// ARCHITECTURE.md's Phase 3/5 notes on the two receivers sharing the same
// x-api-key auth convention either way.
import { COLLECTOR_IMAGE } from './dataSources/otelCollectorCommon';

export interface KubernetesOtelConfigOptions {
  baseUrl: string;
  apiKey: string;
  namespace: string;
  // Static k8s.cluster.name; a cluster doesn't expose its own name to pods.
  clusterName?: string;
  // Comma/space separated name patterns (* and ? wildcards) to cut noise.
  // Namespaces: only collect from includeNamespaces if set, and never from
  // excludeNamespaces. Pods: never collect from excludePods.
  includeNamespaces?: string;
  excludeNamespaces?: string;
  excludePods?: string;
  // Runs the collector pods on the node's network. EKS Auto Mode sets the
  // IMDS hop limit to 1, so resource_detection's ec2/eks detectors can't
  // reach instance metadata from pod networking; it also makes host_metrics'
  // network scraper see the node's interfaces instead of the pod's.
  hostNetwork?: boolean;
  // Adds a one-replica cluster agent (k8s_cluster receiver) for pod phase,
  // restarts, replica counts and node Ready, which node agents can't see.
  clusterAgent?: boolean;
  // Adds the kubelet_stats *_limit_utilization metrics (usage as a share of
  // the pod's/container's limit). They need nodes/proxy, which also reaches
  // the kubelet's exec/attach API, so they are opt-in. Default off.
  limitUtilization?: boolean;
}

// Both workloads: memory_limiter first in every pipeline, sized from the
// container's 512Mi limit.
const MEMORY_LIMITER_YAML = `      memory_limiter:
        check_interval: 1s
        limit_percentage: 80
        spike_limit_percentage: 25
`;

// Restarts a hung collector and keeps a starting one out of the Service.
const HEALTH_PROBES = `          livenessProbe:
            httpGet:
              path: /
              port: 13133
          readinessProbe:
            httpGet:
              path: /
              port: 13133
`;

// Restricted: the collector image already runs as 10001 and needs no
// capabilities; writes go only to mounted volumes.
const RESTRICTED_SECURITY_CONTEXT = `          securityContext:
            runAsNonRoot: true
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
            seccompProfile:
              type: RuntimeDefault
`;

// Kubernetes object names are lowercase alphanumerics, '-' and '.', so any
// other character is dropped rather than risking YAML/regex injection.
function parseNamePatterns(raw: string | undefined): string[] {
  return (raw ?? '')
    .split(/[\s,]+/)
    .map((entry) => entry.replace(/[^A-Za-z0-9*?._-]/g, ''))
    .filter((entry) => entry.length > 0);
}

// Glob -> anchored regex for OTTL IsMatch; '.' becomes [.] so no backslash
// escaping is needed inside the YAML/OTTL string layers.
function globAlternation(patterns: string[]): string {
  const alternatives = patterns.map((p) =>
    p.replace(/\./g, '[.]').replace(/\*/g, '.*').replace(/\?/g, '.'),
  );
  return `^(${alternatives.join('|')})$`;
}

const INIT_IMAGE = 'busybox:1.37.0';

export function buildKubernetesOtelConfig({
  baseUrl,
  apiKey,
  namespace,
  clusterName,
  includeNamespaces,
  excludeNamespaces,
  excludePods,
  hostNetwork,
  clusterAgent = true,
  limitUtilization = false,
}: KubernetesOtelConfigOptions): string {
  const trimmedClusterName = clusterName?.trim() ?? '';
  // A YAML double-quoted scalar needs its backslashes and quotes escaped.
  const escapedClusterName = trimmedClusterName.replace(/\\/g, '\\\\').replace(/"/g, '\\"');

  const clusterNameProcessorYAML = trimmedClusterName
    ? `      # Static cluster identifier - see KubernetesOtelConfigOptions'
      # clusterName doc comment for why this can't be auto-detected.
      resource/cluster_name:
        attributes:
          - key: k8s.cluster.name
            value: "${escapedClusterName}"
            action: upsert
`
    : '';
  const clusterNameProcessorRef = trimmedClusterName ? ', resource/cluster_name' : '';

  const includeNs = parseNamePatterns(includeNamespaces);
  const excludeNs = parseNamePatterns(excludeNamespaces);
  const excludePodPatterns = parseNamePatterns(excludePods);

  // Log files live at /var/log/pods/<namespace>_<pod>_<uid>/<container>/N.log,
  // so filtering by path stops the collector reading noisy files at all.
  const logInclude = (
    includeNs.length > 0
      ? includeNs.map((ns) => `/var/log/pods/${ns}_*/*/*.log`)
      : ['/var/log/pods/*/*/*.log']
  )
    .map((p) => `"${p}"`)
    .join(', ');
  const logExclude = [
    `/var/log/pods/${namespace}_obzly-otel-collector*/*/*.log`,
    ...excludeNs.map((ns) => `/var/log/pods/${ns}_*/*/*.log`),
    ...excludePodPatterns.map((pod) => `/var/log/pods/*_${pod}_*/*/*.log`),
  ]
    .map((p) => `"${p}"`)
    .join(', ');

  // The same rules as a filter processor drop metrics (kubelet_stats) and any
  // OTLP-pushed logs that carry namespace/pod attributes. Node-level metrics
  // have no namespace attribute and are always kept.
  const dropConditions: string[] = [];
  if (excludeNs.length > 0) {
    dropConditions.push(
      `IsMatch(resource.attributes["k8s.namespace.name"], "${globAlternation(excludeNs)}")`,
    );
  }
  if (includeNs.length > 0) {
    dropConditions.push(
      `resource.attributes["k8s.namespace.name"] != nil and not IsMatch(resource.attributes["k8s.namespace.name"], "${globAlternation(includeNs)}")`,
    );
  }
  if (excludePodPatterns.length > 0) {
    dropConditions.push(
      `IsMatch(resource.attributes["k8s.pod.name"], "${globAlternation(excludePodPatterns)}")`,
    );
  }
  const hasFilters = dropConditions.length > 0;
  const conditionLines = (indent: string) =>
    dropConditions.map((c) => `${indent}- '${c}'`).join('\n');
  const filterProcessorYAML = hasFilters
    ? `      # Drops records from the namespaces/pods excluded when this config was
      # generated (log files are also skipped by path in the file_log receiver).
      filter/namespaces:
        error_mode: ignore
        metrics:
          datapoint:
${conditionLines('            ')}
        logs:
          log_record:
${conditionLines('            ')}
`
    : '';
  const filterProcessorRef = hasFilters ? ', filter/namespaces' : '';

  const hostNetworkSpec = hostNetwork
    ? `      hostNetwork: true
      dnsPolicy: ClusterFirstWithHostNet
`
    : '';
  // On the node's network 0.0.0.0 would be every node interface, public
  // ones included; the pod IP is then the node IP the Service targets.
  const otlpHost = hostNetwork ? '${env:KUBE_POD_IP}' : '0.0.0.0';
  const podIPEnv = hostNetwork
    ? `            - name: KUBE_POD_IP
              valueFrom:
                fieldRef:
                  fieldPath: status.podIP
`
    : '';
  const limitMetricsYAML = limitUtilization
    ? `        # Usage as a share of the pod's/container's limit (when it has one).
        metrics:
          k8s.pod.cpu_limit_utilization:
            enabled: true
          k8s.pod.memory_limit_utilization:
            enabled: true
          k8s.container.cpu_limit_utilization:
            enabled: true
          k8s.container.memory_limit_utilization:
            enabled: true
`
    : '';
  // Only the limit metrics need the kubelet's /pods (via nodes/proxy).
  const nodesProxyRule = limitUtilization
    ? `  # nodes/proxy: the *_limit_utilization metrics read pod specs from the
  # kubelet. It also allows kubelet exec/attach, hence opt-in.
  - apiGroups: [""]
    resources: ["nodes/proxy"]
    verbs: ["get"]
`
    : '';

  return `# Obzly OpenTelemetry Collector agent for Kubernetes
#
# Deploys the OTel Collector as a DaemonSet (one pod per node) that:
#   - tails every pod's container logs on that node and forwards them
#   - scrapes this node's own resource metrics (CPU, memory, disk, network)
#   - scrapes per-pod/per-container resource metrics from this node's
#     kubelet (CPU, memory, network, filesystem - the same data
#     "kubectl top pod" uses)
#   - with the cluster agent (a one-replica Deployment), reads pod phase,
#     restarts, replica counts and node conditions from the Kubernetes API
#   - also accepts OTLP logs/traces/metrics pushed directly from apps in the
#     cluster (point an app's OTEL_EXPORTER_OTLP_ENDPOINT at
#     obzly-otel-collector.${namespace}.svc.cluster.local:4317, or :4318 for HTTP)
#
# Apply with: kubectl apply -f obzly-otel-collector.yaml
#
# The API key below was generated just for this collector and is shown here
# only once - save this file somewhere safe (or rotate it later by deleting
# this key in Obzly's API Keys page and generating a new config).
#
# Image tag pinned to a version known to work at generation time - check
# https://github.com/open-telemetry/opentelemetry-collector-releases/releases
# for newer ones before deploying to production.

apiVersion: v1
kind: Namespace
metadata:
  name: ${namespace}
---
apiVersion: v1
kind: Secret
metadata:
  name: obzly-otel-collector
  namespace: ${namespace}
type: Opaque
stringData:
  api-key: "${apiKey}"
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: obzly-otel-collector
  namespace: ${namespace}
data:
  config.yaml: |
    extensions:
      health_check:
        endpoint: ${otlpHost}:13133
      # Keeps file_log's read offsets on the node, so a collector restart
      # or rollout neither skips nor re-sends lines.
      file_storage:
        directory: /var/lib/otelcol/file_storage

    receivers:
      # Tails every container's log files on this node; start_at applies only
      # to files with no saved offset yet.
      file_log:
        include: [${logInclude}]
        exclude: [${logExclude}]
        start_at: end
        storage: file_storage
        include_file_path: true
        include_file_name: false
        operators:
          - type: container

      # Accepts OTLP pushed directly from app SDKs running in the cluster
      # (logs, traces, and metrics all share this one receiver).
      otlp:
        protocols:
          grpc:
            endpoint: ${otlpHost}:4317
          http:
            endpoint: ${otlpHost}:4318

      # Node-level resource metrics (CPU, memory, disk, network,
      # filesystem) for this node - one set of series per DaemonSet pod,
      # so per-node dashboards work the same way as node_exporter's would.
      host_metrics:
        # The node's root filesystem, mounted read-only below; without it the
        # filesystem scraper reports the collector container's own disk.
        # Network comes from the pod's namespace unless hostNetwork is on.
        root_path: /hostfs
        collection_interval: 30s
        scrapers:
          cpu: {}
          memory: {}
          disk: {}
          filesystem: {}
          network: {}
          load: {}

      # Per-pod/per-container resource metrics (CPU, memory, network,
      # filesystem) from this node's own kubelet /stats/summary endpoint -
      # what actually backs "kubectl top pod" and is the only source of
      # k8s.pod.*/k8s.container.*-scoped series (host_metrics above is
      # node-level only, with no pod/container labels at all). Needs the
      # nodes/stats RBAC rule below; kubelet's serving cert is self-signed
      # on most clusters, hence insecure_skip_verify.
      kubelet_stats:
        collection_interval: 30s
        auth_type: serviceAccount
        # KUBE_NODE_IP (the node's hostIP via the downward API), not
        # KUBE_NODE_NAME - the node's Kubernetes object name isn't
        # guaranteed to resolve as a DNS name from inside a pod (true on
        # plenty of real clusters, especially self-managed ones with
        # non-DNS-friendly node names), whereas hostIP is always directly
        # reachable.
        endpoint: "https://\${env:KUBE_NODE_IP}:10250"
        insecure_skip_verify: true
        metric_groups: [container, pod, node, volume]
${limitMetricsYAML}
    processors:
${MEMORY_LIMITER_YAML}      # send_batch_max_size caps every export at 2500 records (half of
      # Obzly's own server-side MaxBatchSize=5000, leaving margin) -
      # Obzly's ingest API hard-rejects (whole batch, no partial
      # acceptance) any single request over that limit; leaving this
      # unset uses the batch processor's own default (send_batch_size:
      # 8192, no hard max), which a busy multi-node cluster can genuinely
      # exceed within one 200ms flush window - a real, observed failure
      # mode (mixed 200/400 responses from the same collector), not a
      # schema problem.
      batch:
        send_batch_size: 2500
        send_batch_max_size: 2500
      # The file_log receiver's container operator unwraps the runtime's log
      # format but never sets severity, so every tailed line arrived as
      # info. This fills it in, only when missing (SDK-pushed OTLP logs keep
      # theirs), from: JSON level/severity/lvl keys, klog (I/W/E/F prefix),
      # logfmt level=, [level] brackets (nginx), then a bare uppercase
      # ERROR/WARN/... token. Obzly normalizes the aliases (notice, WARNING,
      # crit, ...). Lines with no level stay info. Tested on
      # otelcol-contrib 0.161.0 and 0.162.0.
      transform/severity:
        error_mode: ignore
        log_statements:
          - context: log
            conditions:
              - log.severity_number == SEVERITY_NUMBER_UNSPECIFIED and log.severity_text == ""
            statements:
              - merge_maps(log.cache, ParseJSON(log.body), "upsert") where IsString(log.body) and IsMatch(log.body, "^\\\\s*\\\\{")
              - set(log.severity_text, log.cache["level"]) where IsString(log.cache["level"])
              - set(log.severity_text, log.cache["severity"]) where log.severity_text == "" and IsString(log.cache["severity"])
              - set(log.severity_text, log.cache["lvl"]) where log.severity_text == "" and IsString(log.cache["lvl"])
              - merge_maps(log.cache, ExtractPatterns(log.body, "^(?P<klog>[IWEF])\\\\d{4} \\\\d{2}:\\\\d{2}:\\\\d{2}"), "upsert") where log.severity_text == "" and IsString(log.body)
              - set(log.severity_text, "info") where log.cache["klog"] == "I"
              - set(log.severity_text, "warn") where log.cache["klog"] == "W"
              - set(log.severity_text, "error") where log.cache["klog"] == "E"
              - set(log.severity_text, "fatal") where log.cache["klog"] == "F"
              - merge_maps(log.cache, ExtractPatterns(log.body, "(?i)\\\\blevel=\\"?(?P<logfmt>[a-z]+)"), "upsert") where log.severity_text == "" and IsString(log.body)
              - set(log.severity_text, log.cache["logfmt"]) where log.severity_text == "" and IsString(log.cache["logfmt"])
              - merge_maps(log.cache, ExtractPatterns(log.body, "(?i)\\\\[(?P<bracket>trace|debug|info|notice|warn|warning|error|crit|critical|alert|emerg|fatal)\\\\]"), "upsert") where log.severity_text == "" and IsString(log.body)
              - set(log.severity_text, log.cache["bracket"]) where log.severity_text == "" and IsString(log.cache["bracket"])
              - merge_maps(log.cache, ExtractPatterns(log.body, "\\\\b(?P<token>TRACE|DEBUG|INFO|NOTICE|WARN|WARNING|ERROR|CRITICAL|FATAL|PANIC)\\\\b"), "upsert") where log.severity_text == "" and IsString(log.body)
              - set(log.severity_text, log.cache["token"]) where log.severity_text == "" and IsString(log.cache["token"])
      # Cloud/host resource attributes (cloud.*, host.id, host.name, os.type)
      # that Infrastructure reads. Detectors for other clouds return nothing,
      # and the timeout bounds the whole detection. env first so
      # OTEL_RESOURCE_ATTRIBUTES wins; the cluster detectors (eks, aks) before
      # their VM ones so cloud.platform names the cluster service; gcp covers
      # both GCE and GKE; system is the fallback for host.name/os.type. eks
      # reads the aws-auth ConfigMap (RBAC rule below).
      resource_detection:
        detectors: [env, eks, ec2, gcp, aks, azure, system]
        timeout: 5s
        override: false
${clusterNameProcessorYAML}${filterProcessorYAML}      # Enriches every record with pod/namespace/node metadata so Obzly's
      # service_name grouping (dashboards, trace search) has something
      # meaningful to work with for logs tailed from disk (OTLP pushed
      # directly by an app SDK already sets its own service.name). On
      # metrics it adds the workload (Deployment, StatefulSet, ...) names
      # the Infrastructure inventory groups pods by.
      k8s_attributes:
        auth_type: serviceAccount
        passthrough: false
        extract:
          metadata:
            - k8s.pod.name
            - k8s.namespace.name
            - k8s.node.name
            - k8s.deployment.name
            - k8s.replicaset.name
            - k8s.statefulset.name
            - k8s.daemonset.name
            - k8s.job.name
            - k8s.cronjob.name
            - k8s.container.name
        pod_association:
          - sources:
              - from: resource_attribute
                name: k8s.pod.uid

    exporters:
      otlp_http:
        endpoint: "${baseUrl}"
        headers:
          X-API-Key: "\${env:OBZLY_API_KEY}"
        # Uncomment if this endpoint uses a self-signed/internal CA cert:
        # tls:
        #   insecure_skip_verify: true

    service:
      extensions: [health_check, file_storage]
      pipelines:
        logs:
          receivers: [file_log, otlp]
          processors: [memory_limiter, transform/severity, resource_detection${clusterNameProcessorRef}, k8s_attributes${filterProcessorRef}, batch]
          exporters: [otlp_http]
        traces:
          # k8s_attributes isn't in this pipeline: it only reliably enriches
          # records that already carry a k8s.pod.uid resource attribute,
          # which the file_log receiver's container operator sets from the
          # log file path automatically - an OTLP push from an app SDK
          # generally won't set that itself, so it wouldn't enrich these
          # anyway. The app's own OTLP resource attributes (service.name,
          # etc.) already carry through untouched.
          receivers: [otlp]
          processors: [memory_limiter, batch]
          exporters: [otlp_http]
        metrics:
          receivers: [otlp, host_metrics, kubelet_stats]
          processors: [memory_limiter, resource_detection${clusterNameProcessorRef}, k8s_attributes${filterProcessorRef}, batch]
          exporters: [otlp_http]
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: obzly-otel-collector
  namespace: ${namespace}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: obzly-otel-collector
rules:
  - apiGroups: [""]
    resources: ["pods", "namespaces", "nodes"]
    verbs: ["get", "list", "watch"]
  # k8s_attributes needs to list/watch ReplicaSets to resolve k8s.deployment.name
  # (it walks the Pod -> ReplicaSet -> Deployment ownership chain) - the
  # extract.metadata list below requests that field, so this is required,
  # not optional; without it the collector logs a constant stream of
  # "replicasets.apps is forbidden" warnings and every pod's
  # k8s.deployment.name is left unresolved.
  - apiGroups: ["apps"]
    resources: ["replicasets"]
    verbs: ["get", "list", "watch"]
  # ...and Jobs, to resolve a CronJob's pods to k8s.cronjob.name.
  - apiGroups: ["batch"]
    resources: ["jobs"]
    verbs: ["get", "list", "watch"]
  # kubelet_stats reads per-pod/per-container stats from each node's own
  # kubelet (the /stats/summary endpoint) - a separate permission footprint
  # from the pods/namespaces/nodes rule above, which is for k8s_attributes'
  # Kubernetes API metadata lookups, not the kubelet's stats endpoint.
  - apiGroups: [""]
    resources: ["nodes/stats"]
    verbs: ["get"]
${nodesProxyRule}  # resource_detection's "eks" detector confirms this is actually EKS by
  # reading the aws-auth ConfigMap in kube-system - without this rule it
  # can't tell, and silently skips setting cloud.platform=aws_eks (the
  # other detectors read the node's cloud metadata service, not the API).
  - apiGroups: [""]
    resources: ["configmaps"]
    resourceNames: ["aws-auth"]
    verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: obzly-otel-collector
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: obzly-otel-collector
subjects:
  - kind: ServiceAccount
    name: obzly-otel-collector
    namespace: ${namespace}
---
apiVersion: v1
kind: Service
metadata:
  name: obzly-otel-collector
  namespace: ${namespace}
spec:
  selector:
    app: obzly-otel-collector
  ports:
    - name: otlp-grpc
      port: 4317
      targetPort: 4317
    - name: otlp-http
      port: 4318
      targetPort: 4318
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: obzly-otel-collector
  namespace: ${namespace}
spec:
  selector:
    matchLabels:
      app: obzly-otel-collector
  template:
    metadata:
      labels:
        app: obzly-otel-collector
    spec:
      serviceAccountName: obzly-otel-collector
${hostNetworkSpec}      initContainers:
        # The hostPath directory is created root-owned; the collector runs
        # as 10001.
        - name: file-storage-permissions
          image: ${INIT_IMAGE}
          command: ["sh", "-c", "mkdir -p /var/lib/otelcol/file_storage && chown -R 10001:10001 /var/lib/otelcol/file_storage"]
          # Root only for the chown; nothing else.
          securityContext:
            runAsUser: 0
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
              add: ["CHOWN", "DAC_OVERRIDE"]
            seccompProfile:
              type: RuntimeDefault
          volumeMounts:
            - name: file-storage
              mountPath: /var/lib/otelcol
      containers:
        - name: otel-collector
          image: ${COLLECTOR_IMAGE}
          args: ["--config=/etc/otel/config.yaml"]
          env:
            - name: OBZLY_API_KEY
              valueFrom:
                secretKeyRef:
                  name: obzly-otel-collector
                  key: api-key
            - name: KUBE_NODE_IP
              valueFrom:
                fieldRef:
                  fieldPath: status.hostIP
${podIPEnv}          ports:
            - containerPort: 4317
            - containerPort: 4318
${HEALTH_PROBES}${RESTRICTED_SECURITY_CONTEXT}          volumeMounts:
            - name: config
              mountPath: /etc/otel
            - name: tmp
              mountPath: /tmp
            - name: varlogpods
              mountPath: /var/log/pods
              readOnly: true
            - name: varlibdockercontainers
              mountPath: /var/lib/docker/containers
              readOnly: true
            - name: hostfs
              mountPath: /hostfs
              readOnly: true
              mountPropagation: HostToContainer
            - name: file-storage
              mountPath: /var/lib/otelcol
          resources:
            requests:
              cpu: 100m
              memory: 128Mi
            limits:
              memory: 512Mi
      volumes:
        - name: config
          configMap:
            name: obzly-otel-collector
        - name: tmp
          emptyDir: {}
        - name: varlogpods
          hostPath:
            path: /var/log/pods
        - name: varlibdockercontainers
          hostPath:
            path: /var/lib/docker/containers
        - name: hostfs
          hostPath:
            path: /
        - name: file-storage
          hostPath:
            path: /var/lib/otelcol
            type: DirectoryOrCreate
${clusterAgent ? clusterAgentManifest(namespace, baseUrl, clusterNameProcessorYAML, clusterNameProcessorRef, filterProcessorYAML, filterProcessorRef) : ''}`;
}

// The cluster agent reads the Kubernetes API for what no node can see. Only
// the cluster detectors (eks, aks): host ones (ec2, gcp, azure, system) would
// stamp every object with the agent's own node.
function clusterAgentManifest(
  namespace: string,
  baseUrl: string,
  clusterNameProcessorYAML: string,
  clusterNameProcessorRef: string,
  filterProcessorYAML: string,
  filterProcessorRef: string,
): string {
  return `---
apiVersion: v1
kind: ConfigMap
metadata:
  name: obzly-otel-cluster-agent
  namespace: ${namespace}
data:
  config.yaml: |
    extensions:
      health_check:
        endpoint: \${env:KUBE_POD_IP}:13133

    receivers:
      k8s_cluster:
        auth_type: serviceAccount
        collection_interval: 30s
        node_conditions_to_report: [Ready]
        allocatable_types_to_report: [cpu, memory]

    processors:
${MEMORY_LIMITER_YAML}      batch:
        send_batch_size: 2500
        send_batch_max_size: 2500
      resource_detection:
        detectors: [env, eks, aks]
        timeout: 5s
        override: false
${clusterNameProcessorYAML}${filterProcessorYAML}
    exporters:
      otlp_http:
        endpoint: "${baseUrl}"
        headers:
          X-API-Key: "\${env:OBZLY_API_KEY}"

    service:
      extensions: [health_check]
      pipelines:
        metrics:
          receivers: [k8s_cluster]
          processors: [memory_limiter, resource_detection${clusterNameProcessorRef}${filterProcessorRef}, batch]
          exporters: [otlp_http]
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: obzly-otel-cluster-agent
  namespace: ${namespace}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: obzly-otel-cluster-agent
rules:
  # Read-only: what the k8s_cluster receiver watches.
  - apiGroups: [""]
    resources: ["events", "namespaces", "namespaces/status", "nodes", "nodes/spec", "persistentvolumes",
      "persistentvolumeclaims", "pods", "pods/status", "replicationcontrollers",
      "replicationcontrollers/status", "resourcequotas", "services"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["apps"]
    resources: ["daemonsets", "deployments", "replicasets", "statefulsets"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["batch"]
    resources: ["jobs", "cronjobs"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["autoscaling"]
    resources: ["horizontalpodautoscalers"]
    verbs: ["get", "list", "watch"]
  # The eks detector (see the DaemonSet's ClusterRole).
  - apiGroups: [""]
    resources: ["configmaps"]
    resourceNames: ["aws-auth"]
    verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: obzly-otel-cluster-agent
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: obzly-otel-cluster-agent
subjects:
  - kind: ServiceAccount
    name: obzly-otel-cluster-agent
    namespace: ${namespace}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: obzly-otel-cluster-agent
  namespace: ${namespace}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: obzly-otel-cluster-agent
  template:
    metadata:
      labels:
        app: obzly-otel-cluster-agent
    spec:
      serviceAccountName: obzly-otel-cluster-agent
      containers:
        - name: otel-collector
          image: ${COLLECTOR_IMAGE}
          args: ["--config=/etc/otel/config.yaml"]
          env:
            - name: OBZLY_API_KEY
              valueFrom:
                secretKeyRef:
                  name: obzly-otel-collector
                  key: api-key
            - name: KUBE_POD_IP
              valueFrom:
                fieldRef:
                  fieldPath: status.podIP
${HEALTH_PROBES}${RESTRICTED_SECURITY_CONTEXT}          volumeMounts:
            - name: config
              mountPath: /etc/otel
            - name: tmp
              mountPath: /tmp
          resources:
            requests:
              cpu: 50m
              memory: 128Mi
            limits:
              memory: 512Mi
      volumes:
        - name: config
          configMap:
            name: obzly-otel-cluster-agent
        - name: tmp
          emptyDir: {}
`;
}
