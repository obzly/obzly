# Deployment

Three supported ways to run Obzly, depending on your target:

- **[`docker/`](docker/)** - Docker Compose: one VM (app, Postgres, Valkey), the HA shape with
  every role, and multi-site examples. What [`install-obzly.sh`](../install-obzly.sh) automates,
  or set up by hand for more control.
- **[`helm/obzly/`](helm/obzly/)** - the Helm chart: a Site (`mode: ha` with brokers, `api`,
  `worker` and `querier`, or `mode: single`) or a control plane, on any Kubernetes. Postgres and
  object storage are yours; Valkey can run in the release.
- **[`kubernetes/eks/`](kubernetes/eks/)** - plain manifests for `kubectl apply -k`, written for
  AWS EKS with a Graviton nodepool, externally-hosted Postgres, and S3 access via IRSA.

Also here:

- **[`helm/obzly-probe/`](helm/obzly-probe/)** - the Synthetics probe (`obzly-probe`) as a probe
  group: 3 replicas sharing one token by default, spread across zones, with a service account for
  IRSA or Azure workload identity when it scans cloud connections (see
  [`../docs/SYNTHETICS.md`](../docs/SYNTHETICS.md)).
- **[`cloud/`](cloud/)** - read-only CloudFormation and Bicep templates for cloud connections; the
  wizard in Settings → Cloud connections fills them in.

Images are published for `linux/amd64` and `linux/arm64` (from v0.47.0). Every setting is in
[`../docs/configuration.md`](../docs/configuration.md); backups, DR and upgrades are in
[`../docs/runbooks/`](../docs/runbooks/). See each folder's own README for setup steps.

To monitor Obzly itself (Prometheus `/metrics` on `METRICS_ADDR`, SLO alert rules, a Grafana
dashboard), see [`observability/`](observability/).

## Scaling out

Every setup starts as one app process (`SERVICE_MODE=all`). The same image also runs each role on
its own: `api` (UI, query API, ingest), `worker` (writing, compaction, retention), `broker` (the
NATS JetStream ingest buffer) and `querier` (query executors). See the main README's
"Enterprise-grade logging and monitoring" section for what each role does,
[`docker/`](docker/) for the HA Compose file, and the Helm chart or
[`kubernetes/eks/ha/`](kubernetes/eks/ha/) for Kubernetes. Running more than one replica needs
object storage (`OBJECT_STORAGE_URL`).

## Multi-site (control plane + Sites)

Optional (ARCHITECTURE.md "Multi-site: control plane and Sites") - one
centrally-deployed control plane (UI + identity) fronting one or more
independently-deployed Sites, each still just a normal single-site
deployment from the list above with a few extra env vars set. Every
Site-facing file above documents its own commented multi-site block:

- Docker: [`docker/docker-compose.multisite.yml.example`](docker/docker-compose.multisite.yml.example)
  is a full local reference (control plane + a Site, one compose file);
  [`docker/docker-compose.control-plane.yml.example`](docker/docker-compose.control-plane.yml.example)
  is the production shape for a standalone control plane. Enroll an
  EXISTING `docker-compose.yml`/`docker-compose.ha.yml` install as a Site by
  uncommenting its own multi-site env block instead of switching files.
  `install-obzly.sh --help`-equivalent env vars: `OBZLY_ROLE=control-plane`
  (with `PUBLIC_URL`) or `OBZLY_ROLE=site` (with `SITE_ID`/
  `CONTROL_PLANE_ISSUER`).
- Kubernetes: the Helm chart's `mode: control-plane`, or
  [`kubernetes/eks/control-plane/`](kubernetes/eks/control-plane/), a separate Kustomization
  for the control plane. A Site enrolls through the chart's `config.extra` (`SITE_ID`,
  `CONTROL_PLANE_ISSUER`), or by the "Enroll as a Site" section of `kubernetes/eks/`'s and
  `kubernetes/eks/ha/`'s READMEs.
