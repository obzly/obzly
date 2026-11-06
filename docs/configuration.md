# Configuration

Every setting is an environment variable read at startup (`backend/internal/config/config.go`,
the source of truth). In Docker Compose they go in `environment:` or the `.env` file; on
Kubernetes in the ConfigMap and Secret (`deploy/kubernetes/eks/common/`) or the Helm chart's
`config` / `secrets` values. **Required** settings stop the process at startup when missing.

## Core

| Variable | Default | |
| --- | --- | --- |
| `DATABASE_DSN` | **required** | Postgres, `host=... user=... password=... dbname=... port=5432 sslmode=require` |
| `ENV_MODE` | **required** | `production` or `development` (development serves Swagger, allows CORS and binds to 127.0.0.1) |
| `SERVICE_MODE` | `all` | `all`, `api`, `worker`, `broker`, `querier` or `control-plane` - see [Roles](#roles) |
| `HTTP_PORT` | `4005` | UI, REST API and OTLP/HTTP (`/v1/logs`, `/v1/traces`, `/v1/metrics`) |
| `OTLP_GRPC_PORT` | `4317` | OTLP/gRPC receiver |
| `OTLP_GRPC_TLS_CERT_FILE`, `OTLP_GRPC_TLS_KEY_FILE` | empty (plaintext) | PEM certificate and key the OTLP/gRPC receiver serves TLS 1.2+ with; set both or neither. Re-read when the files change (cert-manager rotation needs no restart). Unset, the receiver is plaintext and logs a warning at startup - set them whenever port 4317 is reachable without a TLS-terminating proxy, since API keys travel in its metadata |
| `METRICS_ADDR` | `:9464` | Prometheus `/metrics` listener (`off` disables it); keep it internal |
| `LOGS_DATA_DIR` | `/obzly-data/segments` | Local segments, caches and (in `all` mode) the embedded broker's store |
| `ADMIN_EMAIL` | `admin` (`superadmin` on a control plane) | The bootstrap super-admin's login; changing it renames the account |
| `PUBLIC_URL` | from the request | The browser-facing origin (`https://obzly.example.com`): OAuth metadata and tokens for MCP clients. **Required on a control plane**, where it is also the `iss` of every assertion it signs |
| `TRUSTED_PROXIES` | none | CIDRs of load balancers whose `X-Forwarded-For` (and, without `PUBLIC_URL`, `X-Forwarded-Host`/`-Proto`) is trusted; set it behind any proxy |
| `SHUTDOWN_DRAIN_SECONDS` | `0` | Seconds an HTTP replica keeps serving after SIGTERM with readiness failing; 15 behind a load balancer |
| `API_MAX_BODY_MB` | `5` | Largest request body on `/api/v1` routes; log receiving, OTLP and remote write keep their own limits |
| `SELF_TRACING_SAMPLE_RATE` | `0.01` | Fraction of the replica's own requests and background passes traced into the system project; `0` off |
| `WEBSITE_URL` | empty | Product website linked from the UI |
| `APP_VERSION` | `dev` | Set by the image build; shown in Upgrade status and Site info |
| `BACKEND_ROOT_PATH` | working directory | Where `migrations/` is found |

## Valkey

Caches, rate limits and leases only - no data that needs a backup.

| Variable | Default | |
| --- | --- | --- |
| `VALKEY_HOST`, `VALKEY_PORT` | **required** | |
| `VALKEY_USERNAME`, `VALKEY_PASSWORD` | empty | |
| `VALKEY_IS_SSL` | `false` | |

## Object storage

| Variable | Default | |
| --- | --- | --- |
| `OBJECT_STORAGE_URL` | empty (local disk only) | `s3://bucket?region=...`, `gs://bucket`, `azblob://container`; MinIO: `s3://bucket?endpoint=http://minio:9000&region=us-east-1&use_path_style=true&disable_https=true&hostname_immutable=true`. **Required for the `ha` shape**: replicas read segments others wrote. Credentials come from the cloud SDK chain (IRSA, workload identity, `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY`) |
| `COLD_CACHE_MAX_SIZE_MB` | `20000` | Local cache of segments downloaded back from object storage, least recently used first out |

## Ingest buffer (JetStream) and brokers

| Variable | Default | |
| --- | --- | --- |
| `INGEST_BUFFER` | `valkey` (no buffer) | `jetstream`: a batch is acknowledged once the brokers hold it. In `all` mode the broker runs in-process |
| `NATS_URL` | empty | The broker cluster's client URL(s), comma-separated, on `api` / `worker` / `querier` |
| `NATS_TOKEN` | empty | Shared by brokers and their clients; required wherever NATS listens on the network |
| `NATS_SERVER_NAME` | hostname | Unique per broker (the pod name in a StatefulSet) |
| `NATS_CLUSTER_NAME`, `NATS_ROUTES` | empty | The broker cluster: `nats-route://host:6222` of every broker, itself included |
| `NATS_PORT`, `NATS_CLUSTER_PORT`, `NATS_MONITOR_PORT` | `4222`, `6222`, `8222` | Broker listeners: clients, peers, monitoring and health probes |
| `JETSTREAM_DIR` | `$LOGS_DATA_DIR/jetstream` | The broker's file store (a PVC) |
| `JETSTREAM_REPLICAS` | `3` with routes, else `1` | Ingest stream replicas |
| `INGEST_STREAM_MAX_BYTES` | 10 GiB | Each signal's buffer; when full, ingest answers `429` / `RESOURCE_EXHAUSTED`. The three must fit the broker volume |
| `INGEST_L0_TARGET_BYTES` | 64 MiB | A worker writes a project's first-level segment once this much is buffered... |
| `INGEST_L0_MAX_AGE` | `1h` (at least `5m`) | ...or once the oldest batch is this old. Buffered rows are searchable meanwhile |

## Queries and executors

| Variable | Default | |
| --- | --- | --- |
| `QUERY_MAX_SCAN_GB` | `100` | Most stored data one user query may read (GiB); over it the query is refused before reading. `0` off |
| `QUERIER_TOKEN` | empty | Shared by `api` and `querier` replicas: signs query fragments (executors refuse unsigned ones) and authenticates the executors' result listener |
| `QUERIER_GRPC_ADDR` | `:4319` | An executor's result listener - internal only |
| `QUERIER_ADVERTISE_ADDR` | hostname + port | How `api` replicas reach it (the pod IP on Kubernetes) |
| `QUERY_RESULTS_TLS_CERT_FILE`, `QUERY_RESULTS_TLS_KEY_FILE` | empty (plaintext) | On `querier` replicas: PEM certificate and key the result listener serves TLS 1.2+ with; set both or neither. Re-read when the files change |
| `QUERY_RESULTS_TLS_CA_FILE` | empty (system roots) | On `api` replicas: the CA that signed the executors' certificate. Setting it, `QUERY_RESULTS_TLS_SERVER_NAME` or the cert/key turns TLS on for fetching results; an `api` replica with TLS on can't fetch from a plaintext executor (it then runs the query itself), so switch both tiers together. Read at startup |
| `QUERY_RESULTS_TLS_SERVER_NAME` | the dialled host | On `api` replicas: the name the executors' certificate is verified against. Executors advertise `QUERIER_ADVERTISE_ADDR`, the pod IP on Kubernetes, so without this the certificate needs that IP as a SAN; set it to a DNS name the certificate covers (e.g. `obzly-querier.<namespace>.svc`) |
| `QUERIER_CONCURRENCY` | CPU count | Fragments an executor runs at once |
| `QUERIER_SPILL_MAX_SIZE` | `8GB` | Most disk one DuckDB query handle's spill files may use: the executor's, and on `api`/`worker` replicas each stream's querier and each SQL-mode query (they share the data volume with segments and JetStream); a query that needs more fails instead of filling the disk. Leftover spill files are removed at start |
| `QUERIER_WARM_HOURS` | `24` | On start, an executor downloads its share of this many hours of segments into its cache (up to half of `COLD_CACHE_MAX_SIZE_MB`), and from then on each segment it owns as it is uploaded; `0` turns both off |
| `QUERY_CACHE_TTL_HOURS` | `24` | Histograms and dashboard series keep each finished hour's counts in Valkey this long, so asking again only scans what changed; `0` off |
| `LOG_CONTEXT_DEFAULT_FIELDS` | | Comma-separated fields added to "Show logs around"'s default scope (the log's cluster, namespace, deployment/statefulset/daemonset/job, container and service), e.g. `team` |
| `QUERY_PARTIAL_DOWNLOADS` | `true` | Read only the columns a query needs from a large cold segment (ranged reads into a sparse cache file) instead of downloading it whole |
| `QUERY_CACHE_DELAY_SECONDS` | `300` | Partitions ending within this many seconds of now are never cached - rows still arrive for them |

