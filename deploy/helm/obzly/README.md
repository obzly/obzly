# Obzly Helm chart

One chart for every role (Phase 14 item 10a, with the querier tier from 10b). It renders the same
pods as the plain manifests in `deploy/kubernetes/eks/`, so read those READMEs for the reasoning
behind each tier.

| `mode` | What runs |
| --- | --- |
| `ha` (default) | `broker` StatefulSet (NATS JetStream ingest buffer), `api` (UI, REST, OTLP, query coordination), `worker` (Parquet writes, compaction, retention, alert evaluation), `querier` (query executors) |
| `single` | one `SERVICE_MODE=all` pod on a PersistentVolumeClaim |
| `control-plane` | the multi-site control plane |

Postgres and object storage are always yours (RDS/Aurora, S3 or any S3-compatible store). Valkey
runs in the release unless `valkey.enabled: false` points at your own.

## Install

```bash
kubectl create namespace obzly
helm install obzly deploy/helm/obzly -n obzly \
  --set config.objectStorageUrl='s3://my-bucket?region=eu-west-1' \
  --set secrets.databaseDsn='host=... user=obzly password=... dbname=obzly port=5432 sslmode=require' \
  --set serviceAccount.annotations.eks\.amazonaws\.com/role-arn=arn:aws:iam::123456789012:role/obzly-s3 \
  --set broker.nodeSelector.karpenter\.sh/capacity-type=on-demand
```

Then find the bootstrap admin's token (`NOTES.txt` prints the command) and open the UI through
`kubectl port-forward` or `ingress.enabled`.

On AWS, the service account's role (`serviceAccount.annotations`, IRSA) needs `s3:PutObject`,
`s3:PutObjectTagging`, `s3:GetObject`, `s3:ListBucket` and `s3:DeleteObject` on the bucket.

Prefer a Secret you manage (External Secrets, Sealed Secrets, ...)? Create it with the keys
`DATABASE_DSN`, `NATS_TOKEN` and `QUERIER_TOKEN` (ha mode), optionally `VALKEY_PASSWORD` (for an
external Valkey) and the `GITHUB_*` / `GOOGLE_*` OAuth settings, and set `secrets.existingSecret`.
Otherwise the chart creates `<release>-secrets`; NATS and querier tokens left empty are generated on
install and kept on upgrade.

The bundled Valkey requires a password (`valkey.auth.enabled`), generated the same way into
`<release>-valkey` and passed to every Obzly pod, also with `existingSecret`. NetworkPolicies let
only this release's pods reach Valkey (`valkey.networkPolicy`) and the brokers' client and cluster
ports (`broker.networkPolicy`); they need a CNI that enforces NetworkPolicy.

Obzly containers get `securityContext` defaults: no privilege escalation, seccomp `RuntimeDefault`,
and every capability dropped except the four the entrypoint needs (`CHOWN`, `DAC_OVERRIDE`,
`SETUID`, `SETGID`) to fix the data volume's ownership before it drops to uid 1000. The app itself
runs with none.

## Things to set for production

- `broker.nodeSelector` / `tolerations`: on-demand nodes only. Brokers hold acknowledged batches
  until a worker writes them; size `broker.storage.size` for about an hour of ingest and keep
  three `broker.streamMaxBytes` inside it. `broker.replicas` is 3 (R3 streams, one pod per zone)
  or 1 (no failover); 2 is refused.
- `config.trustedProxies`: your load balancer's CIDRs, so client IPs are real.
- `tls.otlpGrpc.secretName`: a `kubernetes.io/tls` Secret (e.g. from cert-manager) when OTLP/gRPC
  on 4317 is reachable without a TLS-terminating proxy; plaintext otherwise.
  `tls.queryResults.secretName` (`tls.crt`, `tls.key`, `ca.crt`) encrypts the querier result port;
  its certificate must cover `tls.queryResults.serverName` (default
  `<fullname>-querier.<namespace>.svc`) as a DNS name. Renewed certificates apply without a restart.
- `api.autoscaling` and `querier.autoscaling` (HPA on CPU), `querier.networkPolicy` (needs a CNI
  that enforces NetworkPolicy), `metrics.serviceMonitor` (a Prometheus Operator PodMonitor for
  every tier).
- `config.extra` for any other setting, e.g. `QUERY_MAX_SCAN_GB`, or `SITE_ID`,
  `CONTROL_PLANE_ISSUER` to enroll the Site in a control plane.

## Settings and operations

- Any setting from [`docs/configuration.md`](../../../docs/configuration.md) goes in
  `config.extra` (or `secrets.extra` for credentials), for example:
  ```yaml
  config:
    extra:
      ALERT_EGRESS_DENYLIST: 172.20.0.0/16
  ```
  Email (SMTP) and notification channels are set up in Obzly settings, not here.
- `config.publicUrl`: the address users reach Obzly at, for MCP clients' OAuth.
- A control plane serves SCIM at `/scim/v2` for your identity provider; keep it reachable through
  the Ingress if you provision users that way.
- After a Postgres restore, reconcile the catalog with the bucket
  ([backup-restore runbook](../../../docs/runbooks/backup-restore.md)):
  `kubectl -n obzly exec deploy/obzly-worker -- ./main reconcile`.

## Architectures

Releases from v0.47.0 are published for `linux/arm64` and `linux/amd64`, so the chart pins no
architecture. Earlier releases are arm64 only: on amd64 nodes use v0.47.0 or later (the chart's
default), or add `nodeSelector: {kubernetes.io/arch: arm64}` for an older tag.

## Not in v1

Migration Job (the app migrates at startup under a lock), in-cluster Postgres (CNPG) and
Valkey operator modes, `small` / `medium` / `enterprise` presets (item 11's capacity model).
