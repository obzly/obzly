# Obzly Architecture

This document captures the target architecture for Obzly (a fork of the open-source Log Bull
project, since substantially redesigned): an OpenObserve-inspired, self-hosted, Apache-2.0
observability platform covering logs, metrics, and traces. It reflects decisions made during the
redesign planning pass; treat it as the source of truth and update it as decisions change. See
`ROADMAP.md` for the phase-by-phase execution plan.

## Starting point (as inherited)

- Backend: Go 1.24, Gin, GORM, feature-sliced (`controllers/dto/interfaces/models/repositories/services/testing`
  per feature: users, projects, api_keys, logs, audit_logs).
- Frontend: React 19, TypeScript, Tailwind 4, Antd 5.
- Storage: Postgres (metadata: users/projects/api_keys/audit_logs) + OpenSearch (log storage/search,
  requires a JVM) + Valkey (cache/rate-limit) — all bundled into a single Debian container.
- License: Apache-2.0. Single-author project, ~91 commits, 47 tagged releases, clean/disciplined
  codebase (feature-sliced, tested, no TODO debt), but no CONTRIBUTING/SECURITY/governance docs.
- Query model: already storage-agnostic internally — `logs/core` defines a `QueryNode`/`ConditionNode`/
  `LogicalNode` AST, and only `logs/core/query_builder.go` translates that AST into OpenSearch DSL.
  `logs/receiving/worker_service.go` and `logs/cleanup/background_service.go` are already shaped like
  an ingester-buffer and a retention job respectively. This means the storage engine rewrite below is a
  swap behind existing boundaries, not a rewrite of business logic.

## Licensing constraint (drives several choices below)

The goal is to stay Apache-2.0 end to end. Several natural reference projects are **not** license-compatible
to vendor code from, only to imitate architecturally:

| Project | License | What we can do |
|---|---|---|
| OpenObserve core | AGPL-3.0 (+ proprietary Enterprise tier) | Reimplement the *pattern* (hot/cold Parquet tiering, metadata store, coordination) in our own Go code. Never vendor OpenObserve source. |
| Grafana, Loki, Tempo | AGPL-3.0 (relicensed April 2021) | Parsing their **JSON dashboard format** is fine (formats aren't code). Never import Loki's LogQL engine or Tempo's query engine. |
| Prometheus core (incl. `promql` package) | Apache-2.0 | Safe to import directly — this is the standard pattern Thanos/Cortex/Mimir use for PromQL support. |
| Jaeger (CNCF) | Apache-2.0 | Safe to target for trace-query API compatibility instead of Tempo. |
| OpenTelemetry Collector/SDKs (`go.opentelemetry.io/*`) | Apache-2.0 | Safe to import directly for OTLP receivers and wire types. |
| DuckDB / `duckdb/duckdb-go` | MIT | Safe to import (CGO). |
| `parquet-go/parquet-go` | Apache-2.0 | Safe to import. |
| `gocloud.dev/blob` | Apache-2.0 | Safe to import. |
| NATS (`nats-server`, `nats.go`) | Apache-2.0 | Safe to import and embed in-process (JetStream, clustering) — the Scale architecture section ships it inside the one image. |

Re-verify licenses during Phase 0/1's dependency audit rather than trusting this table blindly if time has passed.

## Storage architecture

Replaces OpenSearch (and the JVM) entirely. Modeled on OpenObserve's tiering, implemented in Go:

- **Hot tier**: in-memory buffer (memtable) → flushed to local-disk Parquet segment files on size/time
  threshold. Immediately queryable.
- **Cold tier**: background uploader pushes local Parquet segments to object storage (S3/GCS/Azure/MinIO)
  via `gocloud.dev/blob`, one abstraction over all providers.
- **Compactor**: merges small Parquet files into larger ones, enforces retention (deletes expired
  segments + their metadata rows), replaces/extends `logs/cleanup/background_service.go`.
- **Metadata / file-list index**: stays in **Postgres** (no change from today) — add a `log_segments`
  (generalized: `stream_segments`) table: project_id, stream/signal type, time range, path, row count,
  size, local/uploaded state.
- Designed as a **generic multi-signal stream store from day one** — "stream" is not logs-hardcoded,
  so traces and metrics reuse the identical ingester → segment → compactor → query pipeline later,
  rather than needing their own storage engine.
- Single-node mode: everything above runs in-process in the one Go binary (mirrors OpenObserve's
  single-node mode). An HA/cluster mode (separate ingester/compactor/querier processes coordinated via
  NATS) was planned as a later, optional evolution. **Status (2026-09-30):** adopted in the ["Scale
  architecture"](#scale-architecture-1-pb--5-tbday) section below — NATS JetStream ships inside the
  image (embedded in `all`, a `broker` role in HA) as the durable ingest buffer, with leveled
  segments (L0 → L1 → L2) and date-prefixed object keys
  (`<stream>/<project>/<yyyy>/<mm>/<dd>/<level>/<id>.parquet`) for new uploads. Planned, Phase 14.
- **Compactor and uploader are both wired into background workers** (`*_cleanup.*BackgroundService`,
  one per stream): compaction runs every 5 minutes, unconditionally for every project; upload runs
  immediately after it in the same pass, deliberately never before - by the time upload considers a
  segment, anything mergeable this pass has already been merged away, so object storage only ever
  receives full-sized (~128MB target) segments rather than one small PUT per hot-tier flush interval.
  Upload is a no-op when `OBJECT_STORAGE_URL` is unset (local-disk-only mode, the default). Verified
  end-to-end against MinIO (an S3-compatible target, see `deploy/docker/docker-compose.yml.example`'s commented
  `minio`/`minio-init` services) - real segments uploaded, downloadable back via the querier's cold-tier
  read path (`WithObjectStore`) when a queried segment is no longer on local disk.

## Query engine

- **Default: DuckDB**, embedded via CGO (`duckdb/duckdb-go`, MIT, maintained by the DuckDB team).
  In-process, `database/sql`-compatible, statically links Parquet + `httpfs` (S3/GCS/Azure) extensions.
  Chosen over hand-rolled `parquet-go` filtering because DuckDB gives real SQL/aggregation (needed for
  dashboards and metrics) and handles substring search + heterogeneous per-project fields without
  bespoke bloom-filter/schema-merging engineering.
- Query execution sits behind a small **`QueryEngine` interface** that consumes the existing `QueryNode`
  AST and returns rows — so the engine is swappable without touching callers.
- **Apache DataFusion stays an explicit, open future option**, not abandoned:
  - Short/medium term: watch `datafusion-contrib/datafusion-go` mature (as of this writing: days old,
    12 stars, requires Go 1.26 + a C/Rust toolchain, runtime-downloads prebuilt libs from GitHub by
    default — needs pinning/vendoring for a self-hosted product, S3/object-storage query support
    undocumented). Swap in behind `QueryEngine` once it's production-ready.
  - Long term / at scale: if an HA/cluster mode is built, a DataFusion **Rust sidecar** (official
    `datafusion` + `object_store` crates, gRPC/Arrow Flight SQL to the Go backend) is the natural
    large-scale `QueryEngine` implementation — this is literally OpenObserve's own architecture. Not
    needed for the initial single-node default; adds a second toolchain/process, so only worth it once
    genuinely needed. **Status (2026-09-30): rejected for the scale plan** — the distributed query
    engine is Go-native scatter/gather over DuckDB executors (["Scale
    architecture"](#scale-architecture-1-pb--5-tbday), distributed query); a Rust sidecar would add
    the second toolchain and process this project avoids.
- **Build impact**: DuckDB's CGO requirement breaks the current Dockerfile's `CGO_ENABLED=0`
  buildx cross-compile trick (`--platform=$BUILDPLATFORM` + `GOOS=$TARGETOS GOARCH=$TARGETARCH`).
  Plan to either use a C cross-toolchain (e.g. `zig cc`) or build each target arch natively (drop
  `$BUILDPLATFORM`, separate amd64/arm64 CI jobs).

### Phase 2a spike results (validated)

Ran `duckdb/duckdb-go` (v2, via `github.com/marcboeker/go-duckdb/v2`) against 2M synthetic log rows
across 20 Parquet segment files, querying cold via `read_parquet()` with query shapes mirroring the
existing test suite (time range + level filter, message substring/"contains", client_ip equals, custom
JSON field exists, combined AND, paginated fetch). All queries returned in **5–45ms**, including the
substring "contains" case that was the main open risk of the (rejected) pure-Go plan — no custom
indexing needed. **DuckDB is confirmed as the query engine.**

Build finding: `duckdb-go-bindings` ships **prebuilt** platform libraries (`linux-arm64`, `linux-amd64`,
`darwin-amd64/arm64`, `windows-amd64`) rather than compiling DuckDB's C++ from source — so the Dockerfile
just needs each target arch built *natively* (buildx emulation or native per-arch CI runners), not a C
cross-toolchain. Still need to actually update the Dockerfile and drop the `$BUILDPLATFORM` trick in
Phase 2h to confirm end-to-end.

Note: the spike's data-generation step (row-by-row parameterized `INSERT` in a loop) is *not* an
ingestion benchmark — it's a known-slow path used only to get data on disk to query against. Real
ingest throughput (via DuckDB's Appender API or a direct Parquet writer during the memtable flush)
still needs its own benchmark in Phase 7.

### Known limitation: sub-microsecond query-time precision (found during Phase 2g)

The `timestamp` column in Parquet segments is `TIMESTAMP_NS` (true nanosecond precision — see
`LogSchema`'s comment for why plain `TIMESTAMP` silently truncates it). The `stream_segments` catalog's
`min_time`/`max_time` columns, however, are Postgres `TIMESTAMPTZ`, which is microsecond precision —
Postgres has no built-in nanosecond timestamp type. In the overwhelmingly common case this doesn't
matter: the catalog is only used to prune which segment *files* are worth opening, and the real
row-level filtering happens against the true-nanosecond Parquet data.

It does surface at the extreme: if multiple log rows land within the *same microsecond* (e.g. synthetic
test data spaced 2ns apart) and a query's time window is itself sub-microsecond, the segment-level catalog
can't distinguish those rows' *segments* from each other precisely enough, and results can come back wrong
rather than just imprecisely-pruned. Confirmed via the pre-existing test suite — four tests exercising this
exact scenario (`Test_ExecuteQueryForProject_WithMultipleLogsAt2NanosecondSteps_PreservesNanosecondPrecision`,
`Test_ExecuteQueryForProject_WithNanosecondPrecision_MaintainsProperDESCOrdering`, and two nanosecond-adjacent
quota tests — `Test_EnforceProjectQuotas_WhenLogsCreatedWithNanosecondPrecision_KeepsNewestLogs` and
`Test_EnforceProjectQuotas_WhenLogsCreatedWithinSameNanosecond_CannotDeleteLogs`) fail on this basis and are
left failing rather than papered over. No realistic logging workload queries at sub-microsecond time-range
granularity, so this is being accepted as a known, narrow limitation rather than fixed now. A real fix would
mean storing `min_time`/`max_time` as BIGINT nanoseconds-since-epoch instead of `TIMESTAMPTZ` — a real
schema/repository change, not a quick patch — worth doing only if it ever turns out to matter in practice.

### Object-storage integrity audit (a Phase 7 limitation, closed 2026-10-06)

`logs_uploader`/`spans_uploader`/`metrics_uploader.uploadOne` verifies a segment actually landed
(a follow-up `Exists` check) before marking it durably uploaded — this closes the "ghost upload"
window *at upload time* (a provider reporting success for an object that isn't actually
retrievable). It does not protect against an object being silently deleted or corrupted in the
bucket sometime *after* a successful, verified upload — there is no background job that
periodically re-verifies already-uploaded segments still exist in object storage.
`ReconcileSegments`'s own `Exists` check only ever runs for segments whose *local* copy is
already missing (the local-disk-cache-miss path), not as a continuous audit of every uploaded
segment regardless of local state. It only checks: it no longer downloads the local copy back -
queries read through the queriers' cache and compaction downloads what it merges
(`segments.EnsureLocalFiles`), so a restore only filled a fresh worker's disk with large cold
files it never read, again on every pass (each cut off by the 30 s per-segment timeout). In the common case this doesn't matter — object storage
providers (S3, GCS, MinIO) are themselves durable once a write is acknowledged, and this gap only
matters if something *outside* Obzly's control mutates the bucket directly. Accepted as a known,
narrow limitation rather than built now; a real fix would mean a periodic background job scanning
`ListUploaded` and calling `Exists` on each regardless of local-disk state, which is a real,
scheduled workload (cost against every uploaded segment, not just cache misses) worth adding only
if it turns out to matter in practice. **Closed (2026-10-06, Phase 14 item 10c):** a weekly
leader-locked audit samples `INTEGRITY_AUDIT_PERCENT` of uploaded segments and checks each
object's existence, size and Parquet row count against the catalog (no content checksum: the
catalog doesn't record one), with metrics and alerts; `obzly reconcile` repairs what it finds.
See "Deployment and DR".

## Logs search UI (OpenObserve-style redesign)

The original logs page was builder-first: every query, even a plain text search, meant clicking through
a nested condition-group UI. Redesigned around a free-text search bar as the default, matching OpenObserve/
Splunk-style log tools, with a one-click toggle to a real SQL mode for power users — "best of both", not a
replacement of the structured builder, which stays available as an "Advanced filters" fallback.

- **Free-text search bar, parsed client-side, zero backend change.** `searchQueryParser.ts` is a small
  hand-written recursive-descent parser (`field:value`, `field=value`/`!=`/`>`/`>=`/`<`/`<=`, quoted
  phrases, `AND`/`OR`/`NOT`/`-` negation, parentheses, implicit AND between terms, a bare word searching
  `message`) that turns the search bar text into the exact same `QueryNode` tree the backend's structured
  query API (`POST /logs/query/execute`) already executes — the existing `QueryBuilderComponent` needed no
  changes at all, it's just a second, independent way to build the same tree. Verified directly (compiled
  and run standalone against a dozen real query strings, precedence and negation included) before wiring
  it into the page, the same "verify before implementing" discipline used for every backend query change
  in this project.
- **SQL mode: a real, user-written SQL SELECT statement, not a second free-text dialect.** The user
  explicitly asked for full SQL power (aggregates, projections, arbitrary `WHERE`/`GROUP BY`/`ORDER BY`),
  not just a WHERE-clause fragment — so `logs_querier.ExecuteSQLQuery` runs the statement through a real
  DuckDB engine (DuckDB parses the SQL; nothing here hand-rolls a SQL grammar) against a `logs` TEMP TABLE
  the server materializes per request, scoped to the caller's own project and time range. Security model
  (each layer verified directly against a real DuckDB connection before relying on it, not assumed):
  1. `segments.ValidateSingleStatementSelect` — a quote-aware scanner rejects anything but exactly one
     SELECT statement. This mattered for a real reason, not a theoretical one: the DuckDB Go driver
     happily executes multiple `;`-separated statements in one `Query`/`Exec` call — confirmed directly
     that `"SELECT 1; DROP TABLE logs"` really does drop the table — so single-statement enforcement is
     load-bearing, not defense-in-depth. A keyword blocklist (`PRAGMA`/`ATTACH`/`COPY`/`CREATE`/`INSERT`/
     etc.) is scanned on a copy with quoted string content redacted first — an earlier draft of this
     scanned the raw statement and would have rejected a totally ordinary query like
     `WHERE message = 'delete the cache'` for containing the word "delete" inside quoted data; caught in
     review before shipping. The scanner knows only `'...'` and `"..."`, so the string forms DuckDB ends
     elsewhere (`E'...'` with backslash escapes, `$$...$$`/`$tag$...$tag$`), any `$` outside a string, an
     unterminated quote and comments are refused. DuckDB's own parser (`json_serialize_sql`) then has to
     see exactly one SELECT.
  2. The `logs` table is materialized from the project's own segments — the user's SQL only ever
     references it by name, never a file path or another project's data.
  3. `SET enable_external_access=false` on the (fresh, single-use, thrown-away-after) connection before
     running the user's statement — confirmed directly to block `read_parquet`/`read_csv` of an arbitrary
     path, `ATTACH`, and `COPY TO`, as a real redundant layer beneath the keyword blocklist, not merely
     assumed to work. Then `SET lock_configuration=true`, so no statement can raise `memory_limit` or the
     spill cap.
  4. A query timeout and a row cap, both defensive rather than the primary control. The table is
     materialized under its own timeout, within `QUERY_MAX_SCAN_GB`, and only for a range with a start
     (`timeRange.from` is required in SQL mode).

  Response shape is deliberately generic (`{columns, rows}`), not the fixed log-row DTO — a user's own
  SELECT can project anything.
- **Histogram on the dashboards panel-query API, facets on their own sampled endpoints.** The
  log-volume histogram above the results is a transient, unsaved `dashboards_models.Panel` (`bar`
  type, `source: "logs"`, the search as the panel's query) sent to the same `POST
  /projects/dashboards/{projectId}/query` Phase 4 already built, so it gets the partition cache
  (4g). The sidebar's field facets and the search bar's value suggestions use `POST
  /logs/query/field-values/{projectId}`, and the `pattern_hash` facet `/logs/query/patterns`, both
  with `sampleRows: 100000`: the range is read window by window from its newest end, as a search
  page is, and the facet stops after the window that brings the matching logs it counted to
  100,000 (`logs_querier.topGroups`). A 72-hour facet over a busy project reads its newest
  minutes instead of every file in the range; the response's `sampledFrom` says where the sample
  starts and the facet shows it. Without `sampleRows` (the MCP tools) the whole range is counted.
  Groups are counted exactly within a window; across windows each keeps its top 1,000 (10x the
  limit), so only the long tail can be undercounted. The histogram is its own `LogHistogramComponent` (bars stacked by `level`,
  i.e. the panel query with `groupField: "level"` + a `bucketMs`), with an Auto/fixed bucket-width
  picker, a chart-only level filter, and a cursor line at the hovered/selected result row's time.
  Both follow the query and range of the *last run*, not the live search-bar text, so they always
  describe the same logs as the results table. The histogram starts with the search and its
  buckets sum to the result count the summary shows: the search itself doesn't count (it stops
  reading once its page is full - see *Distributed query*, 4f), and its rows stream in window by
  window. Run always starts a new search (replacing a running one), Stop abandons it keeping the
  rows found, and Reset clears the search text, Advanced builder, hidden levels, histogram zoom and
  sort order over the same time range.
- **Severity facet.** Pinned at the top of the sidebar: a checkbox per level with its count. Unchecked
  levels are ANDed onto the search as `level not_in [...]` (`withHiddenLevels`) without touching the
  search-bar text. The counts are the histogram's buckets summed per level - no query of their
  own. The histogram reads the search *without* that filter and leaves hidden levels out of its
  bars and total itself, so a hidden level still shows what it would bring back and toggling one
  re-reads the same cached histogram.
- **Log context ("Show logs around").** A row's hover button or the detail drawer opens the logs
  just before and after that log from the same source, whatever search found it - the cause of an
  error is usually filtered out of the search. `POST /logs/query/context/{projectId}` takes the
  anchor's id and timestamp; each side is an ordinary cursor page from the anchor (descending
  before, ascending after) through the newest-first window walk, so it reads only the windows
  nearest the anchor, and `(timestamp, id)` cursors split logs sharing its timestamp exactly. The
  default scope is the anchor's workload - every replica, not one pod (`logs_core.AutoContextScope`):
  the `k8s_cluster_name`, `k8s_namespace_name`, workload (`k8s_deployment_name`, or the
  statefulset, daemonset, job/cronjob), `k8s_container_name` and `service_name` it has, plus
  `LOG_CONTEXT_DEFAULT_FIELDS`, like OpenObserve's search-around fields and Loki's stream labels. A
  pod with no workload falls back to `k8s_pod_name`, a log from outside Kubernetes to `host_name`.
  `k8s_deployment_name` is filtered from the JSON fields; promote it on projects that use context
  heavily. A Site older than the route (404 through the proxy) gets an "upgrade the Site" message,
  in the panel and in the MCP tool. `LogContextPanelComponent` shows
  the scope as removable chips (remove one to widen, "+ field" to narrow, e.g. to the same
  `trace_id`, or all project logs) as Grafana's context query editor does, with a ±15 min / 1 h /
  6 h / 24 h window, 25-1000 rows a side (the Site's cap; the MCP tool keeps 100), the anchor pinned
  while scrolling, level colors, gap markers for pauses over a minute, "Load earlier / later" by
  cursor, and Find: a text highlighted in the loaded messages, stepped through with Enter /
  Shift+Enter from the match nearest the anchor. It opens as a side panel
  over the results and pops out to a large window; "Open as search" puts the scope into the
  search bar over the context window (undone with Zoom out). The `log_context` MCP tool is the
  same endpoint.
- **Deploy markers.** The histogram marks Deployment rollouts with a dashed line (toggle "K8s Rollouts"):
  `POST /logs/query/deploys/{projectId}` finds each ReplicaSet - a pod name minus its last
  generated part - whose pods first logged inside the range, not at its start, from the pods' first
  log (per-minute counts for whole minutes where segments have them, raw logs for the rest); its
  Deployment is the ReplicaSet minus its hash. The search narrows them when it filters only on the
  counts' dimensions.
- **Log anomalies.** A drawer from the histogram ("Anomalies") compares the last search's message patterns with
  the previous period, the same time yesterday or last week (`POST /logs/query/changes/{projectId}`,
  `logs_querier.ExecutePatternChanges`): counts per pattern from aggregations grouped by
  `pattern_hash` over each range (per-minute counts where segments have them), the baseline scaled
  to the current range's length; new (none in the baseline), spiking (at least 2x) and dropping (at
  most half) patterns, each with its template, newest log and level (read newest-first until every
  listed pattern is found); a row filters the search to its pattern.
- **Log to trace.** A log with a `trace_id` shows it (and `span_id`) in the detail drawer and the
  context panel, with "Open trace": the Traces tab opens that trace, read from an hour either side
  of the log.
- **Time range presets** stop at 72 hours (5, 15, 30 min; 1, 3, 6, 12, 24, 36, 48, 72 h). A longer
  range is a Custom Range: past that a search's histogram and window walk read days of files.
- **Query tile and results table (compact).** One toolbar row: Search/SQL, the search box, the
  time picker, a sort toggle, a Live button, Run, recent and saved searches (saved per browser and
  project, `savedQueriesStorage.ts`). A thin row under it shows the active filters as removable
  chips (the search's flat top-level AND clauses plus hidden levels), the last run's summary with
  its exact from/to, and Advanced filters. Every click-to-filter action (facet values, drawer
  fields, row hover `+`/`−`, chip removal, severity checkboxes) edits the search text and re-runs
  immediately; include and exclude toggle and replace each other (`field:v` <-> `-field:v`). A run
  parses the search text itself (the text-to-query sync is an effect, one render behind), and
  Load More pages through exactly the query the loaded rows came from. Rows show severity as a
  colored left stripe; default columns are Time, Level and Message (Service and any field are
  optional; Service hides itself when no loaded row has one), zebra striping, and hover `+`/`−` for "show
  only / hide logs like this" by message pattern - each row carries its `patternHash`, computed
  per returned row. Export downloads the loaded rows as CSV (visible columns) or JSON.
- **Message patterns (`pattern_hash`).** `logs_core.PatternRules` masks UUIDs, timestamps, IPs, hex
  ids and numbers (with an optional unit, so `12.5ms` and `3s` match) in a fixed order over a
  message's first 1,024 characters (`PatternMaxChars`: the start tells a message's kind apart, and a
  4 KB pretty-printed JSON message took ~0.35 ms against ~14 µs for a typical line), and the hash is
  the first 12 hex chars of the MD5 of the result. The ingester stores it once per log in a
  `pattern_hash` column (`rowFor`, every write path); rows of segments written before that column
  read it as NULL, and queries use `COALESCE(pattern_hash, <computed>)`
  (`logs_core.StoredPatternHashSQL`, a `systemFieldColumns` entry, so `pattern_hash:<h>` filters and
  group-bys work on old and new files alike, and DuckDB computes the fallback only for NULL rows). The
  same RE2 rule list is compiled into nested `regexp_replace()` calls for that fallback and into Go
  regexps for ingest and Live Tail's matcher; a parity test requires identical templates and hashes.
  `POST /logs/query/patterns/{projectId}` groups by the hash and derives each template from the
  group's sample message, for the sidebar's `pattern_hash` facet (sampled, `sampleRows`).
- **Scope**: SQL mode's own result table is generic (dynamic columns), so the histogram and field sidebar
  — which assume "this is a filter over log rows" — only apply in Search mode, not SQL mode; a SQL query's
  own `GROUP BY`/aggregates already are the power-user version of both. The search-bar text and the
  Advanced builder's tree are two independent editing surfaces sharing one `QueryNode` output state, not
  bidirectionally synced — editing in Advanced does not attempt to re-stringify the tree back into search
  syntax (a real limitation, not an oversight: round-tripping an arbitrary nested tree back into the text
  grammar losslessly is a meaningfully bigger parser-generator problem than parsing text into a tree).

### Live Tail (push-based streaming, with an automatic polling fallback)

"Best of both": stream matching logs to the browser in real time when the deployment's reverse proxy
allows it, and fall back to the original 2-second poll when it doesn't — the client-side toggle only ever
shows one "LIVE" control either way, never asking the user to know or care which transport is active
(a small "(polling)" suffix on the badge is the only visible difference, with a tooltip explaining why).

- **Only enabled once a filter/query narrows things down** — tailing an entire project's unfiltered log
  firehose was explicitly out of scope; `canEnableLiveTail` in `QueryComponent.tsx` gates the toggle.
- **Push transport**: with `INGEST_BUFFER=jetstream`, core NATS instead (step 1d, see "Scale
  architecture"). Otherwise `logs_receiving.Service.SubmitLogs` fire-and-forgets every accepted log onto a
  per-project Valkey Pub/Sub channel (`logs_livetail.Publisher` — Valkey is already a hard dependency,
  for rate limiting, so this adds no new infrastructure). `POST /api/v1/logs/tail/{projectId}` subscribes
  and streams newline-delimited JSON frames for as long as the client holds the connection open,
  evaluating each viewer's filter with `logs_core.Matches` — the in-memory counterpart of
  `logs_querier`'s `whereBuilder`, which instead compiles the same `QueryNode` AST to a DuckDB `WHERE`
  clause for stored search. The two are deliberately kept behaviorally aligned (mirrored operator-by-
  operator, including reusing the same promoted-field resolution the Parquet write path uses) and cross-
  checked directly against real DuckDB output in `querier/matcher_parity_test.go`, so a filter matches
  the same logs whether it's a live tail or a search a few seconds later.
- **Why POST + `fetch`, not a browser `EventSource`**: `EventSource` can't set custom request headers,
  which would have meant a separate short-lived-token auth scheme just to work around it. A POST with a
  normal `Authorization` header and a streamed response body (`pushLiveTail.ts`, reading
  `response.body.getReader()` and splitting on newlines) reuses the exact same auth every other request
  already uses.
- **Automatic fallback is a functional probe, not a proxy-config check** — the browser has no way to
  inspect what's in front of it. The very first frame the stream ever sends is `{"type":"ready"}`,
  written by the backend only once its Valkey `SUBSCRIBE` is actually confirmed active (not a moment
  earlier — Pub/Sub never replays a message published before a subscriber was live, so sending "ready"
  any earlier would be a real race, not just an optimization; a synthetic race in
  `livetail/tests/livetail_test.go` was what first caught this before the fix). If that frame doesn't
  arrive within a few seconds, `pushLiveTail.ts` gives up and `QueryComponent.tsx` starts the original
  2-second poll instead — the same thing that happens if a stream that *was* working drops mid-session
  and doesn't recover. A reverse proxy with response buffering enabled holds the entire streamed response
  until the connection closes, so this timeout is what "detects" that case in practice.
- **Deployment requirement**: the tail endpoint sends `X-Accel-Buffering: no` on its own response, which
  nginx-family proxies (including this host's Nginx Proxy Manager) honor per-response even under a global
  `proxy_buffering on` — so most self-hosted setups need no manual config change at all. A proxy that
  either isn't nginx-based or strips that header will fall back to polling automatically (see above), but
  for real-time streaming to work there, the operator needs to disable response buffering for the
  `/api/v1/logs/tail/*` path explicitly (nginx: `proxy_buffering off;` on that `location`; equivalent
  settings exist for other proxies/load balancers) and ensure it doesn't apply a short idle-read timeout
  to that route (the stream sends a heartbeat frame every 15s specifically to avoid tripping one).

### Query editor (search bar and SQL)

Both modes use CodeMirror 6 (`features/query/ui/QueryEditorComponent.tsx`), about 130 KB gzipped.
Search mode has a stream tokenizer for highlighting. Completion covers fields (from
`/logs/query/fields`), `level` values, and top values of any field over the current range (the
field values endpoint over the newest 100,000 matching logs, cached 30 s per project). Hover shows field types,
operators and keywords. An inline hint explains the operator while a value is typed. Parser
errors are underlined at their position (`SearchQuerySyntaxError.from/to`), and unknown fields
get a warning. SQL mode uses `@codemirror/lang-sql` with the `logs` columns, DuckDB helper
functions and custom-field `json_extract_string` snippets. Enter runs; Shift+Enter adds a line in
SQL.

## Ingestion

- **Native OTLP** receivers for logs, metrics, and traces — HTTP (`/v1/logs`, `/v1/traces`, `/v1/metrics`
  per the OTLP/HTTP spec) and gRPC (port 4317). Use official CNCF packages: `go.opentelemetry.io/proto/otlp`
  for wire types, and ideally embed `go.opentelemetry.io/collector/receiver/otlpreceiver` +
  `pdata`/`pcommon`/`plog`/`ptrace`/`pmetric` rather than hand-parsing protobuf.
- **gRPC auth/multi-tenancy**: OTLP gRPC has no URL to carry a project ID, so project/API-key selection
  travels as gRPC metadata (an API-key header) — define this convention before building exporters/docs
  against it.
- **Keep the existing custom REST log API and per-language SDKs working.** OTLP becomes the standard/
  primary path; the simple REST API stays for lightweight use cases (scripts, `curl`). No breaking change
  for existing users. Longer term, OTel's own per-language SDKs reduce the pressure to hand-maintain as
  many bespoke obzly client libraries.
- Logs gain `trace_id`/`span_id`, resource attributes, scope name/version, and a severity-number↔
  `LogLevel` mapping — the basis for log↔trace correlation.
- Metrics ingestion also plans for **Prometheus remote-write** alongside OTLP metrics, since that's the
  de facto agent-side standard (Prometheus, Grafana Agent, Telegraf).

### Phase 3 implementation notes (logs OTLP receiver, built)

- Built directly against `go.opentelemetry.io/proto/otlp` (v1.11.0 — was pinned to v1.10.0 while this
  project's toolchain was Go 1.24, since v1.11.0 needs Go ≥1.25; bumped once the toolchain moved to
  Go 1.27.1 — see "Runtime/toolchain versions" below) rather than embedding the OTel Collector's
  `otlpreceiver`/`pdata` packages this doc originally floated: `otlpreceiver` pulls in the whole Collector
  component framework (config providers, extension lifecycle, its own receiver.Settings/host interfaces)
  for what turns out to be a thin translation layer — an OTLP `LogRecord`/`ResourceLogs` is a plain
  generated protobuf struct, trivial to walk by hand (`internal/features/otlp/logs/converter.go`). Revisit
  only if metrics/traces (Phase 5/6) end up needing `pdata`'s aggregation helpers for real.
- `internal/features/otlp/logs` converts OTLP straight into `logs_receiving.SubmitLogsRequestDTO` and
  calls the *existing* `LogReceivingService.SubmitLogs` — so OTLP ingestion gets the same validation, rate
  limiting, and quota enforcement as the custom REST API for free, and the REST API/SDKs are untouched.
- **gRPC auth convention** (now defined, per this doc's earlier TODO): `x-api-key` gRPC metadata key,
  the lowercased analogue of the REST/OTLP-HTTP `X-API-Key` header. An API key is *mandatory* for OTLP
  regardless of a project's own `IsApiKeyRequired` setting — unlike the REST API, OTLP has no URL path to
  carry a project ID, so the key is the *only* way to resolve which project a batch belongs to
  (`ApiKeyService.ResolveProjectFromApiKey`, a new project-agnostic sibling of the existing
  per-project `ValidateApiKey`).
- OTLP/HTTP served at server root (`POST /v1/logs`, no `/api/v1` prefix — the path every OTLP
  exporter/collector already defaults to), supporting both binary protobuf (`application/x-protobuf`) and
  JSON (`application/json`, via `protojson`) bodies. OTLP/gRPC served on its own listener/port
  (`OTLP_GRPC_PORT`, default `4317`, the OTel-conventional collector gRPC port) since gRPC needs its own
  `net.Listener`/HTTP2 stack and can't share Gin's `*http.Server`.
- Severity mapping: `SeverityText` wins when present (it flows through the existing `normalizeLogLevel`
  alias table unchanged — "WARNING", "CRIT", etc. already map correctly); otherwise `SeverityNumber`'s
  numeric range maps onto `LogLevel` (TRACE 1-4 and DEBUG 5-8 both collapse to `DEBUG`, since there's no
  separate TRACE level).
- `trace_id`/`span_id` are hex-encoded (32/16 hex chars, the canonical W3C text form) and only set when
  non-empty. All attributes land in `fields` under their normalized name (see "Attribute naming"):
  `resource.k8s.pod.name` -> `k8s_pod_name`, scope name/version as `scope_name`/`scope_version`; on a
  name collision the log record's own attribute wins over a scope one over a resource one.
- Timestamp: `TimeUnixNano` (falling back to `ObservedTimeUnixNano`) is formatted as an RFC3339Nano
  *string*, not passed through as a raw int64/`time.Time` — `internal/util/time.ParseTimestamp` (reused
  unchanged from the REST path) only understands unix seconds/milliseconds and RFC3339(Nano) strings; an
  unconverted raw nanosecond value would be misread as milliseconds and land ~1000x in the future.

## Signal build order

Logs → Traces → Metrics — all three built now. Traces reused the exact storage/query pipeline proven
on logs (just another stream type) and pair with logs via `trace_id`. Metrics were architecturally the
most different (PromQL's time-series query semantics: rate/increase/aggregation over windows, not a
hand-rolled aggregation RPC) and benefited from the storage pipeline being proven twice already before
tackling that — see Phase 6's implementation notes below.

### Phase 5 implementation notes (traces, built)

- **Confirmed: the storage engine really is signal-agnostic.** `internal/features/spans` is a
  near-exact structural mirror of `internal/features/logs` (core/ingester/querier/compactor/
  parquetstore/cleanup/testsupport) built on the *same* `internal/storage/segments` package with zero
  changes to it — spans are just `stream: "spans"` with its own `segments.Schema`. This was the
  Phase 2 storage design's central bet and it held.
- **Span schema**: `trace_id`/`span_id`/`parent_span_id` (hex text, W3C form — same convention Phase 3
  set for logs), `name`/`service_name`/`kind`, `start_time`/`end_time` (`TIMESTAMP_NS`, not a single
  timestamp+duration column — needed for DuckDB-side duration math), `status_code`/`status_message`,
  and `attributes`/`resource_attributes`/`events` as JSON-encoded `VARCHAR` (display-only in v1 — no
  query/group-by reaches into them the way logs' custom `fields` column does, so unlike
  `logs_ingester.rowFor`, no stringify-every-value normalization step was needed).
- **Trace search** (`spans_querier.ListTraces`) required a new DuckDB query shape beyond what logs
  needed: `GROUP BY trace_id` with `min_by`/`FILTER`/`array_agg(DISTINCT ...)` for the summary
  columns, and `bool_or(...)` inside `HAVING` so a service/operation filter matches "this trace has
  *any* span matching" while the summary (span count, duration, error count) still reflects *every*
  span of a matching trace — not just the ones that happened to match the filter. All verified
  directly against DuckDB before writing the production query (same practice as Phase 2's
  `union_by_name`/`TIMESTAMP_NS` spikes), including `approx_quantile` for the latency-percentile
  dashboard metric.
- **Real bug caught by the test suite on the first pass**: a SQL-parameter-ordering bug in the spans
  aggregator, identical in shape to the one Phase 4's logs aggregator hit — a `SELECT`-clause
  placeholder (the time-bucket width) must be bound *before* the `FROM read_parquet([?], ...)`
  placeholder even though FROM is evaluated first logically, because parameters bind in the order
  their `?`s appear in the SQL text, not query-plan order. Worth calling out twice now that it's
  happened twice: this is the one sharp edge of hand-building parameterized SQL strings this way.
- **OTLP trace ingestion** (`internal/features/otlp/traces`) mirrors `otlp/logs` closely (HTTP
  `POST /v1/traces`, gRPC `TraceServiceServer`, `x-api-key` auth) with one structural change: the
  shared OTLP gRPC server (`internal/features/otlp.NewGRPCServer`) now has both signals'
  `RegisterGRPCServer(server)` called onto it rather than each signal owning its own `grpc.Server` —
  gRPC servers happily multiplex multiple registered services over one port, so logs and traces (and
  metrics later) share `OTLP_GRPC_PORT` instead of each wanting their own. Unlike logs, traces has no
  pre-existing custom REST ingestion pipeline to delegate validation/rate-limiting to (there's no
  pre-Phase-5 trace API at all) — OTLP calls `spans_core.SpanRepository.StoreSpansBatch` directly,
  which is fine: an OTLP export arrives already batched from the SDK/collector, unlike individual REST
  log submissions. (Logs had an async Valkey coalescing queue until Phase 14 step 1e; now every signal
  writes directly, or through JetStream.)
- **Jaeger-compatible query API** (`internal/features/traces/jaeger`) implements the well-established,
  long-stable Jaeger HTTP query API (`/api/services`, `/api/services/{service}/operations`,
  `/api/traces`, `/api/traces/{traceID}`) against `spans_core.SpanRepository` — built from Jaeger's own
  publicly documented JSON schema (Apache-2.0), never from Loki/Tempo-adjacent source. Mounted
  per-project (`/api/v1/jaeger/:projectId/api/...`, since Jaeger's API itself has no multi-tenancy
  concept) with its own API-key auth (`X-API-Key` header or `?api_key=` query param, for datasource
  configs that can only attach query params) — this is what a Grafana Jaeger datasource points at.
  Tag-based filtering (Jaeger's `tags` query param) is intentionally not implemented — our own trace
  search only filters by service/operation/duration/errors, a deliberately smaller v1 surface — so a
  `tags` param is ignored rather than rejected, and a datasource configured with one still gets
  results, just unfiltered by that specific tag.
- **Auto-dashboard rule set gains span panels, scoped down from an actual service map.**
  `dashboards_models.Panel` gained a `Source` field (`"logs"` | `"spans"`, defaulting to `"logs"` for
  every panel saved before this phase) plus spans-specific fields (`ServiceName`/`OperationName`/
  `RootSpansOnly`/`Metric`); `dashboards_services.ExecutePanelQuery` dispatches on it to either
  `logs_core` or `spans_core`'s own `ExecuteAggregation`, normalizing both into one shared
  `dashboards_dto.PanelQueryResponseDTO` shape so the frontend chart renderer doesn't need to know
  which signal answered. `GenerateOverview` now appends three span panels (requests/p95 latency/errors
  by service) *only if* the project actually has span data (a cheap `ListServices` check) — what
  ARCHITECTURE.md's Dashboards section called "service-map/latency/error-rate panels" is implemented
  here as three per-service breakdown panels (the RED-method questions a service map answers) rather
  than an actual node/edge graph visualization, which would be a new graph-rendering UI component; a
  real service map is a reasonable future addition, not built here.
- **Frontend**: a new "Traces" tab (search filters, results table, a click-through waterfall in a
  `Drawer`) built directly against the same REST query API (`entity/traces`), not the Jaeger API —
  the Jaeger endpoints exist purely for external tool compatibility. The waterfall computes span depth
  from `parent_span_id` chains client-side (no separate "tree" endpoint) and renders proportionally
  sized/positioned bars colored per-service (a stable hash → fixed palette, so a service keeps its
  color between the search table's tags and the waterfall).

### Phase 6 implementation notes (metrics, built)

- **Third stream type, same storage engine, one real new column.** `internal/features/metrics` mirrors
  `internal/features/spans` structurally (core/ingester/querier/compactor/parquetstore/cleanup/
  testsupport) on the same `internal/storage/segments` package again — `stream: "metrics"`, its own
  schema (`metric_name`, `labels` as JSON-encoded `VARCHAR`, `value DOUBLE`, `timestamp TIMESTAMP_NS`,
  `type`). Confirms the Phase 5 note a third time: the storage engine really is signal-agnostic.
- **The real new design question was the query surface, not storage.** Logs/spans both answer to one
  hand-rolled `ExecuteAggregation` RPC; metrics needed actual PromQL, because that's the entire point
  of Phase 6 (existing Grafana Prometheus dashboards should work unmodified against this). Solved by
  vendoring `github.com/prometheus/prometheus`'s own `promql` engine rather than reimplementing any of
  the language, and writing exactly one adapter (`internal/features/metrics/promql`,
  `metrics_promql.NewQueryable`) implementing `storage.Queryable`/`storage.Querier` — the standard
  shape every third-party PromQL backend (Thanos, Cortex, VictoriaMetrics) uses: `Select` fetches raw
  series for the query's matchers/time-range from `metrics_core.MetricRepository` and wraps each as a
  `storage.Series` via `storage.NewListSeries`; the engine handles every actual language feature
  (aggregations, `rate()`, binary ops, subqueries) against that. Verified end-to-end against real
  DuckDB-backed storage before trusting it (`sum by (service) (...)`, `rate(...[5m])`, label-values —
  all correct on the first real test run). `github.com/prometheus/prometheus` is pinned to `v0.309.1`
  and **still is** after the Go 1.27.1 toolchain bump (see "Runtime/toolchain versions" below) — the
  original reason (`v0.310.0`+ needed Go ≥1.25) no longer applies, but bumping past `v0.309.1` now
  hits a real, unrelated breaking change: the `chunks.Sample` interface our `floatSample` adapter
  (`internal/features/metrics/promql/queryable.go`) implements gained a new required `ST()` method
  somewhere before `v0.314.0`. Needs an actual code change (implement `ST()` correctly, whatever it's
  meant to represent for a plain float sample — for/before native-histogram support) and careful
  re-verification of the PromQL adapter, not just a version bump; tracked as follow-up work, not done
  as part of that toolchain bump.
- **Matcher evaluation happens in application code, not pushed into DuckDB SQL, deliberately.**
  `metrics_querier.SelectSeries` pushes only a metric-name *equality* matcher into the WHERE clause
  (the overwhelmingly common, highly selective case) and evaluates every other matcher — including
  every label matcher and any name regex — in Go against each candidate row's decoded label set. This
  trades query-time efficiency on very large cardinalities for a much smaller, easier-to-verify-correct
  surface: PromQL's matcher semantics (a missing label reads as `""`, regex matchers are fully
  anchored) are exactly Go's own string/regexp behavior once labels are in hand, instead of being
  re-derived as DuckDB JSON-path SQL fragments. Reasonable at the self-hosted scale this project
  targets; real cardinality limits are a Phase 7 hardening concern.
- **OTLP metrics ingestion** (`internal/features/otlp/metrics`) mirrors `otlp/traces` (HTTP
  `POST /v1/metrics`, gRPC `MetricsServiceServer` registered onto the same shared OTLP gRPC server,
  `x-api-key` auth). The real work was converting OTLP's richer type system down onto
  `metrics_core.MetricSample`'s "one float per series per instant" shape: Gauge/Sum become one sample
  per data point; Histogram decomposes into Prometheus's own classic `_sum`/`_count`/`_bucket` shape
  (cumulative `le`-labeled buckets, converted from OTel's per-bucket deltas, plus the mandatory
  `le="+Inf"` bucket) — the same convention the OpenTelemetry Collector's own Prometheus exporter uses,
  implemented from that public spec, not any one codebase. ExponentialHistogram/Summary are skipped in
  v1 (no lossless mapping onto the classic-bucket shape without a real native-histogram feature; Summary
  is itself deprecated in the OTel spec) — documented, not silently mis-converted.
  Sum and Histogram points with `DELTA` temporality become cumulative before storing
  (`otlp/metrics/delta.go`): each stored series (project + `segments.SeriesID` of name and sorted
  labels) keeps a running total in Valkey, one `INCRBYFLOAT` per point and one `EXPIRE` (24 h) per
  series, all pipelined per request. A series' points within a request are added oldest first, later
  requests in arrival order. A forgotten total restarts from zero, which PromQL reads as a counter
  reset. If Valkey fails, a request with delta points is refused with 503 / `UNAVAILABLE` and
  nothing is stored; NaN/infinite deltas are dropped as partial-success rejections. Cumulative-only
  requests never touch Valkey. Metric: `obzly_otlp_delta_samples_total{result}`.
- **Prometheus remote-write ingestion** (`internal/features/prometheus/remotewrite`) is the second
  metrics ingestion path, `POST /api/v1/write` — the same path Prometheus's own
  `--web.enable-remote-write-receiver` serves, so an existing `remote_write:` config block only needs
  its `url`/auth changed. Only the Remote Write 1.0 protobuf format (snappy-compressed `prompb.
  WriteRequest`, what Prometheus/Grafana Agent/the OTel Collector's own remote-write exporter all
  default to) is supported — Remote Write 2.0 is a different wire schema, not just newer metadata, and
  a request sent in that format is rejected with a clear error via the `X-Prometheus-Remote-Write-
  Version` header rather than mis-parsed. A series' `MetricType` comes from the request's own
  `Metadata` list where present, falling back to a name-suffix heuristic (`_total`/`_count`/`_sum`/
  `_bucket` → counter, else gauge) since many agents omit metadata entirely.
- **Prometheus-compatible HTTP query API** (`internal/features/metrics/promapi`) — `/api/v1/query`,
  `/api/v1/query_range`, `/api/v1/series`, `/api/v1/labels`, `/api/v1/label/:name/values`, mounted
  per-project at `/api/v1/metrics/:projectId/api/v1/...` (the prefix a Grafana Prometheus datasource's
  URL field is set to) with the same `X-API-Key`-or-`?api_key=` auth `traces_jaeger` established — this
  is the one root-mounted, non-Bearer-auth query surface metrics has, mirroring that package's own
  jaeger-vs-querying split. The JSON response shapes are hand-built from Prometheus's own publicly
  documented HTTP API spec rather than reusing `promql.Value`'s `MarshalJSON` methods — those are
  explicitly commented in the `promql` package itself as unused within Prometheus (the real server
  marshals through an internal jsoniter extension instead), so hand-rolling the documented wire shape
  directly is both simpler and more predictable. A separate, session-authenticated `metrics_querying`
  package (`GET /api/v1/metrics/query/names/:projectId`, Bearer auth) exists purely so the dashboard
  panel editor's frontend — which has no project API key — can suggest metric names while a user writes
  a PromQL query; the same `traces_jaeger`-vs-`traces_querying` split, one signal later.
- **Dashboards gain a genuinely different kind of panel.** Unlike logs/spans panels (structured
  `AggregationRequestDTO`s dashboards_services builds from separate GroupField/Metric/etc. fields), a
  metrics panel's query *is* PromQL text (`Panel.PromQuery`) — there's no intermediate structured
  request to build, since the query language itself already does everything ExecuteAggregation was
  reimplementing for the other two signals. `executeMetricPanelQuery` runs it as a range query over the
  panel's time window (defaulting to the last hour, since — unlike logs/spans' "unbounded" default — a
  PromQL range query needs concrete bounds to evaluate at all) and normalizes each resulting series into
  the same shared `PanelBucketDTO` shape, with a compact `"key=value, key2=value2"` legend built from
  the series' non-`__name__` labels. `GenerateOverview` gains a discovery pass over
  `ListMetricNames`, generating one time-series panel per metric name present (`rate()` for a counter-
  named metric, `avg()` for a gauge, `_bucket`-suffixed names skipped since a raw bucket count needs
  `histogram_quantile()` grouping to mean anything) — the same "scope down from full semantic-
  convention-aware auto-detection to a simpler, still-useful default" choice `spanOverviewPanels` made,
  capped at 6 panels so a project emitting hundreds of metric names doesn't produce an unusable wall.
- **Frontend scope, deliberately narrower than traces got.** No standalone "Metrics" explorer tab —
  ROADMAP.md's Phase 6 bullets only ever called for ingestion, storage, query-API compatibility, and
  dashboard panels, not a browsing UI, so none was built (unlike Phase 5's trace waterfall, which the
  roadmap did call for). `PanelEditModalComponent` gained a metrics branch: a PromQL textarea plus a
  clickable list of known metric names (fetched via the new `metrics_querying` endpoint) that insert
  into the query on click — a lightweight writing aid, not real autocomplete. The Kubernetes OTel
  Collector config generator (Phase 5.5) gained a `hostmetrics` receiver (node CPU/memory/disk/network)
  and a `metrics` pipeline alongside its existing logs/traces ones, since the same `otlp` receiver
  already accepted the protocol — no separate collector deployment needed for metrics.

### Phase 8 implementation notes (network topology, service map, correlation — built)

- **Network edges come from OBI metrics, not a dedicated stream.** An earlier iteration added a
  `flows` stream fed by Mermin (flows exported as OTel spans, routed by scope-name sniffing in
  `otlp/traces/service.go`). It was removed: Mermin's span attribute schema was undocumented
  upstream, and OBI (OpenTelemetry eBPF Instrumentation) exports standard OTLP metrics
  (`obi.network.flow.bytes` / `.packets`) that the existing metrics path already
  ingests with zero custom code. `servicemap/networkEdges.go` queries them by
  `k8s.src.owner.name` / `k8s.dst.owner.name`. The metrics path keeps names/keys as sent, so these are
  dotted; validated against real output from a live cluster (an earlier underscore-style guess
  matched nothing).
- **Real-data findings**: OBI emits each flow twice per node (workload owner and its fronting
  Service, ~a third of bytes), so `dropServiceAliases` prefers the workload-typed series; no
  cross-node double counting was observed. OBI exports ~once a minute, so `counterIncrease` reads
  3 extra minutes and scales the average rate to the requested window (never extrapolating upward)
  - a plain last-minus-first over Live mode's 60s window made network edges flicker.
- **Service Map computed on read, not persisted** — `servicemap` merges `spans_querier.
  ServiceCallEdges` (a new DuckDB self-join over the spans segment CTE, joining `parent_span_id =
  span_id` within the same `trace_id`) with the OBI network-metric edges, fulfilling the service-map
  deferral `dashboards/services/overview.go` had explicitly flagged as future work rather than
  contradicting any prior direction. Clamped to a 24h max lookback and a 50-node ranked cap
  (collapsing the remainder into a synthetic "other" node) — both real safeguards against an
  unbounded aggregation on every page load, not just documented intentions.
- **Correlation engine is dimensional + temporal only, explicitly not semantic/ML-based** — stated
  plainly in the `correlation` package's doc comments and in the Incidents UI itself, to avoid
  implying root-cause-analysis capability that doesn't exist. A leader-locked worker groups
  same-service, overlapping-time-window signals (logs/spans) that contain a genuine anomaly
  (ERROR log, non-OK span status, or a shared trace_id) into persisted `incident_sessions`, using the
  Service Map's graph to prune which cross-service pairs are even worth comparing — without that
  pruning, cross-service correlation would be an O(services²) comparison as service count grows. No
  metric-threshold trigger: checked for an existing alerting/threshold concept in `metrics/` to reuse
  and found none, so the trigger set was narrowed rather than building one net-new for this MVP.
- **Real test-isolation bug found and fixed while proving the worker's bounded-scan-cost safeguard**
  — the same class of issue as Phase 7's `ListPendingUpload`/`ListUploaded` cross-package pollution.
  The original test timed `correlateAllProjects()` (which sweeps *every* project in the shared test
  Postgres instance — 1,278 of them by the time this package's tests run after the full suite), not
  the one project under test's own incremental-scan cost, making a "second run must stay under 3s"
  assertion measure the wrong thing entirely. Fixed with a new `ExecuteTaskForProjectForTest`
  test-only hook scoped to a single project.

## Dashboards

New subsystem, not tied to one signal:

- **Model**: project-scoped `{id, title, panels[]}`, panel = `{type, title, query, gridPos, vizOptions}`,
  stored in Postgres (JSONB column — no new infra).
- **Frontend**: `react-grid-layout` (MIT) for the drag/resize grid; charting via ECharts (Apache-2.0,
  consistent with OpenObserve's own UI choice) or `uPlot` (MIT) if raw time-series render performance
  becomes the bottleneck.
- **"Bring your dashboard" strategy — protocol compatibility, not JSON translation:**
  - Metrics: implement the **Prometheus HTTP API** (`/api/v1/query`, `/api/v1/query_range`, `/api/v1/series`)
    using the official `prometheus/prometheus` `promql` package against our own `storage.Queryable`
    adapter. This is what makes Grafana's built-in Prometheus datasource — and by extension most
    community Grafana dashboards built on it — work against obzly by just changing the datasource URL.
  - Traces: implement a **Jaeger-compatible** query API (not Tempo, which is AGPL). Grafana ships a
    native Jaeger datasource too, so the same "just point it at obzly" benefit applies.
  - Logs: no drop-in path (LogQL is AGPL). Support best-effort JSON import + our own query language;
    a clean-room LogQL-*subset* parser (built only against the public language spec, never Loki source)
    is a possible stretch goal, not v1.
  - **OpenObserve dashboard import**: bounded, known target — parse their dashboard JSON schema
    (exported via their UI, or pulled from their public `openobserve/dashboards` community repo),
    map panel types to ours, best-effort translate simple queries.
  - Raw **Grafana dashboard JSON** import is also supported at the layout level (title, panel type,
    grid position) regardless of datasource — query wiring is only fully automatic for datasource types
    where we implement a compatible API (Prometheus, Jaeger); Loki-sourced panels need manual/best-effort
    query remapping.
- **Netdata-style auto-populated default dashboard**: a rule table mapping **OTel semantic convention
  names** (open spec, safe to implement against) to panel definitions (e.g. `http.server.request.duration`
  → latency panel, `system.cpu.utilization` → CPU panel, log level distribution → log panel, span error
  rate per service → error-rate panel). On a project's data, run a discovery pass (generalizing the
  existing `logs_querying.GetQueryableFields`/`DiscoverFields` pattern to metric/span names) and
  auto-build an "Overview" dashboard from only the rules that match what's actually present. Starts
  logs-only, gains RED-method/resource panels once metrics land, service/latency panels once traces land.

### Phase 4 implementation notes (logs-only dashboards, built)

- **Aggregation engine, not a separate query language.** Rather than build dashboards on top of a new
  query/aggregation layer, Phase 4 added one generic method to `logs_core.LogRepository`:
  `ExecuteAggregation(projectID, AggregationRequestDTO)`. `AggregationRequestDTO` is `{query, timeRange,
  groupField, bucketMs, limit}` — `groupField`/`bucketMs` independently optional, so the same shape
  produces a time series (`bucketMs` set), a grouped/stacked time series (both set), a top-N breakdown
  (`groupField` set, no bucketing — level distribution, top error messages), or a single running total
  (neither set). `logs_querier` implements it against DuckDB via `to_timestamp(floor(epoch(timestamp) /
  ?) * ?)` bucketing (verified directly — DuckDB has no native "nice" bucket-width function, so this is
  hand-rolled; `date_trunc` was considered but only offers fixed calendar units, not arbitrary widths) and
  `COALESCE(<field-expr>, '')` for the GROUP BY column (a nullable system column like `trace_id`, or a
  custom field absent from some rows, must never fail to scan as a plain string — a real bug caught by the
  test suite on the first pass, alongside a SQL-parameter-ordering bug: the SELECT clause's own `?`s
  precede `FROM read_parquet([?], ...)` in the query text even though FROM is evaluated first logically).
- **Model**: `dashboards` table (migration `20260915000000`), `panels` as one JSONB column rather than a
  child table — a dashboard's panel list is always read/written as one unit. A `Panel` field-for-field
  mirrors `AggregationRequestDTO` plus `{id, title, type, gridPos}` — a saved panel *is* a saved
  aggregation request.
- **Import is genuinely best-effort, by design, not by omission.** Both importers (`import_grafana.go`,
  `import_openobserve.go`) translate title, panel titles/types, and grid position — never queries. Grafana
  uses PromQL/LogQL/etc. per-datasource and OpenObserve has its own SQL dialect; translating either
  correctly is out of scope (this doc's original plan already called this out for Grafana/Loki panels).
  Every imported panel starts with no query (matches all logs in range) and a warning tells the user to
  add one via the normal panel editor.
- **Netdata-style Overview dashboard**: a fixed 4-panel rule set (log volume, errors over time, level
  breakdown, top error messages) — logs-only for now means every rule always applies, so (unlike the
  metrics/traces version this doc originally described) there's no discovery pass needed yet; that
  generalization is still the plan once metrics/traces add fields that aren't universally present.
  Regenerating replaces the same dashboard row in place (`is_auto_generated` flag) rather than
  accumulating duplicates.
- **Frontend**: `react-grid-layout` for the drag/resize grid, `echarts` (modular imports —
  `echarts/core` + only the chart/component/renderer types dashboards use — cut the production bundle by
  roughly 500KB versus the full package) for line/bar/pie; `stat`/`table` panel types render directly
  (a plain number, an antd `Table`) rather than through ECharts. The panel editor reuses
  `features/query`'s existing `QueryBuilderComponent`/`TimeRangePickerComponent` unchanged — a panel's
  filter is the exact same `QueryNode` the log search page already builds. A time-series panel's bucket
  width defaults to "auto" (`bucketMs` unset): the frontend computes a bucket width from whatever range
  is currently selected (`computeAutoBucketMs`, snapped to a "nice" list of widths), the same idea as
  Grafana's `$__interval` — a fixed width baked in at Overview-generation time would look wrong once the
  viewer picks a different range.

## Runtime packaging (Phase 2h)

The original single-image design bundled Postgres, OpenSearch, and Valkey inside the app container,
installed and orchestrated by a long `start.sh`. That's gone. OpenSearch left along with the JVM (no
separate search service to run — logs live in local Parquet files, see "Storage architecture" above).
Postgres and Valkey now run as their own containers, decided deliberately rather than by default: once
OpenSearch/JVM (the actual weight) was gone, bundling two comparatively light services stopped paying
for the added complexity of an in-image init system, and it's the more standard Docker shape (one
process per container) besides — see `deploy/docker/docker-compose.yml.example` for the resulting three-service
compose (`postgres`, `valkey`, `obzly`).

Backend build: `CGO_ENABLED=1` (DuckDB), and the original `--platform=$BUILDPLATFORM` cross-compile trick
is gone with it — CGO builds can't be cross-compiled the way pure-Go ones can (GOOS/GOARCH alone isn't
enough; you need a matching C toolchain/sysroot for the target too). This isn't a real capability loss:
`.github/workflows/ci-release.yml`'s actual release build only ever targeted `platforms: linux/amd64`
despite having the cross-compile scaffolding, so multi-arch images were never actually being published.
Buildx can still produce other architectures later by running the backend-build stage natively per
target platform (QEMU emulation for whichever arch isn't the runner's own) instead of cross-compiling.

Verified for real, not just by reasoning about it: built the full multi-stage image on this host (arm64;
the CGO/DuckDB compile step took ~57s and linked cleanly, confirming `duckdb-go-bindings`' prebuilt
per-platform libs — see the Phase 2a spike notes above — work the same way inside a Docker build as they
did in the ad hoc container runs), then ran the built image against real throwaway Postgres + Valkey
containers with no OpenSearch anywhere: migrations ran, all background workers started, and
`/api/v1/system/health` came back healthy through Docker's own HEALTHCHECK.

Caught one real, separate bug this way: `COPY backend/.env* /app/` (unchanged from the original
Dockerfile) will silently bake in a developer's local `backend/.env` if one exists at build time and
nothing excludes it — it did, in this exact test, shipping a `.env` meant only for local `go test` runs.
No `.dockerignore` existed at all. Added one, scoped to excluding `backend/.env` specifically (plus a
few standard build-context excludes) — the `.env.*.example` files still copy and the container's own
"use the example if no real `.env` exists" fallback still works, but a developer's real local `.env` (and
whatever it might contain) can no longer leak into an image.

## Federated SSO (portal-configurable OIDC)

The portal already had bespoke, cloud-only GitHub/Google OAuth (a fixed `github_oauth_id`/
`google_oauth_id` column pair on `users`, and a hardcoded pair of buttons on the login page). The
request was to add AWS Cognito, Google Workspace, and Microsoft Entra ID as SSO options, configurable
from the portal itself, for the self-hosted product — not just cloud. All three are standard
OIDC-compliant providers (they publish `.well-known/openid-configuration`), so this shipped as **one
generic OIDC connector system**, not three bespoke integrations, additive to (not replacing) the
existing GitHub/Google flow.

- **`oauth_connectors` table** (admin-managed via `/api/v1/sso/connectors` CRUD, admin-role-gated):
  name, provider type (cognito / google_workspace / entra_id / generic_oidc — the type only affects
  labeling, since all four speak the same OIDC discovery protocol), issuer URL, client ID, encrypted
  client secret, scopes, button label, enabled flag. Creating/updating a connector eagerly runs OIDC
  discovery against the issuer so a misconfigured admin form fails fast instead of only failing at the
  first real login attempt.
- **Client secrets are encrypted at rest** (AES-256-GCM), not hashed — unlike API keys, the server must
  send the literal secret back to the IdP on every token exchange. The key is derived (SHA-256) from
  the same DB-auto-generated secret every JWT is already signed with, so no new required env var.
- **`user_sso_identities` table** (`connector_id` + `subject`, unique together) generalizes account
  linking for portal-configured connectors, since there can be any number of them; the existing fixed
  GitHub/Google columns and logic are untouched.
- **Login flow**: `GET /api/v1/auth/sso/connectors?redirectUri=...` (public) returns each enabled
  connector's `id`, label, and a ready-to-navigate-to `authorizeUrl` (discovery + URL-building happens
  server-side, run concurrently across connectors, so the frontend needs zero provider-specific
  knowledge). `POST /api/v1/auth/sso/callback` (public) verifies the `state`, exchanges the code,
  verifies the ID token's signature via JWKS and its audience, and signs the user in via the same
  find-or-create path GitHub/Google already use.
- **`state` is now a real CSRF token.** The pre-existing GitHub/Google flow used a fixed
  `state="github"`/`"google"` string that was never actually verified against anything — a real,
  pre-existing weakness. It's replaced (for all three flows) with a signed, expiring
  (`connectorID.expiry` HMAC-SHA256, base64url, 10-minute TTL) token, verified in constant time.
- Tested against a real fake OIDC provider (`httptest.Server` serving real discovery/JWKS/token
  endpoints, RSA-signed JWTs via `go-jose`) rather than mocking the `go-oidc` library — extends this
  project's "verify against real infra" testing discipline to auth for the first time. Covers audience-
  mismatch rejection, disabled-connector rejection, tampered/expired/malformed state, and the full
  admin CRUD + public list + callback path at the HTTP layer.
- `github.com/coreos/go-oidc/v3` — was pinned to v3.17.0 while this project's toolchain was Go 1.24
  (v3.18.0+ needs Go ≥1.25); bumped to v3.21.0 (latest) once the toolchain moved to Go 1.27.1 — see
  "Runtime/toolchain versions" below.

### SSO email verification

An SSO sign-in that isn't already linked to an account is matched by email, so the email must be verified.
`oauth_connectors.require_verified_email` (default true, including existing connectors) makes the login
refuse to link to an existing account, activate an invited one, or create a new account unless the
provider vouches for the address: `email_verified` (boolean or the string "true"), or for `entra_id`
connectors the optional `xms_edov` claim. Users already linked by provider subject sign in regardless. The
switch exists because some providers never send the claim - Entra ID doesn't, and Cognito sends `false`
for admin-created users not marked verified - so it can be turned off per connector where the directory is
fully controlled. Earlier the claim was parsed but never checked, which let anyone able to set the (for
Entra, unverified and user-editable) email claim sign in as an existing user.

### SSO group mapping (project roles from IdP groups)

Admins can map IdP groups to project roles, so access is managed in the identity provider instead of
per project. Built for AWS Cognito first; the mechanism is generic OIDC.

- **Config**: `oauth_connectors.groups_claim` names the ID-token claim holding the groups (Cognito:
  `cognito:groups`; blank = off). `sso_group_mappings` maps (connector, group name) to (project, role),
  managed via `/api/v1/sso/connectors/:id/group-mappings`. Roles are Admin/Member/Viewer; OWNER is not
  assignable, since ownership only changes by explicit transfer.
- **Sync on every SSO sign-in** (existing, linked and newly registered users alike): the login resolves the
  user's groups to a desired role per project (highest wins when several groups apply), then
  `MembershipService.SyncSSOGroupMemberships` applies it. It lives in the projects package and reaches
  `UserService` through the `GroupMembershipSyncer` interface to avoid an import cycle. A sync failure is
  logged and never blocks sign-in.
- **Only sync-created memberships are managed.** They carry `project_memberships.sso_connector_id`; a later
  sync updates or removes only those. Members added by hand, project owners and memberships from another
  connector are never touched, and changing a synced member's role manually detaches it (an admin's decision
  wins). Deleting the connector keeps the access as manual memberships.
- **A missing claim means "no groups".** Cognito omits `cognito:groups` for a user in no group, so an absent
  claim revokes their group-granted access at next sign-in. Consequence: a wrongly configured claim name
  revokes synced access (never manual access) until fixed.
- **On a control plane, groups map to Sites.** A control plane has no projects of its own (they live on
  the Sites), so `sso_group_mappings`' project half can't apply there - its `project_id` is a foreign key
  to the local projects table, which is what produced "project not found" when a Site's project was
  picked. `sso_site_group_mappings` maps (connector, group) to (Site, role) instead, in `site_grants`'
  shape: a Site-wide ADMIN / MEMBER / VIEWER, or a project role on one of that Site's projects (the Site's
  project id, no foreign key). Managed via `/api/v1/control-plane/admin/sso/connectors/:id/
  site-group-mappings` (central ADMIN). At each SSO sign-in `SSOSiteMappingService.SyncSSOSiteGrants`
  (injected into `UserService` as `SSOSiteGrantSyncer`, control-plane mode only) reconciles the user's
  `site_grants` that carry `sso_connector_id` = this connector: highest role per Site/project across
  groups, re-roled or removed as groups change, and each affected Site's cached token/refusal
  invalidated. Hand-made grants (NULL `sso_connector_id`) are never touched and keep their slot; deleting
  the connector removes the grants it created. The dialog keeps "Control plane admin" (the connector's
  global-admin mapping), which gives no Site access by itself.
- **Global Admin**: a mapping with no project and role `ADMIN` makes group members instance admins. Only the
  root admin may create or delete such mappings (same rule as manually promoting an admin). A group-granted
  admin is tagged (`users.sso_admin_connector_id`); a later sync demotes only tagged admins, never the root
  admin, manually promoted admins, or the last remaining admin, and a manual role change clears the tag.
  Admins must complete TOTP MFA, so the login re-reads the user after the sync and a freshly promoted user
  gets the MFA-setup challenge instead of a token (IdP-side MFA does not count).
- **Not covered**: revocation only happens at the user's next sign-in, not immediately (a demoted admin's
  existing session keeps working until then); a new SSO user still needs "allow external registrations" (or
  a prior invite) to get an account at all.
- **Entra ID**: the groups claim is `groups` and holds group *Object IDs* (GUIDs), so mappings are entered
  by Object ID. Users in more than about 200 groups get no `groups` claim, only `_claim_names`; for
  `entra_id` connectors the login then fetches the IDs from Microsoft Graph (`/me/getMemberObjects`, paged,
  with the sign-in's access token, at a fixed Graph URL rather than one taken from the token). If that
  lookup fails the groups are marked unknown and the sync changes nothing for that sign-in, instead of
  reading "no groups" and revoking access. The Graph permission needed for this call and the
  `_claim_names` handling are implemented from Microsoft's docs and tested against a fake Graph server,
  not yet a real tenant.

## Multi-factor authentication (TOTP, enforced for Admin)

Requested as: MFA enforced for Admin-role accounts, optional for Members, with anti-replay
guaranteed and the bootstrap admin's email configurable via `.env`. Shipped as `internal/features/
users/mfa` (mirrors the `sso` subpackage's shape: `dto`/`services`/`controllers`) plus a few
generalizations to existing auth code.

- **Who:** every Admin-role account, always; every account when Settings → Identity & access →
  **Require MFA for everyone** is on (v0.56.0, `users_settings.is_mfa_required_for_everyone`).
  `User.IsMFARequired()` reads that setting through a 15 s in-process cache
  (`users_models.SetMFAForEveryone`), so every replica applies a change within 15 s without a
  database read per request.
- **Enforcement is checked twice, not once**: at sign-in (`users_services.completeAuthentication`,
  the single chokepoint `SignIn` and `getOrCreateUserFromOAuth`'s existing-user branches both funnel
  through) and on every subsequent request (`users_middleware.RequireMFAIfEnforced`, added to the
  protected route group *after* MFA's own self-service routes are registered, so only they stay
  reachable to an Admin who hasn't set MFA up yet). The second check matters because access tokens
  here are long-lived (10 years) — enforcing only at login would leave an already-signed-in Admin,
  or one just promoted from Member, unaffected until their next fresh login.
- **Two-step login, not a bolt-on second factor check**: `SignIn`/OAuth no longer always return a
  real token. If MFA is enabled, they return `{mfaRequired, mfaToken}`; if MFA is required by role
  but not yet enabled, `{mfaSetupRequired, mfaToken}`. The frontend completes the flow against
  `mfa_controllers`' public `login-verify` (already enabled) or `login-setup`+`login-enable`
  (forced first-time setup) endpoints, which return the real access token on success.
- **The `mfaToken` is not a real access token**, even though it's signed with the exact same key
  every real one is: it deliberately omits the `passwordCreationTime` claim
  `UserService.GetUserFromToken` (and therefore `AuthMiddleware`) requires, so it's structurally
  rejected by every ordinary protected route — verified directly in
  `mfa_services`' and `users_controllers`' tests, not just asserted in a comment.
- **Anti-replay** (`mfa_services.verifyAndFindStep`): pquerna/otp's own `Validate`/`ValidateCustom`
  only answer "is this code currently valid," which alone would let the *same* code be accepted
  repeatedly within its ~90s window. Every verification path here additionally tracks which
  30s time-step matched and persists it (`users.mfa_last_used_step`), rejecting that step or any
  earlier one on a later attempt — so a captured code (shared terminal, log line, etc.) is useless
  the moment it's been used once, and an older code can't be replayed after a newer one succeeds.
- **The TOTP secret is encrypted at rest** via the same `SecretKeyRepository.Encrypt`/`Decrypt`
  (AES-256-GCM, keyed off the DB-auto-generated secret every JWT is signed with) the SSO feature's
  connector secrets already used — pulled out of `sso_services` into the shared repository so both
  features (and anything future) use one reviewed implementation instead of a copy each.
- **Admin can reset another user's MFA** (including another admin's) as the lost-device safety
  valve — necessary because `Disable` itself refuses to let an Admin remove their own *required*
  MFA, so without this a lost device would be a permanent lockout for that account.
- **Root admin, generalized past a fixed email**: the bootstrap admin was previously identified
  purely by `Email == "admin"` in several permission checks (only *that* account can activate/
  deactivate/promote other Admins) — fragile, and blocking the requested "admin email configurable
  via `.env`" outright, since renaming it would have silently broken those checks. Replaced with an
  explicit `users.is_root_admin` boolean; `ADMIN_EMAIL` (optional, defaults to the historical
  `"admin"`) sets the email on first creation and renames the existing account in place on later
  startups if it differs (`UserRepository.SyncRootAdminEmail` — refuses with a clear error rather
  than silently failing if the target email is already taken by a different account).

## Data Sources catalog (onboarding integrations)

Requested as: a page listing ready-to-use onboarding configs for common infrastructure/apps
(Kubernetes, Docker, Linux servers, Nginx, MongoDB, MySQL, Redis, Valkey, PostgreSQL, and other
OTEL-supported sources), auto-filled with the project's real endpoint and a fresh API key -
generalizes what `KubernetesOtelConfigComponent` already did for Kubernetes alone into a catalog
covering many sources, per `frontend/src/features/projects/ui/dataSources/`.

- **One generic modal drives every source** (`DataSourceConfigModal`), not a bespoke component
  per integration - each catalog entry (`DataSourceDefinition`) only supplies what's actually
  source-specific: its extra form fields and a `build(ctx)` function turning
  `{baseUrl, apiKey, fieldValues}` into `{content, language, filename, instructions}`. Adding a new
  source is one new file plus one line in `catalog.ts`; the page and modal need no changes.
- **A dedicated API key per generated config**, created fresh each time (same reasoning
  `KubernetesOtelConfigComponent` already established: a key's real value is only ever shown once,
  and a config meant to be applied close to as-is shouldn't carry a placeholder the way the
  language-SDK "how to send logs from code" snippets deliberately do).
- **OTLP/HTTP, not gRPC**, is the exporter in every OTel-Collector-based config here - it shares
  the exact host/port the browser is already using to reach the UI, so whatever TLS/reverse-proxy
  already fronts that URL just works, whereas OTLP gRPC's port (4317) may not be separately exposed
  depending on how an instance is deployed.
- **Database sources** (PostgreSQL/MySQL/MongoDB/Redis/Valkey) pair the OTel Contrib
  metrics receiver for that engine with optional `filelog` tailing of its default log file/format -
  metrics and logs are two different receivers in the same collector, both exported to the same
  Obzly project. Redis and Valkey share one builder (`redisDataSource.ts`): they're wire-protocol
  compatible, so the same `redis` receiver works against either unmodified.
- **Linux Server** ships a shell script that installs the official `.deb` collector package as a
  systemd service (journald logs + hostmetrics + OTLP); **Windows Server** the equivalent
  (Event Log channels + hostmetrics); **Docker** ships a docker-compose sidecar tailing the
  daemon's own JSON container log files, plus per-container CPU/memory/network/block-IO metrics
  via `docker_stats` (the Docker socket, mounted read-only - same data `docker stats` shows) and
  the Docker host's own `host_metrics` through a read-only `/hostfs` mount;
  **Syslog** listens for RFC3164/5424 messages for anything (network gear, appliances) that only
  speaks syslog; **Custom (OTLP)** and **Prometheus-format metrics** are fallbacks for anything
  else OTel-compatible or already exposing a `/metrics` endpoint - no bespoke receiver needed for
  either.
- **JVM apps and Cassandra** use the standalone OpenTelemetry JMX Scraper (otelcol-contrib no
  longer has a `jmx` receiver): a generated `jmx-scraper.properties` plus a collector that receives
  its OTLP. The scraper jar version is pinned in `jmxScraper.ts`.
- **Shared collector conventions** (`otelCollectorCommon.ts`): one pinned contrib version
  (`COLLECTOR_IMAGE`, also used by the Linux `.deb` and the Kubernetes manifest), `memory_limiter`
  first and `batch` (capped under the ingest `MaxBatchSize`) last in every pipeline,
  `resource_detection` for the cloud/host attributes Infrastructure reads, a `health_check`
  extension, and `file_storage` offsets for every receiver that tails files, the journal or the
  Windows Event Log, so a restart neither skips nor re-sends lines. The collector's own OTLP
  receiver has no authentication, so host templates bind it to 127.0.0.1 unless "Accept OTLP from
  other machines" is ticked (Docker publishes its ports on 127.0.0.1 the same way; Kubernetes
  serves it in-cluster). `catalog.test.ts` renders every
  entry's variants and checks these; `OBZLY_DATASOURCE_OUT=<dir>` writes them out for
  `otelcol-contrib validate`.
- **Fluent Bit** is the one entry that isn't an OTel Collector config at all - Fluent Bit ships its
  own built-in OpenTelemetry output plugin, so the generated artifact is a `[OUTPUT]` block for an
  existing `fluent-bit.conf`, not a collector YAML.
- **Card icons** (`public/icons/datasources/<id>.svg`, rendered via `DataSourceIcon`) are a curated
  subset of [simple-icons](https://simpleicons.org) (MIT-licensed), used purely to indicate which
  technology each card is for, not as any claim of endorsement - extracted at build time rather
  than pulled in as a runtime dependency, since only ~25 of the ~1000s of icons in that package are
  needed. Sources with no suitable icon (no logo exists, e.g. SNMP/StatsD; or the closest match
  belongs to a distinct product we shouldn't visually conflate it with, e.g. Valkey vs. Redis) fall
  back to the generic Data Sources nav glyph instead of guessing.
- Full current catalog: Kubernetes, Docker, Linux Server, Windows Server, Syslog, SNMP, ZooKeeper,
  VMware vCenter, Consul, etcd, Nginx, Apache, HAProxy, Traefik, Envoy, IIS, PostgreSQL, MySQL,
  MongoDB, Redis, Valkey, Elasticsearch, Cassandra, CouchDB, Memcached, SQL Server, Riak, RabbitMQ,
  Kafka, JVM, Apache Flink, Apache Spark, Custom (OTLP), Prometheus-format metrics, StatsD,
  HTTP Uptime Check, Fluent Bit - more added over time in `dataSources/catalog.ts`.

## Runtime/toolchain versions

Kept current as of this writing:

| Component | Version | Where pinned |
|---|---|---|
| Go | 1.27.3 | `Dockerfile` and `Dockerfile.probe` (`golang:1.27.3-bookworm`), `backend/go.mod` (`go` directive), `.github/workflows/ci-release.yml` (`go-version`, 3 jobs), `.github/workflows/vulncheck.yml` |
| Node.js | 26.8.2 (exact pin, not a floating major tag) | `Dockerfile` (`node:26.8.2-alpine`), `.github/workflows/ci-release.yml` (`node-version`, 2 jobs) |
| PostgreSQL | 18 | `deploy/docker/docker-compose.yml.example`, `backend/docker-compose.yml.example` (dev/test, what CI's `test-backend` job runs against), `install-obzly.sh` (all `postgres:18-alpine`) |
| Valkey | 9.2 | `deploy/docker/*.example`, `backend/docker-compose.yml.example`, `deploy/kubernetes/eks/{common,control-plane}/valkey-deployment.yaml`, `deploy/helm/obzly/values.yaml`, `install-obzly.sh` (all `valkey/valkey:9.2-alpine`) |

These four have drifted out of sync with each other more than once already (a rebuild/redeploy that
only bumps one of the files above and not the others is the easy way to reintroduce it) - when
bumping any of them, grep for the old version string across the whole repo rather than trusting
memory of which files matter.

**Bumping Go from 1.24 to 1.27.1 also unblocked two dependencies that were pinned below their latest
release purely because of a `go.mod`-declared minimum Go version, not any code incompatibility** —
both bumped and fully re-tested (`go test ./...`, only the pre-existing nanosecond-precision flakes
unrelated to this failing, same as before the bump):
- `github.com/coreos/go-oidc/v3` v3.17.0 → v3.21.0 (SSO)
- `go.opentelemetry.io/proto/otlp` v1.10.0 → v1.11.0 (OTLP receivers)

**One dependency stayed pinned old, but no longer for a Go-version reason** —
`github.com/prometheus/prometheus` (the vendored PromQL engine, Phase 6) is still on `v0.309.1`.
Bumping it now hits a real breaking API change unrelated to Go version: some release before
`v0.314.0` added a required `ST()` method to the `chunks.Sample` interface our `floatSample` adapter
(`internal/features/metrics/promql/queryable.go`) implements. Fixing this needs an actual code
change (implementing `ST()` correctly - likely a start-timestamp field added for native-histogram
support - and re-verifying the PromQL adapter's correctness end-to-end), not just a version bump, so
it's left as explicit follow-up work rather than attempted inline here.

**PostgreSQL 17 → 18 and Valkey 8.0 → 9.1 are major-version bumps for any *already-running* instance**
(a fresh install has no existing data, so this only matters for upgrades):
- Postgres's on-disk format isn't compatible across major versions - starting a `postgres:18` container
  against an existing v17 data directory fails outright. An in-place upgrade needs a real
  `pg_dump`/`pg_restore` (or `pg_upgrade`) migration, not just an image tag change - do this before
  swapping the image on any existing deployment, this repo's own compose/install files intentionally
  default new installs to 18 without attempting to migrate existing ones automatically.
- Also note: Postgres 18+ images changed their expected bind-mount point from `/var/lib/postgresql/data`
  to `/var/lib/postgresql` (they now manage a version-specific subdirectory internally, to support
  `pg_ctlcluster`-style in-place upgrades). Mounting at the old path makes the container detect a
  stale/incompatible mount and refuse to start - the compose/install files in this repo already mount
  at the new path.
- Valkey's RDB/AOF persistence format has stayed compatible across recent majors, but this wasn't
  independently re-verified for the 8.0 → 9.1 jump specifically; since everything Valkey stores here
  (rate-limit counters, ingestion-rate counters, query tracking, the SSO/MFA secret-derivation cache)
  is ephemeral/regenerable rather than a source of truth, the lower-risk move on an existing
  deployment is to let it start with an empty data directory on the new version rather than trust an
  unverified cross-major RDB load.

**Both migrations above were validated on this project's own test instance**, not just documented in
theory:
- Postgres: `pg_dumpall -U postgres` from the running 17 instance, then piped into `psql` against a
  freshly-initialized 18 instance. Verified afterward via direct `SELECT`/`\dt` queries (row counts,
  table listing, and specific column values including `mfa_enabled`/`is_root_admin`) and by confirming
  the app's own goose migration tracking recognized the already-applied migration state with no
  re-migration attempted. The old v17 data directory and the `pg_dumpall` backup were preserved (moved
  aside, not deleted) rather than wiped.
- Valkey: rather than pre-emptively wiping the data directory, the 8.0 → 9.1 image bump was tried
  first against the *existing*, unmodified data volume. It loaded cleanly - Valkey's own startup log
  confirmed `Loading RDB produced by Valkey version 8.0.11` followed by `Done loading RDB, keys loaded:
  42, keys expired: 0` - so the cross-major RDB format compatibility mentioned above held in practice
  for this jump, and no fallback to a fresh start was needed. The old data directory was still copied
  aside first as a defensive backup.

## Deployment / networking convention (this host)

This cloud host fronts externally-reachable services with Nginx Proxy Manager over a shared external
Docker network named **`frontend`** (confirmed via `docker network ls`/`docker ps` — NPM and every other
app's web-facing container already sit on it). Compose files for this project should:

- Declare `frontend` as an `external: true` network.
- Attach **only** the web UI / API gateway container to `frontend`.
- Keep internal components (Postgres, object storage, NATS, workers) on a separate internal-only network,
  never on `frontend`.
- **Not** publish host ports for anything meant to be externally reachable — NPM is pointed at the
  container by name on `frontend` manually by the user instead.

**Live Tail and response buffering.** See "Live Tail" above for the full design — the short version for
whoever is configuring the reverse proxy: `POST /api/v1/logs/tail/{projectId}` is a long-lived streaming
response, and if the proxy in front of it buffers responses, the browser falls back to a 2-second poll
automatically (no broken UI either way, just less real-time). To get real streaming through **this
host's own Nginx Proxy Manager**: edit the proxy host's Advanced tab and add
```
location /api/v1/logs/tail/ {
    proxy_pass http://obzly-app:4005;
    proxy_buffering off;
    proxy_read_timeout 3600s;
}
```
(the app already sends `X-Accel-Buffering: no` on this response, which recent NPM/nginx versions may
already honor without this — the explicit `location` block is the belt-and-braces version for older
versions or anyone who wants to be certain). Any other self-hosted reverse proxy/load balancer needs the
equivalent "disable response buffering + don't apply a short idle timeout" setting for that one path.

## High availability

The Storage architecture section above notes single-node mode as the current default and an HA/cluster
mode (separate ingester/compactor/querier processes via NATS, a DataFusion query-engine sidecar) as a
later, optional evolution. **Status (2026-09-30):** that evolution is now designed in ["Scale
architecture"](#scale-architecture-1-pb--5-tbday) — NATS adopted (embedded / `broker` role),
DataFusion still rejected; this history is kept. This section covers a nearer-term milestone: making the **existing** binary
safe to run as N replicas at all - on Kubernetes, or on plain Docker/Compose with multiple containers of
the same image - without that larger rearchitecture. Everything here is deployment-agnostic by
construction (a Valkey lock, not a Kubernetes-specific primitive), so the same code path is correct in
both shapes.

**What was already safe, no changes needed**: auth (stateless JWT, secret + user data in Postgres), rate
limiting and the concurrent-query limiter (Valkey-backed counters, no in-process state), and Live Tail
(Valkey Pub/Sub fan-out - a log ingested by one replica correctly reaches a viewer whose stream landed on
another). Object storage (S3/GCS/Azure/MinIO, `internal/storage/segments/objectstore.go`) already has a
synchronous on-demand download on a local cache miss at query time (`resolvePath` in each of
`logs_querier`/`spans_querier`/`metrics_querier`) - not just the periodic reconciler - so once
`OBJECT_STORAGE_URL` is configured, a query landing on a different replica than the one that ingested/
compacted a segment still gets correct results. **Local-disk-only mode (no object storage configured) is
still single-node-only** - there's no other copy of the hot-tier Parquet data, so don't run N replicas
without object storage configured.

**Known gap (found 2026-09-30): segments written by `SERVICE_MODE=api` replicas are never uploaded.**
`UploadPendingSegments` runs only where `RunsBackgroundWorkers()` (`all`/`worker`) and uploads from
that process's own `record.Path`, while the HA overlay gives `api` and `worker` separate `emptyDir`s.
So a segment an `api` replica flushes is invisible to other replicas and dies with the pod on any
termination (RollingUpdate, scale-down, Spot reclaim), and the worker's oldest-first upload batches
stall on those rows. Object storage makes the *worker's* segments durable, not the `api` replicas'.
Single-replica `all` installs on a PVC are unaffected. Fix: an S-sized interim uploader on every
signal-storage replica (ROADMAP Phase 14-A), then JetStream ingest (Phase 14-B), which moves writing
to workers entirely.

**Interim fix (built 2026-09-30).** `stream_segments.writer_id` (nullable) records whose disk holds a
segment until upload. Each process keeps its id in `LOGS_DATA_DIR/.writer-id`, so the id follows the
disk: a restart on the same volume keeps uploading what it wrote, a fresh `emptyDir` gets a new id.
`SegmentRepository.Create`/`ReplaceSegments` stamp it, and every uploader lists only
`writer_id = <own>` (plus pre-existing NULL rows on `all`/`worker` replicas), so another replica's
rows no longer stall the queue. `api` replicas upload their own segments every 15 s; `all`/`worker`
replicas upload theirs after each compaction tick whether or not they hold the compaction lock
(compaction first on the holder, so it mostly uploads merged files); every replica runs one bounded
(15 s) upload pass after the shutdown flush. What remains: a hard kill (SIGKILL, OOM, node loss) of
an `api` pod on `emptyDir` still loses what it flushed in the last ~15 s plus its RAM buffer (item 1
removes it). Compaction downloads an input that another replica wrote and uploaded to its catalog
path (`segments.EnsureLocalFiles`, 2026-09-30) and skips a group whose input is not uploaded yet;
before, such a group failed with "No files found" every pass. This needs `LOGS_DATA_DIR` to be the
same path on every replica, as in the manifests.

**Orphaned pending segments (2026-10-09).** A pending row whose writer's disk is gone (an evicted or
replaced pod's `emptyDir`) can never upload: it was retried every pass, skipped by queries, counted
as an upgrade blocker, and blocked compaction of any group holding it - for spans with sampling,
its whole day. Every process with a writer id now heartbeats into `segment_writers` once a minute,
and each stream's reconcile pass (leader-locked, object storage only, from the storage layer)
moves to `lost_segments` the pending rows over an hour old whose writer hasn't been seen for 10
minutes, and rows from before `writer_id` existed after 24 hours (`segments.MarkOrphansLost`).

**Compaction attempts (2026-10-08).** A merge that crashed DuckDB killed the worker on every pass,
so the project's small segments piled up until searches timed out. Each merge attempt is counted
on its segments *before* it runs (`stream_segments.compaction_attempts`, so a crash still counts);
segments at `MaxCompactionAttempts` (3) are left out of compaction and stay queryable, logged each
pass. A failed merge no longer aborts the project's other groups and removes its partial file.

**What this session's HA work fixed** - four real cross-replica races, all closed via
`internal/util/leaderlock` (a small Valkey-backed renewable lease - `SET NX PX` to acquire, a
compare-and-renew Lua script so only the current owner can extend it, a compare-and-delete Lua script so a
stale owner's release can't steal back a lease someone else has since taken over):

- **Background workers** (`logs_cleanup`/`spans_cleanup`/`metrics_cleanup`'s quota/retention/compaction/
  reconciliation tickers, one lock name per worker per signal type) now call `leaderlock.RunExclusive`
  on every tick instead of running unconditionally - only the replica that wins a given tick's lock
  actually does the pass; every other replica skips it. Non-blocking and one-shot-per-tick by design: which
  replica "wins" can vary tick to tick, and that's fine, since the only requirement is "at most one replica
  runs a given pass at a time," not "always the same replica." `RunExclusive` renews the lease every
  TTL/3 while the pass runs, so a pass longer than its TTL stays exclusive, and
  cancels the pass's context if the lease is lost or can't be renewed in time.
- **Migrations** (`runMigrations` in `cmd/main.go`) now run `goose up` inside
  `leaderlock.RunExclusiveBlocking` - unlike the workers above, every replica *must* eventually run this
  (goose's own version tracking makes a second run a no-op, not a skip), so a replica that loses the race
  blocks and retries until the winner releases the lock, rather than skipping migrations entirely.
- **Initial admin bootstrap** (`UserRepository.CreateInitialAdmin`) had a check-then-insert race with no
  database constraint behind it - two replicas booting together could both observe "no root admin yet" and
  both try to create one. Closed with a real Postgres constraint
  (`idx_users_single_root_admin`, a unique partial index on `is_root_admin WHERE is_root_admin = TRUE`,
  migration `20260918050000`) rather than app-level locking, so it's correct regardless of what's calling
  it: the losing replica's insert fails with a clean unique-violation error (SQLSTATE 23505), which
  `CreateInitialAdmin` now treats as "another replica already created it," not a fatal startup error.
- **Startup query-slot cleanup** (`ConcurrentQueryLimiter.CleanupAllQuerySlots`, wired via
  `LogQueryService.CleanupPendingQueries`) unconditionally wiped every user's concurrent-query counter in
  Valkey on every replica's startup - correct for "the first replica coming up from a clean state," wrong
  on a rolling deploy or scale-up, where it would zero out an already-running replica's legitimate
  in-flight counters. Now wrapped in a non-blocking `leaderlock.RunExclusive`: at most one replica in a
  simultaneous startup performs it, and since stale slots already self-expire via their own TTL, skipping
  it entirely when another replica's already handling startup is an acceptable outcome, not a correctness
  gap.

**Verification**: `internal/util/leaderlock`'s own test suite exercises real contention against real
Valkey (10 concurrent goroutines racing `RunExclusive`, exactly one wins; a lease that isn't renewed
expires and is correctly picked up by another owner; a stale owner's `Release` can't delete a lease
someone else now holds). `internal/features/users/repositories`'s
`Test_CreateInitialAdmin_ConcurrentReplicas_CreatesExactlyOneAdmin` fires 8 concurrent `CreateInitialAdmin`
calls with the same email (matching the real "admin" default every replica would use) and asserts every
call succeeds and exactly one admin row exists afterward.

**Phase B: `SERVICE_MODE` (built).** The single binary now splits into separately-scalable roles via a
`SERVICE_MODE` env var, so the request-serving tier can scale independently with zero risk of duplicate
background work by construction rather than by coordination - the leader-locking above is what makes
running N replicas of any mode correct; this is what makes the *background workers* and *HTTP/gRPC
serving* independently deployable and scalable in the first place. Every mode is the exact same image;
`config.ServiceMode` (`internal/config/config.go`) gates two independent halves of `cmd/main.go`'s
startup:

- `all` (default, unset) - both halves, one process. Existing single-container deployments
  (`docker-compose.yml.example`) need zero changes; this is why it's the default.
- `api` - HTTP/gRPC serving (REST, OTLP, the UI, ingest) but none of the logs/spans/metrics cleanup
  workers. The tier meant to sit behind a load
  balancer/HPA.
- `worker` - only the logs/spans/metrics quota/retention/compaction/reconciliation workers, no HTTP/gRPC
  serving at all. Typically a small, separate deployment (replica count 1, or >1 relying on the leader
  lock for failover) alongside one or more `api` replicas.

Migrations and initial-admin bootstrap run in every mode (both are already safe to run redundantly, or
from a mode that never actually serves the UI, per the fixes above) - a deployment isn't required to
ever run an `all`-mode replica as long as at least one `api` and one `worker` replica exist somewhere.
`deploy/docker/docker-compose.ha.yml.example` demonstrates the split concretely: two `SERVICE_MODE=api`
replicas plus a `SERVICE_MODE=worker` replica, one shared Postgres/Valkey/object-storage backing them
all - the same split maps directly onto separate Kubernetes Deployments instead, no code differences
required for either target.

Verified directly (not just built): the compiled binary was run in all three modes against real
Postgres/Valkey - `worker` mode opens no HTTP/gRPC ports and starts all three signal types' cleanup
workers; `api` mode opens both ports, serves a real health-check request, and logs zero cleanup-worker
startup lines; `all` mode (unset) does both, matching pre-`SERVICE_MODE` behavior exactly. See
`internal/config/service_mode_test.go` for the mode-to-behavior mapping's own unit tests.

## Scale architecture (1 PB / 5 TB/day)

**Status: planned (ROADMAP Phase 14).** This section is the design; the review record — verified
findings with code citations, rejected alternatives, arithmetic and open questions — is
[`docs/ENTERPRISE_SCALE_PLAN.md`](docs/ENTERPRISE_SCALE_PLAN.md). Targets: 1–5 TB/day expected
ingest, 5 TB/day sustained as the acceptance target, ~1 PB stored at 1-year retention (≈22 TB/day
raw at 8× ZSTD) as the storage design ceiling.

**Constraint: Postgres + Valkey + S3 and one image.** No Kafka, ClickHouse or Rust sidecar. NATS
(`nats-server`, Apache-2.0) embeds in-process, so it ships inside the image, but it is new
*stateful* infrastructure at the HA tiers and is treated as such:

| Mode | Where NATS runs | Fits |
|---|---|---|
| Embedded in `SERVICE_MODE=all` | Inside the single process; JetStream file store (R1) under `LOGS_DATA_DIR` | 1–5 GB/day single-node installs |
| `SERVICE_MODE=broker` (new role) | 3-replica StatefulSet running only the embedded server; R3 streams; on-demand nodes across three AZs | Every multi-replica tier; keeps `api`, `worker`, `querier` stateless and Spot-friendly |

The official NATS image and chart are a supported alternative to `broker` mode. NATS is never
embedded in `api` replicas in HA.

### Ingest and durability

- `api` publishes each validated batch to a JetStream stream per signal (subjects
  `ingest.<stream>.<shard>.<project>`) and acks the client **after the JetStream commit**. This
  replaces the Valkey logs list and the synchronous per-replica `segments.Writer` for spans and
  metrics. Valkey keeps locks, rate limits, caches and MCP OAuth state.
- **Durability contract.** No WAL by design: JetStream is the durability layer. R3 (broker role) =
  zero loss for any single-node failure (an `api` pod, a `worker`, one broker); loss only if a broker
  quorum is lost. Embedded R1 (`all`) survives a process crash; a host loss can drop up to
  JetStream's `sync_interval` (default 2 min) unless `sync_always` is set.
- `worker` replicas run durable pull consumers with work-queue retention (a message is removed once
  its rows are in an uploaded, catalogued segment). L0 files flush **by size** (64 MB target) or at a
  **1-hour max-age** for low-volume pairs — no timer-driven small flushes.
- Backpressure: stream `max_bytes` with `discard: new`, sized to ~1 h of ingest; a rejected publish
  maps to 429 / `RESOURCE_EXHAUSTED` + `Retry-After`.
- Live Tail moves to core NATS: ingest publishes to `tail.<project>` only when a tail is open.
- `api` shutdown: readiness false on SIGTERM, a drain delay before `srv.Shutdown`, a deadline on gRPC
  `GracefulStop`. After JetStream an `api` pod holds nothing beyond in-flight requests.
  **Built (14-A):** `GET /api/v1/system/ready` returns 503 from the moment SIGTERM arrives, otherwise
  the `/system/health` checks; `GET /api/v1/system/live` checks nothing, so a database outage no
  longer restarts every replica; `/system/health` is unchanged for Docker and downdetector users.
  `SHUTDOWN_DRAIN_SECONDS` (default 0, 15 in the multi-replica manifests) keeps serving before
  `srv.Shutdown`; `GracefulStop` gets a 10 s deadline, then `Stop`. The Kubernetes manifests point
  readiness and liveness at the new endpoints (startup stays on `/system/health`) and set
  `terminationGracePeriodSeconds: 60`.
- **Built (14-B, item 1 step 1a): the broker.** `internal/broker` embeds `nats-server` v2.15
  (Apache-2.0). `SERVICE_MODE=broker` runs only the NATS server — client (4222), cluster (6222) and
  monitoring/health (8222) listeners, token auth for clients and routes (`NATS_TOKEN`), JetStream
  file store (`JETSTREAM_DIR`), peers from `NATS_ROUTES`, lame-duck shutdown on SIGTERM, never
  Postgres or Valkey. `INGEST_BUFFER=jetstream` (default `valkey`) makes `all` start the server
  in-process with no listener (R1) and `api`/`worker` connect via `NATS_URL` (R3); every replica
  creates the ingest streams idempotently — `INGEST_LOGS`/`INGEST_SPANS`/`INGEST_METRICS`, subjects
  `ingest.<signal>.>`, work-queue retention, file storage, `DiscardNew` at
  `INGEST_STREAM_MAX_BYTES` (10 GiB), 2-minute duplicate window — retrying bounded attempts because a
  request sent during a leader election can go unanswered. Nothing publishes yet (steps 1b–1e move
  logs, then spans/metrics, then Live Tail, then drop the Valkey queue). Workers export
  `obzly_jetstream_stream_{bytes,max_bytes,messages,replicas_lagging}`; alerts at 80% full and on a
  lagging replica. `deploy/kubernetes/eks/ha/broker.yaml` (StatefulSet ×3, one per zone, PVC, PDB,
  NATS `/healthz` probes) ships as a preview outside the kustomization. Verified locally: three
  brokers + `api` + `worker` formed R3 streams; stopping one broker showed one lagging replica per
  stream with `api` still ready, and it caught up on restart.
- **Built (14-B, item 1 step 1b): logs through JetStream.** With `INGEST_BUFFER=jetstream` the logs
  receivers (REST, OTLP/HTTP, OTLP/gRPC) publish each accepted batch to
  `ingest.logs.<shard>.<project>` (64 shards, FNV of the project id; JSON arrays of at most 1 MB per
  message, broker `max_payload` 8 MB) and answer only after every message's JetStream ack. A full
  stream answers 429 / `RESOURCE_EXHAUSTED` with RetryInfo, no ack 503 / `UNAVAILABLE`, both with
  `Retry-After: 5`, and nothing of the batch counts as accepted. `all`/`worker` replicas run
  `broker.ShardRunner`: each registers in a Valkey sorted set, holds up to ceil(64 / live workers)
  shard leases (`leaderlock`, renewed every 5 s, 30 s TTL), gives extras back when a worker joins,
  and runs one durable pull consumer per shard (`logs-shard-NN`, `MaxAckPending` unlimited, AckWait =
  max-age + 10 min). Each pull aims at 12 MB (`broker.readMaxBytes`): its message count is sized from
  the previous pull's average message, between 1 and 256 (`nextFetchBatch`), and capped by the
  largest message seen lately, assumed at least 256 KB (`fetchBatchCap`), so a shard that switches
  from small batches to 1 MB ones pulls at most 48 MB at once; the embedded server
  allows 256 MB of pending data per client (`MaxPending`). nats.go's `FetchBytes` was not used: it
  asks for a million messages and allocates 32 MB of channels per call. Until v0.56.0 a
  pull took 256 messages - about 100 MB of 400 KB log batches, over NATS's 64 MB slow-consumer
  limit - so the server dropped the connection, the acks were lost and the batches came back only
  after AckWait. The sink (`ingest.Sink`) buffers each project's rows in its own writer (outside the
  30 s force-flush) and writes an L0 segment at `INGEST_L0_TARGET_BYTES` (64 MB) of batches or
  `INGEST_L0_MAX_AGE` (1 h, floor 5 min), then uploads it and only then acks the batches it covers;
  a failed upload keeps them held (`+WPI`) and retries each tick. Giving a shard away flushes,
  uploads and acks first. Delivery is at-least-once: a worker lost between upload and ack, or a
  flush failure, redelivers after AckWait and writes those rows twice. A batch delivered 50 times and
  published over a day ago is terminated instead, logged and counted in
  `obzly_ingest_terminated_messages_total` (the consumer's own `MaxDeliver` stays unlimited: it counts
  deliveries only, and an outage of a few hours would reach it). The metrics label index is
  best-effort: a row Postgres rejects is logged and the batch still acks. Rows become queryable when their L0 is
  uploaded, so max-age is the freshness bound for low-volume projects until Live Tail moves to the
  stream (1d). Metrics: `obzly_ingest_publishes_total{result}`, `obzly_ingest_held_messages`,
  `obzly_ingest_owned_shards`.
- **Built (14-B, item 1 step 1c): spans and metrics through JetStream.** The same path for OTLP
  traces, OTLP metrics and Prometheus remote-write: publish to `ingest.spans|metrics.<shard>.<project>`,
  answer after the ack, one shard runner and sink per signal on every `all`/`worker` replica. The
  signal-agnostic sink is `internal/ingest` (each signal adds only its decode-and-append step and
  durable writers); retryable transport statuses come from `otlp.RetryableGRPCError` /
  `WriteRetryableHTTPStatus`. Remote-write answers 503 even when the buffer is full, since
  Prometheus retries 429 only with `retry_on_http_429`. Self-tracing still writes spans directly
  into the replica's own writer: it is internal and best-effort, and an outage of the broker must not
  stall it. As with logs, a sample is queryable once its L0 is uploaded, so dashboards' newest minutes
  lag by up to `INGEST_L0_MAX_AGE` for low-volume projects.
- **Built (14-B, item 1 step 1d): Live Tail on core NATS.** With the broker in use, a receiver publishes
  each accepted batch to `tail.logs.<project>` (one message per batch, split at 1 MB) only while some
  replica has a tail open on that project. An open tail announces itself on
  `tail.interest.<project>` every 5 s; each replica keeps those announcements for 15 s, and marks its
  own tails at once. The tail subscribes, announces, flushes, and only then sends "ready", so a
  replica hears the announcement within the broker's delivery time. A tail on `api-a` gets logs ingested
  on `api-b`. Without the broker, Valkey Pub/Sub as before (`logs_livetail` `bus.go`).
  **Known gap:** the client's polling fallback (used when a proxy buffers the stream) reads stored
  logs, which with JetStream ingest lag by up to `INGEST_L0_MAX_AGE`; a polled tail shows new logs only
  once they are written.
- **Built (14-B, item 1 step 1e): the Valkey logs queue is gone.** Without the broker
  (`INGEST_BUFFER=valkey`, still the default and kept as a name so existing configs work), logs are
  written straight into the receiving replica's segment writer, as spans and metrics always were:
  same ~30 s freshness, and Valkey holds only locks, limits and caches. Decided 2026-10-01 not to make
  JetStream the default: that would give every single-node install a JetStream store on disk and
  freshness up to the L0 max-age. `LogWorkerService` and `obzly_logs_queue_length` (and its alert and
  dashboard panel) are removed. For one release each api/all replica drains, once at startup, any
  list a hard-killed older release left (`logs_receiving.DrainLegacyQueue`).
- **Built (14-B, item 1): the query-time tail.** With JetStream, rows are in no segment until a
  worker's L0 flush (up to `INGEST_L0_MAX_AGE`), which left the Logs page's newest minutes empty in
  results, counts, the histogram and facets. Those rows are exactly the batches still in the stream
  (workers ack only after upload), at most about `INGEST_L0_TARGET_BYTES` per project. So an `api`/`all`
  replica reads a project's pending batches with batched direct get (`broker.PendingIngest`, each
  request capped at 12 MB with `max_bytes` for the same slow-consumer reason; the
  ingest streams set `allow_direct`, since a work-queue stream rejects a consumer overlapping the
  shard consumers) and writes them to uncatalogued Parquet files under `LOGS_DATA_DIR/tail`
  (`logs_jetstream.Tail`); every logs query (`LogQuerier.queryFiles`: search, count, aggregations,
  patterns, field discovery, the SQL sandbox) adds them to its segment files - no merge logic.
  Queries never wait for the broker: they take the files built so far and start a background
  refresh when they are over 2 s old. A refresh drops delta files whose batches a worker has acked,
  re-reads one only partly acked, and adds a delta for batches newer than the last one seen, so it
  decodes only new data; past 8 deltas they merge Parquet-to-Parquet. (The first version rebuilt
  everything synchronously: ~5 s per rebuild on a busy project, with every Logs page query waiting
  on it.) A query skips the tail when a segment it planned became readable after the last refresh,
  so flushed rows are never counted twice; the newest rows then appear on the next refresh. The
  same tail serves spans and metrics (2026-10-01): `ingest.Tail` is signal-agnostic, each signal
  supplies its decode-and-write step (`logs_jetstream`/`spans_jetstream`/`metrics_jetstream.NewTail`),
  and the queriers add it through `segments.AppendTail` (logs `queryFiles`, spans/metrics
  `fromExprFor`), so traces and dashboards are current too. Tail files live under
  `LOGS_DATA_DIR/tail/<signal>`. Not covered: the catalog-based project stats.
- **Built (14-B, item 1): date-prefixed object keys, segment levels, batch deletes.** New uploads
  go to `<stream>/<project>/<yyyy>/<mm>/<dd>/<level>/<id>.parquet`, dated by the segment's newest
  row (UTC), so a bucket lifecycle rule on a date prefix (expire, or move to a colder storage class)
  only ever matches data at least that old; objects uploaded before keep the key their catalog row
  records, so nothing moves. `stream_segments.level` (migration `20261001100000`): 0 = a writer's
  flush, 1 = compaction output (`max(1, highest input level)`); rows from before read 0.
  `ObjectStore.DeleteBatch` deletes up to 1,000 keys per S3 `DeleteObjects` request on `s3://`
  buckets (AWS, MinIO - via the s3blob client, as gocloud blob has no batch delete) and runs 8
  parallel single deletes elsewhere; compaction and retention use it.
- **Built (14-B, item 1): leveled, shard-owned compaction.** `segments.PlanCompaction` replaces the
  three copies of "merge every small segment every pass": L0 runs (a writer's flushes) merge into L1
  once they reach 128 MB or 256 MB raw or 16 files, or once their oldest file is a day old (a lone
  old L0 is relabelled L1 without a rewrite); L1 runs of one UTC day (by earliest row) merge into a
  cold L2 file once they reach 512 MB (capped at 1 GB) or the day has been over for 6 hours, so cold
  data ends up as about one file per project per day. Each byte is rewritten about twice instead of
  on every pass. L1 merges sort by hour bucket first (`CompactionTimeColumn`), then
  `CompactionSortColumns`, so a row group spans about an hour; L2 merges concatenate those L1 files
  in time order and keep that. (2026-10-02: the first version sorted by service first and let L2
  span days, so a 24-hour search that touched a 3-day L2 had to read all of it; listing segments
  newest first for newest-first searches - `LogQuerier.newestFirstFiles` - cut that from 3.5 s to
  0.3 s, and hour-first, one-day files fix it at the source.) The compactor's DuckDB
  handle is limited to 1 GB, 1 thread and `preserve_insertion_order = false`, and its merges cap
  Parquet row groups at 64 MB (`ROW_GROUP_SIZE_BYTES`, which DuckDB allows only with insertion
  order off, so writer flushes don't use it); one pass merges at most 512 MB of input per project,
  so a backlog is worked off over several passes. **Why (2026-10-01):** the first version had no
  raw cap and sorted every merge with DuckDB's default limit (80% of RAM): on the test Site a
  ~3,000-file backlog of old L0 made the worker sort millions of rows per merge, take ~7 GB, push the
  host's load average past 200 for hours and get OOM-killed. Measured afterwards: these sorts do not
  spill (270 MB raw needed 256-512 MB), while an unsorted merge of the same data streams in under
  192 MB. Rows from before the level
  column that are 32 MB or more count as L1 (they were already compaction output). With JetStream,
  every `worker` compacts exactly the projects whose ingest shard it holds (`broker.OwnsProject`,
  fed by the registered shard runners) - one shard key and one lease for ingest and compaction - so
  compaction throughput scales with workers; without it, the per-stream cluster-wide lease remains.
  A handoff overlap is safe: `ReplaceSegments` refuses a swap whose sources are already gone.
- **Compaction in HA.** Leveled compaction L0 → L1 (128 MB) → L2 (512 MB–1 GB), promoting by age as
  well as size (older than ~1 day merges into L1 regardless of bytes). A fixed shard count per stream
  (e.g. 64) with one renewable `leaderlock` per shard replaces today's one lease per stream, so
  throughput scales with worker count. **One shard key and one lease cover both the JetStream
  consumer and compaction**, so the worker that wrote a shard's L0 files compacts them, often from its
  own disk. Crash safety is the existing temp-file → upload → single-transaction `ReplaceSegments`
  swap; a lost worker's shards move to others after the lease TTL.

### Parquet quality

`COMPRESSION ZSTD, COMPRESSION_LEVEL 3, ROW_GROUP_SIZE 262144, PARQUET_VERSION V2` (pin and record
the DuckDB engine version). Compaction-time sort (logs/spans by `project_id, service_name,
timestamp`; metrics by `series_id, timestamp`) — this supersedes the "never rely on file order"
note in `segments/compactor.go` for pruning effectiveness only; nothing relies on file order for
correctness. Per-segment stats (services, levels, metric names, promoted min/max) in a catalog
`stats JSONB`. Promoted hot JSON keys → typed columns is the one `schema_version` bump (2→3), with a
generalised `datamigration` rewrite. Bloom filters are evaluated, not assumed (DuckDB writes them
only for dictionary-encoded columns).

**Built (14-A, 2a part 1):** every segment file (writer flush, compaction merge, legacy rewrite) is
written through `segments.copyToParquetSQL` with the options above, verified against the DuckDB
engine the pinned go-duckdb v2.4.3 ships (v1.4.1); `format_test.go` checks the files come out
ZSTD. On 500k synthetic log rows: 39.6 MB (defaults, Snappy) → 23.1 MB (ZSTD 3) → 20.7 MB (+ V2),
−48%. The compactor sorts by `segments.CompactionSortColumns` — logs and AI logs by `service_name,
timestamp`, spans by `service_name, start_time`, metrics by `metric_name, labels, timestamp` until
a `series_id` exists — using only the sort columns the input files have (early logs lack
`service_name`); `project_id` is constant per file and not a key. Fresh flushes stay unsorted.
Sorting cost ~4% size on that set.

**Built (14-C, 2b): promoted log fields.** A project promotes up to 8 text and 4 number log fields
(`project_promoted_fields`, `GET/POST/DELETE /projects/{id}/promoted-fields`, "Promoted log
fields" in project settings). Logs files (schema version 3) carry fixed typed columns
`promoted_s1..s8` (VARCHAR), `promoted_n1..n4` (DOUBLE) and a per-row `promoted_mask` of the
promotions active when the row was written; the field also stays in `fields`, which remains
complete. A slot is used once per project (a retired field's column is never reused), so a column
means one field in every file and nothing is rewritten when a promotion is added or removed.
Reading: the where-builder emits a token per promoted field, and each fragment resolves it
against its own segments (`segments.ResolvePromotedFields`): the bare column when every file has
the promotion on every row (`stats.promoted_full`, the AND of the masks, kept on merge) - DuckDB
then never reads `fields` and pushes the filter into the scan - else `CASE WHEN promoted_mask has
the bit THEN column ELSE the JSON END`, which is also what tail files and pre-promotion files get.
Number fields compare numerically (`= 3` matches "3.0"; old rows via `TRY_CAST`) and support range
filters, which unpromoted custom fields don't. On 2M synthetic rows with a ~10-key `fields`, a
`status = '500'` count took 8 ms from the column vs 516 ms from the JSON (the mixed `CASE` form:
301 ms - it still reads `fields`). Old files, which lack the columns, are read through a schema
anchor (`segments.RegisterSchemaAnchor`): a zero-row Parquet file with the full current schema
added to every logs read, so `union_by_name` always yields the columns. Versions 2 and 3 merge
freely (into 3); the data migration relabels version 2 segments as 3 (no rewrite) and still
rewrites version 1 ones. Not done: backfilling old files' columns (they age out), the Live Tail
matcher reads `fields` as before (no need), and the SQL tab doesn't expose the columns.

**Built (14-A, 2a part 2): per-segment stats.** `stream_segments.stats JSONB` holds each segment's
distinct `service_name` (logs, AI logs, spans), `lower(level)` (logs, AI logs) and `metric_name`
(metrics), NULLs excluded, capped at 256 values per list (over the cap = unknown). The writer
computes them from its temp table before the COPY; a merge takes the union of its inputs' stats
without a scan (any input without stats makes the list unknown); the legacy rewrite reads them from
its output. Queries use them now, before item 3's SQL-side pruning: logs `ExecuteQuery`,
aggregations and patterns skip segments ruled out by `service_name` / `level` equals or IN at the
query root or under top-level ANDs (same string coercion and level lowercasing as the SQL; OR and
NOT never prune), and metrics skip segments without the `__name__`-equality metric. Rows without
stats, and legacy (schema v1) segments, whose names queries only see after the read-time rewrite,
are never skipped. Skips are counted in `obzly_query_segments_pruned_total{stream}`. Not yet:
the 1 TB measurement (needs item 11's `datagen`), GIN indexes and catalog-side filtering (item 3).

### Catalog

`stream_segments` partitioned `LIST(stream)` → `RANGE(min_time)` monthly, PK `(id, stream,
min_time)`, covering index, GIN on `stats` paths, a separate L0 partition. Queries bound
`min_time >= from − max_segment_span`, otherwise partitioning helps retention only. Retiring a month
= S3 batch delete, `DETACH PARTITION … CONCURRENTLY` (PG 14+) and `DROP TABLE`. `ListByTimeRange`
becomes `PlanSegments(project, stream, from, to, PruningHints)`. Rows gain `checksum_sha256`,
`etag`, `storage_class`, `last_verified_at`, `level`, `writer_id`.

**Built (14-B, item 3 part 1) — with one design change: `RANGE(max_time)`, not `min_time`.** A query
window is `min_time <= to AND max_time >= from`; with `max_time` as the key Postgres skips every month
that ended before `from` (all history, for the usual recent-window query) with no span bound, which
matters because segment times come from client timestamps and one backfilled batch can span years.
Retention already deletes by `max_time`, so a month can be dropped once every project's retention has
passed its upper bound. Layout (migration `20260930150000`): `LIST(stream)` for `logs`, `ai_logs`,
`spans`, `metrics`, each `RANGE(max_time)` by UTC month with a DEFAULT partition for out-of-range
times, plus `stream_segments_other` for any other stream; PK `(id, stream, max_time)`; indexes
`(project_id, max_time, min_time)`, `(project_id, min_time)` and the pending-upload/schema-version
partials. `obzly_ensure_segment_partition(stream, month)` creates a month (advisory-locked, moving
rows its default partition already holds); the `catalog-partitions` leader task keeps the current
month and three ahead. The one-time copy keeps the old table as `stream_segments_unpartitioned` for
one release. Measured on 5M segments (5,000 projects × 4 streams × a year): planning p95 0.15 ms flat
→ 1.2–1.5 ms partitioned (runtime pruning over ~50 partitions; well inside 50 ms); retiring one month
(411k rows) 6.4 s `DELETE` + `VACUUM` → ~70 ms `DETACH` + `DROP`; the copy took 1m44s with catalog
writes waiting, which is why it runs while catalogs are small.

**Built (item 3 part 2): `PlanSegments`.** `PlanSegments(project, stream, from, to,
PruningHints{Services, Levels, MetricNames})` is the one catalog planning call; `ListByTimeRange` is it
with no hints. Each hint list is the set of values every matching row's field must take (`nil` = no
constraint; several conditions on one field intersect, since a row has one value per field; empty =
nothing can match). The stats predicate runs in the catalog query — `jsonb_exists_any` over the
segment's list — and never drops segments without stats, lists recorded as unknown, or legacy (v1)
segments; a window count over the candidates keeps `obzly_query_segments_pruned_total` exact without
fetching pruned rows. Consumers: logs (service/level under top-level ANDs), metrics (`__name__`
equality) and span aggregations/operations (per-span service). Trace search does **not** prune by
service: its filter is trace-level (any span), and the trace's other spans live in segments without
that service. On a busy project (30,000 segments in 30 days, 20 services) a one-service 30-day plan
went 470 ms → 43 ms p50 in the app, almost all of it from no longer decoding the 28,500 rows the
filter drops. No GIN index: the per-project btree already narrows every plan to one project's window,
so GIN would only help cross-project catalog queries, which don't exist yet.

**Built (item 3 part 3): exact retention, empty months dropped (decided 2026-10-01).** Retention
stays per segment, exactly as each project configures it. Retiring whole months ahead of that would
only save anything if row deletes stopped for month partitions, keeping data up to a month longer
than configured; row-by-row retention costs ~6.4 s per month at the 1 PB ceiling, so exactness
wins. A month partition therefore empties on its own once every project's retention has passed it,
and the `catalog-partitions` task drops each ended, empty month (`DropEmptyPastPartitions`) so the
partition count — and planning time — stays bounded; months still holding rows (unlimited retention)
are kept. Each drop is one transaction that locks the stream's parent first (no insert can land
between the emptiness check and the drop, and queries, which lock parent before child, cannot
deadlock with it) under a 2 s `lock_timeout`, so it never stalls catalog queries behind a long
reader; a timed-out pass retries at the next run. A later backfill for a dropped month lands in the
stream's default partition. Counted in `obzly_catalog_partitions_dropped_total{stream}`. The
pre-partitioning copy, `stream_segments_unpartitioned`, is dropped by migration `20261001090000`
(partitioning shipped in v0.33.0, so the one-release rollback boundary never needs it). Still to do:
the audit columns with their consumers (items 6, 10c).

### Distributed query

- **Roles.** The coordinator runs in every `api` replica (any replica can take a query). Executors
  run as a `SERVICE_MODE=querier` Deployment on Spot, spread over AZs, with an `emptyDir` cache
  bounded by `COLD_CACHE_MAX_SIZE_MB` (`segments/cacheeviction.go`) - or, with the chart's
  `querier.persistence`, a StatefulSet with a volume each so the cache survives restarts (see
  *Querier cache* below). Below ~100 GB/day `all` and `api` run the executor in-process.
- **Flow.** Per-user limit in Valkey → `PlanSegments` + bytes-scanned budget → live executors from
  NATS micro-service discovery → **rendezvous hashing** of (segment, executor) so each segment keeps
  a warm owner → fragment request, cancel and progress over NATS request/reply → executor runs the
  fragment in DuckDB and returns Parquet over an internal, authenticated gRPC listener (never the
  OTLP `:4317` server) → coordinator merges with DuckDB over `read_parquet([...])`.
- **Failures.** A lost executor's segments are re-sent to each segment's next-ranked executor
  (read-only fragments, safe to retry); a lost coordinator surfaces a retryable error; an AZ loss is
  absorbed by the remaining zones; one broker loss is invisible; a whole-broker outage degrades to
  in-process execution.
- Keyset pagination `(timestamp, id)` replaces offset + count, with the offset API kept for one
  release. The user SQL sandbox runs on one executor. Scaling moves only ~1/N of segment affinity.
- **As built (`internal/query`, 4a-4b).** A fragment is SQL over `{{source}}` plus the segment
  records it reads; the final SQL runs over `{{results}}`. Executors register as the NATS micro
  service `obzly-query` (subject `obzly.query.exec.<id>`, result address in its metadata);
  coordinators ping it every 5 s and drop an executor after 3 missed pings. Only segments with an
  object key are sent out; the query-time tail and not-yet-uploaded segments run in-process. A
  failed fragment's segments go to their next-ranked executor (2 tries), then in-process. Results
  are fetched over `QUERIER_GRPC_ADDR` (default `:4319`, `/obzly.query.v1.Results/Fetch`, a stream
  of 1 MB chunks) with `QUERIER_TOKEN` as a bearer token, and removed once fetched (unfetched
  ones after 5 min). One fragment in-process runs the final SQL itself, so a single replica pays
  no merge. Settings: `SERVICE_MODE=querier` needs `NATS_URL`, `NATS_TOKEN`, `QUERIER_TOKEN` and
  `OBJECT_STORAGE_URL`; `QUERIER_ADVERTISE_ADDR` (default hostname and port; the pod IP on
  Kubernetes), `QUERIER_CONCURRENCY` (default CPU count). `api` replicas use executors when
  `QUERIER_TOKEN` is set with `INGEST_BUFFER=jetstream`. Coordinators sign each fragment with an
  HMAC-SHA256 keyed by `QUERIER_TOKEN` (header `Obzly-Fragment-Signature`) and executors refuse an
  unsigned or wrongly signed one, so holding `NATS_TOKEN` alone no longer lets a client send an
  executor SQL; the broker still stays inside the Site like the result listener. The executor's one
  DuckDB handle, shared by every concurrent fragment, is capped at 60% of the pod's memory limit
  and spills under `LOGS_DATA_DIR/query-results/spill`; DuckDB's default of 80% left the Go side
  (result streaming, the cold cache) little room before an OOM kill. Every DuckDB handle (queriers,
  SQL-mode sandboxes, compactors, token filters) spills to a directory of its own (`h-*` under the
  spill root, removed on close, leftovers removed at start): handles sharing one directory wrote
  spill files with the same names and crashed the process. Query handles cap their spill files at
  `QUERIER_SPILL_MAX_SIZE`.
- **What runs where (4d).** Through fragments: logs search, count, histogram/aggregations,
  patterns, field discovery; trace search (per-trace partials, filters applied to the merged
  trace), get-trace, services/operations, k8s metadata (latest value per fragment, then latest
  of those), span count/error/avg aggregations. One in-process query (`SingleFragment`): span
  percentiles, the service map join (parent and child can be in different segments), and the user
  SQL sandbox. Metrics are still in-process until item 5. Rendezvous scores mix both hashes
  (splitmix64): plain FNV of ids differing in one character gave one executor most segments.
- **Scan budget (4e).** `QUERY_MAX_SCAN_GB` (default 100, `0` off) caps the compressed bytes of
  the segments one user query plans to read: over it, a histogram, dashboard panel, pattern list
  or trace search is refused before any I/O (HTTP 422, `code: QUERY_TOO_LARGE`, the message names
  both sizes), and a search skips only its count (`countSkipped`, `total` -1) - its page is a
  top-N that reads far less than it plans. The budget rides on the request DTO (`ScanBudget`,
  never from JSON) and only HTTP services set it, so background work (quota passes, correlation)
  is never limited, even in `SERVICE_MODE=all`.
- **Pagination (4c).** `POST /logs/query/execute` returns `nextCursor` (opaque: the last row's
  timestamp and id) on a full page; sending it as `cursor` returns the rows strictly after it in
  `(timestamp, id)` order - id breaks timestamp ties, so pages never repeat or skip a row and
  new rows at the newest end don't shift them. Cursor pages aren't counted (`total` -1), nor is a
  request with `trackTotal: false`; absent `trackTotal` counts, so older clients see no change.
  Without a search filter a segment wholly inside the window counts from its catalog
  `row_count` (segments hold one project's rows) when it is readable (on disk, or uploaded with
  object storage configured; a missing file is skipped by the query, so by the count too); only
  boundary segments and the tail are read.
- **Search partitions (4f, after OpenObserve).** A page search no longer reads its whole range: it
  walks time windows from the newest end (oldest for `sortOrder: asc`) - 5 min, then ×4 (20 min,
  80 min, 5 h, 21 h, 3.5 d, ...) - each a normal coordinator query over the segments overlapping
  it, with `LIMIT` the rows still needed, and stops once `offset + limit` rows are in
  (`logs/querier/partition.go`, `selectPartitioned`). The newest 100 rows of a 7-day search come
  from its first minutes, so the rest of the week is never downloaded or scanned. Windows are
  disjoint and ordered, so the page equals the one-shot query's; the window walk is sized from
  where segments have data (an open-ended search doesn't walk empty years) but always covers
  `[from, to]`; pending tail files go into every window (late rows of any age); a cursor starts
  the walk at the cursor; an offset over 10 000 still reads the range at once.
  `obzly_search_windows_total` counts windows read.
- **Streamed search.** `POST /logs/query/stream/{projectId}` takes the execute request and answers
  NDJSON as the walk goes: `rows` (a window's rows past the offset, in page order), `progress` (a
  window read without any), then `done` (`nextCursor`, `total`, `executedIn`) or `error`. Access,
  validation and concurrency errors stay plain JSON before the first frame (as Live Tail); the
  client going away cancels the search; the control plane's proxy streams it (`FlushInterval:
  -1`). The Logs page uses it with `trackTotal: false` and shows rows as they arrive; a Site
  without it (or a refused request) falls back to `execute`.
- **Total from the histogram.** Counting reads the whole range, so the Logs page no longer asks
  the search for it: the histogram (the same query and range, started alongside the search)
  sums to the total, shown as "N+" until it arrives.
- **Partition result cache (4g, after OpenObserve).** A time-bucketed aggregation (the histogram,
  dashboard series) is computed per partition - an hour, or one bucket if wider, on a fixed grid
  from the epoch (`logs/querier/aggregate_partitions.go`). Every whole partition that ended more
  than `QUERY_CACHE_DELAY_SECONDS` (300) ago is kept in Valkey for `QUERY_CACHE_TTL_HOURS` (24, `0`
  off; key `query_partition:logs:<project>:<hash of query, group, bucket>:<partition>`); a later run
  reads those and computes the rest - partial edge partitions, the newest minutes and misses - in
  one query that keeps the partition next to each bucket so each is stored on its own. A bucket
  split over two partitions is summed back, as fragments are, so results are identical. Late rows
  older than the delay and retention deletes show only after the TTL. `obzly_query_partition_cache_total`
  counts hits and misses.
- **Per-minute log counts (`logs_1m`).** Every compacted logs segment (L1 or cold) gets a rollup
  segment - like the metrics rollups, catalogued in its own stream with `stats.source` naming the
  segment - holding, per minute, the count of its logs per level, service, namespace, container and
  `pattern_hash` (`logs_compactor.RollupSegments`, after each compaction pass: up to 10 segments a
  project a pass, newest first, starting none after a minute; a fresh merge is still on disk, an
  older segment is downloaded and removed again). The pass uploads its merges before building any
  counts: a merge replaces uploaded segments with a file only on the worker's disk, which queries
  can't read until it is uploaded. A segment changes only by being merged into a new one with its own counts, so a
  source's counts never go stale; counts whose source is gone are deleted the next pass. A bucketed
  aggregation of whole minutes grouped and filtered only by those dimensions - the Logs histogram -
  reads, per partition, the counts' whole minutes for segments that have readable counts, and the raw
  logs for the partial edge minutes, segments without counts (the newest day's L0s) and the tail,
  summed per bucket (`logs_querier/aggregate_rollups.go`); results equal the raw counts exactly.
  Time presets start on a whole minute, so the range's oldest edge needs no raw read. The counts
  also hold the pod, for deploy markers.
- **Streamed aggregations.** `POST /projects/dashboards/{projectId}/query/stream` answers a panel
  query as NDJSON frames (after OpenObserve's streaming search): a time-bucketed logs panel reads
  its uncached partitions in groups newest first - one partition, then 4, 16... - and sends each
  group's counts as a `buckets` frame to add to the earlier ones (cached partitions in the first
  frame), with heartbeats and an `error`/`done` frame, like the streamed search. Any other panel is
  one frame. The Logs histogram and dashboard panels fill in as frames arrive (`mergeBuckets`) and
  use the one-shot query on an older Site; the total and level counts are reported once all are in.
  A retry after a vanished segment happens only while nothing was sent.
- **Column-only reads of cold segments.** An executor reading a large (>= 64 MB) segment that is
  neither local nor cached fetches only the columns the fragment's SQL names
  (`segments.ReferencedColumns` against the stream's schema; `SELECT *` reads all) - the Parquet
  footer, then those columns' chunks and bloom filters (`parquet_metadata`), by ranged reads - into a
  sparse cache file at their own offsets (`<id>.partial.parquet`, its columns in `.cols`); DuckDB
  reads only the footer and those chunks. A later fragment adds the columns it lacks. Legacy
  segments, and reads needing over 60% of a file's bytes (a search's message and fields), download
  it whole as before. Cache eviction counts a file's allocated blocks. `QUERY_PARTIAL_DOWNLOADS=false`
  turns it off; `obzly_query_partial_reads_total` counts fetched and cached partial reads.
- **Querier cache (4h).** Segments are spread over executors by an *affinity* id, not the NATS
  instance id: the executor's `.writer-id` on its data volume, advertised in its service metadata
  (`query.Ranked`), so with a persistent volume a restarted querier gets back the same share and
  its cache stays useful. On start it downloads its share of the last `QUERIER_WARM_HOURS` (24, `0`
  off) of uploaded segments, newest first, up to half the cache (`segments.WarmCache`); and every
  uploader announces each upload on core NATS `obzly.segments.uploaded`, so the owning querier
  downloads it right away from a bounded queue (`query.ServePrefetch`; OpenObserve's download on
  ingest). A missed notice only means a query fetches the file. The cache evicts least recently
  *used* first: a cache hit sets the file's mtime (`resolveFile`), so files a running query just
  resolved and the newest data warmed first at start-up are not the first to go. Before, eviction
  went by download time, and a querier near `COLD_CACHE_MAX_SIZE_MB` deleted files queries were
  about to open (`IO Error: Cannot open file`, fragments retried elsewhere). Size the cache to
  hold a querier's share of the range people search, with room to spare.
- **Deadlines.** A fragment has 170 s (`query.fragmentTimeout`, was 120 s), under the control
  plane's 180 s wait for a Site (`siteResponseHeaderTimeout`, was 60 s), so a slow search ends with
  the Site's 408; load balancers in front of both need an idle timeout of at least 180 s. A page
  search's whole window walk also has 170 s (`searchDeadline`): past it the page comes back with
  the rows found, `partial: true` and `searchedThrough` (how far back it got) - a rare term no
  longer walks a week window by window for many minutes. A streamed search sends a `heartbeat`
  frame every 10 s while a window runs; the Logs page cuts off a stream silent for a minute.
- **Cancellation.** A coordinator whose context ends before an executor answers (the browser left,
  a newer search replaced it, a deadline) publishes `obzly.query.cancel.<fragment id>`; the
  executor cancels that fragment, running or still queued for a slot, and DuckDB stops. The
  streamed search, facets and log panel queries (histogram, dashboards) run on the HTTP
  request's context, and the Logs page aborts them when the search changes - abandoned searches
  no longer hold querier slots until their deadline.
- **Token filters (4i, after OpenObserve's full-text index).** Before a log segment is uploaded, its
  ID-like tokens - maximal runs of 12+ letters and digits holding a digit, and UUIDs - go into a
  bloom filter (1% false positives, `segments/tokenbloom.go`) uploaded beside it as
  `<key>.bloom`; `stats.token_bloom` records it. A search whose message condition reached through
  ANDs is an ID-like term sends the token with its fragments, and each executor checks the
  segment's filter (reading only the token's bits, from disk, its cache or a one-time download)
  and skips segments without it - never downloaded or scanned. Search, count, histogram,
  severity counts and patterns all use it; a 7-day lookup of one request id reads a handful of
  files. ID-like terms match as whole tokens (`contains()` then a boundary regex), so a skipped
  segment never held a match; other terms keep substring matching. Each logs reconcile pass
  backfills up to 20 segments uploaded before filters existed (newest first); a failed backfill
  is marked `token_bloom: false` and scanned. Filters go with their segments on deletion and
  are fetched by warm-up and pre-fetch. Building one costs ~1 s and ~2 MB per million rows.
  `obzly_token_filter_skipped_segments_total`.
- **Text search.** A `contains` condition compiles to DuckDB `contains(column, ?)`, not
  `LIKE '%v%' ESCAPE '\'`: the ESCAPE clause turns off DuckDB's substring fast path (3.4 s against
  0.96 s over 20M log-like rows). Same matches, case-sensitive, `%` and `_` literal.

### Metrics

Two streams: `metrics_series(series_id, metric_name, labels MAP, first/last_seen)` and
`metrics_samples(series_id, timestamp, value)` sorted by series; matchers compile to DuckDB
predicates on the series stream; a Postgres label index serves `/labels`, `/label/<n>/values`,
`/series`; `metrics_5m` / `metrics_1h` rollups selected by range and step.

As built so far (5a): on today's single `metrics` stream, matchers compile to DuckDB predicates
(`matcherSQL`: a label is `coalesce(json_extract_string(labels, '$."name"'), '')`, so an absent
label reads as `""` as in Prometheus; `=~`/`!~` are `regexp_full_match`, RE2 like Go, validated in
Go first) and series select, metric names and label names/values run through the distributed
query coordinator. A query without a metric-name equality used to pull every sample in the window
into Go; now executors return only matching rows.

5b is additive, so nothing written before it needs a rewrite: samples keep their `labels` column
and gain `series_id` (`segments.SeriesID`: xxhash64 of the normalized name, a NUL, and the
canonical labels JSON; the DuckDB function `obzly_series_id` is the same code). Sorted L0->L1
merges fill it for current-version files that predate it (legacy v1 files hash un-normalized
names, so they are left alone; cold L2 merges concatenate and can't), and sort metrics by hour,
`metric_name`, `series_id`, `timestamp`. A `metrics_series` stream then holds one row per series
(first/last seen) for label lookups, and series select becomes two-phase - matchers on the series
stream yield ids, samples are read by id - with 5a's label predicates for rows that have no id.

As built (5b-2): the metrics ingester keeps a per-project buffer of the series its samples used
(first/last seen, latest type) and writes it as a `metrics_series` segment whenever that project's
samples flush; on JetStream the sink flushes, uploads and acknowledges both together. Merges of
that stream collapse rows per series at every level (`segments.DeduplicatingMerges`), its
catalog time is `last_seen` (so retention drops a series quiet for the retention period), and it
has its own catalog partition. `metric_series_coverage` records each project's first series
flush: a lookup window starting at or after it reads the series stream (plus the query-time tail's
samples), an earlier one reads samples as in 5a, since samples ingested before the stream existed
have no series rows. Label names/values and metric names use it.

Series select (5b-3) in a covered window runs in two phases: the matchers on the series stream
(and the tail's samples) give each series' id, name and labels; samples are then read by
`series_id IN (...)` and `metric_name IN (...)` (the names also feed catalog pruning), returning
only id, time and value, and the labels come from phase 1. Ids and names travel as one
comma-separated string each, so a remote executor binds them like any argument; past 100,000
series (`maxSeriesIDs`, a broker message limit concern) or before coverage it filters samples by
matchers as in 5a.

The label index (5c) is two Postgres tables, `metric_label_index (project, metric_name,
label_name, label_value, first_seen, last_seen)` and `metric_name_index (project, metric_name,
metric_type, first_seen, last_seen)`, upserted (earliest first, latest last seen) from each series
flush in batches of 5,000 through `unnest` of array literals, deleted with the project and pruned
by retention on `last_seen`. It holds label values per metric, never per series, so it answers
label names, label values and metric names only when the matchers are none or one metric-name
equality and the window is covered; the query-time tail's samples are added on top. Anything else
goes to the series stream, and `/series` always does.

Rollups (5d) are M4 downsampling: `metrics_5m` and `metrics_1h` keep, per series and bucket, the
first, last, min and max samples with their timestamps, plus count and sum
(`segments.BuildRollup`). The compaction pass builds both for each cold (L2) metrics segment that
lacks them - cold segments are final, so a rollup never goes stale - at most 1 per project per pass
so existing data backfills gradually; each rollup's `stats.source` is its segment's id, they are
written at the final level so they are never merged, and retention and deletion treat them like
the samples. A select whose PromQL step is at least a bucket, and whose range window (if any) spans at
least 5 buckets (`rollupFor`), reads the rollups of the
cold segments in the window, presented as samples (each bucket's four points unnested), plus raw
samples for every segment not rolled up yet (L0/L1, the tail); a rollup whose segment is gone is
ignored. The caveat: rollups return real samples, so line charts look the same and
`max/min/last_over_time` are exact. `rate`/`increase` miss a counter reset inside a bucket, and
extrapolate from the bucket's sample spacing where a series starts or ends inside the window - on the
test Site `rate(x[1h])` over 1 h buckets read up to 43% high for churning pods, hence the 5-bucket
rule (over 5 m buckets: within 6%); `count_over_time`, `sum_over_time` and
`avg_over_time` see 2-4 points per bucket instead of every sample, so they are approximate at
coarse steps.

PromQL push-down (5f): every PromQL query (API, dashboards, the Prometheus API) goes through
`metrics_promql.NewRangeQuery` / `NewInstantQuery`, which hands each `rate`, `increase` or
`sum/avg/min/max/count_over_time` over one metric's selector - optionally inside
`sum/avg/min/max/count by (...)` - to storage (`EvaluateRangeFunction`), then gives the engine
the query with that subtree replaced by a series holding the result at every step (a stale
marker where there is none; `+ 0` drops its name as the function would). The engine still
evaluates everything else, so `a / b`, `topk(...)` or `histogram_quantile` over pushed-down
operands work unchanged. In storage, the steps are split into contiguous time slices, one per
remote executor (one in-process without any; `query.Request.Slices`). A slice reads every segment
and tail file overlapping (its first step - range, its last step], so it holds every sample its
steps need and computes them whole: it turns the samples into one partial per series and step -
count, first and last samples, counter resets inside the window, sum, min, max - by copying each
sample to the steps whose window holds it and grouping (keyed by `hash(labels)`; per-row window
frames and string keys were 3-5x slower), applies Prometheus's `extrapolatedRate` in the same
float order, and aggregates. The coordinator only concatenates slices, so only groups x steps
leave an executor and the 2M-raw-sample cap no longer applies. A slice goes to the executor
ranked best for its largest segment (where it is most likely cached), one slice per executor
where possible; it runs in-process if it needs a tail file, and a failed one is retried whole on
one other executor, never split. Storage declines - and the engine reads samples as before - when
it can't match the engine exactly: no metric-name equality (the engine would reject duplicate
label sets), `offset`/`@`, `without`, a subquery, more than 32 steps per sample, rollups in the
window, or two samples of a series in the same millisecond (a series in both the tail and a
segment). `obzly_promql_pushdown_total
{result=pushed|fallback|unsupported}` counts each. Results equal the engine's to float rounding
(sums in another order); the parity suite checks every query both ways.

### Retention and lifecycle

`retention_policies` (org or project scope, per stream, hot/warm/cold days, storage classes, sample
rate, tail rules, rollup-only aging) replaces `MaxLogsLifeDays`. Lifecycle by prefix per
stream/level/date and by object tags for per-project schedules (lifecycle rules cap at 1,000 per
bucket); never transition objects under 1 MB. `DeleteObjects` batch deletes. Trace tail sampling is
deterministic: keep when `error OR slow OR hash(trace_id) < rate`.

Retention policies (6a, as built): `retention_policies (project_id, signal)` - one row per signal
(`logs`, `traces`, `metrics`), `GET/PUT /projects/{id}/retention-policies`, the "Retention &
sampling" section of project settings. `retention_days` (0 = the project's `max_logs_life_days`,
capped by a plan's) drives each signal's retention pass; `rollup_days` is 6b's below. Org scope
waits for item 9. Sampling, one deterministic decision (`util/sampling`: DuckDB's
`md5_number(id) % 1e6` below `rate x 1e6`, mirrored in Go and tested equal, so a trace's logs and
spans agree at one rate):
- Traces (`sample_rate`, `slow_ms`): tail sampling when a day's spans merge into cold (L2) files -
  the first point where every span of the day is together. A trace is kept if any of its spans
  that day, in any of the day's files at any level, is an error or at least `slow_ms` long, or if
  its id hashes under the rate; every merge of that day, in any pass or replica, decides each
  trace the same way, so traces are kept or dropped whole (the fan-out test checks 0 partial
  traces, late spans included). A lone L1 file of an ended day is now rewritten cold too, so every
  day is sampled (and, for metrics, rolled up). Limits: the last ~30 h stay unsampled until their
  day merges; a trace crossing UTC midnight is decided per day; an error span arriving after its
  day merged doesn't bring back spans already dropped. `obzly_spans_sampled_out_total`.
- Logs (`sample_rules`: `[{service, level, rate}]`, first match wins, unmatched kept): head
  sampling at ingest before the broker publish, by trace id when present (else the log id);
  sampled-out logs still count as accepted. Rules are cached 30 s per replica.
  `obzly_logs_sampled_out_total`.

Object tags for lifecycle rules (6a): every upload in the date-prefixed layout is tagged
`obzly-stream`, `obzly-project`, `obzly-level` (from its key; S3 only). Prefix rules cover stream-,
date- and level-wide transitions; per-project schedules use tag filters (a bucket allows 1,000
rules). Keep `ObjectSizeGreaterThan` at 1 MB or more (smaller objects cost more to transition than
they save). Example - spans of one project to Infrequent Access after 30 days:

```json
{"Rules": [{"ID": "spans-acme-ia", "Status": "Enabled",
  "Filter": {"And": {"Tags": [{"Key": "obzly-stream", "Value": "spans"},
                              {"Key": "obzly-project", "Value": "<project id>"}],
                     "ObjectSizeGreaterThan": 1048576}},
  "Transitions": [{"Days": 30, "StorageClass": "STANDARD_IA"}]}]}
```

Not yet: a `storage_class` on catalog rows so the planner warns about or refuses Glacier reads
(only transitions that keep objects instantly readable - Standard-IA, Glacier Instant Retrieval -
are safe today).

Rollup-only aging (6b, as built): a metrics policy's `rollup_days` (0 = off) keeps metrics'
`metrics_5m`/`metrics_1h` rollups after raw samples age out at the metrics retention. The retention
pass (`MetricCompactor.AgeOut`) builds any rollup a retiring raw segment is missing (keeping the
segment for the next pass if that fails), marks its rollups `stats.aged`, then deletes it; the
rollups, the `metrics_series` stream and the label index age out at the rollup cutoff instead, so
label lookups and the series-id select keep working over aged time. Queries read aged rollups at
any step - the 5 m one below a 5 m step - as the only data left for that time (M4 points: charts and
`max/min/last_over_time` look the same, sums and counts are approximate; see the rollup caveat).
An aged rollup whose segment is still present (its deletion failed) is not read, and the
compaction pass deletes a rollup whose segment vanished without aging (e.g. a data-migration
rewrite), so samples are never counted twice.

### Self-observability

`promhttp` on an internal `METRICS_ADDR`, never proxied: ingest, JetStream stream fill and consumer
lag, broker Raft health, segments by level, compaction backlog, query duration and bytes scanned,
cache hit ratio, leaderlock holders, S3 requests by op. SLOs: ack p99 <500 ms including the
JetStream commit; ingest→queryable <2 min p95 for high-volume pairs; consumer lag <5 min; query p95
<5 s for 1 h windows under budget; compaction backlog <10 min.

**Built (14-A, part 1):** `internal/telemetry` serves a private registry on `METRICS_ADDR` (default
`:9464`, `127.0.0.1:9464` in development, `off` disables) in every `SERVICE_MODE`, including
`worker`, which has no HTTP server. Recorded where the work happens, not per feature: HTTP latency
by route template and status (gin middleware), OTLP gRPC latency by method and code (interceptor),
accepted/rejected rows per signal at each receiver, the segment writer (rows/bytes appended and held
in RAM, flushes by result, rows lost to a failed flush, file size, flush lag = ingest→queryable on
that replica), object storage requests/latency/bytes by op and result (the request-cost guardrail),
segment reads per query by source (local, cache, object store = cold-cache miss, missing) and bytes
scanned, every leader-locked pass (runs by ok/skipped/error, duration, last success, lock held), the
Postgres pool, Go runtime and process. The three queriers' identical segment-resolution code moved
into `segments.ResolveFiles` so reads are counted once. A 15 s sampler reports the cluster-wide
gauges — Valkey logs-queue depth on ingest replicas, catalog segments, small-segment (compaction)
backlog, pending uploads and oldest-pending age on worker replicas — aggregate those with `max()`.
Rules and alerts for every SLO that has a metric today ship in
`deploy/observability/prometheus/obzly-rules.yaml`, validated by a Go test against the metric
definitions. **Part 2:** `obzly_duckdb_memory_bytes` / `obzly_duckdb_temporary_storage_bytes` per
handle kind (writer, querier per stream, compactor) from `duckdb_memory()` at scrape time — each
handle defaults to a `memory_limit` of 80% of RAM, and a process opens about a dozen, so their sum is
what shows OOM risk; and a seed Grafana dashboard, `deploy/observability/grafana/obzly-overview.json`,
checked by the same test. Self-tracing is part 3, below. Still to do for item 7: the JetStream/broker
metrics once item 1 exists (the Valkey queue stands in for consumer lag until then).

**Self-tracing (built, part 3).** `SELF_TRACING_SAMPLE_RATE` (default 0.01, `0` off) of each signal-
storage replica's HTTP requests, OTLP gRPC calls and leader-locked passes are recorded as traces in
the Site's reserved project, "Obzly (system)" (`projects.is_system`, one per Site by a unique partial
index, created idempotently at startup by `ProjectService.EnsureSystemProject`). It has no members,
so only global admins see it; it requires an API key and has none, so nothing outside the process
ingests into it; it keeps 7 days and cannot be deleted. Spans are root spans with the method and
route template (or gRPC method, or task name) and status only — never query strings, headers or
client addresses — and never adopt an incoming `traceparent`, so a client cannot force sampling. The
loop guard is the exporter itself: a batch processor writes spans straight into the local spans store
(`spans_core.StoreSpansBatch`), never over HTTP/OTLP, so storing them produces no request to trace.
The control plane stores no signals and does not trace. Buffered spans are flushed before the final
segment flush at shutdown.

### Alerting

Rules, states, channels, routes, silences and a Postgres notification outbox; the evaluator runs in
`worker`, sharded by rule hash with one lease per shard; PromQL and log/span aggregations reuse the
existing engines. Channel secrets use `SecretKeyRepository` AES-GCM (guards leakage, not a database
dump). Webhook egress uses the pinned resolve-check-dial pattern with an alerting policy that denies
private, loopback, link-local, CGNAT and reserved ranges by default - including an IPv4 address
NAT64 or 6to4 embeds, and Teredo - refuses redirects and caps response size.

**Built (8):** `features/alerting` and the project "Alerts" tab (Active, Rules, Channels,
Silences). Tables `alert_rules`, `alert_rule_channels` (a rule's channels - the "routes" of the
design, project-scoped), `alert_states` (per rule and label fingerprint), `notification_channels`
(config AES-GCM), `silences`, `notification_outbox`.
- Rules: `metrics` (an instant PromQL query, each series checked), `logs` (count of logs matching
  a query in a window, optionally per field), `log_patterns` (one series per message pattern of the
  logs matching a query - usually errors - that is new against the baseline before the window, an
  hour to 7 days, or whose rate rose to at least the threshold times the baseline's; the value is
  that ratio, a very large number for a new pattern; `minCount` skips rare ones;
  `ExecutePatternChanges` underneath) or `traces` (count, errors, avg/p50/p95/p99 duration
  of a service/operation's spans in a window, optionally per service/name/status/kind), compared to
  a threshold. `interval` (>= 15 s), `for`, `repeat` (re-notify while firing; 0 = once), labels and
  a summary. A preview endpoint (project owners and admins) runs the query once without changing
  anything. Logs, log-pattern and trace rules run under the `QUERY_MAX_SCAN_GB` budget.
- Evaluation: `ALERT_EVAL_SHARDS` loops in `worker`/`all`, each under leader lock
  `alert-eval-<n>-of-<shards>` (renewed while a pass runs; a pass that loses it is cancelled),
  ticking every 10 s and running the shard's due rules (`fnv(rule id) % shards`), 8 at once. Each
  rule is claimed first by a compare-and-set on `last_eval_at`, so overlapping passes never
  evaluate it twice. The Prometheus state machine (pending after the condition holds,
  firing after `for`, resolved when it stops; a pending series that stops is dropped) writes states
  and outbox rows in one transaction; a failed query records `last_error` and changes no state.
  Alerts are labelled with the rule's labels, the series' and `alertname`.
- Delivery: the outbox is claimed `FOR UPDATE SKIP LOCKED` every 5 s by any worker, 50 rows a
  batch, with a 5-minute lease committed before sending, so no transaction is held across a send
  and a dispatcher that dies mid-batch only delays its rows; 8 sends in flight, each result
  recorded on its own; silences (label matchers, `=` or anchored regex) apply at dispatch; a
  failure backs off from 10 s doubling to 15 min and gives up after 8 attempts, and 5 failures in a
  row pause a channel for a minute. Sent and given-up rows are deleted after 7 days (leader lock
  `alert-outbox-prune`, hourly, 1,000 rows a statement). Webhook (JSON body
  `obzly.alert/1`, optional headers), Slack (incoming webhook text with `&`, `<`, `>` escaped, so
  telemetry can't mention or link), PagerDuty (Events API v2,
  `dedup_key` = rule:fingerprint, resolve on recovery) and email (the SMTP relay set in Obzly settings - since v0.55.0, before that `ALERT_SMTP_*` -
  STARTTLS or TLS 1.2+, channels hold recipients only). Channels themselves are defined in Obzly
  settings and shared across projects; on a control plane they are central and Sites pull them
  (`sharedconfig`): sealed to the Site's pinned X25519 key, requested with an HMAC under the key
  the Site and control plane share (so only the Site can fetch it), the Site reporting the version
  it applied; a changed Site key waits for an admin to accept it. HTTP sends go through `util/egress` (shared with the
  control plane's Site client and OAuth client-metadata fetches): every resolved address checked,
  public only unless `ALERT_EGRESS_ALLOWLIST`, `ALERT_EGRESS_DENYLIST` always, no redirects, no
  environment proxy, 10 s timeout, 64 KB response cap; a channel test reports only the receiver's
  status. `obzly_alert_notifications_total{result}` and `obzly_alert_notification_latency_seconds`.
- Authz: rules - project owners and admins; silences - members who can write; channels - the
  project owner or a global admin; reading - any member, though a channel's target (hosts, email
  recipients) is shown to project owners and admins only and no project sees which other projects
  share a channel. Routes are under `/projects/{id}/...`, which the control-plane proxy already
  allows.
- **Managed rules (v0.56.0):** a feature can keep rules it owns in a project
  (`alert_rules.managed_by`, a key such as `synthetics:<monitor>:down`). Synthetics monitors with
  alert channels get a down rule, and optionally degraded and certificate rules, in their
  destination project: the control plane adds them to the shared-config document
  (`sharedconfig.SetMonitorAlerts`), and each Site upserts them and deletes stale ones
  (`alerting.ApplyManagedRules`; a channel the Site doesn't have for that project is left off the
  rule with a warning). They show in Alerts read-only; editing or deleting one answers
  409. A child monitor's down and degraded rules end with `unless on()` its parents' failure
  queries when they share the project, so an outage notifies once, for its root cause.
- Measured: rule to notification 15.2 s with the real loops (target < 60 s). Not yet: grouping
  several alerts into one notification (`group_wait`), org-scoped channels (item 9).

### Organizations, teams and SCIM

`organizations`, memberships, teams and team project roles; a `Default` org for existing installs;
effective role = max(direct, team, SSO mapping). Authentication is unchanged. Existing SSO group
mappings attach to the `Default` org as they are. SCIM 2.0 with a per-org token stored as a SHA-256
hash like `access_tokens`. Proposed: orgs live on the control plane and span Sites, with no
fan-out to Sites (grants resolve lazily at token exchange).

**Built (9a, organizations and teams):** decided 2026-10-06 - organizations live where users are
managed (the control plane, or a standalone install as its own) and span Sites. Tables
`organizations`, `organization_memberships` (ORG_OWNER / ORG_ADMIN / ORG_MEMBER),
`organization_sites` (a Site belongs to one organization), `teams`, `team_memberships`,
`team_grants` (a team's role on a Site - site-wide ADMIN/MEMBER/VIEWER or PROJECT_* on a project -
with site `''` meaning a standalone install's own projects, where site-wide is MEMBER/VIEWER only).
The migration creates a `Default` organization: admins own it, other users are members, every Site
belongs to it; access is unchanged. Effective access is the strongest of a user's own roles and
their teams': on a control plane `GrantService.ResolveEffectiveGrant` (per proxied request and
per token exchange) and `ListForUser` (session revocation) include team grants; on a standalone
install the project-role lookup and project list do (`effectiveRolesSQL`; a team never makes an
owner, a site-wide grant skips the system project). SSO group mappings are untouched and teams only
add, so every pre-upgrade mapping resolves to the same role (test). Changes to a team's members or
grants invalidate the affected users' cached Site tokens. API `/api/v1/orgs/...` (global admins
create/delete organizations and assign Sites; owners and admins manage members, teams and team
grants, only owners make owners, the last owner stays; members read) - on the control plane and on
standalone installs, not on Sites enrolled in a control plane, and not proxied. UI: the
Organizations tab.

**Built (9b, SCIM 2.0):** `/scim/v2` (Users, Groups, ServiceProviderConfig, ResourceTypes,
Schemas) on the control plane and standalone installs, authenticated by an organization's SCIM
token (`scim_tokens`: prefix + SHA-256, shown once, owners and global admins only - a token can
create accounts). Users are the organization's members (`userName` = sign-in email, `externalId`
per membership); Groups are its teams (roles stay team grants, set in Obzly). Ownership decides
what deprovisioning does: an account the organization's SCIM created (`users.scim_org_id`) and
that isn't a global admin is deactivated (sessions revoked) on `active=false` and DELETE, and only
it can be renamed or re-emailed by SCIM. SCIM never joins an existing account by email (any
organization's IdP could pull in, and see the name of, anyone's): creating one answers 409, and an
owner adds it as a member, which the IdP then matches with a `userName` filter. Any account SCIM
didn't create (added by an owner, or a global admin) only has its membership suspended (`organization_memberships.suspended`), so the
organization's teams grant nothing, on the control plane and locally, and a suspended owner or
admin can't manage the organization (or mint a SCIM token to reactivate themselves). Accounts get no password and
sign in with SSO. Filters: `attribute eq "value"` on userName, emails.value, externalId, id
(users) and displayName, externalId, id (groups); PATCH in Okta's and Entra's forms (pathless
values, `members[value eq ...]`, capitalised ops, string booleans); unsupported attributes are
ignored.

**Built (9c, one authorization path):** `internal/authz`. `Authorize(ctx, user, action, resource)`
decides project actions - `project.read` (any role), `project.write` (not viewers: dashboards,
silences), `project.manage` (admins and owners: rules, keys, members, settings), `project.own`
(the owner: notification channels, which hold credentials) - from the effective role (own
membership or team grants, the strongest; a global admin may do anything). `Require(action)` is
the route form for `/projects/:id/...`, with the decision readable in the handler
(`FromContext`); alerting's routes declare their gates this way and its service no longer checks.
`CanUserWriteToProject` is gone; `ProjectService.CanUserAccessProject` / `CanUserManageProject`
remain as thin wrappers for their existing call sites, and `Test_LegacyAccessChecks_OnlyShrink`
ratchets those call sites (19, 10, plus 4 direct role lookups) so new code goes through authz.
Every denial is audit-logged (once a minute per subject, action and resource, at most 20 a minute
per subject and then one notice; on the project's log when it exists) and every decision counted in `obzly_authz_decisions_total{action,result}`.

**Built (`org_plans`):** an organization's quotas (`org_plans`, 0 = unlimited, no row = no plan),
set by global admins only (an owner setting their own would be none) and read by members:
logs/s, largest log, logs kept and storage per project, retention, projects per Site, and Sites and
members per organization. Enforcement is per Site, not summed across Sites. The control plane
enforces Sites (assigning one) and members (adding one directly, through a team, or by SCIM;
suspended members hold their seat until removed). A Site learns its plan from
`GET /api/v1/site-plans/{site}` on the control plane - public like the JWKS, an ES256 document
signed with the assertion keys for that Site alone (`aud`), `token_use: site_plan` so it never
passes as a sign-in assertion (and the assertion check refuses anything with a `token_use`), valid
24 h - fetched every 5 minutes by each api/worker replica, verified against the JWKS, applied if
newer than the one held, and kept in `site_org_plan` so it survives restarts and control-plane
outages (a failed refresh keeps the last plan and warns after an hour). A standalone install
applies its Default organization's plan. On a Site the stricter of a project's own limit (or its
owner's user plan) and the organization's wins (`Project.Effective*`, `orgplan.Stricter`), and the
organization's applies even when the project turns rate limiting off - ingest rate and log size,
cleanup's count and storage, retention for every signal; project creation stops at the per-Site
count. The project response carries the plan (`orgPlan`) for the settings page; the Organizations
tab has a Plan view.
Next: organization-level actions (alert channels, retention defaults).

### Deployment and DR

One Helm chart replaces the three kustomize/compose shapes: `broker` (StatefulSet), `api`,
`querier`, `worker`, `control-plane`; external or in-cluster (CloudNativePG, a Valkey operator)
Postgres and Valkey. amd64 + arm64 images. Runbooks for backup/restore, DR and upgrade; `secret_keys`
is the root of trust, so restores are full-database and backups are encrypted with a key held
outside the database. JetStream needs no backup (it is a buffer); R3 across AZs protects unflushed
data. A weekly integrity audit closes the object-storage limitation above (built, below).

**Built (10a/10b, 2026-10-06):** `deploy/helm/obzly`, one chart with `mode: ha` (the `broker`
StatefulSet with routes for its replica count and `JETSTREAM_REPLICAS` to match - 1 or 3+, never
2 - plus `api`, `worker` and `querier`), `single` (`SERVICE_MODE=all` on a PVC) or
`control-plane`. It renders the same pods as `deploy/kubernetes/eks/` (probes, drain, grace
periods, zone spread, emptyDir caches), with in-cluster or external Valkey, a chart-made or
existing Secret (NATS and querier tokens generated once and read back on upgrade), PDBs, optional
HPAs, the querier NetworkPolicy, a PodMonitor and an Ingress; it refuses a render missing what a
mode needs. Images are published for `linux/amd64` and `linux/arm64`: the CI runner is arm64,
so the build stage runs there and cross-compiles amd64 with bookworm's cross toolchain (same
glibc as the runtime image) instead of emulating a CGO build; QEMU only runs the amd64 runtime
stage's package install. Still open: the pre-upgrade migration Job, CloudNativePG and Valkey
operator modes, presets from item 11's capacity model.

**Built (10c, 2026-10-06):** runbooks `docs/runbooks/{backup-restore,dr,upgrade}.md` with a measured
restore drill. `obzly reconcile` (`/app/main reconcile [--apply]`) compares the catalog with object
storage both ways: a row whose object and local copy are gone moves to `lost_segments` (so queries
stop reading it), one whose object is gone but file isn't is re-uploaded, an object no row knows
(identified by the segment id in its key, older than `--orphan-min-age`) and uploaded after the
restore point given as `--adopt-since` is adopted from the file (row count, time range, stats)
unless it overlaps cataloged data of its project - likely a compaction output whose inputs the
restored catalog still lists, so adopting it would double rows. Orphans uploaded before the restore
point are retired data never deleted from the bucket and are only reported: the first drill found
~11,700 such objects on the test Site (old key layout, from before deletes covered objects), which
adopting by footer alone would have resurrected. Size mismatches are reported. The weekly integrity
audit (leader-locked on workers) checks a sample of uploaded segments for existence, size and row
count, records `integrity_audits`, and exports `obzly_integrity_audit_*` with two alerts. Upgrade
status adds: releases recorded with a newer schema (`schema_releases`, written after startup
migrations) - one is a warning (rollout or supported rollback), two or more a blocker; rewrite
failures from the catalog (`stream_segments.rewrite_failed_at`), so every replica reports them;
the JetStream ingest buffer per signal (batches, bytes, oldest, replicas behind) with warnings;
the last integrity audit.

### Capacity and cost model

Planning rates (to be replaced by the Phase 14 performance suite): `api` ~0.7 MB/s raw per vCPU;
`worker` ~3 vCPU per TB/day; `querier` ~1 vCPU per 25 TB stored; brokers sized to ~1 h of ingest ×
3 replicas. Prices are approximate us-east-1 list, September 2026 — verify before quoting.

**S3 cost by ingest tier** (8× ZSTD, 1-yr retention, size-flushed L0):

| Ingest/day | Stored (1 yr) | Shape | L0 files/day | Requests | Storage | Total/mo |
|---|---|---|---|---|---|---|
| 1 GB | 46 GB | 5 proj | 360 | $0.07 | $0.32 | <$1 |
| 5 GB | 228 GB | 10 proj | 730 | $0.14 | $1.60 | ~$2 |
| 10 GB | 456 GB | 20 proj | 1.5k | $0.27 | $3.20 | ~$3.50 |
| 50 GB | 2.3 TB | 50 proj | 3.7k | $0.69 | $16 | ~$17 |
| 100 GB | 4.6 TB | 100 proj | 7.4k | $1.40 | $32 | ~$33 |
| 1 TB | 46 TB | 500 proj | 38k | $7 + $12 | $320 | ~$340 |
| 5 TB | 228 TB | 2,000 proj | 154k | $29 + $12 | $1,600 | ~$1,640 |
| 22 TB (1 PB at 1 yr) | 1 PB | 5,000 proj | 404k | $75 + $12 | $7,200 | ~$7,290 |

**Sizing by tier:**

| Ingest/day | Raw MB/s | `api` | `worker` | `querier` | `broker` (on-demand) | Postgres | Valkey |
|---|---|---|---|---|---|---|---|
| 1 GB | 0.01 | 1 × m7g.large, `all` | in-process | in-process | embedded (R1) | db.t4g.medium | cache.t4g.small |
| 5 GB | 0.06 | 1 × m7g.large, `all` | in-process | in-process | embedded (R1) | db.t4g.medium | cache.t4g.small |
| 10 GB | 0.12 | 2 × m7g.large | 1 × m7g.large | in `api` | 3 × m7g.medium | db.t4g.medium Multi-AZ | cache.t4g.small |
| 50 GB | 0.6 | 2 × m7g.large | 1 × m7g.large | in `api` | 3 × m7g.medium | db.m7g.large Multi-AZ | cache.t4g.medium |
| 100 GB | 1.2 | 2 × m7g.xlarge | 1 × m7g.large | in `api` (optional 1) | 3 × m7g.medium | db.m7g.large Multi-AZ | cache.t4g.medium |
| 1 TB | 11.6 | 4 × m7g.xlarge | 1 × m7g.xlarge | 2 × m7g.xlarge | 3 × m7g.large | db.m7g.xlarge Multi-AZ | cache.m7g.large |
| 5 TB | 58 | 20 × m7g.xlarge | 4 × m7g.xlarge | 10 × m7g.xlarge | 3 × m7g.xlarge | db.m7g.2xlarge Multi-AZ | cache.m7g.xlarge |
| 22 TB (1 PB at 1 yr) | 259 | 90 × m7g.xlarge | 17 × m7g.xlarge | 10 × m7g.xlarge | 5 × im4gn.2xlarge | db.m7g.4xlarge Multi-AZ | cache.m7g.xlarge |

**Monthly cost by tier** (Spot = 70% off with 30% of `api` on-demand; `broker` on-demand):

| Ingest/day | Compute on-demand | Compute w/ Spot | `broker` | Postgres + Valkey | S3 or local disk | Total (Spot) |
|---|---|---|---|---|---|---|
| 1 GB | $60 | $60 (no Spot) | embedded | $70 | ~$4 gp3 (46 GB) or <$1 S3 | **~$135** |
| 5 GB | $60 | $60 (no Spot) | embedded | $70 | ~$20 gp3 (250 GB) or ~$2 S3 | **~$135–150** |
| 10 GB | $180 | ~$100 | ~$100 | $117 | ~$3.50 | **~$320** |
| 50 GB | $180 | ~$100 | ~$100 | $350 | ~$17 | **~$570** |
| 100 GB | $298 | ~$175 | ~$100 | $350 | ~$33 | **~$660** |
| 1 TB | $833 | ~$335 | ~$190 | $714 | ~$340 | **~$1,580** |
| 5 TB | $4,046 | ~$1,720 | ~$410 | $1,427 | ~$1,640 | **~$5,200** |
| 22 TB (1 PB at 1 yr) | $13,900 | ~$6,450 | ~$2,660 | ~$2,630 | ~$7,290 | **~$19,000** |

Below ~100 GB/day managed Postgres and Valkey are most of the bill (in-cluster operators cut it);
from 10 GB/day the broker is the price of zero-loss HA; at the TB tiers storage dominates S3 and
Spot saves more than the brokers cost.

### Spot

| Role | Spot fit | Requirements |
|---|---|---|
| `api` | Good with ~30% on-demand baseline (separate nodegroup + topology spread) — **after item 1** (its Phase A interim at minimum) | After item 1 an `api` pod acks only after the JetStream commit and holds nothing beyond in-flight requests, so an interruption loses nothing that was acked. Until then, `flushAllSegmentsOnce` on SIGTERM is local-disk only and in `SERVICE_MODE=api` nothing uploads, so on `emptyDir` even a drained pod loses every segment it flushed — keep `api` on-demand (or `all` on a PVC) until the interim lands. Readiness→false immediately, LB deregistration ≤30s, `terminationGracePeriodSeconds` ~60s, NTH/Karpenter drain. Collectors retry 503. |
| `querier` | Ideal | Item 4 fragments must be idempotent + retried on another executor; rendezvous rebalances cache. Cache on `emptyDir` over instance-store-backed node ephemeral storage (i4i/im4gn), not an EBS PVC — an EBS-backed pod is pinned to one AZ and cannot follow a Spot replacement. |
| `broker` | **No — on-demand only** | Holds acked-but-unflushed data and Raft state. Three pods across three AZs (five at the ceiling), PDB `maxUnavailable: 1`. |
| `worker` | Good | JetStream consumers redeliver unacked messages to the next owner of the shard, so a reclaimed worker loses nothing. Already idempotent (temp-then-rename, `ReplaceSegments` tx, expiring leases). Orphans cleaned by `CleanupStaleTempFiles` + `reconcile`. Migration Job on-demand only. |
| `control-plane` | Fine, keep 1 on-demand | Stateless but query chokepoint. |
| Postgres | On-demand, Multi-AZ, PITR | Catalog = source of truth. Same rule for an in-cluster CNPG cluster: on-demand nodegroup, instances in two AZs. |
| Valkey | On-demand | Only locks/limits/caches since item 1 step 1e removed the logs queue → loss is a hiccup. In-cluster operator: AOF on PVC, on-demand nodes. |

### Upgrade and compatibility

Expand/contract migrations, a one-version rollback boundary, never a mass S3 rewrite. The admin
**Upgrade status** page (Settings → Updates; `GET /api/v1/system/upgrade-status`, one row per
deployment - the control plane and every Site through the proxy - with the full detail on expand) is the readiness signal before each release; Phase 14 adds JetStream
consumer lag, stream fill, broker health and a checkable release marker. Upgrade Sites first and
the control plane last.

| Change | Existing data | Rollback |
|---|---|---|
| Parquet ZSTD/sort/stats (2, Phase A) | Readable as-is (`union_by_name`); **no `schema_version` change** — codec and sort are transparent to `read_parquet`, rows without `stats` simply are not pruned until the compactor rewrites them or they expire. No rewrite pass. | Transparent — nothing to undo. |
| Promoted typed columns (2, Phase C) | `schema_version` 2→3; `union_by_name` reads old files with the promoted column NULL and `where_builder` falls back to the JSON path; optional background rewrite by the generalised `datamigration` worker. Note `compactor.go:62` refuses to merge across versions, so either let compaction upgrade on merge (stamp `CurrentSchemaVersion`, per-version `SourceExpr`) or accept that un-rewritten v2 files never merge with v3 — recommend the former. | Reader handles both versions. |
| Catalog partitioning (3) | One-time copy into partitioned table in a transaction (rows have `min_time`). Seconds–minutes at today's size — hence Phase B, early. | Keep old table renamed one release. The one step to treat carefully. |
| `level` column (1) | Backfill existing = L1 (≥128 MB → L2). | Column default. |
| `writer_id` column (1, Phase A interim) | Nullable; existing rows stay NULL and are uploaded by `all`/`worker` replicas as today. | Column default. |
| Object key layout (1) | **No S3 rename.** Old objects stay at old keys; querier resolves `object_key`. Only new uploads use the date-prefixed layout; lifecycle rules apply to the new prefix; old data ages out via catalog deletion as today. | Nothing to undo. |
| Broker role (1) | New `broker` StatefulSet (or embedded in `all`) started before any `api` publishes to it; streams and consumers are created idempotently at startup. No existing data. | Remove the role once nothing publishes to it. |
| Logs Valkey queue → JetStream (1) | One release with both paths behind a flag: `api` switches to publishing to JetStream, the old Valkey drain worker empties the list, then the list path is removed. `shutdownDrainDeadline` covers in-flight. Rolling `api` on `emptyDir` today loses that pod's un-uploaded segments (finding 1b), so the Phase A interim uploader must ship before any `api` roll is treated as lossless. | Flag flip back to the Valkey path. |
| Spans/metrics synchronous writer → JetStream (1) | Same flag: new batches publish to JetStream; the in-process writers flush what they hold and stop. Nothing on disk changes. | Flag flip. |
| Metrics two-stream layout (5) | Dual-read of legacy `labels`-JSON segments and new series/samples until expiry or `datamigration` rewrite — same as v1/v2 today. | Dual-read stays. |
| Orgs (9) | `Default` org, all projects attached, ADMIN→ORG_OWNER; existing SSO group mappings attach unchanged. No visible change single-tenant. | Expand/contract. |
| Control plane ↔ Site version skew (4, 8, 9) | One UI on the control plane serves every Site, and Sites upgrade one at a time. Order: Sites first, control plane last. New Site routes (cursor pagination, alerts, SCIM-related) ship beside the old ones for one release, so the older UI keeps working until the control plane catches up; the new UI feature-detects a Site's version via `/api/v1/site/info`. A release that changes the control-plane↔Site token or assertion contract must itself be expand/contract. | Old routes stay one release. |

## Multi-site: control plane and Sites

Decouples the frontend into one centrally-deployed UI + identity layer (a **control
plane**) while backends stay deployed independently per region/cluster as **Sites** -
one frontend to update, simpler multi-backend management, and a Site's query/admin
surface never has to be reachable from end-user browsers directly. Fully opt-in and
backward compatible: unset every env var below and a deployment is exactly the
single-site install it always was.

### Roles

Same binary, one new `SERVICE_MODE`:

- **Site** - any `all`/`api`/`worker` deployment with `CONTROL_PLANE_ISSUER` set.
  Unchanged in every other respect: its own Postgres, projects, plans, API keys,
  root admin, local MFA, break-glass local login. The only addition is a public
  `POST /api/v1/auth/site/exchange` endpoint (404s when `CONTROL_PLANE_ISSUER` is
  unset) and a public, secret-free `GET /api/v1/site/info` (`{siteId, siteName,
  version, mode, controlPlaneConfigured}`) a control plane uses to sanity-check a
  Site's `api_url` before trusting it.
- **Control plane** - `SERVICE_MODE=control-plane`. Reuses `internal/features/users`
  as-is (users/SSO/MFA/local accounts/invites - identical code path, identical
  claim shape) for central identity, adds a Site registry
  (`internal/features/control_plane`), and serves the SPA same-origin exactly like
  a Site does. `mode.RunsSignalStorage()` is false here: no storage engine wired,
  no ingest pipeline, no OTLP listener - a control plane never stores or receives
  a signal itself.

### Request flow: the central reverse proxy

The browser only ever talks to the control plane - never directly to a Site. A
Site's real address is never sent to the browser (`Site.ApiURL`'s json tag is `"-"`
on the non-admin DTO); the SPA addresses everything Site-scoped at
`/sites/{siteId}/api/v1/...` on the control plane's own origin, one path segment
below its own `/api/v1/...` (`frontend/src/constants.ts`'s `getApiBase()`).
`ProxyController` (mounted on its own `/sites` router group, registered before
`mountFrontend` so it's never shadowed by the SPA fallback) then:

1. Validates `siteId` against `^[a-z0-9][a-z0-9-]{0,62}$` and looks the Site up
   (404 if unknown/disabled).
2. Runs the requested path (after `path.Clean`) through a denylist-then-allowlist
   policy: a Site's own identity/admin/ingest surface (`/auth/`, `/sso/`, `/users/*`
   except `GET /users/me`, `/logs/receiving/`, `/docs/`, `/system/`) is refused
   outright; the query/data surface (projects, logs/traces/metrics query and tail,
   dashboards, disk usage, audit logs, settings, plans, API keys, service map,
   correlation) is allowed. Refusals are 404, not 403, so the surface a control
   plane exposes for a Site gives away nothing about what it denied.
3. Resolves the Site and the caller's `GrantService.ResolveEffectiveGrant(userID, siteID)`
   on **every** request (not just at token-mint time), through a 3 s in-process
   cache per (user, Site) - a revoked grant or disabled Site takes effect within
   3 s, not only once a cached Site token happens to expire.
4. Gets a Site-local access token for (user, Site) from `SiteTokenService`
   (mints a fresh one via token exchange on a cache miss; a cached token is bound
   to the `api_url` that issued it, and changing or removing a Site purges its
   tokens, so they never reach a new address) and builds a one-shot
   `httputil.ReverseProxy` (`Rewrite`, not the older `Director`, so hop-by-hop
   headers are dropped automatically) that strips every inbound
   `Authorization`/`Cookie`/`X-Api-Key`/`X-Forwarded-*` header, calls
   `SetXForwarded()` fresh, and injects `Authorization: Bearer <site token>`.
   `FlushInterval: -1` so Live Tail's NDJSON stream reaches the browser line-by-
   line instead of buffering. A Site's own `Set-Cookie` is stripped from the
   response - it belongs to a session model the browser's actual (central) origin
   knows nothing about.
5. A `401` from the Site (a stale/revoked cached token, or the Site rotated its
   signing secret) is buffered - not streamed to the browser - and the request is
   retried exactly once against a freshly re-exchanged token; a second `401`
   becomes a generic `502` with a fixed body (the control plane's own security
   and CORS headers are restored for the retry), never a raw `401` (which would
   incorrectly trip the frontend's global logout-on-401 handling for the
   **central** session, a different token entirely). Safe to buffer: a Site's
   `AuthMiddleware` rejects an invalid token before any handler runs, so nothing
   streaming could have started yet.
6. gzip compression is excluded for `^/sites/` (`gin-contrib/gzip`'s
   `WithExcludedPathsRegexs`) - a Site's response may already be gzipped
   (`ModifyResponse` passes `Content-Encoding` straight through), and re-gzipping
   an already-gzipped body corrupts it for the browser.

Ingest (`/v1/logs`, `/v1/traces`, OTLP gRPC, Prometheus remote-write, the Jaeger/
promapi query APIs' own API-key auth) is **never** proxied - it stays root-mounted
on each Site directly, reachable by collectors regardless of the control plane's
own availability (ARCHITECTURE.md's usual non-goal: a Site can never be *fully*
hidden; what the proxy hides is the query/admin surface, which is what matters for
keeping a Site's identity/admin plane off the public path).

### Identity model

- **`sites`** (`id` slug PK, `name`, `api_url`, `enabled`) and **`site_grants`**
  (`user_id`, `site_id`, `project_id` NULL-able, `role`, `created_by`) - a grant
  with `project_id IS NULL` is Site-wide: `ADMIN` (Site admin - it can reset local
  passwords and MFA there, so only the root admin grants it, directly or via an SSO group
  mapping; a team gets it only from an `ORG_OWNER`), or `MEMBER` / `VIEWER`
  (`PROJECT_MEMBER` / `PROJECT_VIEWER` on **every** project of the Site - the Site
  expands it against its own project list at each token exchange, so a project created
  later is picked up at the user's next exchange (their Site token lives up to an
  hour, or sooner after a sign-out/in); a higher explicit per-project grant wins for
  that project). At most one
  Site-wide grant per user per Site (the unique index). Otherwise a specific project
  role (`PROJECT_ADMIN`/`PROJECT_MEMBER`/`PROJECT_VIEWER`) - the same
  nullable-project-means-Site-wide shape `sso_group_mappings` uses. These Site-wide
  MEMBER/VIEWER roles exist only as control-plane grants; a Site's own user roles are
  unchanged. A
  central `ADMIN` gets **no implicit Site access** - only explicit grants (they can
  grant themselves); `OWNER` is never centrally grantable, same rule as SSO group
  mappings, since a Site's OWNER stays local.
- **`control_plane_signing_keys`** - the control plane's own ES256 keypairs
  (private key AES-GCM-encrypted via the existing `SecretKeyRepository`, same
  scheme as any other at-rest secret in this codebase), rotatable
  (root-admin-only) with a 24h grace window so a key just-retired doesn't
  immediately invalidate an assertion signed moments before rotation.
- **`central_identities`** (Site-side: `user_id`, `issuer`, `subject`, twin of
  `user_sso_identities`) and **`users.central_admin`** /
  **`project_memberships.central_managed`** (twins of `sso_admin_connector_id`/
  `sso_connector_id`) - a Site's own bookkeeping for who it JIT-provisioned or
  linked via a control plane, and which memberships are centrally- vs. manually-
  managed. `MembershipService.syncManagedMemberships` is the one reconciliation
  routine both SSO group sync and central-assertion sync now share (selector-based:
  the only difference is which column marks ownership).

### Token exchange

A per-user, per-Site, 60-second ES256 **assertion** (never a shared Site-level API
key - an API key carries no user identity and can't authorize the native query
APIs the UI needs): `iss` = the control plane's `PUBLIC_URL`, `aud` = the target
`SITE_ID`, `sub`/`email`/`name`/`site_role`/`projects`/`amr`/`jti`, plus
`all_projects_role` (`PROJECT_MEMBER`/`PROJECT_VIEWER`) for a Site-wide MEMBER/VIEWER
grant - a separate claim so `site_role` keeps its meaning (the user's local role:
`ADMIN` or `MEMBER`) and an older Site that doesn't know it ignores it rather than
misreading it. Minted by
`AssertionService.Mint` from the caller's resolved grant, verified Site-side by
`central_verifier.go` (`go-oidc`'s `NewRemoteKeySet` against
`CONTROL_PLANE_JWKS_URL` + a manual `nbf`/`iat` skew check, since go-oidc doesn't
enforce that itself) and exchanged one-shot via `POST /api/v1/auth/site/exchange`
for a normal, 1-hour-lived local access token carrying `auth_origin: "control-plane"`
- from that point on every Site-side authz/audit/rate-limit path is byte-identical
to a local login. `RequireMFAIfEnforced` skips its own check for such a session only
when the assertion's `amr` (copied into the token) contains `mfa` - the user passed MFA
on the control plane; an `amr=pwd` session is held to the Site's own rule, so a Site
ADMIN grant doesn't give Site admin without MFA. `SiteTokenService` keys its cache and
singleflight by `amr` too.

`SiteTokenService` caches the exchanged token per (user, Site) in Valkey - AES-GCM
encrypted at rest under the same DB-derived key everything else uses, TTL = the
token's own `exp` minus a 5-minute margin - and collapses concurrent cache misses
for the same (user, Site) via `singleflight`, so a burst of one user's own
requests never mints more than one assertion at a time. A new or revoked grant
(`GrantService.CreateGrant` / `DeleteGrant`) invalidates that cache entry, and any
remembered Site refusal, immediately; central
sign-out/deactivation (`SessionRevocationListener`) invalidates every cached token
for that user across every Site they have any grant on, and best-effort calls the
Site's own `POST /api/v1/users/logout` with the exact token being revoked.

The exchange endpoint itself layers: an optional `CONTROL_PLANE_EGRESS_CIDRS`
source check, IP- and subject-keyed rate limiting (reusing the existing login
rate limiter under a distinct key namespace), a single-use `jti` replay guard
(Valkey `SET`-if-absent, kept until the assertion's `exp` plus the 2-minute skew), and a
403 (no user created) if the assertion grants neither `ADMIN` nor any project.
Resolution order for who the assertion identifies: an existing `central_identities`
row, then (if `CONTROL_PLANE_LINK_BY_EMAIL`, default true) an existing local
account by email - **never** the local root admin, nor an account with its own sign-in
(a password, an SSO identity, a GitHub/Google id), mirroring the OAuth flow's refusal to
auto-link such accounts (a central email is self-service and unverified) - otherwise a
fresh, password-less `MEMBER` account is JIT-provisioned. One consequence worth stating outright:
a central account must **not** share an email with a Site's root admin - it could neither link
(root is never linkable) nor be JIT-created (the email is taken). That is why a control plane's
own bootstrap admin defaults to `superadmin` (`config.EffectiveRootAdminEmail`) while a Site's
stays `admin`: the two break-glass accounts can't shadow each other out of the box. When it does
happen (an explicit `ADMIN_EMAIL` collision), the Site refuses; the control plane relays that refusal as a
403 with the Site's message, and remembers it for 30s per (user, Site) so a retrying SPA doesn't
re-mint on every request. Policy refusals like this are deliberately **not** counted by the
exchange endpoint's rate limiter - every exchange arrives from the control plane's single source IP,
so counting deterministic refusals there would lock every user out for 15 minutes on one user's
misconfiguration (which is exactly what the first real deployment hit).

### SSRF / egress hardening

`api_url` registration (`SiteService.validateSiteURL`) requires an absolute,
host-only URL (no userinfo/path/query/fragment), `https://` unless
`SITE_ALLOW_INSECURE_URLS` (meant for a local/internal-only setup like the
multisite compose example), and rejects a literal loopback/link-local/unspecified/
multicast/`0.0.0.0/8` address or a cloud metadata endpoint (`fd00:ec2::254`,
`168.63.129.16`, `100.100.100.200`), also when NAT64, 6to4 or an IPv4-compatible
address carries it (`egress.PrivateNetworkFloor`, the same floor every outbound
call applies) - private RFC1918/ULA ranges are intentionally **allowed**, since that's the
normal shape for a Site on a Docker/Kubernetes-internal network. Every actual
outbound call (`SiteTokenService`'s exchange, `SiteController`'s connectivity
check, best-effort Site logout) runs a pre-flight resolve-and-check **at call
time**, plus an optional `CONTROL_PLANE_EGRESS_ALLOWLIST` CIDR list for defense in
depth against a mis-registered `api_url`. Registration-time validation alone doesn't
defeat DNS rebinding - and neither does a pre-flight check, since the transport
resolves again when it dials - so every outbound client shares one transport
(`newHardenedTransport`, `egress.go`) whose `DialContext` resolves the hostname
itself, applies the same policy to *every* resolved address, and connects to that
checked IP directly rather than by name. TLS is unaffected (`http.Transport`
handshakes after the dial, using the URL's hostname for SNI/verification). The
same transport also sets no environment-derived proxy, TLS ≥ 1.2, dial/handshake
timeouts; the one-shot callers add a hard overall timeout, no redirect following,
and a bounded response size on top.

### Site groups (same name)

Site names aren't unique. Sites whose names match (trimmed, case-insensitive) appear as one
entry in the site picker, e.g. `EKS-PRD` for `eks-prd-cluster-1` and `eks-prd-cluster-2`.
The project picker then lists every member's projects, with a section for each Site ID. It
fetches the other members' lists through `/sites/{id}/api/v1/projects` and skips any member
the caller has no grant on. Picking one of those projects switches to that Site. This is
done entirely in the frontend (`shared/site/siteGroups.ts`): grants, data and queries stay
per Site ID.

### What stays per-Site (non-goals)

Ingest, API keys, projects, plans, root admin, local MFA/break-glass login, audit
logs - all stay exactly where they are today, per-Site, never centrally managed.
Explicitly out of scope: cross-site aggregation (the UI addresses one Site at a
time), central management of a Site's own projects/plans/API keys, moving ingest
through the control plane, and persisting or caching proxied signal data
centrally - the control plane never stores a log/trace/metric row itself.

### Trade-offs

Every Site-scoped query byte (including Live Tail) now transits the control
plane's memory - never disk, since it's a pure streaming reverse proxy - making it
a bandwidth/latency/availability chokepoint for **query** traffic specifically:
a control-plane outage blocks queries against every Site it fronts, though ingest
and each Site's own break-glass local login keep working regardless (ingest was
never proxied to begin with). Mitigated the same way any other stateless tier in
this codebase is: run ≥ 2 control-plane replicas behind a load balancer (see
`deploy/kubernetes/eks/control-plane/`'s default `replicas: 2`) - safe to do
freely, since a control plane holds no per-pod local state and migrations are
already leader-locked.

## Synthetics, the infrastructure inventory and cloud connections

Diagram: `docs/ARCHITECTURE.drawio`, page "Synthetics and cloud" (`docs/ARCHITECTURE-synthetics-cloud.png`).

### Synthetics ([docs/SYNTHETICS.md](docs/SYNTHETICS.md))

- **Where things live:** monitors, destinations (a Site project's OTLP URL and an encrypted ingest
  key) and probe groups live on the control plane. `obzly-probe` agents pull their config
  (`GET /api/v1/synthetics/probe/config`, ETag, every 180 s), run the checks and push results to the
  destination Site as OTLP metrics (`synthetics_*`) and one log line per run. The control plane
  keeps only interactive Test results, in Valkey, for 10 minutes.
- **Probe groups (v0.56.0):** one location and one token, any number of instances. Each instance
  names itself (`X-Obzly-Probe-Instance`, the pod name in the Helm chart) and heartbeats through
  the Test long-poll it keeps open (`synthetic_probe_members`, touched every 15 s); a member unseen
  for 60 s is gone. The control plane answers every config and task call with the live members
  (`X-Obzly-Probe-Members`), and each instance runs only the monitors (and cloud connections) it
  wins by rendezvous hashing over them (`cmd/probe` `owner()`), so members agree without talking
  to each other and only a leaver's monitors move. Test tasks go to the group's queue, so any
  member answers. A replaced token keeps working for 24 h (`synthetic_probe_tokens.expires_at`).
- **Folders and dependencies (v0.56.0):** `synthetic_checks.folder` (a path) and `parent_ids`
  (loops refused, a deleted parent forgotten). Status is computed in the browser from the latest run
  per location; a failing monitor with a down parent shows **Impacted**.
- **Alerts per monitor (v0.56.0):** central notification channels per monitor, delivered as
  managed alert rules on the destination Site (see "Alerting": Managed rules).

### Infrastructure inventory ([docs/INFRASTRUCTURE.md](docs/INFRASTRUCTURE.md))

- A leader-locked worker job on each Site rebuilds every project's `inventory_assets` every 5
  minutes from what is already stored (series first/last seen, instant PromQL, the log rollups,
  trace services, firing alerts), and appends state and health changes to `inventory_changes`.
- From v0.56.0 it also reads `cloud_resource_info` and `cloud_lb_targets`: an EC2 instance or
  Azure VM joins the agent host with the same `host_id`, an EKS or AKS cluster the cluster of its
  name, and load balancers, databases, buckets, functions and scaling groups become assets of their
  own (`inventory/derive_cloud.go`). A resource only the cloud reports is "not reporting" after
  three of its connection's health scans plus 5 minutes (`cloud_refresh_seconds`; 50 minutes at the
  default 15) instead of 10, and `cloud_health` from the cloud's status checks feeds its health.
- The control plane stores no inventory: its Infrastructure tab reads each reachable Site through
  the proxy (`/api/v1/inventory`) and merges the results in the browser.

### Cloud connections (v0.56.0)

- **What:** read-only access to an AWS account or Organization, or an Azure subscription or
  management group (`cloud_connections`, control plane): identifiers only - account or subscription
  ID, role ARN or client ID, external ID - never keys. Managed in Settings → Cloud connections with
  a wizard that fills in `deploy/cloud`'s CloudFormation and Bicep templates or a CLI script.
- **Who scans:** a probe group, like a monitor: connections arrive in the probe config (`cloud`)
  and one live member runs each with `internal/cloud/discovery` - the AWS SDK (EC2, ELBv2 with
  target health, EKS, RDS, Auto Scaling, Lambda, S3; an Organization through the same role name in
  each account, four accounts at a time) or Azure REST (a token from IMDS or Entra, then Resource
  Graph). Two scans, both admin-adjustable: a **full discovery** every 4 hours by default, and a
  **health scan** every 15 minutes that re-reads only what the last discovery found (state, status
  checks, load balancer targets, Azure Resource Health), so new resources appear after the next
  discovery. One health scan follows each discovery and only health scans send
  `cloud_resource_info` gauges to the connection's destination. Each scan reports its outcome per
  use (`ok`, `needs_permission` with the missing actions, `error`), its kind, throttled calls and
  whether it hit its time limit to `POST /api/v1/synthetics/probe/cloud/{id}/scan`. Test access
  from the wizard is a probe task answered on `.../tasks/{id}/cloud-result`.
- **Throttling:** calls are paced per account and API endpoint (10/s, Resource Graph 3/s); an
  endpoint stands down for as long as the cloud asks (`Retry-After`, Azure's quota-reset headers,
  up to 5 min) before the call is retried, and the AWS SDK runs in adaptive retry mode. A scan
  that hits its time limit (80% of its interval) still sends what it found.
- **Credentials:** the probe's own identity (instance profile, IRSA, EKS Pod Identity, Azure
  managed identity), an AWS role assumed with an external ID, Azure workload identity, or
  **federation through the control plane**: it is an OIDC issuer (`/.well-known/openid-configuration`,
  `iss = PUBLIC_URL`) and signs a 10-minute token for `sub = probe:<group ID>` and the provider's
  audience (`POST /api/v1/synthetics/probe/cloud/{id}/token`), which AWS STS or Microsoft Entra
  exchanges for credentials. Entra supports only RS256 for external issuers, so these tokens use a
  separate RSA key (`cloud_federation_keys`) published in the same JWKS as the ES256 signing keys;
  Sites accept only ES256, so a federation token can't be replayed against a Site.

### Obzly settings (v0.56.0)

Tabs: Overview, General, Sites (control plane), Cloud connections (control plane), Notifications &
alerting, Identity & access, Updates and Audit logs (CSV export). Overview reads, per Site through
the proxy, the system dashboard and `GET /api/v1/system/usage` (admin-only, cached a minute): data
received per hour over the last day and data kept, by signal, summed from `stream_segments`. That
route and `/system/upgrade-status` are the only `/system/` routes the proxy forwards.

## Attribute naming

One rule for every stored name - log field keys, metric names and labels, span attributes -
so PromQL never needs quoting (`internal/util/attrnames`, mirrored in
`frontend/src/shared/lib/attributeNames.ts`):

- drop a leading `resource.`; every character outside `[a-zA-Z0-9_]` becomes `_`; a leading digit
  gets a `_` prefix (`resource.k8s.pod.name` -> `k8s_pod_name`, `system.cpu.time` ->
  `system_cpu_time`, `app.kubernetes.io/name` -> `app_kubernetes_io_name`).
- On collision the most specific source wins (record > scope > resource), and an already-normal key
  wins over a converted one.
- Log levels are stored lowercase and matched case-insensitively (`level:ERROR` still works).
- Applied at ingest (OTLP converters, REST receiving) and again in the three ingesters, so a
  current segment can never hold other names.
- Old names keep working: log queries normalize field names (where builder, Live Tail matcher,
  group-bys); PromQL is rewritten by `metrics_promql.NormalizePromQL` (parser-based) at every entry
  point, including Grafana's API and dashboard import.

**Existing data.** `stream_segments.schema_version`: 1 = written before the rule, 2 = current.
Queries read version 1 files through `segments.SourceExpr`, which wraps them in
`SELECT * REPLACE (obzly_norm_fields(fields) AS fields, lower(level) AS level ...)` (Go scalar UDFs
registered on each DuckDB handle) and unions them with version 2 files. The `datamigration` worker
(leader-locked, throttled, resumable) rewrites version 1 segments with the same expressions
(`segments.MigrateLegacySegment`: download if reclaimed, rewrite, atomic `ReplaceSegments`, delete
the old file and object) and rewrites stored dashboards once (`data_migrations` marker). Progress:
`GET /api/v1/system/data-migration`. Browsers rewrite their saved/recent searches once on load.

## AI observability (Claude Code usage)

A project-scoped **AI Observability** tab shows Claude Code usage: estimated cost, tokens, cache
hit rate, sessions, users, models, tools and API errors, plus a per-session timeline. Onboarding is
the Data Sources "Claude Code" entry, which generates the env vars (or a `managed-settings.json`
`env` block) that point Claude Code's built-in OTel exporter at the project.

- **Source is Claude Code's OTel events, not its metrics.** `claude_code.api_request` carries
  per-request `model`, `cost_usd_micros`, token counts, `duration_ms` and `ttft_ms`; `tool_result`,
  `tool_decision`, `user_prompt` and `api_error` fill in the rest. The metrics receiver turns OTel
  delta temporality (Claude Code's default) into cumulative (see "OTLP metrics ingestion"); the
  generated config still sets cumulative so it also works against Sites older than v0.57.0.
- **Cost is an estimate.** It's Claude Code's API-equivalent price; on Pro/Max/Team plans it is
  not what's billed, and remaining quota isn't exported.
- **Own stream.** Logs whose instrumentation scope is `com.anthropic.claude_code*` go to the
  `ai_logs` stream (`logs_core.StreamAILogs`), a second logs-schema parquet store, instead of
  `logs`. Routing happens in the ingest worker (`logs_core.SplitAIEvents`), and live tail skips
  them, so Search, patterns, fields and live tail only show infra/app logs. Compaction, upload,
  reconciliation, retention and project deletion cover both streams; the size/count quotas apply
  to `logs` only.
- **Queries**: `features/aiusage` (`/api/v1/ai-usage/{overview,sessions}`) runs fixed,
  parameterised DuckDB statements through `LogRepository.ExecuteScopedSQL`, which materializes
  the project's segments for the range once (narrowed by message prefix) and runs several
  statements over them.
- **LLM apps** (`/api/v1/ai-usage/llm/{overview,calls}`): spans with `gen_ai.operation.name`,
  read through `SpanRepository.QueryLLMCalls`. Fragments extract a fixed column set (service,
  operation, model, provider, tokens, error, duration, trace and span id; current and older
  GenAI attribute names), never content attributes, and a fixed statement aggregates them over
  the table `llm` (one `GROUPING SETS` pass for the overview).
- **Model prices** (`ai_model_prices`, per project): USD per million input, output and cached
  input tokens for an exact model name or a `prefix*`; exact beats prefix, the longest prefix
  wins. Only LLM apps use them; Claude Code reports its own cost.
- **Budgets** (`ai_budgets`, `/api/v1/projects/{id}/ai-budgets[/status]`): a calendar day,
  week (from Monday) or month in an IANA zone. Spend = Claude Code `cost_usd_micros` plus
  priced LLM calls, the same queries as the tabs. Each budget keeps a managed alert rule
  (`source = ai_cost`, `managed_by = ai-budget:<id>`, every 5 minutes); its series carry the
  user or model, the threshold, a severity (warning below 100%, critical from 100%) and the
  period, so each threshold notifies once per period and resolves when the period rolls over.
  `ApplyManagedRules` only touches `synthetics:` rules.
- **Budget spend is cached by day:** a day in the budget's time zone is closed an hour after
  it ends; its per-user, per-model totals (Claude Code cost, LLM tokens) go to Valkey for 40
  days, keyed by project, source, zone and date, so every budget of a project and zone shares
  them. An evaluation reads the closed days from Valkey and only the open days live (all of
  them when Valkey fails). Every budget query has the `QUERY_MAX_SCAN_GB` limit; over it, the
  rule records the error instead of alerting.

## MCP server (AI assistants)

`/mcp` serves the Model Context Protocol over streamable HTTP in stateless JSON mode (official Go
SDK), so any replica can answer any request. It is read-only by design.

- **Credential**: personal access tokens (`access_tokens`, `obz_pat_…`, SHA-256 stored, scope
  `mcp:read`, 30/90/365-day expiry, MFA code required to create). Accepted only by `/mcp`;
  `AuthMiddleware` never sees them. Each request re-checks revocation, expiry and user status.
  A password change or MFA reset revokes all of a user's access and OAuth refresh tokens
  (`UserRepository.UpdateUserPassword`/`ClearMFA`), and a token older than the password is refused.
- **Tools call the REST API in-process.** `mcp_server.apiExecutor` mints a 2-minute session for
  the token's user and calls this server's own router. The session carries
  `auth_origin=access-token` (counts as MFA) only when the token's `mfa_satisfied` is set - the
  session that created it had passed MFA - so a Member's token doesn't skip MFA after the Member
  is promoted to Admin. Permission
  checks, validation, concurrency limits and error mapping are the REST endpoints' own.
- **Multi-site** falls out of the same path: on a control plane a tool's call goes to
  `/sites/{site}/api/v1/...`, so the existing proxy does the grant check, path policy and
  per-user Site token exchange. `list_sites` lists the caller's Sites.
- **Guardrails**: per-user rate limit (burst 30, then 1/s), 175 s per call (above a search's
  170 s deadline, below the 180 s proxy timeout), row caps (50 default, 200 max), long messages
  cut at 2 KB, results cut at 120 KB, one log line per call.
- **Log hunting**: `search_logs` returns each log's `id` and full timestamp, and `log_context`
  takes them to return the logs before and after it from its workload (`same` to choose the
  fields, e.g. `["k8s_pod_name"]` for one replica, `["*"]` for the whole project) - what led up to
  an error.
- **Search syntax**: `logs_core.ParseSearchQuery` is the Go port of the search bar parser, so
  agents write `level:error service_name:api "timeout"`. Matching is case-sensitive, as in the UI,
  except `level`.
- **OAuth 2.1** for browser sign-in (claude.ai, Claude Desktop). Obzly is its own authorization
  server:
  - Discovery: 401s from `/mcp` point to `/.well-known/oauth-protected-resource/mcp` (RFC 9728),
    which names `/.well-known/oauth-authorization-server` (RFC 8414). Origin = `PUBLIC_URL`, else
    the request's (forwarded) scheme and host (`util/publicurl`).
  - Clients: Client ID Metadata Documents (https `client_id`, fetched with a public-address-only
    dialer, no redirects, 64 KB cap, cached 1 h) or RFC 7591 registration (public clients only,
    rate-limited). Redirects match exactly; loopback may change port (RFC 8252).
  - `/oauth/authorize` is an SPA consent page (the session lives in localStorage, not a cookie).
    It posts to `/api/v1/oauth/authorize/{validate,decision}`. An unknown client or redirect is
    shown, never redirected to. Codes: 60 s, single use (Valkey GETDEL), PKCE S256 required,
    `iss` returned (RFC 9207).
  - `/oauth/token`: opaque 1 h access tokens in `access_tokens` (kind `oauth`, audience = the
    `/mcp` resource URI, checked on every request) and 30-day rotating refresh tokens
    (`oauth_refresh_tokens`, 90-day family cap). Reusing a rotated refresh token revokes the
    whole family. Profile → Connected apps lists and revokes grants.

## Open-source readiness

Apache-2.0 license already in place. Still needed before/around launch: CONTRIBUTING.md, SECURITY.md,
CODE_OF_CONDUCT.md, issue/PR templates, CHANGELOG going forward, a stated versioning policy (still
pre-1.0), and a decision on SDK strategy (pull the existing per-language client libraries — currently
outside this repo — into a monorepo, or keep them polyrepo with a clear index).