## Alerting

| Variable | Default | |
| --- | --- | --- |
| `ALERT_EVAL_SHARDS` | `1` | Rule evaluation split this many ways, each under its own leader lock |
| `INVENTORY_RETENTION_DAYS` | `30` | Infrastructure inventory: an asset not seen for this many days is dropped (terminated pods and containers after 7 days; a Job or CronJob run's pod once it succeeds, or a day after it failed) |
| `INVENTORY_MAX_ASSETS_PER_PROJECT` | `50000` | Infrastructure inventory: past this many assets in a project the least recently seen are dropped, with a warning in the worker log |
| `ALERT_EGRESS_ALLOWLIST` | empty | CIDRs webhooks, and Synthetics Tests run from the control plane, may reach although not public (in-network receivers) |
| `ALERT_EGRESS_DENYLIST` | empty | CIDRs webhooks and control-plane Synthetics Tests may never reach (e.g. the cluster's service range) |

### Email and notification channels

Both are set up in **Obzly settings**, not the environment:

- **Email (SMTP):** host, port, STARTTLS or implicit TLS (never plaintext), username, password
  (stored encrypted, never shown again) and From address. Used by email channels and to send
  invites. **Send test email** checks it.
- **Notification channels** (Slack, webhook, PagerDuty, email) are defined once and made available
  to every project or chosen ones; alert rules pick from them.
- **Multi-site:** on a control plane both are central. Each Site fetches them every 5 minutes as a
  document signed with the assertion keys and sealed to the Site's own key, keeps the last copy if
  the control plane is unreachable, and shows them read-only. The control plane reads the Site's
  key from its `/api/v1/site/info` once and pins it; the Site signs each request with that key
  (an HMAC under the X25519 key it and the control plane share), so only it can fetch its
  document, and reports the version it applied, which drives the sync status. If a Site later
  serves a different key (restored, rebuilt, or something else answering at its address), nothing
  is sealed to it until an admin compares the fingerprint (shown on the Site's own Notification
  channels page) and clicks **Key changed - accept?**. **Move Site's own here** (Settings → Sites)
  takes over a Site's existing channels under the same ids, so its rules keep working, plus its
  relay if the control plane has none; an id that belongs to a central channel reaching other
  Sites is refused.
- **Upgrading from `ALERT_SMTP_*`:** on first start of v0.55.0 the variables are imported once (a
  control plane's as its central relay, a Site's as its own) and then ignored; remove them.

## Integrity audit

A sample of uploaded segments checked against object storage ([backup-restore runbook](runbooks/backup-restore.md#integrity-audit)).

| Variable | Default | |
| --- | --- | --- |
| `INTEGRITY_AUDIT_HOURS` | `168` | How often; negative turns it off. Runs only with object storage |
| `INTEGRITY_AUDIT_PERCENT` | `1` | Share of uploaded segments sampled (at least one) |
| `INTEGRITY_AUDIT_MAX_SAMPLES` | `500` | Cap per audit |

## Sign-in

SSO connectors (OIDC), SSO group mappings, MFA policy and SCIM tokens are configured in the UI,
not here.

| Variable | Default | |
| --- | --- | --- |
| `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET` | empty | GitHub sign-in |
| `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET` | empty | Google sign-in |

## Multi-site

Control plane side:

| Variable | Default | |
| --- | --- | --- |
| `CONTROL_PLANE_EGRESS_ALLOWLIST` | any public address | CIDRs it may call Sites at |
| `SITE_ALLOW_INSECURE_URLS` | `false` | Allow `http://` Site URLs (local Compose only) |

Site side:

| Variable | Default | |
| --- | --- | --- |
| `CONTROL_PLANE_ISSUER` | empty (standalone) | The control plane's `PUBLIC_URL`; enables token exchange |
| `SITE_ID` | **required** with an issuer | The Site's slug in the control plane |
| `SITE_NAME` | `SITE_ID` | Display name |
| `CONTROL_PLANE_JWKS_URL` | `<issuer>/.well-known/jwks.json` | Where to fetch signing keys, e.g. over an internal address. The Site fetches its organization plan next to it (`…/api/v1/site-plans/<SITE_ID>`) every 5 minutes |
| `CONTROL_PLANE_LINK_BY_EMAIL` | `true` | Attach a central user to an existing local account with the same email - only a password-less one (e.g. an invite); an account with a password, SSO identity or GitHub/Google login, or the root admin, is refused |
| `CONTROL_PLANE_EGRESS_CIDRS` | empty | Accept token exchange only from these CIDRs |

## Roles

`SERVICE_MODE` picks what a process does; the same image runs every role.

| Mode | Runs | Notes |
| --- | --- | --- |
| `all` | everything in one process | The single-container install |
| `api` | UI, REST, OTLP ingest, query coordination | Stateless; any number behind a load balancer |
| `worker` | Parquet writes from the buffer, compaction, retention, quotas, uploads, alert evaluation and delivery, integrity audit | No HTTP |
| `broker` | NATS JetStream only | 3 (one per zone) or 1 |
| `querier` | Query executors with a segment cache | Stateless |
| `control-plane` | Central sign-in, organizations, SCIM, Site registry and proxy | No signal data |

Per-project settings (retention, sampling, promoted fields, quotas, rate limits) are set in the
UI or API, not here.

## Synthetics probe (`obzly-probe`)

The probe is a separate binary and image ([SYNTHETICS.md](SYNTHETICS.md)); it reads only these:

| Variable | Default | |
| --- | --- | --- |
| `OBZLY_CONTROL_PLANE_URL` | required | The control plane as the probe reaches it |
| `OBZLY_PROBE_TOKEN` | required | The probe group's token, shown once when the group was added or its token replaced (Synthetics → Probe groups). Every instance of a group uses the same token; a replaced token keeps working for 24 h unless it was revoked on replacing |
| `OBZLY_PROBE_INSTANCE` | the host name | This instance's name within its probe group (`[A-Za-z0-9._-]`, up to 128). Each monitor and cloud connection runs on one live instance; the Helm chart sets it to the pod name |
| `OBZLY_PROBE_DATA_DIR` | `/var/lib/obzly-probe` | Where the last config is cached (secrets included, mode 0600) so checks keep running while the control plane is unreachable, and each cloud connection's latest inventory (`cloud/<id>.json`) so a restart doesn't rediscover early |
| `OBZLY_PROBE_MAX_CONCURRENCY` | `256` | Checks run at once (each waits on the network, so a mass outage of slow timeouts still fits) |
| `OBZLY_PROBE_TASK_WAIT` | `25s` | Long-poll for editor Test runs (1 to 55 s); keep it under the read timeout of any proxy in front of the control plane (CloudFront's default is 30 s) |
| `OBZLY_PROBE_DENY_CIDRS` | empty | CIDRs checks may never reach; loopback, link-local and the cloud metadata addresses (`169.254.169.254`, `fd00:ec2::254`, `168.63.129.16`, `100.100.100.200`, `192.0.0.192`) are always refused, also inside NAT64, 6to4 or IPv4-compatible IPv6. Cloud connection scans don't go through this policy: they call the AWS and Azure APIs, and Azure's instance metadata for a managed identity |
| `OBZLY_PROBE_ENV_ALLOW` | empty | Comma-separated environment variable names (a trailing `*` is a prefix, e.g. `TEAM_A_*`) that monitors' `${env:NAME}` placeholders may read, besides names starting with `OBZLY_SECRET_`. `OBZLY_PROBE_*`, `AWS_*`, `AZURE_*` and `GOOGLE_*` are never read, even when listed; a placeholder naming anything else fails the run |
| `OBZLY_PROBE_HEALTH_ADDR` | off | e.g. `:8080` serves `/healthz`: 200 once the probe has a config and the control plane answered recently |

Values for a monitor's `${env:NAME}` placeholders that aren't stored on the monitor are read from
the probe's own environment.
