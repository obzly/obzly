# Obzly — Phase Roadmap

Companion to `ARCHITECTURE.md` (read that first for the *what/why*; this is the *in what order*).
Each phase should leave the system in a working, testable state — no big-bang integration at the end.

## Phase 0 — Fork & baseline
- Stand up CI on the fork immediately (keep the existing lint/test pipeline working).
- Define target audience explicitly (self-hosted small-to-mid teams, not PB/day scale) — this framing
  drives several Phase 2 engineering tradeoffs (e.g. pure-Go vs. DuckDB, single-node default vs. HA).
  **Superseded (2026-09-30)** by Phase 14's enterprise-scale target; the single-node default stays.
- `ARCHITECTURE.md` / `ROADMAP.md` (this pass) as the shared design reference.

## Phase 1 — Stabilize the current system
- Root-cause and fix the test flakiness evident in git history ("Improve tests stability" commits) —
  don't just add retries.
- Dependency audit: ~~`golang-jwt/jwt` v4→v5~~ done (2026-10-07; HS256 pinned, v4's `iat` check
  kept); CVE sweep: `govulncheck` runs in CI on every PR; re-verify the license table in
  `ARCHITECTURE.md`.
- ~~Add `/metrics` (Prometheus format)~~ done in Phase 14 item 7 (`METRICS_ADDR`); split
  ~~liveness/readiness~~ done in Phase 14-A (`/system/ready`, `/system/live`).
- ~~Add `SECURITY.md`~~ done (2026-10-07); focused pass on auth: JWT handling, API key scoping, OAuth
  redirect validation, rate limiting (findings addressed in `bef2058` / `c8a209d`; Phase 14's new
  surface - SCIM, organizations, alert egress, authz, MCP OAuth - still to review).
