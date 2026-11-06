# Kubernetes manifests (EKS / Graviton)

Deploys obzly to an EKS cluster with an arm64 (Graviton) nodepool. Postgres runs
externally (RDS/Aurora); S3 credentials come from IRSA, not static keys.

The Helm chart in `deploy/helm/obzly` deploys the same shapes (`mode: single`, `mode: ha`) with
values instead of edited YAML; these plain manifests suit `kubectl apply -k` and GitOps tools that
prefer them.

This directory's manifests run a single replica (see "Why these design choices"
below for why that's a manifest-simplicity choice, not a hard limit). For real
horizontal scaling - multiple request-serving replicas, an independently-scaled
background-worker tier - see `ha/` instead.

## Before applying anything

1. **IAM role for S3 (IRSA)**: create an IAM role (outside this repo - eksctl,
   Terraform, whatever you already use) trusted by your cluster's OIDC provider,
   with permissions scoped to the bucket obzly uploads segments to
   (`s3:PutObject`, `s3:PutObjectTagging`, `s3:GetObject`, `s3:ListBucket`,
   `s3:DeleteObject` on that bucket). `s3:PutObjectTagging` is needed since
   v0.41.0: uploads carry tags for bucket lifecycle rules, and S3 refuses a tagged
   upload without it. Put its ARN into `common/serviceaccount.yaml`'s
   `eks.amazonaws.com/role-arn` annotation.

2. **S3 bucket + region**: fill in `OBJECT_STORAGE_URL` in `common/configmap.yaml`
   (`s3://<bucket>?region=<region>`, no `endpoint`/`use_path_style` - those are
   MinIO-only). The bucket must already exist; obzly never creates it.

3. **Postgres**: fill in `DATABASE_DSN` in `secret.yaml` with your externally-hosted
   instance's connection details. The app runs its own migrations against it on
   every startup (see `deployment.yaml`'s `Recreate` strategy comment for why
   this matters with more than one replica).

4. **Storage class**: `pvc.yaml` defaults to `storageClassName: gp3`. Check
   `kubectl get storageclass` and change it if your cluster names its EBS class
   differently (e.g. `gp2` on an older cluster).

5. **Real client IPs behind a load balancer**: set `TRUSTED_PROXIES` in
   `common/configmap.yaml` to your VPC CIDR (`aws ec2 describe-vpcs --vpc-ids <id>
   --query 'Vpcs[0].CidrBlock'`). An ALB (either target type) or an ingress
   controller connects to the pod from an address in that range and passes the
   real client in `X-Forwarded-For`. Left empty, obzly trusts no proxy, so
   request logs, the audit log, sign-in rate limiting, project IP allow-lists
   and the `client_ip` of ingested logs all show the load balancer's private IP.
   An NLB with client IP preservation needs nothing here. Apply the ConfigMap,
   then `kubectl -n obzly rollout restart deployment` - pods only read it at
   startup.

## Applying

```bash
# Everything except secrets:
kubectl apply -k .

# Secrets separately, after filling in secret.yaml (or swap this step for
# your own secret-management tool - External Secrets Operator, Sealed
# Secrets, the AWS Secrets Manager CSI driver, etc. - keeping the Secret's
# name/namespace/keys the same):
kubectl apply -f secret.yaml
```

## Why these design choices

