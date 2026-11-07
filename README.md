<div align="center">
  <img src="assets/logo.svg" style="margin-bottom: 20px;" alt="Obzly Logo" width="250"/>

  <h3>Self-hosted observability, from one container to an enterprise cluster</h3>
  <p>Logs, traces, metrics, and dashboards. Self-hosted, open source, scales out when you need it</p>

  <!-- Badges -->
  [![Apache 2.0 License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
  [![Platform](https://img.shields.io/badge/platform-linux%20%7C%20macos%20%7C%20windows-lightgrey)](#)
  [![Self Hosted](https://img.shields.io/badge/self--hosted-yes-brightgreen)](#)
  [![Open Source](https://img.shields.io/badge/open%20source-❤️-red)](#)

  <p><b><a href="https://demo.obzly.com">Try the live demo</a></b>: the real UI with sample data, running in your browser</p>

  <p>
    <a href="#-features">Features</a> •
    <a href="#-enterprise-grade-logging-and-monitoring">Enterprise</a> •
    <a href="#-system-requirements">System Requirements</a> •
    <a href="#-installation">Installation</a> •
    <a href="#-usage">Usage</a> •
    <a href="#-license">License</a> •
    <a href="#-contributing">Contributing</a>
  </p>
</div>

Obzly speaks OTLP (gRPC and HTTP), so anything that can export OpenTelemetry - the standard
SDK for your language, or an OTel Collector in front of a database/web server/queue you don't
control - can send it logs, traces, and metrics with no proprietary client library required. A
generated, ready-to-run Collector config for 35+ common sources (Nginx, Postgres, Kafka,
Kubernetes, Redis, and more) is built into the **Data Sources** page - pick one, paste the config,
done.

For a quick one-off from your own app, plain HTTP works too:

```bash
curl -X POST "http://YOUR_OBZLY_HOST/api/v1/logs/receiving/YOUR_PROJECT_ID" \
  -H "Content-Type: application/json" \
  -H "X-API-Key: YOUR_API_KEY" \
  -d '{"logs": [{"level": "INFO", "message": "User logged in", "fields": {"user_id": "12345"}}]}'
```

Language-specific SDKs are on the roadmap - track progress in [ROADMAP.md](ROADMAP.md).

---

## ✨ Features

### 🐳 **Easy Deployment**

Obzly is an Apache 2.0 licensed, self-hosted observability platform. Unlike the ELK stack or a full
Grafana/Loki/Tempo/Mimir setup, it starts as a single app container (plus Postgres and Valkey) with
no cluster to operate - and the same image grows into a scale-out, highly available deployment when
your volume does (see [Enterprise-grade logging and monitoring](#-enterprise-grade-logging-and-monitoring)).

- **Docker-based**: Launch in Docker with one command - the entire process takes less than 2 minutes
- **Zero configuration**: Works out of the box without any setup
- **Kubernetes-ready**: a Helm chart and plain manifests for the same image, published for
  `linux/amd64` and `linux/arm64`
- **Self-hosted**: All your data stays on your infrastructure - no data ever leaves your servers
- **Open source**: Apache 2.0 licensed - you or your security team can audit every line of code

### 📊 **Logs, Traces, Metrics, and Dashboards**

One place for all three signals, correlated by the same project/API key:

- **Logs**: full-text and field search with a query builder or SQL, a field sidebar (which also
  offers services, workloads and pods seen in the last 36 hours but not in the range), live tail,
  log patterns, logs around any result from the same source (what led up to an error), log
  anomalies (new, spiking and dropping patterns against an earlier period), alerts on new or spiking
  error patterns, Kubernetes rollout markers on the histogram, a link from a log to its trace, an adjustable
  timezone, and cursor paging that new arrivals don't shift. Promote up
  to 12 fields per project to typed columns for fast filters and number ranges
- **Traces**: OTLP trace ingestion, a trace explorer and a service map, plus a Jaeger-compatible
  query API so existing Jaeger clients work against Obzly unmodified
- **Metrics**: OTLP + Prometheus remote-write ingestion, queryable via a PromQL-compatible API -
  point an existing Grafana Prometheus datasource at it. 5-minute and 1-hour rollups keep long
  ranges fast and can outlive raw samples
- **Dashboards**: build your own, or import an existing Grafana or OpenObserve dashboard JSON
- **Incidents**: error logs, failed spans and their shared traces grouped into incident sessions
- **AI Observability**: Claude Code usage per project - cost, tokens, cache use and sessions;
  LLM apps instrumented with OpenTelemetry `gen_ai.*` (calls, tokens, latency, errors, estimated
  cost); and budgets per project, user or model that alert before spend runs away

### 🚨 **Alerting**

Rules over metrics (PromQL), logs (counts of a search, optionally per field) or traces (count,
errors, latency percentiles of a service or operation), with `for` durations, repeat intervals,
labels and a live preview. Notifications go to webhooks, Slack, PagerDuty or email, through an
outbox with retries and silences. Outgoing requests only reach public addresses unless you allow
more. Rule to notification takes about 15 seconds. Channels and the email relay are defined once
in Obzly settings and shared across projects - on a multi-site install, centrally on the control
plane, with each Site keeping a copy so alerts still go out if the control plane is down.

### 🌐 **Synthetic Monitoring**

Check APIs and endpoints from your own locations with `obzly-probe` (a small binary, container or
Helm chart): HTTP with assertions on status, latency, headers and JSON, multi-step API flows
(variables carried between up to 10 requests, secrets masked), TLS certificates (expiry, chain,
protocol; self-signed allowed per monitor), TCP, DNS and ICMP. Maintenance windows (one-off or
recurring) keep monitors running but mute their alerts and leave the time out of uptime. Monitors live on the control
plane - edit, clone, enable/disable, or import and export them as YAML - and probes send results
to a Site project as OTLP, so dashboards and alert rules work on them as on any metric. An
overview, per-location availability, response-time breakdowns and run-by-run detail come built in,
and **Test** runs an unsaved monitor from every location in seconds. Group monitors in folders,
make one depend on another (a child of a down parent shows "Impacted" and its alerts stay quiet,
so an outage pages once), send each monitor's alerts to the shared notification channels, and run
probes as groups: several instances per location share the monitors and take over from each
other within a minute.

### 🗄️ **Infrastructure Inventory**

Every cluster, node, host, namespace, workload, pod, container and service your agents report, in
one list with health and the reason for it (CPU, memory, disk, restarts, replicas, error logs,
firing alerts), lifecycle (new, not reporting, terminated) and a change feed - built from the
data you already send, with nothing to install. A tree view from cluster to container, an overview
of what needs attention, and per-asset charts with links into logs, traces and dashboards. On a
multi-site install the control plane shows every Site at once. **Cloud connections** add what runs
no agent - load balancers, databases, buckets, functions, scaling groups - from AWS accounts and
Azure subscriptions, scanned by a probe group with read-only access (a guided wizard writes the
CloudFormation, Bicep or CLI); Obzly stores only identifiers, never cloud keys.

### 🏢 **Enterprise-Grade Logging and Monitoring**

The single container is the smallest shape of one architecture. The same image runs each role
on its own (`SERVICE_MODE`), so each part scales and fails independently:

| Role | Does | Scales |
|---|---|---|
| `api` | UI, query API, OTLP/HTTP and gRPC ingest, query coordinator | Stateless, any number behind a load balancer |
| `worker` | Writes ingest to Parquet, compaction, retention, quotas, uploads | Ingest and compaction shards are shared across workers |
| `broker` | NATS JetStream ingest buffer | Three nodes, one per zone |
| `querier` | Runs query fragments, with its own segment cache | Stateless, add replicas for query throughput |
| `control-plane` | One login and UI in front of many Sites; organizations, SCIM | One per company, in front of every Site |

- **Durable ingest**: a batch is acknowledged only after the JetStream buffer has stored it
  (three replicas on a broker cluster), so losing an `api` pod, a worker or one broker loses no
  acknowledged data. When the buffer is full, clients get `429` / `RESOURCE_EXHAUSTED` and
  OTel Collectors retry. Rows are searchable before they are written to Parquet.
- **Columnar storage on object storage**: ZSTD-compressed Parquet on S3, GCS, Azure Blob or MinIO,
  compacted in levels into hour-sorted files of one day each, with a partitioned Postgres
  catalog whose per-file statistics let queries skip files that can't match.
- **Distributed query**: each `api` replica splits a query into fragments for the `querier`
  replicas, or runs it itself when there are none. Every segment keeps going to the same executor
  (rendezvous hashing), so caches stay warm. A failed fragment is retried on another executor. Searches page by cursor, so new logs
  arriving don't shift pages. A per-query scan budget (`QUERY_MAX_SCAN_GB`) refuses runaway
  queries before they read anything.
- **Fast wide searches** (after OpenObserve): a search reads its range newest-first in growing
  time windows and stops once the page is full, streaming rows as each window is read, so a 7-day
  search touches minutes of data rather than the week. The result count comes from the
  histogram, whose finished hours are cached in Valkey. Queriers keep their cache on a volume,
  pre-fetch each segment they own as it is uploaded, and warm up on start. Searching for an ID
  (a request id, order number, UUID) skips every segment whose token bloom filter lacks it.
- **High availability**: rolling updates with readiness draining, leases so each ingest and
  compaction shard has exactly one owner, and no single query node.
- **Multi-site**: one control plane in front of many Sites (regions, environments or business
  units), with central identities and per-Site access grants; data stays in each Site.
- **Access and governance**: organizations and teams spanning Sites, SSO through any OIDC
  provider (Microsoft Entra ID group mapping included), SCIM 2.0 provisioning from Okta or Entra ID,
  MFA, per-project roles, one authorization path with denials in the audit log, and per-project
  rate limits and quotas.
- **Data lifecycle**: retention per project and signal, trace tail sampling that keeps whole
  traces, log head sampling, metric rollups that outlive raw samples, and object tags for bucket
  lifecycle rules.
- **Disaster recovery**: backup/restore, DR and upgrade runbooks with a measured restore drill
  (RPO under a minute), `obzly reconcile` to square the catalog with object storage after a
  restore, a weekly integrity audit, and an Upgrade status page that says when it's safe to
  deploy.
- **Monitoring Obzly itself**: every role serves Prometheus metrics on an internal port, with a
  ready Grafana dashboard and SLO alert rules in
  [`deploy/observability`](deploy/observability/README.md), and samples its own requests and
  background work as traces in a system project.

Deploy the roles with Docker Compose ([`deploy/docker`](deploy/docker), including an HA file
with every role), the Helm chart in [`deploy/helm/obzly`](deploy/helm/obzly) (`ha`, `single` and
`control-plane` modes), or the plain Kubernetes manifests in
[`deploy/kubernetes/eks`](deploy/kubernetes/eks) (single replica, or `ha/` with three brokers,
`api`, `worker` and `querier`). Every setting is listed in
[docs/configuration.md](docs/configuration.md); operations are in [docs/runbooks](docs/runbooks). The architecture is described in [ARCHITECTURE.md](ARCHITECTURE.md) ("Scale
architecture"). The scale work and its load and failure testing are tracked as Phase 14 in
[ROADMAP.md](ROADMAP.md).

### 🔌 **35+ Data Sources, Generated Configs**

The **Data Sources** catalog generates a ready-to-run OTel Collector config (with a dedicated,
freshly-created API key baked in) for common infrastructure - web servers, databases, message
queues, container/orchestration platforms, and more - so onboarding a new source is usually
copy, paste, run.

#### Languages

The **Languages** category sets up the OpenTelemetry SDK or agent of each OpenTelemetry language -
Java, .NET, Python, Node.js, browser JavaScript, Go, PHP, Ruby, Rust, C++, Erlang/Elixir, Swift
and Kotlin - plus the OpenTelemetry Operator for Kubernetes, which injects zero-code
instrumentation into annotated pods. Server-side apps export OTLP/HTTP straight to Obzly using the
standard `OTEL_*` env vars, as plain exports, a Dockerfile plus env file, or a Kubernetes Secret.
Browser and mobile apps never get an API key: their template generates a small collector gateway
that holds the key and accepts OTLP (with CORS for browsers) from the app.

### 🔐 **SSO, MFA and Provisioning**

- **Federated SSO**: Generic OIDC, configurable per-connector from the portal - works with AWS
  Cognito, Google Workspace, Microsoft Entra ID, or any standard OIDC provider - with IdP groups
  mapped to project and Site roles
- **TOTP MFA**: Required for Admin-role accounts, optional for Members, compatible with Google
  Authenticator/Authy/1Password/etc.
- **SCIM 2.0**: users and groups provisioned from Okta or Microsoft Entra ID into an organization
  and its teams; deprovisioning deactivates accounts the organization's SCIM created
- **Organization plans**: quotas per organization - ingest rate, log size, storage, retention and
  projects on each of its Sites, plus Sites and members - set by global admins and enforced even
  where a project turns its own rate limiting off

### 🎯 **Project Management**

Install Obzly once and use it for all your applications. Each project gets its own isolated space
with separate project IDs and API keys - organize by microservice, environment, or however fits
your team - and its own retention, sampling and promoted fields.

### 👥 **Teams and Organizations**

Add members to projects with role-based permissions, or group people into teams whose roles apply
to every member - organizations hold teams and, with a control plane, span Sites. A full audit log
records who changed what and when.

### 🔑 **API Keys with Usage Tracking**

Each key shows when it was last used, and a dedicated view surfaces keys that have gone unused for
7+ days for easy bulk cleanup - deleting a key (single or bulk) requires an MFA confirmation for
any user who has it enabled.

---

## 💻 System Requirements

Obzly requires the following minimum system resources to run properly:

- **CPU**: At least 2 CPU cores
- **RAM**: Minimum 4 GB RAM
- **Storage**: 20 GB available disk space (more recommended for log retention)
- **Docker**: Docker Engine 20.10+ and Docker Compose v2.0+
- **Network**: Internet connection for initial setup and updates

These are the requirements of the default single-container install, which suits developers and
small teams. For larger volumes, run the roles separately (see
[Enterprise-grade logging and monitoring](#-enterprise-grade-logging-and-monitoring)) and size each
one for its load.

**Note on Storage**: By default, all data is stored locally on your server in the mounted volume
(`./obzly-data` directory). Storage usage depends on your ingestion volume, but the system includes
configurable retention policies and limits to help manage disk space while keeping your most
important data accessible.

Optionally, older segments can be offloaded to S3-compatible object storage (AWS S3, MinIO, GCS,
Azure Blob) to keep local disk usage down - segments are compacted before upload, and are
transparently downloaded back on demand if a query needs one that's no longer local. See the
commented-out `minio`/`minio-init` services and `OBJECT_STORAGE_URL` in
[`deploy/docker/docker-compose.yml.example`](deploy/docker/docker-compose.yml.example) to enable
it; it's off by default.

---

## 📦 Installation

Obzly installs with a one-line script (recommended for a single host), Docker Compose, or on
Kubernetes. The fastest method is the automated installation script, which installs Docker (if
needed), pulls the image, sets up Postgres/Valkey alongside it, and configures automatic startup on
system reboot.

### Option 1: Automated Installation Script (Recommended, Linux only)

The installation script will automatically:

- ✅ Install Docker with Docker Compose (if not already installed)
- ✅ Pull the Obzly image and set up the container
- ✅ Configure automatic startup on system reboot
- ✅ Get you up and running in under 2 minutes

```bash
sudo apt-get install -y curl && \
curl -fsSL https://github.com/obzly/obzly/releases/latest/download/install-obzly.sh \
| sudo bash
```

The installer attached to each release installs that release's image (`OBZLY_IMAGE_TAG` overrides
it). If ufw is active it asks before opening the HTTP and OTLP ports; pass
`| sudo bash -s -- --open-firewall` (or `--no-open-firewall`) to answer up front.

### Option 2: Docker Compose Setup

Obzly needs Postgres and Valkey alongside it (see [ARCHITECTURE.md](ARCHITECTURE.md) for why they
run as separate containers rather than bundled into the app image). From the repo root, copy
[`deploy/docker/docker-compose.yml.example`](deploy/docker/docker-compose.yml.example) to
`docker-compose.yml` and [`deploy/docker/.env.example`](deploy/docker/.env.example) to `.env`, set
a real `POSTGRES_PASSWORD` in `.env`, then run:

```bash
docker compose up -d --build
```

See [`deploy/docker/README.md`](deploy/docker/README.md) for more detail, including the HA file
that runs every role (`docker-compose.ha.yml.example`).

### Option 3: Kubernetes

The Helm chart deploys a Site (`mode: ha` - brokers, `api`, `worker`, `querier` - or `single`) or a
control plane; Postgres and object storage are yours, Valkey can run in the release:

```bash
helm install obzly deploy/helm/obzly -n obzly --create-namespace \
  --set config.objectStorageUrl='s3://my-bucket?region=eu-west-1' \
  --set secrets.databaseDsn='host=... user=obzly password=... dbname=obzly port=5432 sslmode=require'
```

See [`deploy/helm/obzly/README.md`](deploy/helm/obzly/README.md). Plain manifests for
`kubectl apply -k` are in [`deploy/kubernetes/eks`](deploy/kubernetes/eks/README.md) (single
replica, `ha/`, `control-plane/`).

### Option 4: Multi-site (control plane + Sites)

Optional - one centrally-deployed UI/identity layer (a **control plane**) fronting one or more
independently-deployed backends (**Sites**), each still just a normal installation from the
options above with a couple of extra env vars set. Useful once you're running more than one
region/cluster's worth of Obzly and want one place to sign in and manage access, without moving
any Site's actual data or exposing its query/admin API to the internet directly - see
ARCHITECTURE.md's ["Multi-site: control plane and Sites"](ARCHITECTURE.md#multi-site-control-plane-and-sites)
section for the full design.

Try it locally in one command:

```bash
cp deploy/docker/docker-compose.multisite.yml.example docker-compose.yml
cp deploy/docker/.env.example .env   # set POSTGRES_PASSWORD
docker compose up -d --build
```

> **The control plane's bootstrap admin is `superadmin`** (not `admin`, unless you set `ADMIN_EMAIL`).
> A central account is matched to a Site's existing local account **by email** on first use, and a
> Site's root admin (`admin`) is deliberately never linkable - so the two break-glass accounts get
> different default logins and can't shadow each other. Onboard everyone else from the control
> plane's **Users → Invite user** (or SSO); `superadmin` itself can't be used *on* a Site.

or install-obzly.sh's `OBZLY_ROLE=control-plane`/`OBZLY_ROLE=site` env vars, or the Helm chart's
`mode: control-plane` / `deploy/kubernetes/eks/control-plane/` for a control plane on Kubernetes (each Site
enrolls via its own `deploy/kubernetes/eks/README.md`/`.../ha/README.md` "Enroll as a Site"
section). See [`deploy/README.md`](deploy/README.md#multi-site-control-plane--sites) for every
option in one place.

---

## 🚀 Usage

Getting started with Obzly is simple and takes just a few minutes:

1. **Launch and access Obzly**: Start Obzly and navigate to `http://localhost:4005`
2. **Create your first project**: Click "New Project" and copy the generated project ID. Each
   project gets its own isolated space with separate API keys.
3. **Send data**: Use the **Data Sources** page for a generated OTel Collector config, or point any
   OTLP exporter/SDK directly at Obzly's OTLP endpoint.
4. **Start viewing**: Watch logs, traces, and metrics stream in real time. Search by text, filter
   by fields, and query within specific time ranges.
5. **Manage your setup**: Add team members, control access to projects, set API key limits, and
   configure domain restrictions as needed.

### 🔑 Resetting Admin Password

If you need to reset the admin password, you can use the built-in password reset command:

```bash
docker exec -it obzly ./main --email="admin"
```

You'll be prompted for the new password (input hidden, like `passwd`). `--new-password=...` is
still accepted for scripted/automated use, but avoid it interactively - a password passed as a
command-line argument is visible to other processes on the host (`ps`) and lingers in shell
history.

Replace `admin` with the actual email address of the user whose password you want to reset - or
the value you set `ADMIN_EMAIL` to (see below) for the bootstrap admin account specifically. On a
multi-site control plane the bootstrap account is `superadmin` by default, and this resets a
*central* account; on a Site, it resets that Site's own local/break-glass account - each role only
ever manages its own local users.


### 🔐 Resetting MFA (lost authenticator)

If an admin loses their authenticator and no other admin can use **Reset MFA** in User
Management (typically the root admin), clear it from the command line:

```bash
docker exec -it obzly ./main --email="admin" --reset-mfa
```

The user signs in with their password and, being an admin, is asked to set MFA up again. Add
`--new-password` (or run the password reset above) if the password is lost too. The reset is
written to the audit log. On a control plane use its container and login, for example
`docker exec -it obzly-control-plane ./main --email="superadmin" --reset-mfa`.

SSO users don't need this when their connector has **Provider enforces MFA** turned on (off by
default) and the provider's ID token says a second factor was used (`amr` with `mfa`, `otp`,
`hwk`, `swk`, `sms` or `fido`): such sign-ins skip Obzly's own authenticator step.
### 🔐 Multi-Factor Authentication (MFA)

TOTP-based MFA (compatible with Google Authenticator, Authy, 1Password, etc.) is **required for
Admin-role accounts** and **optional for Members**. An Admin without MFA set up is prompted to set
it up immediately on their next sign-in (or blocked from every other action if they're already
signed in) - there's no way to skip it. Members can turn it on/off for themselves from their
profile. If someone loses their authenticator device, any admin can reset their MFA (Users page →
Reset MFA) so they can sign in and set it up again - including resetting another admin's, since an
admin can't turn off their own required MFA.

### 🏷️ Attribute names

Obzly stores every attribute, label and metric name in one PromQL-safe form: no dots and no
`resource.` prefix. For example, `resource.k8s.pod.name` becomes `k8s_pod_name` and `system.cpu.time`
becomes `system_cpu_time`, so PromQL needs no quoting: `sum by (k8s_pod_name) (system_cpu_time)`.
Log levels are stored lowercase (`error`, `warn`, ...). Old names still work in searches, PromQL
and imported dashboards, and data stored before this change is converted in the background.
Progress: `GET /api/v1/system/data-migration` (admins).

### 🤖 Connecting an AI assistant (MCP)

Obzly serves a read-only [MCP](https://modelcontextprotocol.io) endpoint at `/mcp`, so assistants
such as Claude Code and Cursor can search logs, run SQL over them, inspect traces and service
maps, and run PromQL, with exactly the access your account has.

1. In **Profile → AI assistant access (MCP)**, create a token. It is shown once, and needs your
   authenticator code if MFA is on.
2. Add the server to your assistant, for example:
   ```bash
   claude mcp add --transport http obzly https://obzly.example.com/mcp \
     --header "Authorization: Bearer obz_pat_..."
   ```

**claude.ai and Claude Desktop** sign in through the browser instead: add a custom connector with
the URL `https://obzly.example.com/mcp`, sign in to Obzly when asked, and allow access. Connected
apps are listed on the same Profile page and can be disconnected there. Set `PUBLIC_URL` to the
address users reach Obzly at, so OAuth metadata and tokens use the right origin.

Tokens work only on `/mcp`, never on the REST API. On a multi-site control plane one endpoint
covers every Site you can access: the assistant calls `list_sites` and passes `site` to each tool.

### ✉️ Changing the Admin Account's Email

By default the bootstrap super-admin account's email is the literal `admin`. Set `ADMIN_EMAIL` in
your `.env` to use a real email address instead - on the next restart, if the account already
exists under a different email, it's renamed in place (not duplicated).

### 📚 Operating Obzly

- **Configuration**: every environment variable, with its default, is in
  [docs/configuration.md](docs/configuration.md).
- **Upgrades**: Settings → Upgrade status says whether it's safe to deploy the next version
  (schema, background rewrites, uploads, the ingest buffer); the order and rollback rules are in
  [docs/runbooks/upgrade.md](docs/runbooks/upgrade.md).
- **Backups and disaster recovery**: [docs/runbooks/backup-restore.md](docs/runbooks/backup-restore.md)
  and [docs/runbooks/dr.md](docs/runbooks/dr.md). With object storage, square the catalog with
  the bucket after restoring Postgres:
  ```bash
  docker exec -it obzly ./main reconcile          # report only
  ```
- **Monitoring Obzly**: metrics, a Grafana dashboard and alert rules are in
  [`deploy/observability`](deploy/observability/README.md).

---

## 📝 License

This project is licensed under the Apache 2.0 License - see the [LICENSE](LICENSE) and
[NOTICE](NOTICE) files for details.

Obzly started as a fork of the open-source [Log Bull](https://github.com/logbull/logbull) project
and has since been substantially redesigned (see [ARCHITECTURE.md](ARCHITECTURE.md) for the full
history) - traces, metrics, dashboards, SSO, MFA, the Data Sources catalog, and the storage engine
rebuild were all built on top of that original base.

---

## 🤝 Contributing

Contributions are welcome! Obzly is open source (Apache 2.0 license) and built by developers for
developers - start with [CONTRIBUTING.md](CONTRIBUTING.md). See [ARCHITECTURE.md](ARCHITECTURE.md) and [ROADMAP.md](ROADMAP.md) for the design
reference and what's planned next.

Found a security problem? Please report it privately - see [SECURITY.md](SECURITY.md).

**If you find Obzly useful, please star it on GitHub ⭐ - it really helps the project grow!**
