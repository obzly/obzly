# Monitoring Obzly itself

Every Obzly process (`all`, `api`, `worker`, `control-plane`, `broker`, `querier`) serves Prometheus metrics on an
internal listener, separate from the UI/API port:

| Setting | Default | Notes |
|---|---|---|
| `METRICS_ADDR` | `:9464` (`127.0.0.1:9464` when `ENV_MODE=development`) | `host:port`, or `off` to disable. Must differ from `HTTP_PORT` and `OTLP_GRPC_PORT`. |

Path: `/metrics`. Keep the port private: don't publish it on a public load balancer. The control
plane never proxies it. The Kubernetes manifests in `deploy/kubernetes/eks` expose it as a
`metrics` container port with `prometheus.io/scrape|port|path` pod annotations and no Service.

## Self-tracing

Each Site also records `SELF_TRACING_SAMPLE_RATE` (default `0.01`, `0` to turn off) of its own HTTP
requests, OTLP gRPC calls and background passes as traces in a reserved project, **Obzly (system)**,
created automatically and visible only to global admins. Open it in the Traces view to see which
routes or background passes are slow. Spans carry the route template and status only, no query
strings or headers; they are kept 7 days. The control plane does not trace.

## Rules and alerts

[`prometheus/obzly-rules.yaml`](prometheus/obzly-rules.yaml) holds recording rules and alerts for
the SLOs in ARCHITECTURE.md "Scale architecture": ingest ack p99 < 500 ms, ingest→queryable p95
< 2 min, query p95 < 5 s, compaction and upload backlog. It also alerts on failed segment flushes
(those rows are lost), object storage errors, a filling or lagging ingest buffer, failing query
fragments or executors, a saturated Postgres pool, alert notifications given up on after every
retry, and integrity audits that failed or stopped running. Load it with Prometheus `rule_files:`, or wrap the `groups:` list in a `PrometheusRule` for
the Prometheus Operator. A backend test (`internal/telemetry/rules_test.go`) parses the file and
fails if a rule references a metric Obzly doesn't define.

## Grafana dashboard

[`grafana/obzly-overview.json`](grafana/obzly-overview.json) is a seed dashboard: ingest (accepted
rows, ack p99, status codes, JetStream ingest buffer batches and fill), the segment writer (rows in RAM,
ingest to queryable, rows lost to failed flushes), object storage and the catalog (request rate and
latency, upload lag, compaction backlog), queries (p95, segment reads by source, cold cache hit
ratio, bytes scanned), distributed query (fragments by where and result, fragment duration,
executors seen and retries, planned bytes per query, queries refused by the scan budget, executor
load), background tasks (time since last success, pass duration), runtime (DuckDB memory per
handle, Go heap, Postgres pool, CPU), and alerting, access and integrity (notifications by result,
rule-to-notification p95, authorization decisions, the last integrity audit). Import it in Grafana and pick your Prometheus data source; the
SLO panels use the recording rules above, so load those first. The same backend test checks every
panel query against Obzly's metric definitions.

`obzly_catalog_*`, `obzly_jetstream_*` and `obzly_integrity_audit_*` are cluster-wide values that every sampling replica
reports, so aggregate them with `max()`, not `sum()`.
