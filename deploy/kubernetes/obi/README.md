# OBI (OpenTelemetry eBPF Instrumentation) - optional

Runs OBI as a per-node DaemonSet with the `network` metrics feature, exporting
`obi.network.flow.bytes` / `obi.network.flow.packets` to Obzly over
OTLP/HTTP. Obzly's Service Map uses them to draw edges for services that have no
tracing SDK (e.g. a bare Postgres/Redis).

Separate namespace, not part of `../eks`: it needs `hostPID`, `hostNetwork` and eBPF
capabilities, so adopting it is an independent decision.

1. Edit `daemonset.yaml`: set `OTEL_EXPORTER_OTLP_ENDPOINT` to your Obzly URL
   (optionally `OTEL_EBPF_KUBE_CLUSTER_NAME`).
2. Edit `secret.yaml` with an Obzly API key, then `kubectl apply -f secret.yaml`
   (after the namespace exists: `kubectl apply -k .` first).
3. `kubectl apply -k .`

Easier: the Data Sources catalog ("eBPF Network Map (OBI)") generates this bundle
pre-filled with a real API key and endpoint.

Verified on a live EKS cluster: starts on every node with the narrow capability set and
`hostNetwork`. Set `OTEL_EBPF_KUBE_CLUSTER_NAME`; OBI often can't detect it (it logs a warning).
OBI reports each flow twice per node (workload and its Service); Obzly de-duplicates this when
building the Service Map.

## Optional: Kubernetes metadata cache

`k8s-cache.yaml` deploys OBI's k8s-cache (one shared API-server watch instead of one per
node; useful on large clusters). It is not in `kustomization.yaml`. To enable it, apply the
file and set `OTEL_EBPF_KUBE_META_CACHE_ADDRESS=obi-k8s-cache.obzly-obi.svc:50055` on the
DaemonSet. The catalog entry has a yes/no field that does both. Image tag and its
RBAC reuse are unvalidated on a live cluster.

## Optional: application traces

OBI can also produce traces for your applications with no code changes (HTTP, gRPC, SQL,
Redis, Kafka and similar calls), exported to Obzly over the same OTLP connection. It
instruments nothing until told which workloads: `traces-config.yaml` holds a
`discovery.instrument` list of namespaces (`k8s_namespace: "*"` for everything except
OBI's own built-in system exclusions - point this at your actual application namespace(s),
not the namespace OBI/the collector itself runs in, or it'll find nothing to instrument).
Apply it, then add to the DaemonSet: `OTEL_EBPF_METRICS_FEATURES=application,network,
network_flow_packets`, the env var `OTEL_EBPF_CONFIG_PATH=/config/obi-config.yml`, a mount
of the `obi-config` ConfigMap at `/config`, and one of these permission levels:

| Option | securityContext | Result (EKS, Bottlerocket 1.62, kernel 6.12, arm64) |
|---|---|---|
| Capabilities only | add `DAC_OVERRIDE` | Hangs at "attaching uprobes" (SELinux). Untested elsewhere. |
| SELinux label | + `seLinuxOptions: {type: super_t}` | Traces work; Node.js injector fails (no runtime metrics). |
| SELinux label + SYS_ADMIN | + `SYS_ADMIN` | Traces and Node.js injector work. |
| Privileged | `privileged: true` | Everything works; full host access. |

The catalog entry ("Trace applications", then "Trace permissions") generates all of this.
Costs extra CPU on every node.

Diagnosed live on a real EKS cluster (kernel in the 5.14-6.6 range): without
`DAC_OVERRIDE`, uprobe attachment hangs indefinitely right after logging "attaching
uprobes as uprobe_multi links" - no error, no traces ever exported, network metrics keep
working normally the whole time (so that's the specific symptom to look for).

**`DAC_OVERRIDE` alone does not fix it on Bottlerocket (checked 2026-09-27).** On EKS with
Bottlerocket 1.62, kernel 6.12, arm64, every OBI pod with `DAC_OVERRIDE` still stopped at
"attaching uprobes as uprobe_multi links" and exported no traces or application metrics.
Network metrics kept working. The same DaemonSet with `privileged: true` instrumented the
workloads within seconds and traces and HTTP/DB request metrics reached Obzly, so the blocker
is privilege-related, not OBI itself. It is Bottlerocket's SELinux policy: unprivileged with
`securityContext.seLinuxOptions: {type: super_t}` (Bottlerocket's label for privileged
containers), uprobes attach within seconds and traces reach Obzly. What still fails is
`join target netns: operation not permitted` (setns needs `CAP_SYS_ADMIN`), so OBI's Node.js
injector doesn't attach: no trace-context propagation or runtime metrics for Node.js
services. With `super_t` + `SYS_ADMIN` the netns errors are gone and the Node.js injector
attaches. In none of the three setups (`super_t`, `super_t` + `SYS_ADMIN`, `privileged`) did
any trace span more than one service, so cross-service linking needs separate work (OBI's
context propagation setting, or whether the services call each other directly rather than
through an external load balancer). Note that a pod on a node with no matching
workload also stops at "attaching uprobes"; check a node that runs the target pods.
The images are pinned (`otel/ebpf-instrument:v0.14.0`, the first release with the tracefs
uprobe fallback, and the matching `otel/opentelemetry-ebpf-k8s-cache:v0.14.0`): `main` moves, and
pods on one cluster ended up on different builds.

If you generate this bundle from a control plane, set the endpoint to the Site's public URL:
the control plane does not accept telemetry.
