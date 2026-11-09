# Docs

- [`configuration.md`](configuration.md) - every environment variable, with its default, grouped
  by purpose, and what each `SERVICE_MODE` runs.
- [`SYNTHETICS.md`](SYNTHETICS.md) - synthetic monitoring: monitors, probes and probe groups,
  destinations, folders, dependencies, alerts per monitor, YAML import/export
  ([`examples/synthetics-checks.yaml`](examples/synthetics-checks.yaml)).
- [`INFRASTRUCTURE.md`](INFRASTRUCTURE.md) - the Infrastructure inventory (assets, health,
  lifecycle, changes) and cloud connections (AWS and Azure, the wizard, permissions, credentials);
  templates in [`../deploy/cloud/`](../deploy/cloud/).
- [`CLOUD_COST_POSTURE.md`](CLOUD_COST_POSTURE.md) - design for the cost and posture uses of cloud
  connections: FOCUS exports or billing APIs, native posture with the Obzly baseline as fallback,
  explorer and trends, recommendations and the commitment planner (approved, not built yet).
- [`runbooks/`](runbooks/) - operating Obzly:
  - [`backup-restore.md`](runbooks/backup-restore.md) - what to back up, key material
    (`secret_keys`), the restore order, `obzly reconcile`, the integrity audit and the restore
    drill with its measured results.
  - [`dr.md`](runbooks/dr.md) - failure scenarios, what each loses, and the order for a rebuild.
  - [`upgrade.md`](runbooks/upgrade.md) - Upgrade status verdicts, rollout order, rollback.
- [`ARCHITECTURE.drawio`](ARCHITECTURE.drawio) - system architecture diagram, four pages (open in
  [diagrams.net](https://app.diagrams.net) / the draw.io desktop app / the VS Code draw.io
  extension to view or edit):
  - **"Obzly Architecture"** - the single-site system overview.
  - **"Multi-site"** - the control plane + Sites overview (ARCHITECTURE.md "Multi-site: control
    plane and Sites"): the browser's central session, the control plane's identity/registry/
    token-exchange/proxy pieces, and how each Site stays an otherwise-unchanged single-site
    deployment behind it.
  - **"Query path"** - how a search runs (ARCHITECTURE.md "Distributed query" 4f-4h): the
    coordinator's newest-first time windows and early stop, the streamed response, the Valkey
    partition cache, and the queriers' affinity, persistent cache, warm-up and pre-fetch on upload.
  - **"Synthetics and cloud"** - control-plane features beyond the proxy (ARCHITECTURE.md
    "Synthetics, the infrastructure inventory and cloud connections"): probe groups pulling
    monitors and cloud connections, results to a Site, the shared-config document carrying
    channels and monitor alerts, the control plane as OIDC issuer for cloud federation, and the
    Site's inventory merging agent and cloud data.

  [`ARCHITECTURE.png`](ARCHITECTURE.png) / [`ARCHITECTURE-multisite.png`](ARCHITECTURE-multisite.png)
  / [`ARCHITECTURE-query-path.png`](ARCHITECTURE-query-path.png) /
  [`ARCHITECTURE-synthetics-cloud.png`](ARCHITECTURE-synthetics-cloud.png) are rendered previews of those
  pages - regenerate them after editing the diagram rather than hand-editing the PNGs, with the
  draw.io desktop app's CLI (`drawio -x -f png -p <page number> -b 20 -o <file>.png
  ARCHITECTURE.drawio`, pages numbered from 1), or headless via
  `docker run --rm -v "$PWD/docs":/data rlespinasse/drawio-export -f png -o . ARCHITECTURE.drawio`,
  which exports one PNG per page named after the page.

See [`../ARCHITECTURE.md`](../ARCHITECTURE.md) for the full written architecture doc these
diagrams summarize. The first two pages predate Phase 14: they show the single `obzly` process,
not the `api`/`worker`/`broker`/`querier` roles or the JetStream ingest buffer - see
ARCHITECTURE.md's "Scale architecture" for those until they are redrawn; the "Query path" page
shows the `api` coordinator and `querier` executors.

- [`ENTERPRISE_SCALE_PLAN.md`](ENTERPRISE_SCALE_PLAN.md) - the gap assessment and phased plan
  for 1-5 TB/day ingest, with ~1 PB stored at 1-year retention as the ceiling: verified
  bottlenecks, recommended design with acceptance criteria, S3 and compute cost by ingest tier,
  the in-cluster operator option, Spot-instance implications, the migration and version-skew story
  for existing data, and open questions. Its design now lives in `../ARCHITECTURE.md` ("Scale
  architecture", including what was built) and its work in `../ROADMAP.md` (Phase 14); this file
  keeps the reasoning and the cost model.