- Test flakiness, root-caused 2026-10-06/07: the broker cluster test's ports (#86) and test-user
  email collisions (#87).

## Phase 2 — Storage engine rebuild
- **2a. Spike — done.** DuckDB via CGO against 2M synthetic rows / 20 Parquet segments: all query
  shapes mirroring the existing test suite (time range, level filter, message "contains", client_ip
  equals, custom field exists, combined AND, paginated fetch) returned in 5–45ms. DuckDB confirmed as
  the query engine — see `ARCHITECTURE.md` "Phase 2a spike results". Still open: multi-arch Dockerfile
  build (2h) and a real ingest-throughput benchmark (Phase 7) — the spike's write loop doesn't count.
- **2b. Ingester — done.** `internal/storage/segments.Writer`: generic, signal-agnostic memtable →
  local Parquet segment writer (DuckDB Appender API, size/time thresholds). `logs/ingester.LogIngester`
  on top of it exposes the same `StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem)` signature as
  `logs/core.LogCoreRepository.StoreLogsBatch`, so it's a drop-in alternative — but it is **not yet
  wired into `logs/receiving/worker_service.go`**; that cutover is 2g, once 2c (metadata index) and 2d
  (querier) exist and the full test suite can run against the new backend end to end. 10 tests passing,
  including round-trip queries against the written Parquet files mirroring the real query test suite
  (level filter, message LIKE, client_ip equals, custom field exists).
- **2c. Metadata index — done.** `stream_segments` Postgres table (migration `20260914054608`) +
  `segments.SegmentRepository` (`Create`, `ListByTimeRange`, `ListPendingUpload`, `MarkUploaded`,
  `DeleteByIDs`, `ListByProject`). `LogIngester` now records a `SegmentRecord` after every flush, so 2b's
  writer and 2c's index are connected end to end. 16 tests passing (segments + ingester packages) against
  real Postgres + Valkey, including a round-trip proving a flushed segment is found via
  `ListByTimeRange`.
- **2d. Querier — done.** `logs_querier.LogQuerier.ExecuteQuery`/`DiscoverFields`, matching
  `logs/core.LogCoreRepository`'s existing signatures. Reuses the `QueryNode` AST unchanged; a new
  `where_builder.go` translates it to parameterized DuckDB SQL (custom fields via
  `json_extract_string` with a verified-safe scalar JSONPath, null-safe negation so a missing field
  still counts as "not equal"). Uses `SegmentRepository.ListByTimeRange` to prune which files to open,
  plus an explicit row-level `timestamp` filter (a segment's own range can span wider than the query
  window — caught this as a real bug via the test suite, not just assumed). 20 tests covering every
  condition operator, AND/OR/NOT nesting, pagination, sort order, and project isolation — 35 tests total
  across segments/ingester/querier, write→catalog→read proven end to end against real Postgres.
- **2e. Compactor — done.** Generic `segments.Compactor` (`MergeSegments` via
  `COPY (SELECT * FROM read_parquet([...])) TO ... FORMAT PARQUET`, schema-agnostic — reusable for
  future traces/metrics compaction as-is; `DeleteSegmentFiles`). `logs_compactor.LogCompactor` on top:
  `CompactSmallSegments`, `EnforceRetention`, `EnforceCountQuota`, `EnforceSizeQuota` (reuses
  `logs/cleanup/background_service.go`'s tiered cleanup-percentage policy for size quotas). Deliberate
  behavior change from the OpenSearch version, documented in code: retention/quota operate at segment
  granularity (whole files), not per-row, since Parquet segments are immutable — a project can hold up
  to one segment's worth of rows past its exact boundary. 15 tests passing (50 total across the four
  storage packages), including merge correctness and oldest-first eviction for both quota types.
- **2f. Object storage upload — done.** `segments.ObjectStore` wraps `gocloud.dev/blob` (streaming
  Upload/Download, provider chosen entirely by URL — `s3://`, `gs://`, `azblob://`, `file://`, `mem://`
  for tests); `fileblob` blank-imported by default (no cloud SDK weight), cloud drivers opt-in at 2g.
  `logs_uploader.LogUploader` uploads pending segments and optionally reclaims local disk
  (`DeleteLocalAfterUpload`, off by default). `LogQuerier` gained `WithObjectStore(...)` — downloads a
  segment on demand into a local cache if it's missing from disk; unchanged behavior without it. 13 tests
  (63 total across five storage packages) verified end-to-end against a real Postgres + an in-memory
  `gocloud.dev` bucket: ingest → upload → reclaim local disk → query still works via on-demand download.
  Found and fixed a real cross-package test-isolation issue (`ListPendingUpload` is deliberately not
  project-scoped, so tests could pick up other packages' stale rows sharing the same Postgres instance).
  Note: `gocloud.dev/blob`'s core package pulls in OpenTelemetry/gRPC/Google API indirect deps regardless
  of driver used — heavier than the API surface suggests, worth knowing before Phase 2h's Dockerfile work.
- **2g. Cutover — done.** OpenSearch implementation deleted outright (`logs/core/repository.go`,
  `query_builder.go`); new `logs_core.LogRepository` interface backed by `logs_parquetstore.LogParquetStore`
  (composing the ingester/querier/compactor), wired in from `cmd/main.go` before anything else starts.
  Every consumer (`receiving`, `querying`, `cleanup`, `downdetect`) now fetches the repository lazily at
  call time rather than at package-init time — required, not stylistic, since `SetRepository` can only run
  from inside `main()`. Ran the full pre-existing test suite (~4500+ lines never touched before this phase)
  against real Postgres + Valkey, no OpenSearch needed. Found and fixed 5 real bugs this way (see
  ARCHITECTURE.md and the commit message): a `resolveTimeRange` default-upper-bound bug, a DuckDB
  `TIMESTAMP` (microsecond) vs `TIMESTAMP_NS` precision bug plus the out-of-range sentinel it led to,
  missing custom-field stringification at the repository layer, and a shared test helper batching logs into
  one segment (fixed by flushing per item, which also restored full retention/quota precision). All green
  except 3 tests exercising sub-microsecond query boundaries — documented in ARCHITECTURE.md as a known,
  narrow, deliberately-accepted limitation (Postgres `TIMESTAMPTZ` is microsecond precision for the segment
  catalog even though the underlying Parquet data is true nanosecond).
- **2h. Runtime packaging — done, Phase 2 complete.** Dockerfile rewritten: `CGO_ENABLED=1`, dropped the
  `--platform=$BUILDPLATFORM` cross-compile trick (CGO can't cross-compile that way; CI's real release
  build only ever targeted `linux/amd64` anyway, so no capability actually lost). Runtime stage no longer
  installs Postgres/OpenSearch/JVM/Valkey — Postgres and Valkey run as their own containers via the new
  `docker-compose.yml.example` (a deliberate decision, not the default: see ARCHITECTURE.md). Updated
  `install-obzly.sh`, `backend/docker-compose.yml.example` (dev/test), both `.env.*.example` files, the
  README install section, and CI's `test-backend` job (OpenSearch wait/config steps removed) to match.
  Verified for real: built the full image on this host (arm64 — the CGO/DuckDB link step took ~57s),
  ran it against throwaway Postgres + Valkey with no OpenSearch anywhere — migrations, all background
  workers, and `/api/v1/system/health` all came back healthy through Docker's own HEALTHCHECK. Found and
  fixed a real, separate bug this way: no `.dockerignore` existed, so `COPY backend/.env*` was silently
  baking a developer's local `.env` into the image — added one.

## Phase 3 — OTLP ingestion for logs — done
- **Native OTLP logs receiver, HTTP + gRPC — done.** New `internal/features/otlp/logs` package: converts
  OTLP straight into `logs_receiving.SubmitLogsRequestDTO` and calls the *existing*
  `LogReceivingService.SubmitLogs`, so OTLP ingestion reuses all existing validation/rate-limiting/quota
  logic and the custom REST API/SDKs are completely unchanged. HTTP served at server root (`POST
  /v1/logs`, the OTLP/HTTP spec's default path — not under `/api/v1`), accepting both binary protobuf
  (`application/x-protobuf`) and JSON (`application/json` via `protojson`) bodies. gRPC
  (`collectorlogsv1.LogsServiceServer`) served on its own listener (`OTLP_GRPC_PORT`, default `4317`),
  started/stopped alongside the main HTTP server in `cmd/main.go` (graceful shutdown included). Built
  directly on `go.opentelemetry.io/proto/otlp` v1.10.0 (pinned — v1.11.0 needs Go 1.25) rather than the
  OTel Collector's `otlpreceiver`/`pdata` this doc originally floated — see ARCHITECTURE.md's "Phase 3
  implementation notes" for why.
- **`trace_id`/`span_id`, resource attributes, scope name/version, severity mapping — done.** Added
  `trace_id`/`span_id` end to end: `logs_core.LogItem`/`LogItemDTO`, the `logs` Parquet schema (two new
  VARCHAR columns), `where_builder.go` (as the first *nullable* system columns — old segments won't have
  them at all, handled via `segments.ParquetFilesExpr`'s new `union_by_name=true`, verified directly
  against DuckDB), and `querier.go`'s scan (as `sql.NullString`). OTLP resource attributes land in
  `fields` as `resource.*`, scope name/version/attributes as `scope.*`, log record attributes unprefixed.
  `SeverityText` wins when present (reuses the existing `normalizeLogLevel` alias table unchanged);
  otherwise `SeverityNumber`'s numeric range maps onto `LogLevel`.
- **gRPC API-key/project auth convention — done and documented.** `x-api-key` gRPC metadata key (the
  lowercased analogue of the REST/OTLP-HTTP `X-API-Key` header). New
  `ApiKeyService.ResolveProjectFromApiKey` resolves a project from a bare token with no project ID
  already known (OTLP's only identifying information) — an API key is mandatory for OTLP regardless of a
  project's own `IsApiKeyRequired` setting, since there's no URL path to fall back on.
- 23 new tests (8 converter unit tests covering severity mapping, timestamp fallback, trace/span hex
  encoding, resource/scope/attribute merging, nested `AnyValue` conversion; 7 HTTP + 3 gRPC integration
  tests covering valid/missing/invalid API keys and empty batches, the latter using an in-memory
  `bufconn` listener rather than a real port) plus the full pre-existing suite, run against real
  throwaway Postgres + Valkey. All green except the same 4 pre-existing, deliberately-accepted
  nanosecond-precision tests from Phase 2g (ARCHITECTURE.md's known-limitation section — corrected the
  count there from 3 to 4 while re-verifying, having found the same limitation also fails a second quota
  test independently).

## Phase 4 — Dashboards v1 (logs-only) — done
- **Aggregation engine — done.** New `logs_core.LogRepository.ExecuteAggregation` (one generic
  `{query, timeRange, groupField, bucketMs, limit}` method covers time series, grouped/stacked time
  series, top-N breakdown, and single-total panel shapes), implemented in `logs_querier` against DuckDB.
  Caught and fixed two real bugs via the test suite on the first pass: a SQL-parameter-ordering bug (the
  SELECT clause's own `?`s precede `FROM read_parquet([?], ...)` in the query text, even though FROM
  is evaluated first logically) and a NULL-scan bug grouping by a nullable system column or an
  inconsistently-present custom field (fixed with `COALESCE(..., '')`). 8 new tests (aggregator_test.go),
  73 total across the storage/logs packages.
- **Dashboard model/API — done.** `internal/features/dashboards`: Postgres-backed CRUD
  (`dashboards` table, migration `20260915000000`, panels as one JSONB column), permission level matches
  log search (any project member, not the stricter api-keys/settings "manage project" role), panel-query
  execution endpoint (takes a full panel definition so it doubles as live-preview for an unsaved panel),
  project-deletion cleanup listener. 20 new controller tests, real end-to-end: HTTP → receiving → DuckDB
  aggregation → HTTP, against real Postgres.
- **OpenObserve + Grafana dashboard JSON import — done**, at the scope this doc's original plan called
  for: title, panel titles/types, and grid layout come across; queries do not (both tools' query
  languages are out of scope to translate) — every imported panel starts with no query and a warning
  telling the user to add one. OpenObserve's flat-`panels`-or-`tabs[].panels` shapes both handled.
- **Netdata-style auto-populated "Overview" dashboard — done.** Fixed 4-panel rule set (log volume,
  errors over time, level breakdown, top error messages) — logs-only, so (unlike the metrics/traces
  version ARCHITECTURE.md originally described) every rule always applies and no discovery pass is
  needed yet. Regenerating replaces the same dashboard in place rather than duplicating it.
- **Frontend — done.** `react-grid-layout` for the drag/resize grid; `echarts` via modular imports
  (only the chart types dashboards use, not the full package — cut the production bundle by ~500KB).
  The panel editor reuses `features/query`'s existing `QueryBuilderComponent`/`TimeRangePickerComponent`
  unchanged. A time-series panel's bucket width defaults to "auto" — computed client-side from whatever
  range is currently selected, the same idea as Grafana's `$__interval`, since a fixed width baked in at
  generation time would look wrong once the viewer picks a different range. New "Dashboards" nav tab.
  Verified with a real `tsc -b && vite build` plus `eslint`/`prettier` clean (no frontend test runner
  exists in this repo yet — build/typecheck/lint is the verification bar here, same as CI's own
  `lint-frontend` job).

## Phase 5 — Traces — done
- **Span storage on the Phase 2 pipeline — done.** New `internal/features/spans` package, a
  near-exact structural mirror of `internal/features/logs` (core/ingester/querier/compactor/
  parquetstore/cleanup/testsupport), built on the *same* `internal/storage/segments` package with zero
  changes needed there — confirms the Phase 2 storage design's central bet that it's genuinely
  signal-agnostic. Span schema: trace/span/parent IDs (hex text), name/service/kind,
  start/end time (`TIMESTAMP_NS`), status, and JSON-encoded attributes/resource attributes/events
  (display-only in v1). Retention reuses the project's existing `MaxLogsLifeDays` setting rather than
  a new per-signal field (count/size quotas not implemented for spans in v1 — a deliberate,
  documented scope decision, not an oversight). 23 new tests across ingester/querier/compactor.
- **OTLP trace ingestion, HTTP + gRPC — done.** `internal/features/otlp/traces` mirrors `otlp/logs`
  (HTTP `POST /v1/traces`, gRPC `TraceServiceServer`, `x-api-key` auth) with one structural change:
  the OTLP gRPC server is now shared across signals (`internal/features/otlp.NewGRPCServer` +
  `RegisterGRPCServer(server)` per signal) rather than each owning its own — gRPC servers multiplex
  multiple services over one port. 16 new tests (6 converter unit tests, 10 HTTP+gRPC integration
  tests), including a real end-to-end verification (HTTP export → storage → query confirms exact data
  landed).
- **Trace query API + basic waterfall UI — done.** `internal/features/traces/querying`: REST API
  (search/get/services/operations) over `spans_core.SpanRepository`, same permission model as the log
  search API. Trace search's `GROUP BY trace_id` query needed a new DuckDB shape beyond what logs
  required — `min_by`/`FILTER`/`array_agg(DISTINCT ...)` for summary columns, `bool_or(...)` inside
  `HAVING` so a service/operation filter matches "any span in this trace" while the summary still
  reflects every span of a matching trace — verified directly against DuckDB before implementing, same
  practice as Phase 2's schema spikes. Caught the same class of SQL-parameter-ordering bug Phase 4's
  logs aggregator hit (documented in ARCHITECTURE.md, now that it's happened twice). Frontend: a new
  "Traces" tab (search filters, results table, click-through waterfall in a Drawer) that computes span
  depth from `parent_span_id` chains client-side. 14 new backend tests.
- **Jaeger-compatible query API — done.** `internal/features/traces/jaeger` implements Jaeger's
  long-stable public HTTP query API against `spans_core.SpanRepository`, mounted per-project
  (`/api/v1/jaeger/:projectId/api/...`) with its own API-key auth — what a Grafana Jaeger datasource
  points at. Tag-based filtering intentionally not implemented (ignored, not rejected) since our own
  trace search has a smaller v1 filter surface than Jaeger's tag-query language. 8 new tests, including
  a real trace round-tripped through the Jaeger JSON schema (processes map, `CHILD_OF` references).
- **Auto-dashboard rule set gains span panels — done**, scoped down from an actual service-map graph
  visualization to three per-service breakdown panels (requests/p95 latency/errors by service — the
  RED-method questions a service map answers) added to the Overview dashboard only when a project
  actually has span data. `dashboards_models.Panel` gained a `Source` field (defaults to `"logs"` for
  every pre-Phase-5 panel) and spans-specific fields; `ExecutePanelQuery` normalizes both signals'
  aggregation responses into one shared shape so the frontend renderer doesn't need to know which
  signal answered. Panel editor UI gained a data-source toggle with service/operation/metric pickers
  for spans panels. 6 new dashboards tests (3 backend spans-panel tests, 1 overview-with-spans test,
  2 existing tests updated for the response shape change).
- Full backend suite green throughout (same 4 pre-existing, accepted nanosecond-precision tests
  skipped, ARCHITECTURE.md's known-limitation section — unrelated to this phase).

## Phase 6 — Metrics — done
- `internal/features/metrics` (core/ingester/querier/compactor/parquetstore/cleanup/testsupport): the
  third stream type on the Phase 2 storage engine, structurally mirroring spans — confirms the storage
  engine is signal-agnostic a third time. Matcher evaluation for `SelectSeries`/label discovery happens
  in Go, not DuckDB SQL (only a metric-name equality pushes down) — simpler-to-verify-correct PromQL
  matcher semantics over raw query-time efficiency, reasonable at self-hosted scale.
- `internal/features/metrics/promql`: a `storage.Queryable`/`storage.Querier` adapter over
  `metrics_core.MetricRepository`, feeding the real, vendored `github.com/prometheus/prometheus`
  `promql.Engine` (pinned to `v0.309.1` - see ARCHITECTURE.md's "Runtime/toolchain versions" for why
  it's stayed there even after the Go toolchain moved past the version constraint that originally
  pinned it) — verified end-to-end against real DuckDB-backed storage (`sum by`, `rate()`, label
  values) before trusting it.
- `internal/features/otlp/metrics`: OTLP metrics ingestion (HTTP `POST /v1/metrics` + gRPC, shared OTLP
  gRPC server). Gauge/Sum convert 1:1 to samples; Histogram decomposes into Prometheus's classic
  `_sum`/`_count`/`_bucket` cumulative-bucket shape (OTel's own per-bucket deltas converted to
  Prometheus's cumulative `le` convention, matching the OpenTelemetry Collector's own Prometheus
  exporter). ExponentialHistogram/Summary skipped in v1 (documented, not mis-converted).
- `internal/features/prometheus/remotewrite`: Prometheus remote-write ingestion (`POST /api/v1/write`,
  Remote Write 1.0 protobuf/snappy only — 2.0 is a different wire schema, rejected with a clear error
  rather than mis-parsed). Metric type comes from the request's own `Metadata` where present, else a
  name-suffix heuristic.
- `internal/features/metrics/promapi`: the Prometheus-compatible HTTP query API (`/api/v1/query`,
  `/api/v1/query_range`, `/api/v1/series`, `/api/v1/labels`, `/api/v1/label/:name/values`), mounted
  per-project at `/api/v1/metrics/:projectId/api/v1/...` with the same API-key auth convention
  `traces_jaeger` established — this is the phase where an existing Grafana Prometheus-datasource
  dashboard genuinely works against obzly unmodified, just by pointing the datasource URL here.
- `internal/features/metrics/querying`: a small session-authenticated companion (list metric names) so
  the dashboard panel editor's frontend can suggest names while writing PromQL — it has no project API
  key, so it can't call `promapi` directly.
- Dashboards: `Panel` gained a `metrics` source with a raw `PromQuery` field (unlike logs/spans, a
  metrics panel's query *is* PromQL text, no structured aggregation request to build) —
  `executeMetricPanelQuery` runs it as a range query and normalizes each series into the shared
  `PanelBucketDTO` shape. `GenerateOverview` gained a discovery pass over `ListMetricNames`, generating
  one time-series panel per metric present (capped at 6) — resource/RED-method panels, scoped down to
  "one sensible default query per metric name" rather than semantic-convention-aware auto-detection,
  the same scope-down style `spanOverviewPanels` used in Phase 5.
- Frontend: `PanelEditModalComponent` gained a PromQL textarea + clickable metric-name suggestions.
  Deliberately no standalone metrics-explorer tab (unlike traces' Phase 5 waterfall UI) — not called
  for by this phase's own scope. Kubernetes OTel Collector config generator gained a `hostmetrics`
  receiver + `metrics` pipeline.
- 40+ new backend tests across 7 new packages (storage engine, PromQL adapter, OTLP converter/HTTP/
  gRPC, remote-write converter/HTTP, promapi HTTP, metrics_querying HTTP) plus 6 new/updated dashboards
  tests, all against real Postgres/Valkey/DuckDB throwaway infra. Full backend suite green throughout
  (same 4 pre-existing, accepted nanosecond-precision tests skipped).

## Phase 7 — Hardening
- **Logs ingestion throughput/load testing — done.** `backend/cmd/loadtest`: a standalone external
  HTTP client (concurrent workers, configurable batch size/duration/rate cap, latency percentiles,
  sample error capture) - deliberately calls the same `POST /api/v1/logs/receiving/:projectId` any
  real client uses, not an in-process shortcut, so the numbers reflect what a real user would see.
  Run for real against the live test deployment (not just built and trusted): sustained
  **~31,000 logs/sec / ~6.2 MB/sec** at 20 concurrent workers/100-log batches over 30s, p50 56ms /
  p99 168ms latency, 928k logs accepted with 0 rejected. 20 of 9,303 requests (0.2%) hit the tool's
  own 10s client timeout under sustained load ("context deadline exceeded", not a server error) -
  noted as a real, not-yet-explained tail-latency finding rather than smoothed over; worth profiling
  if it recurs at higher concurrency or shows up as a user complaint.
- **OTLP load testing (logs, traces, metrics; HTTP and gRPC) — done (2026-09-27).** `loadtest -mode
  otlp -signal logs|traces|metrics -protocol http|grpc [-gzip]`. Run against a throwaway stack on
  the 2-CPU test host (30s, 20 workers, 100 items/request, gzip; 0 rejected, 0 errors, stored
  counts match accepted):

  | Path | Throughput | p50 / p99 |
  |---|---|---|
  | REST logs (JSON) | 21.8k logs/s | 77 / 266 ms |
  | OTLP/HTTP logs | 13.5k logs/s | 124 / 449 ms |
  | OTLP/gRPC logs | 16.7k logs/s | 96 / 353 ms |
  | OTLP/HTTP traces | 24.3k spans/s | 65 / 267 ms |
  | OTLP/gRPC traces | 34.3k spans/s | 46 / 206 ms |
  | OTLP/HTTP metrics | 30.7k points/s | 47 / 253 ms |
  | OTLP/gRPC metrics | 50.8k points/s | 31 / 130 ms |

  Two real bugs found and fixed:
  1. The OTLP gRPC receiver had no gzip codec registered, so every request from a default OTel
     Collector `otlp` (gRPC) exporter failed with `Unimplemented`. The gRPC twin of the earlier
     OTLP/HTTP gzip bug.
  2. The logs queue worker stored at most one 5,000-log batch per 1s tick per worker (1 worker on
     2 CPUs), capping log writes at 5k/s while ingest accepted 15-20k/s, so the Valkey queue grew
     without bound under sustained load. Workers now drain until a batch comes back short. After the
     fix, a 60s run at 15.7k logs/s kept the queue under 75k and emptied it as soon as load stopped.
- **Crash-recovery testing for the WAL/memtable path — done.** First finding: there is no WAL. "Memtable"
  is purely an in-process Go slice (`segments.Writer.projectBuffer.rows`) with zero durability - grepped
  the whole backend for "WAL"/"write-ahead"/"durab" and confirmed every hit is about object-storage upload
  durability (the cold tier), not a write-ahead log for the hot-tier buffer. That's an accepted, documented
  tradeoff (`cmd/main.go`'s periodic 30s force-flush comment already says so; Phase 14 item 1 moves
  durability off the node without a WAL — acked data lives in NATS JetStream), not a bug - so "crash
  recovery" here can't mean "replay a log," only "verify the accepted loss window holds and a crash can't
  corrupt what survives." That review surfaced a real, separate bug it fixed: `Writer.writeSegment` ran
  `COPY ... TO '<final segment path>'` directly - a process killed mid-COPY (SIGKILL, OOM) could leave a
  truncated, corrupt `.parquet` file sitting permanently at the exact path a real segment would later
  occupy. Fixed with a temp-path-then-atomic-`os.Rename` (same directory/filesystem, so the rename itself
  can't fail partway) - the final `.parquet` name now only ever exists once a complete file is in place.
  Added `Writer.CleanupStaleTempFiles()` (glob-scoped to `*.parquet.tmp` only, so it can never touch a
  real, catalog-registered segment regardless of age), called once per signal (logs/spans/metrics) at
  startup before ingestion resumes - sweeps up exactly the one trace a crash mid-flush can leave. 3 new
  tests in `writer_test.go`, all passing for real against actual DuckDB output (not mocked): a successful
  flush leaves no temp file behind, a simulated crash-leftover temp file gets removed while a real,
  already-finalized segment right next to it is left completely untouched, and cleanup on a
  never-yet-written stream directory is a clean no-op rather than an error (the actual temp-then-rename
  and cleanup mechanics - not just a mocked crash - are what these prove).
  Separately verified against the live test deployment under real, continuous production-like traffic
  (not synthetic): rebuilt, deployed, `docker kill -s SIGKILL` while genuine `/v1/logs`/`/v1/metrics`
  requests were actively landing - restarted cleanly with no crash loop, no read errors on the surviving
  292-plus segments, and ingestion/quota-enforcement resumed normally. The actual loss window measured
  ~21s (last flushed segment vs. kill time), inside the documented ≤30s bound as designed. This
  particular kill didn't happen to land mid-COPY (no `.tmp` file was left to clean up) - the temp-file
  scenario itself is what the 3 unit tests above prove directly, deterministically, rather than depending
  on timing a live kill exactly right.
- **Object-storage upload failure/retry testing — done.** Code review of
  `logs_uploader`/`spans_uploader`/`metrics_uploader.uploadOne` and `ReconcileSegments` first
  (spans/metrics uploaders had zero test files before this - added mirrors of logs' suite, 6 tests
  each, 18 total). Found and fixed two real bugs: (1) `parquetstore`'s `UploadPendingSegments`/
  `ReconcileSegments` only wrapped the whole batch in one `uploadTimeout` (5m) - a single hung
  `Upload`/`Exists`/`Download` call to object storage (e.g. a black-holed connection) could starve
  every other segment in the batch for the full 5 minutes instead of just failing its own call.
  Fixed with a `perSegmentTimeout` (30s) derived from the batch ctx on every individual call, same
  fix mirrored identically across all three uploaders. (2) `uploadOne` marked a segment durably
  uploaded (`MarkUploaded`, eligible for local-disk reclaim) right after `store.Upload` returned
  success, with no check that the object actually landed - a "ghost upload" (provider reports
  success but the object isn't retrievable) would be silently trusted. Fixed with a follow-up
  `Exists` check before `MarkUploaded`; if it fails or reports missing, the segment stays pending
  and is retried next pass instead of being marked durable. New `Test_ReconcileSegments_
  RestoresMissingLocalCopy` test added to all three packages (logs' didn't have reconciliation
  coverage at all) proving a segment whose local copy is deleted gets correctly re-downloaded.
  Then verified live against the real deployed MinIO container, not just unit tests: `docker pause
  obzly-minio` (cgroups-freezer, black-holes any connection with no RST/FIN) during continuous
  production-like traffic. The next scheduled upload pass hit the per-segment timeout as designed -
  completed in ~30s per failed segment rather than hanging the whole 5-minute batch window, all
  8 pending segments (4 logs, 4 metrics) failed individually with `context deadline exceeded` and
  were correctly left pending (not marked uploaded, not lost) via `errors.Join`-aggregated error
  reporting. Backlog grew to 14 logs/16 metrics pending over the ~6.5-minute outage (traffic kept
  flowing throughout with zero ingestion impact - upload is fully decoupled from the write path).
  `docker unpause`'d MinIO, confirmed the very next scheduled pass (`06:05:03`) uploaded cleanly
  with no errors and the backlog resumed draining normally at the following passes - no manual
  intervention, no stuck/orphaned segments, no data loss. Known, accepted, documented limitation
  this task did *not* eliminate: the upload-time `Exists` check only closes the ghost-upload window
  at the moment of upload - there's no ongoing background integrity audit that would catch an
  object silently deleted or corrupted in the bucket sometime *after* a successful, verified
  upload; `ReconcileSegments`'s own `Exists` check only ever runs for segments whose *local* copy
  is already missing, not as a continuous audit of every uploaded segment.
- ~~Backup/restore story for the data volumes.~~ Moved to Phase 14-D (DR runbooks).
- Raise test coverage on ingest/query/cleanup paths specifically.

## Phase 8 — Network topology, service map, and correlation
Two features built on the existing signals: a service-relationship map and a first-cut
cross-signal correlation engine. Network topology comes from OBI (OpenTelemetry eBPF
Instrumentation) network metrics ingested through the existing metrics path. An earlier
iteration built a dedicated `flows` stream fed by Mermin; it was removed because OBI's metrics
need no bespoke stream, converter, or privileged manifest set in this repo.

- **8a. Network edges via OBI metrics — done, validated against a live test cluster.** No new
  stream or ingestion code: `servicemap/networkEdges.go` queries `obi.network.flow.bytes` /
  `obi.network.flow.packets` (from OBI's `network` preset) through the existing metrics querier,
  grouped by the `k8s.src.owner.name` / `k8s.dst.owner.name` attributes. The metrics path stores
  names and keys as sent, so they are dotted, not Prometheus-style; the first version of this query
  assumed underscored names and matched nothing until checked against real output. Checked against real data: `obi.network.flow.packets` does arrive; OBI reports each flow twice
  on the same node (once with the workload as owner, once with the fronting Kubernetes Service,
  about a third of all bytes), so Service-typed series are dropped when a workload-typed twin exists
  on that node. Cross-node double counting was not observed. OBI exports about once a minute, so
  the query reads 3 extra minutes and scales the counter's rate to the requested window; before
  that, Live mode's 60s window held 0-1 samples and network edges flickered in and out.
  Deployment: optional `deploy/kubernetes/obi/` manifest set (separate namespace, not referenced from
  the core `eks` kustomization; `hostPID`/`hostNetwork` plus narrow eBPF capabilities) and an
  "eBPF Network Map (OBI)" Data Sources catalog entry that generates the same bundle pre-filled with
  a real API key/endpoint. Verified on a live EKS cluster: OBI starts on every node with the narrow capability set and
  `hostNetwork`, in socket-filter mode. Set the optional cluster name, since OBI can't always
  detect it.
  Optional application traces (zero-code spans via OBI's application mode): the catalog entry has a
  "Trace applications" switch plus a namespace list, which adds a `discovery.instrument` ConfigMap and the
  `application` metrics feature. Written from OBI's docs; not yet validated on a live cluster, in
  particular that app tracing runs together with network mode.
- **8b. Service Map — done.** New `spans_querier.ServiceCallEdges` (a DuckDB self-join over the
  spans segment CTE, `child.parent_span_id = parent.span_id AND child.trace_id = parent.trace_id`,
  grouped by service-name pair, counting calls/errors) merged with Phase 8a's
  network-metric edges into one graph via a new `servicemap` package —
  `GET /api/v1/service-map/:projectId`, computed on read (no persisted graph model), fulfilling
  the service-map deferral `dashboards/services/overview.go` explicitly flagged as future work.
  Clamped to a 24h max lookback and capped at 50 ranked nodes (by traffic volume) with the
  remainder collapsed into a synthetic "other" node — both real, enforced safeguards, not just
  documented intentions. New Service Map tab renders it via ECharts' `graph` series (already a
  dependency, zero new libraries) — solid edges for spans-confirmed calls, dashed for network-only-
  inferred, so a service with zero tracing instrumentation (a bare Postgres/Redis reachable only
  via network flow data) is visibly distinguishable from a normally-instrumented one instead of
  being invisible.
- **8c. Correlation engine — done, deliberately narrow MVP.** New `incident_sessions`/
  `incident_session_signals`/`correlation_watermarks` Postgres tables (relational, not a segment
  stream — low-volume, needs joins) and a leader-locked worker that, per project, scans a bounded
  incremental window `(watermark, now]` (capped 24h lookback even on cold start) across logs/
  spans, groups candidates by service+5-minute time window, and persists a session only if
  the group contains a genuine anomaly (an ERROR log, a non-OK span status, or a shared trace_id
  link). Cross-service grouping is pruned by Phase 8b's service graph (two services only merge if
  the graph shows them connected recently), which is what keeps this from becoming an O(services²)
  comparison as service count grows. Explicit non-goal, stated in doc comments and the UI itself:
  this is dimensional + temporal correlation, not semantic/ML/embedding-based similarity — no
  metric-threshold trigger either (checked `metrics/` for an existing threshold/alerting concept
  to reuse; none exists, so the trigger set was narrowed to logs/spans/trace_id rather than
  building one net-new). A hard per-project time/row budget skips (not blocks) a project that
  exceeds it on one cycle, retrying next cycle. New Incidents tab: a sessions list + detail view
  that re-hydrates each signal live against its owning stream (never a stale copy) and composes
  existing per-signal rendering rather than a new bespoke visualization; a signal whose underlying
  row was already deleted by retention degrades to a single "no longer available" placeholder
  instead of breaking the view. Proved the bounded-scan-cost safeguard with a real test (200
  already-scanned historical rows don't slow down or get reprocessed by a second run) — caught and
  fixed one real test-isolation bug in the process: the original assertion timed
  `correlateAllProjects()` (which sweeps every project in the shared test Postgres — 1,278 of them
  by the time this package's tests run after the full suite), not the one project under test's own
  incremental-scan cost; fixed with a new `ExecuteTaskForProjectForTest` test-only hook scoped to a
  single project, the same kind of fix this session already applied once before for
  `ListPendingUpload`/`ListUploaded`'s cross-package pollution.
- Done (2026-09-27): OBI label names validated live (LAB-A EKS sends `obi_network_flow_bytes`,
  which the service map reads); the service map's node drawer has a "View traces" button that
  opens the Traces tab filtered to that service (spans-derived nodes only).
- Not yet done: metric-threshold correlation triggers (deferred, no existing alerting concept to
  build on yet).

## Phase 9 — Open-source readiness
- ~~CONTRIBUTING.md, CODE_OF_CONDUCT.md, issue/PR templates, CHANGELOG going forward, versioning
  policy~~ done (2026-10-07): versioning is SemVer from commit subjects (CONTRIBUTING.md), release
  notes per release, operator actions in the upgrade runbook. Open: a code-of-conduct contact.
- ~~README overhaul reflecting the new multi-signal scope and architecture~~ done (2026-10-07).
- Decide SDK strategy: pull per-language client libraries into this repo vs. keep polyrepo with an index.
- Docs: what stays in-repo vs. on the marketing site.

## Phase 10 — Launch
- Soft launch once Phases 1–9 are done and stable.
- First patch cadence, tag 1.0 with a clear versioning/deprecation policy.

## Phase 11 — Multi-site: control plane and Sites — done
Decouples the frontend into one centrally-deployed UI/identity layer (a **control plane**) fronting
one or more independently-deployed backends (**Sites**) - see ARCHITECTURE.md's ["Multi-site:
control plane and Sites"](ARCHITECTURE.md#multi-site-control-plane-and-sites) section for the full
design. Fully opt-in/backward compatible: unset, a deployment is exactly the single-site install
it always was.

- **Phase 0** (prep, zero behavior change): `SERVICE_MODE=control-plane` plumbing in
  `config.go`/`cmd/main.go`, `GenerateAccessTokenWithOptions`/`auth_origin` claim,
  `GET /api/v1/site/info`, frontend `getApiBase()`/`IS_CONTROL_PLANE`.
- **Phase 1** (Site-side token exchange): `central_identities`, `users.central_admin`/
  `project_memberships.central_managed`, `central_verifier.go`/`central_exchange.go`,
  `POST /api/v1/auth/site/exchange` (opt-in via `CONTROL_PLANE_ISSUER`).
- **Phase 2** (control-plane backend): `internal/features/control_plane` - Site registry +
  grants, `SigningKeyService`/JWKS, `AssertionService`, `SiteTokenService` (encrypted Valkey
  cache, singleflight), `ProxyController` (`/sites/{id}/api/v1/...`, denylist→allowlist path
  policy, header hygiene, 401-retry-once), session-revocation hooks.
- **Phase 3** (frontend): `shared/site` (SiteContext), `entity/sites`, a Site picker, the
  "Sites" admin tab (register/edit/remove a Site, grant/revoke access), per-Site-scoped
  localStorage.
- **Phase 4** (deployment + docs): compose/Kubernetes examples for both roles (including a
  local multi-site reference and a standalone control-plane shape), `install-obzly.sh
  --role`-equivalent env vars, this architecture section, the second `ARCHITECTURE.drawio`
  page, and the `SECURITY_REVIEW.md` multi-site checklist.

Explicit non-goals (kept per-Site, never centralized): cross-site aggregation, a Site's own
projects/plans/API keys, ingest (stays regional, never proxied), and persisting/caching proxied
signal data centrally.

## Phase 12 — MCP for AI assistants
Read-only MCP server so assistants can investigate telemetry with the user's own access - see
ARCHITECTURE.md ["MCP server"](ARCHITECTURE.md#mcp-server-ai-assistants).

- **Phase 1** (done): personal access tokens, `/mcp` (stateless streamable HTTP), 18 read-only
  tools over logs, traces, metrics (new PromQL endpoint), dashboards and correlation, one
  endpoint for all Sites on a control plane, Profile page token management and setup snippets.
- **Phase 2** (done): OAuth 2.1 authorization server for claude.ai / Claude Desktop connectors
  (protected resource metadata, PKCE, client ID metadata documents and dynamic registration,
  consent page, audience-bound tokens, rotating refresh tokens with reuse detection, connected
  apps).

## Phase 13 — AI observability
Monitor AI tool usage - see ARCHITECTURE.md ["AI observability"](ARCHITECTURE.md#ai-observability-claude-code-usage).

- **Phase 1** (done): Claude Code. Data Sources entry, `ai_logs` stream separate from Search,
  AI Observability tab (cost/tokens/cache/sessions/users/models/tools/errors, session timeline).
- **v0.57.0** (done): a "Claude Code usage" dashboard template, budgets (per project, user or
  model, alerting once per period), delta temporality in the OTLP metrics receiver, and LLM apps
  from the OpenTelemetry `gen_ai.*` conventions with a model price table - see
  [`docs/AI_OBSERVABILITY.md`](docs/AI_OBSERVABILITY.md).

## Phase 15 — Synthetics, Infrastructure and cloud connections — done
Control-plane features beyond the proxy - see ARCHITECTURE.md ["Synthetics, the infrastructure
inventory and cloud connections"](ARCHITECTURE.md#synthetics-the-infrastructure-inventory-and-cloud-connections),
[`docs/SYNTHETICS.md`](docs/SYNTHETICS.md) and [`docs/INFRASTRUCTURE.md`](docs/INFRASTRUCTURE.md).

- **v0.55.0** (done): Synthetics phase 1 (HTTP/TLS/TCP/DNS/ICMP monitors run by `obzly-probe`,
  results to a Site project as OTLP, Test via the probe long-poll, YAML import/export), the
  Infrastructure inventory from the agents' data (health, lifecycle, changes, across Sites), and
  notification channels and email defined once in Obzly settings and synced to Sites.
- **v0.56.0** (done): probe groups (spread with failover, 3 replicas in the chart), monitor folders,
  dependencies with Impacted status and muted child alerts, alerts per monitor as managed Site
  rules, the dependency map; cloud connections (AWS and Azure inventory scanned by a probe group,
  wizard with CloudFormation/Bicep/CLI, the control plane as OIDC issuer) merged into
  Infrastructure; Obzly settings in tabs with Overview, Updates and audit CSV export; require MFA
  for everyone; JetStream reads capped by bytes.
- **v0.57.0** (done): multi-step API monitors and maintenance windows.
- **Next:** cost and security posture over the same cloud connections (designed and approved:
  [`docs/CLOUD_COST_POSTURE.md`](docs/CLOUD_COST_POSTURE.md); built in three releases - cost,
  posture, recommendations), AWS `tag:GetResources`
  breadth and Azure Resource Health, CloudWatch / Azure Monitor health for load balancers and
  databases, browser monitors.

## Phase 14 — Enterprise scale
Takes Obzly to 1–5 TB/day ingest (5 TB/day sustained as the acceptance target) with ~1 PB stored at
1-year retention as the ceiling, on Postgres + Valkey + S3 and one image that embeds NATS JetStream.
Design: ARCHITECTURE.md ["Scale architecture"](ARCHITECTURE.md#scale-architecture-1-pb--5-tbday);
review record, costs and open questions: [`docs/ENTERPRISE_SCALE_PLAN.md`](docs/ENTERPRISE_SCALE_PLAN.md).

**Status (2026-10-08):** built - items 1, 2a, 2b, 3, 4, 5, 6a, 6b, 7, 8, 9, 10a, 10b, 10c, 12.
Open - 11 (performance validation, which also holds the volume "done when" runs of items 1, 2a, 4
and 5; parked until a large server is available).

Sizes: S <1 wk, M 1–3 wk, L 1–2 mo, XL 2–4 mo (one engineer). Critical path 7 → 3 → 1 → 4 → 5 → 6b,
~8–17 months in sequence; total ~14–29 engineer-months, so the schedule is bounded by the critical
path, not headcount (~4 quarters with 3–4 engineers if B and C run uninterrupted).

- **14-A — See and measure** (parallel; no schema or protocol breaks beyond one nullable column)
  - **7. Self-observability** (M; first) — internal `/metrics`, 1% self-tracing, published SLOs,
    seed dashboard and alerts. *Done when* every SLO has a metric and an alert rule.
    **Part 1 done (2026-09-30):** `METRICS_ADDR` listener in every mode, ingest/writer/object
    store/query/leader-task/catalog/Postgres-pool metrics, SLO recording rules and alerts in
    `deploy/observability/prometheus/` (see ARCHITECTURE.md "Self-observability"). **Part 2:**
    DuckDB memory per handle, seed Grafana dashboard. **Part 3 (self-tracing):** 1% of requests and
    background passes as traces in the Site's reserved "Obzly (system)" project
    (`SELF_TRACING_SAMPLE_RATE`). JetStream metrics (buffer bytes, messages, replicas behind)
    came with item 1.
  - **11. Performance validation** (M) — `cmd/datagen` 1 TB recipe, `cmd/loadtest` modes, nightly
    suite with a 10% regression gate, Spot-interruption chaos test, capacity model.
    *Done when* the nightly gate blocks a regression and the capacity model is published.
  - **2a. Parquet format** (M; part of item 2) — ZSTD options, compaction-time sort, per-segment
    stats. *Done when* ZSTD files are ≥25% smaller on the 1 TB set and a service-filtered 1 h query
    skips ≥80% of row groups. **ZSTD/V2 options and compaction sort done (2026-09-30)**; −48% on a
    500k-row synthetic set. **Per-segment catalog stats done (2026-09-30)**, used to skip log
    segments by service/level and metric segments by name. Remaining: the 1 TB measurement (item 11).
  - **1-interim. Upload from every replica** (S) — fixes `api`-written segments never being uploaded
    in the HA overlay (nullable `stream_segments.writer_id`); plus readiness-on-SIGTERM and shutdown
    deadlines (S). **Done (2026-09-30):** `stream_segments.writer_id` + per-replica uploads (`api`
    every 15 s, `all`/`worker` after each compaction tick, all on shutdown); `/system/ready`,
    `/system/live`, `SHUTDOWN_DRAIN_SECONDS`, gRPC stop deadline, split probes in the manifests.
  - **10a. Multi-arch + Helm v1** (M) — `linux/amd64` next to arm64; one chart for today's roles.
    **Done (2026-10-06):** images for `linux/amd64` and `linux/arm64` - the CI runner is arm64, so
    the backend is cross-compiled for amd64 (bookworm cross toolchain, same glibc) rather than
    built under QEMU; `deploy/helm/obzly` with `ha` (broker, api, worker, querier), `single` and
    `control-plane` modes, in-cluster or external Valkey, generated tokens kept across upgrades,
    PDBs, HPAs, the querier NetworkPolicy, a PodMonitor and Ingress; a `Helm chart` workflow
    lints, renders and schema-validates every mode. Not yet: migration Job, CNPG/Valkey operator
    modes, presets (item 11).
  - **12. Frontend test baseline** (M) — Vitest + Playwright smoke before item 4; honour
    `TrackTotal`. **Done (2026-10-07):** Vitest unit tests in CI (`npm test`: search parser,
    attribute-name parity with the backend, escaping, level ordering) and a Playwright smoke suite
    (`npm run test:e2e`, sign in, search, main tabs, no page errors) run against the test Site;
    `TrackTotal` was already honoured by the backend.
- **14-B — Write path & catalog** (sequential)
  - **3. Catalog partitioning** (L; after 7) — monthly partitions, covering index, `PlanSegments`,
    partition detach/drop. *Done when* `PlanSegments` p95 <50 ms on a 5M-row catalog and retiring a
    month is detach + drop. **Part 1 (2026-09-30):** catalog partitioned `LIST(stream)` →
    `RANGE(max_time)` monthly (see ARCHITECTURE.md for why `max_time`), partition maintenance; on 5M
    rows planning p95 1.5 ms and a month retires in ~70 ms by detach + drop. **Part 2:** `PlanSegments`
    with SQL-side stats pruning (busy-project 30-day plan 470 → 43 ms). **Part 3:** retention stays
    exact per segment (decided 2026-10-01: month-granular retirement would keep data up to a month
    longer than configured); ended months are dropped once empty (one locked transaction, ~ms).
    `stream_segments_unpartitioned` dropped after v0.35.0. Remaining: audit columns with items 6/10c.
  - **1. JetStream ingest** (XL; after 3) — `broker` role and embedded NATS, JetStream ingest for
    logs/spans/metrics, size-flushed L0, date-prefixed keys, `DeleteBatch`, sharded compaction with
    one shard key for ingest and compaction, Live Tail on core NATS, then Valkey queue removal.
    *Done when* 5 TB/day holds for 24 h with p99 ack <500 ms including the JetStream commit, killing
    any one `api`/`worker`/broker pod loses 0 acked rows, and compaction throughput scales with
    workers. **Step 1a (2026-10-01):** `broker` role, in-process server for `all`, ingest streams
    behind `INGEST_BUFFER=jetstream`, stream metrics/alerts, preview broker StatefulSet. **Step 1b
    (2026-09-30):** logs publish to JetStream and are acked to the client after the JetStream
    commit; workers own a fair share of 64 shard leases, write L0 by size (64 MB) or max-age (1 h,
    floor 5 min) and ack only after upload. **Step 1c (2026-10-01):** spans (OTLP) and metrics (OTLP,
    remote-write) on the same path. **Step 1d (2026-10-01):** Live Tail on core NATS, published only
    while a tail is open (cross-replica). Open: the polling fallback reads stored logs, which lag by
    the L0 max-age. **Step 1e (2026-10-01):** the Valkey logs queue is removed; without the broker
    logs write directly (JetStream stays opt-in, decided 2026-10-01). Next: sharded compaction and the
    date-prefixed key layout; remove `DrainLegacyQueue` a release after 1e.
    **Query-time tail (2026-10-01):** logs queries include rows still pending in JetStream, so the
    Logs page is current despite the L0 max-age; the same for spans and metrics (traces,
    dashboards) since 2026-10-01. **Date-prefixed keys (2026-10-01):** new uploads keyed
    `<stream>/<project>/<yyyy>/<mm>/<dd>/<level>/<id>`, `stream_segments.level`, and
    `ObjectStore.DeleteBatch` (S3 `DeleteObjects`). **Leveled, shard-owned compaction
    (2026-10-01):** L0 -> L1 (128 MB / 256 MB raw, 16 files or 1 day, sorted) -> L2 (512 MB-1 GB,
    concatenated, one UTC day per L2 file); merged files sorted by hour bucket first; compactor DuckDB
    bounded to 1 GB, 512 MB of merges per project per pass; with JetStream each worker compacts the
    projects whose ingest shard it holds. Item 1's building blocks are done; its
    "done when" (5 TB/day for 24 h, zero acked-row loss on any pod kill, compaction scaling) needs
    item 11's load and chaos runs.
- **14-C — Read path**
  - **4. Distributed query** (XL; after 1) — coordinator in `api`, `querier` executors, NATS
    discovery/dispatch, gRPC results, rendezvous affinity, keyset pagination. *Done when* a 30-day
    dashboard on 1 PB returns <10 s under budget, killing an executor mid-query returns an identical
    result, and adding an executor moves ≤ ~1/N of cache affinity.
    - *4a (done):* `internal/query` - coordinator, fragment interface (fragment SQL over
      `{{source}}`, final SQL over `{{results}}`), rendezvous assignment, in-process executor. The
      logs search (count + page) runs through it; one fragment runs the final SQL itself, so the
      single-executor path is still one DuckDB query.
    - *4b (done):* `SERVICE_MODE=querier` executors - NATS micro-service discovery and dispatch,
      results over a token-protected internal gRPC listener, retry on the next-ranked executor
      then in-process; the query-time tail stays in-process.
    - *4c (done):* keyset pagination - `cursor` / `nextCursor` over `(timestamp, id)` (the
      Logs page's Load More uses it; `offset` still works); `trackTotal: false` skips the count;
      an unfiltered count takes catalog row counts for segments wholly inside the window and reads
      only boundary segments and the tail.
    - *4d (done):* logs histogram/dashboard aggregations, patterns and field discovery, and trace
      search, get-trace, services/operations, k8s metadata and span aggregations run through the
      coordinator (fragments return partials - counts, sums, per-trace aggregates, latest values -
      and the final SQL merges them, so a top-N or a trace filter sees full values). In-process on
      purpose: span percentiles (no mergeable partial), the service map's parent/child join, and
      the user SQL sandbox (user SQL stays on the `api` replica). Metrics move with item 5's new
      layout.
    - *4e (done):* scan budget - `QUERY_MAX_SCAN_GB` (default 100 GiB of compressed segments per
      query, `0` off) refuses a histogram, dashboard panel, pattern list or trace search before
      reading anything (422 `QUERY_TOO_LARGE`), and skips a search's count (`countSkipped`, the
      page still returns); page reads and background work are never limited. Metrics:
      `obzly_query_{planned_bytes,rejected_total,fragments_total,fragment_duration_seconds,
      fragment_retries_total,executors}` and executor-side `obzly_query_executor_*`; a
      "Distributed query" dashboard row and two alerts. Item 4's "done when" (30-day dashboard on
      1 PB under 10 s, executor kill mid-query, 1/N affinity) needs item 11's load and chaos runs.
    - *4f (done):* search partitions and streaming (after OpenObserve) - a page search walks
      time windows newest-first (5 min ×4 each step) and stops once the page is full;
      `POST /logs/query/stream` sends each window's rows as NDJSON; the Logs page streams, skips
      the count and takes the total from the histogram.
    - *4g (done):* partition result cache - time-bucketed aggregations are computed per hour
      partition and finished partitions are kept in Valkey (`QUERY_CACHE_TTL_HOURS`,
      `QUERY_CACHE_DELAY_SECONDS`), so a repeated histogram scans only what changed.
    - *4i (done):* token bloom filters - ID-like tokens of each log segment in a filter beside
      it; an ID search skips segments without the token on the executors, before any download
      or scan; ID-like terms match whole tokens; a backfill covers older segments. Plus one 170 s
      deadline for a whole search (partial results), heartbeats, fragment cancellation on the
      executors, `contains()` (3.5x), and Stop / Reset on the Logs page.
    - *4j (done):* facets and histogram - field values and patterns read the newest windows
      until 100,000 matching logs (`sampleRows`, `/logs/query/field-values`), severity counts
      come from the histogram, which reads the search without the level filter; the cache
      evicts least recently used (hits touch mtime); reconcile only verifies remote copies
      (no re-download); MCP tool calls get 175 s; time presets stop at 72 h.
    - *4l (done):* stored message patterns and per-minute counts - `pattern_hash` computed once at
      ingest (first 1,024 characters) instead of six regexes per row per query; per compacted
      segment, per-minute counts by level, service, namespace, container and pattern
      (`logs_1m`), which the histogram reads for whole minutes; streamed aggregations (histogram
      and dashboard panels fill in partition group by group, newest first); log anomalies
      (new, spiking and dropping patterns against an earlier period) and `log_patterns` alert
      rules; Kubernetes rollout markers; log-to-trace links; column-only reads of cold segments from object
      storage.
    - *4k (done):* log context - "Show logs around" a result: the same source's logs before and
      after it (`/logs/query/context`, cursor pages from the anchor), scope chips to widen or
      narrow, side panel with pop-out, "Open as search", and the `log_context` MCP tool; after
      OpenObserve's search around and Grafana's log context.
    - *4h (done):* querier cache - a stable affinity id (the data volume's `.writer-id`) instead
      of the NATS instance id, the chart's `querier.persistence` (a volume per querier), warm-up
      on start (`QUERIER_WARM_HOURS`) and pre-fetch on upload (`obzly.segments.uploaded`); the
      fragment deadline is 170 s and the control plane waits 180 s for a Site. Found on a
      production Site, 2026-10-08/09: a crashing compaction left ~13,800 small log segments and
      7-day searches timed out cold; with these, the same search returns its first rows from the
      newest window.
  - **5. Metrics two-stream layout** (XL; parallel with 4 once the fragment interface exists) —
    series/samples streams, label index, rollups. *Done when* `rate()` over 1 h across 1M series is
    p95 <5 s and the PromQL parity suite passes.
    - *5a (done):* PromQL matchers compile to DuckDB predicates (label = `coalesce(
      json_extract_string(labels, path), '')`, regex = `regexp_full_match`) and every metrics query
      runs through the coordinator, so executors filter where the segments are; the Go matcher is
      a test oracle. On the test Site, name-regex queries went from 10-12.5 s to 0.13-0.28 s.
    - *5b (in progress)*, additive so data written before it stays readable: 5b-1 (done) every
      sample carries `series_id` (xxhash64 of name + canonical labels; SQL `obzly_series_id`
      fills older current-version files at L0->L1 compaction) and compaction sorts by hour,
      metric name, series id; 5b-2 (done) a deduplicated `metrics_series` stream (written with
      each samples flush, acked with it on JetStream, merged to one row per series) serving label
      names/values and metric names for windows after the project's `metric_series_coverage`
      start; 5b-3 (done) two-phase series select (series stream -> ids -> samples by id and metric
      name, no label parsing per sample), with 5a's predicates before coverage or past 100k
      series.
    - *5c (done):* Postgres `metric_label_index` (metric, label, value, first/last seen) and
      `metric_name_index` (metric, type, first/last seen), upserted from each series flush and
      pruned by retention, answer label names/values and metric names without reading a segment
      when the window is covered and the matchers are at most a metric-name equality (anything
      else needs per-series correlation: the series stream).
    - *5d (done):* `metrics_5m` / `metrics_1h` rollups - per series and bucket the first, last,
      min and max samples with their times, plus count and sum - built by the compaction pass
      from each cold (L2) metrics segment (1 per project per pass, backfilling older ones), tied
      to it by `stats.source`, never merged. A PromQL step of 5 min / 1 h reads rollups for the
      cold segments in the window and raw samples for the rest; a range function needs 5 buckets
      in its window (`rate(x[1h])` reads 5 m rollups, not 1 h).
    - *5e (done):* parity suite - 20 PromQL queries (selectors, regex, absent labels, name regex,
      rate/increase across resets, aggregations, `*_over_time`, topk, offset, absent), range at
      two steps and instant, compared exactly with an in-memory reference that selects series with
      Prometheus's own matchers, through both select paths (`promql/parity_test.go`); and a
      benchmark of `sum(rate(x[5m]))` over 1 h at a 1 m step (`promql/bench_test.go`, opt-in via
      `OBZLY_METRICS_BENCH_SERIES`). Single host, local executor: by series id 1k series p95
      0.39 s, 3k 1.5 s, 7k 2.8 s (label predicates 1.5 / 4.2 / 7.4 s) - linear, ~0.36 ms a series.
      **Item 5's "done when" is not met:** every raw sample reaches Go for the PromQL engine
      (capped at 2M per select, so ~7.6k series at 15 s over an hour), which would put 1M series
      at minutes. It needs 5f.
    - *5f (done):* PromQL push-down - `rate`/`increase`/`*_over_time` over one metric, optionally
      in `sum/avg/min/max/count by`, evaluated in fragments (per-series, per-step partials merged
      by the coordinator) and handed back to the engine as a series per result, which evaluates
      the rest of the query; storage declines whatever it can't answer exactly. Parity suite: 33
      queries, range and instant, both ways, plus a cross-executor merge test; mutations of each
      formula branch fail it. Single host, `sum(rate(x[5m]))` over 1 h: 7k series p50 2.0 s
      (engine by series id 2.4 s, label predicates 6.0 s); 30k series (7.8M samples, past the
      engine's cap) p50 10.2 s. Still linear (~0.6 µs a sample per core), so item 5's 1M-series
      bar needs executors.
    - *5g (done):* push-down by time slice - the steps split into one contiguous slice per
      executor, each reading every file its steps need, so executors compute and aggregate their
      steps whole and the coordinator only concatenates (no per-series partials cross the
      network); slices run whole and retry whole. Interleaved files no longer make push-down
      decline. Item 5's "done when" needs item 11's multi-node load run: on one host the
      executors share the same cores.
  - **2b. Promoted typed columns** (M) — the one `schema_version` bump (2→3), generalised rewrite.
    **Done (2026-10-06):** per-project promoted log fields (8 text, 4 number) in typed columns
    with a per-row mask; queries read a fragment's files from the column alone when every row has
    the promotion (8 ms vs 516 ms from the JSON on 2M rows), else per row; number fields get range
    filters. Schema anchors let pre-v3 files read the new columns as NULL; v2/v3 merge; the data
    migration relabels v2 and rewrites v1. See ARCHITECTURE.md "Parquet quality".
  - **10b. Helm adds `querier`** (S). **Done with 10a (2026-10-06).** The plain manifests
    already run every role: `eks/ha/` has the three-broker StatefulSet, `api`/`worker` on
    JetStream, `querier` with a NetworkPolicy on its result port, and the shared pieces in
    `eks/common/` (2026-10-05).
- **14-D — Lifecycle & DR** (after B)
  - **6a. Retention policies, lifecycle, sampling** (L; parallel with C) — per-signal policies,
    tag/prefix lifecycle rules, deterministic tail sampling. *Done when* a fan-out test shows 0
    partial traces after sampling. **Done (2026-10-06):** `retention_policies` per project and
    signal (retention days, metric rollup days, trace rate + slow rule, log rules) with an API and
    a settings section; trace tail sampling when a day's spans merge cold, deciding each trace
    over the whole day (the fan-out test: 500 traces over 6 flush files plus late spans, 0
    partial); log head sampling at ingest by trace id with the same hash; upload tags for
    lifecycle rules. Org-scoped policies wait for item 9; Glacier-aware planning is open.
  - **6b. Rollup-only aging** (S; after 5). **Done (2026-10-05):** "Metric rollup retention
    (days)" (a metrics retention policy, 6a) keeps the 5 m / 1 h rollups after raw samples age out; the retention pass
    rolls up and marks a retiring segment's rollups aged before deleting it, and queries read aged
    rollups at any step. Orphaned (non-aged) rollups are cleaned up. See ARCHITECTURE.md
    "Retention and lifecycle".
  - **10c. DR runbooks** (M) — backup/restore, DR and upgrade runbooks incl. key material,
    `obzly reconcile`, weekly integrity audit, Upgrade status additions (JetStream lag, release
    marker). *Done when* a restore drill meets RPO 5 min and the key-material check.
    **Done (2026-10-06):** runbooks for backup/restore, DR and upgrade; `obzly reconcile`
    (lost / re-upload / orphan adoption with an overlap guard / size mismatch); the weekly
    integrity audit with metrics and alerts; Upgrade status with release markers, persisted
    rewrite failures, the ingest buffer and the last audit. Drill on a copy of the test Site
    (backup-restore.md): RPO 19-20 s, restore 2-5 s, `secret_keys` and a pre-disaster session
    intact; reconcile against the real bucket adopted only post-restore orphans.
- **14-E — Enterprise track** (a separate engineer from B onward)
  - **9. Organizations, teams, SCIM** (XL) — orgs/teams/authz, then SCIM 2.0. *Done when* every
    pre-upgrade SSO mapping resolves to the same role and the Okta/Entra SCIM checklists pass.
    **9a done (2026-10-06):** organizations on the control plane spanning Sites (a standalone
    install is its own), members and roles, teams with Site/project grants merged into effective
    access (strongest wins), a Default organization for existing installs, the Organizations tab;
    SSO mappings resolve to the same roles (test).
    **9b done (2026-10-06):** SCIM 2.0 per organization (Users = members, Groups = teams), owner-only
    tokens, deactivation only of accounts its SCIM created, suspension otherwise; Okta's and Entra's
    request sequences replayed in tests (the vendors' own validators need a public endpoint).
    **9c done (2026-10-06):** `internal/authz` (`Authorize`, `Require`), `CanUserWriteToProject`
    retired, a call-site ratchet test for the remaining legacy checks, denials audit-logged,
    decisions counted. **`org_plans` done (2026-10-08):** quotas per organization, enforced per Site
    (the stricter of a project's own limit and the plan's, even with its rate limiting off) from a
    plan document the control plane signs for each Site; Sites and members on the control plane.
    Item 9 is complete.
  - **8. Alerting and notifications** (L; project-scoped first, org-scoped channels after 9) —
    *Done when* rule-to-notification p95 <60 s and the egress tests (private IP, redirect, DNS
    rebinding) are refused. **Done (2026-10-06):** rules over metrics (PromQL), logs and traces,
    the Prometheus state machine, an outbox with backoff and silences, webhook/Slack/PagerDuty/
    email channels through a shared hardened egress client, and an Alerts tab. Rule to
    notification measured at 15.2 s; private-IP, redirect and rebinding egress refused in tests.
    Not yet: notification grouping, org-scoped channels (with 9).

Open questions (tracked in the plan): upgrade-status semantics, querier shape, interim channel
authz, compression figures, CNPG at 1–5 GB, ingest mix, NATS packaging default, JetStream
partitioning at the ceiling, embedded R1 sync policy, organizations vs the control plane.