- **Single replica** (`deployment.yaml`, `pvc.yaml`): kept simple on purpose - a
  `ReadWriteOnce` PVC as the app's only local storage, no object storage
  required. The app itself supports running as N replicas safely (leader-locked
  background workers/migrations/admin-bootstrap - see `ARCHITECTURE.md`'s "High
  availability" section) once `OBJECT_STORAGE_URL` is configured; this manifest
  just doesn't require that setup. Use `ha/` for real multi-replica scaling.
- **`Recreate` deploy strategy, not `RollingUpdate`**: with `replicas: 1` and a
  `ReadWriteOnce` PVC, a rolling update briefly running two pods would have the
  second one unable to even mount the volume, on top of both potentially
  running migrations at once. `Recreate` means a short downtime window on
  every deploy instead - fine for a single-node app.
- **No Ingress**: this repo doesn't know your domain, TLS setup, or which
  ingress controller you're running. `service.yaml` is a plain `ClusterIP`;
  point your own Ingress/ALB at it.
- **In-cluster Valkey, no PVC**: it's used for caching/rate-limiting only, not
  durable state (see `backend/internal/util/rate_limit`) - losing it on a
  restart just resets caches/limits. Point `VALKEY_HOST`/`VALKEY_PORT` in
  `common/configmap.yaml` at an external instance (e.g. ElastiCache) instead if you'd
  rather not self-host it. It does hold the API-key cache, token revocations
  and Live Tail, so it starts with `--requirepass` from `VALKEY_PASSWORD` in
  `secret.yaml` (required since v0.57.0), and `common/networkpolicy-valkey.yaml`
  admits only Obzly's pods (enforced only by a CNI with NetworkPolicy support).
- **`nodeSelector: kubernetes.io/arch: arm64`** on both Deployments: pins
  scheduling to the Graviton nodepool these manifests target. Releases from
  v0.47.0 are published for `linux/amd64` too, so on amd64 nodes remove it -
  with v0.47.0 or later; earlier ones are arm64 only.
  The Helm chart (`deploy/helm/obzly`) pins no architecture. On
  `common/valkey-deployment.yaml` it's genuinely optional - that's the
  upstream `valkey/valkey` image, which is multi-arch regardless of anything
  in this repo.

## Enroll as a Site

Optional (ARCHITECTURE.md "Multi-site: control plane and Sites") - lets a
separately-deployed control plane (see `../control-plane/`) manage a central
UI/identity layer while this cluster's obzly install keeps its own data,
projects, plans, API keys and root admin exactly as it always has.

1. Uncomment the `SITE_ID`/`SITE_NAME`/`CONTROL_PLANE_ISSUER`/
   `CONTROL_PLANE_JWKS_URL`/`CONTROL_PLANE_EGRESS_CIDRS` block in
   `common/configmap.yaml` (`ha/` reads the same one). `SITE_ID` must
   match the slug you register this Site under in the control plane's
   Obzly settings → Sites.
2. **`CONTROL_PLANE_JWKS_URL`**: pick whichever address this cluster can
   actually reach the control plane at - its public origin (simplest, if
   this cluster has general internet egress) or an in-cluster/VPC-internal
   DNS name if the control plane runs in the same or a peered cluster/VPC.
   Leave it unset to default to `<CONTROL_PLANE_ISSUER>/.well-known/jwks.json`.
   `CONTROL_PLANE_ISSUER` itself must stay the control plane's exact public
   `PUBLIC_URL` regardless - that's the `iss` string being verified, not an
   address to dial.
3. **Restrict the query/admin surface to the control plane at the ingress**,
   so the reverse proxy is genuinely the only path to it (ARCHITECTURE.md's
   trade-offs section: "what the proxy hides is the query/admin surface").
   Two common approaches on an ALB/AWS Load Balancer Controller ingress:
   - An IP allowlist (`alb.ingress.kubernetes.io/inbound-cidrs` or a WAF
     rule) restricted to the control plane's known egress address(es) - set
     `CONTROL_PLANE_EGRESS_CIDRS` above to the SAME range so the exchange
     endpoint enforces it independently too (defense in depth, not
     either/or).
   - mTLS between the control plane and this ingress, if your ALB/ingress
     controller setup already supports client-cert verification.
   Leave `/v1/logs`, `/v1/traces`, `/api/v1/logs/receiving`, and the OTLP
   gRPC port (`:4317`) open regardless - ingest stays regional and reachable
   by collectors directly, by design (ARCHITECTURE.md's non-goals).
4. This Site's own root admin/local accounts/local MFA keep working
   unchanged (break-glass path) - the exchange endpoint is additive, not a
   replacement for the existing sign-in flow.

## Operating

Every setting is in `docs/configuration.md` (add it to `common/configmap.yaml`, or to
`secret.yaml` if it's a credential). Backups, DR and upgrades are in `docs/runbooks/`; after a
Postgres restore, reconcile the catalog with the bucket from any obzly pod:

```bash
kubectl -n obzly exec deploy/obzly -- ./main reconcile            # report only
```

## Known gap: not-yet-uploaded data on pod loss

The hot tier (`obzly-data` PVC) holds segments written in the last ~15 s that haven't been
uploaded to S3 yet, plus rows still buffered in memory. If the pod is lost before they are written
and uploaded, they are gone with it, as with a local disk on any single-node deployment. Setting
`INGEST_BUFFER=jetstream` (no `NATS_URL`) runs the JetStream buffer inside the pod, on the PVC:
a batch is acknowledged only once the buffer holds it, so a process crash or pod restart loses
nothing. Losing the node can still drop up to the last ~2 minutes, the buffer's disk sync interval.
For no loss on a node failure, use `ha/` with the three-node broker.
