# Docker Compose deployment

Single-VM deployment: builds the app image from source and runs it alongside its own Postgres
and Valkey containers - as one app container, or with its roles split out (see "HA and scale-out"
below). This is what
[`install-obzly.sh`](../../install-obzly.sh) automates (it writes its own
generated `docker-compose.yml` rather than using this file directly, but the
result is the same shape); use the files here if you'd rather set it up
by hand, customize it, or aren't on the Linux/root setup `install-obzly.sh`
assumes.

See the main [README.md](../../README.md#-installation) for the short version.
This file covers the same ground with a bit more detail.

## Setup

Run these from the **repo root**, not from inside `deploy/docker/` - Docker
Compose's build context (`context: .` in the compose file) needs to be the
repo root, where the `Dockerfile` lives.

```bash
cp deploy/docker/docker-compose.yml.example docker-compose.yml
cp deploy/docker/.env.example .env
```

Then edit `.env`:

- Set a real `POSTGRES_PASSWORD` (`openssl rand -hex 16` is a good way to
  generate one).
- Everything else in `.env.example` is optional - see its own comments.

```bash
docker compose up -d --build
```

Obzly comes up on `http://localhost:4005`. Data lives in `./obzly-data`
(hot-tier Parquet segments) and `./pgdata`/`./valkey-data` (Postgres/Valkey),
all relative to wherever you ran `docker compose` from (the repo root).

### Using the published image instead of building

The examples build from source. To run a release instead, replace each service's `build:` block
with `image: ghcr.io/obzly/obzly:${OBZLY_IMAGE_TAG:-latest}` and set `OBZLY_IMAGE_TAG` in `.env`
(for example `v1.1.0`; images are published for `linux/amd64` and `linux/arm64`). Upgrading is
then `docker compose pull && docker compose up -d`.

### Configuration

Add any setting from [`../../docs/configuration.md`](../../docs/configuration.md) to the `obzly`
service's `environment:` - for example `PUBLIC_URL` so MCP clients get the right OAuth origin,
or `TRUSTED_PROXIES` behind a reverse proxy. Email (SMTP) and notification channels are set up in
Obzly settings.

## Optional: S3-compatible cold-tier storage (MinIO)

`docker-compose.yml.example` has a commented-out `minio`/`minio-init` service
pair for testing the object-storage cold tier locally (ARCHITECTURE.md) -
uncomment those plus the `OBJECT_STORAGE_URL`/`AWS_ACCESS_KEY_ID`/
`AWS_SECRET_ACCESS_KEY` lines in the `obzly` service, and set
`MINIO_ROOT_USER`/`MINIO_ROOT_PASSWORD` in `.env`. Off by default -
local-disk-only works fine without any of this.

For real AWS S3 instead of MinIO, see
[`../kubernetes/eks/README.md`](../kubernetes/eks/README.md)'s notes on
`OBJECT_STORAGE_URL` format, or just set `OBJECT_STORAGE_URL=s3://<bucket>?region=<region>`
directly and provide real `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`
credentials (no IRSA outside Kubernetes - this is a plain container, so
static credentials are the only option here).

## HA and scale-out

[`docker-compose.ha.yml.example`](docker-compose.ha.yml.example) runs the roles separately:

- two `api` replicas (`obzly-api-a`, `obzly-api-b`, each with its own disk) behind your own load
  balancer;
- `obzly-worker`, which writes ingested batches to Parquet and runs the background work;
- `broker`, the NATS JetStream ingest buffer: the api replicas acknowledge a batch once the
  broker holds it, so killing an api replica or the worker loses no acknowledged data, and rows
  are searchable while they wait;
- two query executors (`obzly-querier-a`, `-b`), which the api replicas send query fragments to;
- Postgres, Valkey and MinIO. Object storage is required: it is how one replica reads segments
  another wrote.

Same setup steps as above, copying the HA file instead; also set `NATS_TOKEN` and
`QUERIER_TOKEN` in `.env` (`openssl rand -hex 32` each). Its header comment explains each piece.
One broker keeps one copy of each batch; for a broker that survives a node loss, use the
Kubernetes [`ha/`](../kubernetes/eks/ha/) manifests (three brokers). The executors' result port
(`4319`) is never published to the host.

`QUERY_MAX_SCAN_GB` (default 100) limits how much stored data one user query may read. To watch
all of this, point a Prometheus at each service's `:9464/metrics` and import the dashboard from
[`../observability/`](../observability/).

## Upgrading

```bash
git pull
docker compose up -d --build
```

Postgres/Valkey data isn't touched by a rebuild - only the `obzly` image
gets replaced. Migrations run automatically on startup.

## Backups

Back up `./pgdata` with Postgres's own tools (`pg_dump`, or WAL archiving for point-in-time
recovery) and, with object storage, keep bucket versioning on. `./obzly-data` only holds
segments not yet uploaded and caches when object storage is on; without it, it holds every
segment and needs backing up too. Restores, key material and `./main reconcile` are covered in
[`../../docs/runbooks/backup-restore.md`](../../docs/runbooks/backup-restore.md).

## Multi-site (control plane + Sites)

Optional (ARCHITECTURE.md "Multi-site: control plane and Sites"). Two ways
to try it:

- **Enroll THIS install as a Site**: uncomment the commented `SITE_ID`/
  `CONTROL_PLANE_ISSUER`/... block in `docker-compose.yml.example`'s (or
  `docker-compose.ha.yml.example`'s) `obzly`/`obzly-api-*` environment -
  everything else about this deployment (its data, projects, plans, API
  keys, root admin) stays exactly as it is.
- **Try the whole thing locally**: `docker-compose.multisite.yml.example`
  spins up a control plane plus a Site (each with its own Postgres/
  Valkey; copy the Site block for more) in one compose file - see its own header comment for the full
  walkthrough. `docker-compose.control-plane.yml.example` is the leaner,
  production shape for a standalone control plane once you're enrolling
  real, separately-deployed Sites instead.
