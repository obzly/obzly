# Obzly — Enterprise scale plan (pre-review draft, 2026-09-29; revised 2026-09-30, NATS)

Gap assessment and phased roadmap for taking Obzly from its current "self-hosted small-to-mid team"
scale to **~1 PB stored at 1-year retention (≈22 TB/day raw at 8× ZSTD) as the storage design
ceiling, with 1–5 TB/day as the expected enterprise ingest band and 5 TB/day sustained as the
acceptance target**, on **Postgres + Valkey + S3 and one image** — the image embeds a NATS
JetStream server, run in-process on a single node and as its own `broker` role in HA. This is a draft for internal review; once agreed, the design sections move into
`ARCHITECTURE.md` and the phases into `ROADMAP.md` as described at the end of this document.

**Revision 2026-09-30** (after a pre-review pass that checked every claim against the code and
recomputed every number): findings row 1 corrected and row 1b added (the HA split never uploads
`api`-written segments); the loss contract restated per flush tier; the local WAL removed from the
schedule; one `schema_version` bump, in Phase C; the S3 figures reconciled and the 1 PB row
redefined as its own ~22 TB/day tier; security notes added to items 8–10; acceptance criteria per
item; open questions collected at the end. A second revision the same day adopts NATS
JetStream (shipped inside the image) as the durable ingest buffer, Live Tail transport and querier
control plane, and spells out how the querier works in HA (item 4).


## Context

Obzly's `ROADMAP.md` Phase 0 deliberately framed the target as "self-hosted small-to-mid teams, not
PB/day scale", and the storage engine (Go memtable → DuckDB-written Parquet → S3, Postgres segment
catalog, Valkey for locks/queue) was built to that framing. The goal is now to re-frame toward an
enterprise deployment: **~1 PB stored at 1-year retention as the ceiling, 1–5 TB/day ingest as the
expected band**, with **no infrastructure beyond Postgres + Valkey + S3 and one image** (no
Kafka/ClickHouse/Rust sidecar; NATS ships inside the image, see below), plus four enterprise
capabilities: **alerting &
notifications, organizations/teams/SCIM, Helm chart + multi-arch images, backup/restore & DR
runbooks**.

"One image" means NATS is not a separate product to install: `nats-server` is an Apache-2.0 Go
library that embeds in-process (`server.NewServer`), including JetStream, clustering and leaf nodes.
It is still new *stateful* infrastructure at the HA tiers, and the plan says so: JetStream keeps
messages on disk and replicates them with Raft, so the pods that host it keep their disks. Two
packaging modes, both from the same image:

| Mode | Where NATS runs | Fits |
|---|---|---|
| Embedded in `SERVICE_MODE=all` | Inside the single process; JetStream file store (R1) under `LOGS_DATA_DIR` | 1–5 GB/day single-node installs. No new process or pod. |
| `SERVICE_MODE=broker` (new role) | A 3-replica StatefulSet running only the embedded server; R3 streams; PVC or instance NVMe; on-demand nodes across three AZs | Every multi-replica tier. `api`, `worker` and `querier` stay stateless and Spot-friendly. |

The official `nats` image and Helm chart are a supported alternative to `broker` mode (independent
upgrades, NATS tooling) but a second image, so not the default. NATS is never embedded in `api`
replicas in HA: that would make them stateful and pin them to on-demand nodes. Optional Kubernetes
tooling the Helm chart may render (CloudNativePG / Valkey-operator CRs, a ServiceMonitor, NTH or
Karpenter for Spot drains) and the PgBouncer pool-sizing guidance run or observe the same Postgres,
Valkey and NATS; none is required.

**This document is the assessment and roadmap.** After review, its design moves into
`ARCHITECTURE.md` (design) and its phases into `ROADMAP.md` (execution). Implementation follows
phase by phase.

## Verified findings (what breaks at this scale, with the mechanism)

All confirmed by reading code, not inferred (re-verified 2026-09-30; `cmd/main.go` is cited by
function or constant name because its line numbers move):

| # | Area | Current mechanism | Why it breaks |
|---|---|---|---|
| 1 | Ingest | Logs: every `api`/`all` replica accumulates in RAM, pushes to the single Valkey list `obzly:logs:queue` (`logs/receiving/worker_service.go:68`) and also consumes from it — `runIngestPipeline` in `cmd/main.go` calls `StartWorkers()` on every replica that `RunsSignalStorage()`, `max(NumCPU/4,1)` consumers each (`worker_service.go:75`); dequeue is an atomic `RPOP COUNT` (`util/cache/queue.go:64`), so the "should only be called on ONE instance" comment at `worker_service.go:42-47` is stale (predates the HA split; `deploy/kubernetes/eks/ha/deployment-api.yaml:22` already runs two, `ARCHITECTURE.md:1042` documents it). Spans/metrics: synchronous in-process `segments.Writer` on whichever `api` replica received them. No WAL; writer thresholds 100k rows / 128MB (`segments/writer.go:21-22`), force-flushed every 30s on every ingest replica (`segmentFlushInterval` / `flushAllSegmentsOnce` in `cmd/main.go`); `DefaultMaxAge` 10m (`writer.go:23`) is only `FlushStale`'s threshold and nothing in the binary calls it. | Every ingested byte transits one non-persistent Valkey (RAM SPOF; `deploy/kubernetes/eks/valkey-deployment.yaml` has no PVC) and costs a JSON marshal + unmarshal per line; the list is unbounded and the only 429 is the per-project token bucket (`receiving/service.go:375`), not queue depth, so overload grows RAM instead of shedding. Measured ~22k lines/s ≈ 4.4 MB/s is end-to-end on one 2-vCPU node (ROADMAP Phase 7, 200 B lines) — a per-replica cost baseline, not a one-consumer cap; 5 TB/day needs ~58 MB/s, i.e. ~13× that, all funnelled through the one Valkey (≈290k lines/s if all of it is logs at 200 B/line). Hard-kill RAM loss ≤30s per replica for all three signals (measured ~21s, ROADMAP Phase 7); logs additionally lose the in-flight RPOP'd batch and the whole queue if Valkey restarts. |
| 1b | Ingest (HA split) | `UploadPendingSegments` runs only where `RunsBackgroundWorkers()` (`all`/`worker`: `runBackgroundCleanupWorkers` in `cmd/main.go`, `config.go:87-89`; sole callers `logs/cleanup/background_service.go:265`, spans `:188`, metrics `:187`) and uploads from that process's own `record.Path` (`logs/uploader/uploader.go:88`). `ha/deployment-api.yaml:89` and `deployment-worker.yaml:64` are separate `emptyDir`s; the compose HA example keeps them "deliberately NOT shared". | A segment flushed by a `SERVICE_MODE=api` replica is never uploaded by anyone, is invisible to other replicas (`resolvePath` needs `object_key`) and dies with the pod on any termination — RollingUpdate, scale-down, Spot reclaim, graceful or not (`flushAllSegmentsOnce` only writes the same disk). The worker's uploader lists those rows oldest-first in batches of 50 (`segments/repository.go:59-68`, `parquetstore.go:36`), fails on them, and past 50 stops uploading its own. `ha/README.md:11-12` "safe to scale up/down or roll freely" is wrong today. Single-replica `all` installs on a PVC are unaffected. |
| 2 | File format | `COPY ... TO ... (FORMAT PARQUET)` with **no options** in `writer.go:314`, `compactor.go:87,125`. `compactor.go:45` says never rely on file order. | Snappy not ZSTD (~30–40% larger); arrival order → row-group min/max stats useless for pruning; `fields`/`attributes`/`labels` opaque JSON VARCHAR. |
| 3 | Catalog | `stream_segments` single heap, indexes `(project_id,stream,min_time,max_time)` + partial pending-upload (`migrations/20260914054608`). `ListByTimeRange` prunes on time only (`segments/repository.go:42-54`) with an interval-overlap predicate. | 1 PB ≈ 2–8M rows (fine) but every query lists *all* overlapping segments; retention = millions of row deletes + vacuum churn; L0 churn shares the table with cold history. |
| 4 | Query | The querier resolves, downloads and opens every segment in the window on one replica (`logs/querier/querier.go`; `segments/query.go` only builds the `read_parquet([...])` expression; the DuckDB handle is opened once per `LogQuerier`, `querier.go:75`, and only the user-SQL sandbox opens one per query). Offset pagination + unconditional `count(*)` (`querier.go:245`), `maxOffset=1_000_000`. `maxConcurrentQueries=3` is a per-user Valkey counter (`logs/querying/concurrent_limiter.go:20`), not a per-replica cap. | 24h at 5 TB/day is ~0.8 TB/day compressed today (Snappy, ~6×) or ~0.6 TB/day after item 2 (ZSTD, 8×) = 600–800 GB through one pod; N users × 3 queries all land on whichever replica received them. Count doubles every page's cost. |
| 5 | Metrics | Labels as canonical JSON string; only `metric_name` pushes down, matchers evaluated in Go, `maxSeriesRows=2_000_000` abort (`metrics/querier/querier.go`). | 1M points/s → one `rate()` over 1h pulls billions of rows before matchers apply. |
| 6 | Retention | Whole-segment deletion by `MaxLogsLifeDays` for all signals; no per-signal retention/quotas for spans/metrics; one S3 delete per key; object key `<stream>/<project>/<id>.parquet` has no date prefix. No sampling/rollups. | Can't use S3 lifecycle rules; millions of single deletes; 5 TB in = 5 TB stored. |
| 7 | Self-observability | No `/metrics`, no OTel SDK on itself; health = disk>95% + `SELECT 1`, and all three Kubernetes probes hit that one endpoint (`ha/deployment-api.yaml:63-76`), which has no shutdown state. | Cannot tune or validate anything below; readiness cannot flip on SIGTERM. |
| 8 | Alerting | Absent. | Enterprise requirement. |
| 9 | Tenancy | users→projects only; ADMIN/MEMBER global; authz split between middleware and per-handler `CanUserWriteToProject`. | No org boundary for quotas/SSO/SCIM; unauditable authz. |
| 10 | Deploy/DR | Kustomize only: a single-replica `Recreate` base, a multi-replica HA overlay (`deploy/kubernetes/eks/ha/`: `obzly-api` ×2 RollingUpdate + `obzly-worker`), a control-plane overlay, and `docker-compose.ha.yml.example`; no Helm. Every CI job is `runs-on: self-hosted` (amd64) and images are `platforms: linux/arm64` only, built under QEMU (`ci-release.yml`). No S3 integrity audit (documented limitation). | Enterprise requirement: one chart replacing the three overlays, amd64 images, audit. |
| 11 | Perf validation | `cmd/loadtest` exists; no regression suite, no dataset generator, no capacity model. | Cannot prove 5 TB/day. |

Also in scope (found during review): Live Tail publishes every ingested line to Valkey pub/sub;
control-plane Site status cache is per-process; zero frontend tests; N replicas × GORM pools will
exceed Postgres `max_connections`.

## Recommended design (to be written into ARCHITECTURE.md as "Scale architecture (1 PB / 5 TB/day)")

Sizes: S <1wk, M 1–3wk, L 1–2mo, XL 2–4mo (one engineer familiar with the codebase).

1. **Ingest — NATS JetStream as the durable ingest buffer; Valkey leaves the data path** (XL)
   - **Interim, Phase A (S):** until the rest of this item lands, run the `UploadPendingSegments` tick on every `RunsSignalStorage()` replica, each listing only its own pending rows via a new nullable `stream_segments.writer_id` (additive). Closes finding 1b and makes the HA overlay and Helm v1 honest before Phase B.
   - **Write path:** `api` validates each batch and publishes it to a JetStream stream per signal (subjects `ingest.<stream>.<shard>.<project>`), then acks the client **after the JetStream commit** — R3 on the `broker` role, R1 when embedded in `all`. The same path serves logs, spans and metrics, replacing the Valkey logs list and today's synchronous per-replica `segments.Writer` for spans and metrics.
   - **Loss contract:** R3 = **zero loss on any single-node failure** — an `api` pod, a `worker` or one broker — because nothing is acked before a quorum holds it; loss needs a broker quorum gone. Embedded R1 survives a process crash (the file store is in the OS page cache); a host loss can drop up to JetStream's `sync_interval` (default 2 min) unless `sync_always` is set, traded against throughput. The per-tier loss table, the SIGTERM-must-upload rule and the node-local WAL question all fall away.
   - **Write to Parquet:** `worker` replicas run durable pull consumers (work-queue retention, so a message is removed only once its rows are in an uploaded, catalogued segment). Consumers are sharded by the `<shard>` subject token, each shard owned by one worker at a time via a `leaderlock`, so a (project, stream) always lands in one writer. Because the buffer is durable, L0 files flush **by size** (target 64 MB) or at a **1-hour max-age** for low-volume pairs — no 5s/60s/300s tiers and no per-project flush floor. Each upload inserts a catalog row `level=0`, then acks the covered messages.
   - Object key → `<stream>/<project>/<yyyy>/<mm>/<dd>/<level>/<id>.parquet` for every new upload, so S3 lifecycle rules can target streams, levels and dates (item 6 builds on it; one key change, not two).
   - Leveled compaction in `worker` replicas: L0 → L1 (128MB) → L2 (512MB–1GB, cold). **Promote by age as well as size** (anything older than ~1 day merges into L1 regardless of bytes) so low-volume projects never accumulate tiny cold objects.
   - **Compaction in HA.** Today each stream has one cluster-wide Valkey lease (`logs-compaction`, `spans-compaction`, `metrics-compaction` in `features/*/cleanup/background_service.go`), so one worker compacts a stream at a time however many run. Target: a fixed shard count per stream (e.g. 64, more than the worker count), shard = `hash(project) % N`, one renewable `leaderlock` per shard (`util/leaderlock`); each worker claims shards up to its fair share and releases extras when workers join, so throughput scales with worker count. **One shard key for ingest and compaction**: the JetStream consumer shard (the `<shard>` subject token) and the compaction shard are the same and one lease covers both, so the worker that wrote a shard's L0 files also compacts them, often from its own disk rather than S3. Crash safety is the existing mechanism: a merge writes a temp file, uploads it, then swaps the catalog rows in one transaction (`ReplaceSegments`, `segments/repository.go`); a worker lost before the swap changes nothing, its leases expire and the next owner redoes the merge, and `CleanupStaleTempFiles` (`segments/writer.go`) plus `reconcile` remove leftovers. Only a lost worker's shards move, after the lease TTL. Unlike the querier, this is exclusive ownership, not fan-out with retry: two workers merging the same segments would duplicate rows. A lost worker delays its shards by the lease TTL with no user waiting, and falling behind shows as compaction backlog (item 7 metric and SLO; the Upgrade status page counts segments awaiting compaction). Leases stay in Valkey; handing out compaction jobs through NATS is possible but not planned.
   - **Backpressure is native:** each stream has `max_bytes` with `discard: new`, sized to the worker lag budget (~1 h of ingest); a rejected publish maps to 429 / gRPC `RESOURCE_EXHAUSTED` + `Retry-After`. OTel Collectors retry natively.
   - **Live Tail on core NATS:** ingest publishes a copy to `tail.<project>` only when that subject has interest (a tail is open), replacing Valkey pub/sub and the separate live-tail gating item.
   - **Shutdown and readiness (S):** a shutdown flag that turns readiness false on SIGTERM (separate from the liveness endpoint), a drain delay before `srv.Shutdown` so the load balancer deregisters first (today `srv.Shutdown` runs immediately with a 10s context), and a deadline on gRPC `GracefulStop`, which is unbounded today. With JetStream, an `api` pod holds nothing beyond in-flight requests.
   - **Broker role:** `SERVICE_MODE=broker` starts only the embedded NATS server (cluster routes, JetStream store, monitoring on `:8222`); `all` starts it in-process; `api`/`worker`/`querier` connect as clients (`NATS_URL`, NKey or user credentials per role, TLS). Broker upgrades roll one pod at a time and wait for JetStream to report the stream current before the next.
   - Rejected (one line each in doc): Valkey Streams (RAM SPOF at 58 MB/s); a node-local WAL (dies with the node under Spot; JetStream replaces it); S3 as the only durability with 5s L0 flushes (the previous design: request cost forced adaptive flush tiers and a hard-kill loss window); Kafka (a separate JVM-era system for what an embedded JetStream does).
   - Files: new `internal/broker/` (embedded server, config, health, stream/consumer bootstrap), `config.go` (`SERVICE_MODE=broker`, `NATS_URL`), `logs/receiving/worker_service.go` (publish instead of `QueueLog`), the spans/metrics ingest paths, `segments/writer.go` (consumer-fed), `segments/compactor.go`, `features/*/uploader`, `logs/livetail`, `cmd/main.go` (roles, shutdown path). Two one-line fixes ride with it: correct the stale "ONE instance" comment at `worker_service.go:42-47`, and the `valkey-deployment.yaml:1-4` header, which says Valkey holds no durable data although it holds the logs queue until this item lands. Migration: `stream_segments.level SMALLINT` (ride with item 3's migration); `writer_id TEXT` lands earlier with the Phase A interim.

2. **Parquet quality** (L total)
   - Compaction-time sort: logs/spans `ORDER BY project_id, service_name, timestamp`; metrics `ORDER BY series_id, timestamp` — in today's `Compactor.MergeSegments` from Phase A, in L1+ once item 1 lands; the writer's fresh files (future L0) stay unsorted. Supersedes the `compactor.go:45` note only for pruning effectiveness — nothing relies on file order for correctness.
   - `COMPRESSION ZSTD, COMPRESSION_LEVEL 3, ROW_GROUP_SIZE 262144, PARQUET_VERSION V2`. Pin and record the DuckDB engine version (`SELECT version()` through the pinned go-duckdb) before relying on any option. Evaluate bloom filters on `trace_id`/`span_id`/`service_name` rather than assume them: DuckDB writes Parquet bloom filters only for columns that stay dictionary-encoded, and high-cardinality ids may not.
   - Per-segment stats computed in the same DuckDB pass (`list(DISTINCT service_name)`, levels, metric_names, promoted min/max) → catalog `stats JSONB`.
   - Promoted hot JSON keys → typed columns via `project_promoted_fields` table (same pattern as Phase 3's `trace_id`/`span_id` add; `union_by_name=true` handles old segments); per-org cap on promoted-column count. `schema_version` 2→3 (Phase C, with this bullet only — the bump exists to drive the optional rewrite, not for reader compatibility; Phase 3's column add did not bump). Generalise the `datamigration` worker: today `ListLegacy`/`CountBySchemaVersion` filter `schema_version = 1` (`segments/migrate.go:73,85`) and `RewriteLegacySegment` applies only the v1 name UDFs (`segments/compactor.go:114`); change the selector to `< CurrentSchemaVersion` and dispatch the rewrite by (stream, from-version).
   - Files: `segments/writer.go`, `compactor.go`, `schema.go`, `segments/migrate.go`, `features/datamigration`, new `segments/stats.go`, `where_builder.go`.

3. **Catalog** (L)
   - `PARTITION BY LIST(stream)` → `RANGE(min_time)` monthly; PK `(id, stream, min_time)`; covering index `INCLUDE (path, object_key, size_bytes, row_count, level, stats)`; GIN on `stats->'services'`, `stats->'metric_names'`.
   - **Partition pruning needs a lower bound on `min_time`.** The planner's overlap predicate (`min_time <= to AND max_time >= from`) lets Postgres skip only partitions that start after `to`; every older month still matches. Bound it with `min_time >= from − max_segment_span`, where the maximum span is known from the catalog (L2 segments cover at most a day or so), or partitioning helps retention and not query planning.
   - Separate L0 partition/table to isolate churn. Retention = S3 batch delete, then `ALTER TABLE … DETACH PARTITION … CONCURRENTLY` (PG 14+) and `DROP TABLE` on the detached child — Postgres has no `DROP PARTITION`. A monthly partition can be dropped only once every project's retention has expired it; with per-project retention (item 6) the shorter-retention projects still delete rows inside live partitions.
   - `ListByTimeRange` → `PlanSegments(project, stream, from, to, PruningHints{Services, Levels, MetricNames, TraceIDs, MaxBytes})`.
   - Add `checksum_sha256`, `etag`, `storage_class`, `last_verified_at`.
   - Worker ticker pre-creates next month's partitions (`segments/partitions.go`). One-time swap migration while data is still small.

4. **Distributed query — Go-native scatter/gather, new `SERVICE_MODE=querier`** (XL)
   - **Roles.** Both stateless, both from the one image. The **coordinator** runs in every `api` replica, so any replica behind the load balancer can take a query and there is no single query node to lose. **Executors** run as a `SERVICE_MODE=querier` Deployment of N replicas on Spot, spread over AZs, each with a disk cache on `emptyDir` bounded by `COLD_CACHE_MAX_SIZE_MB` and evicted by the existing least-recently-modified sweep (`segments/cacheeviction.go`). Below the querier threshold (≤100 GB/day) there are no executor replicas: `all` and `api` run an in-process executor, so HA is just "any `api` replica can answer".
   - **One query, end to end.** (1) The request lands on any `api` replica; the per-user concurrency limit is checked there in Valkey, as today (`logs/querying/concurrent_limiter.go`). (2) The coordinator plans from the catalog (`PlanSegments`, item 3) and rejects the query before any I/O if the bytes to scan exceed the budget. (3) It reads the live executor list from NATS: each executor registers as a NATS micro service and answers discovery pings, so membership is current within seconds. (4) It assigns each segment by **rendezvous hashing** of (segment id, executor id), so a segment goes to the same executor while membership is stable and hits a warm cache; segments are grouped into one fragment per executor. (5) **Control over NATS, data over gRPC:** the fragment request (fragment SQL, segment list, deadline) goes by NATS request/reply to that executor's subject, as do cancel and progress; the executor fetches uncached segments from S3, runs the fragment in DuckDB, writes the result as Parquet, and the coordinator pulls it over the executor's internal gRPC listener — results can be large and NATS messages cap at 1 MB by default. (6) The coordinator runs the final SQL over the fragment results (`read_parquet([...])`), so DuckDB does the merge. Rewrite rules: COUNT→SUM, AVG→SUM/COUNT, quantiles→`reservoir_quantile`, `LIMIT k` pushdown.
   - **Failure handling.** *An executor dies* (Spot reclaim, crash, OOM): NATS reports no responder or the fragment misses its deadline; rendezvous gives every segment a ranked executor list, so the coordinator re-sends the lost fragment's segments to each segment's next-ranked live executor. Fragments are read-only and idempotent, so retry is safe, up to a retry cap inside the query deadline; only that executor's ~1/N of cache goes cold and rewarms from S3. *The coordinator's `api` pod dies*: the query fails with a retryable error and the client retries on another replica; there is no query state to recover, and executors drop the orphaned fragment at its deadline or on the cancel sent at shutdown. *An AZ goes down*: the other zones' executors take over by the same rule (S3 and the catalog are regional); size N so the remaining zones carry the load. *One broker node dies*: the R3 cluster keeps serving, and discovery and request/reply do not depend on JetStream storage. *The whole broker cluster is down*: dispatch stops; the coordinator falls back to running the query in-process — slower, still available — and reports degraded mode.
   - **Scaling.** HPA on executor CPU and fragment queue depth. Adding or removing an executor re-homes only about 1/N of segments (the rendezvous property), so scaling does not flush every cache. The partial-result cache for immutable L1+ segments lives on the executor that owns each segment, so it follows the same affinity.
   - **Executor data endpoint.** Fragments must not share the OTLP gRPC server: that server has no TLS, no credentials and no interceptors, and listens on `:4317` on every Site for collectors (`otlp/grpcserver.go:30,38`); an executor runs SQL over any project's Parquet, so a fragment endpoint there would be an unauthenticated cross-project read. Use a separate internal listener (`QUERIER_GRPC_ADDR`) with mTLS or a shared-secret interceptor, closed by NetworkPolicy. "Never proxied by the control plane" (`site-scoped-api` checklist) is necessary, not sufficient.
   - **Keyset pagination** `(timestamp, id)` cursor replaces offset+count; counts approximate by default (`sum(row_count)` of fully-covered segments + exact on boundary segments), exact opt-in. The offset API stays for one release beside the cursor API, so a control-plane UI and N-1 Sites keep working during a rollout (see the version-skew row in the migration table). `TrackTotal` already exists in the query DTO and is ignored — honouring it is a cheap Phase A win.
   - **Special cases.** The user SQL sandbox is not distributable: it runs on one executor chosen by hashing the query, with the same retry-on-another rule. PromQL (item 5) keeps its engine on the coordinator and pushes `Select` down as fragments. `DiscoverFields` reads catalog stats. Live Tail does not use the querier (item 1).
   - **Multi-site.** Each Site has its own coordinators and executors. The control plane only proxies HTTP to a Site's `api`; the executor gRPC and NATS ports stay inside the Site's cluster.
   - **Cost note.** Cross-AZ traffic between executor and coordinator is billed (~$0.01/GB each way). Results are mostly aggregates or LIMITed pages, but a large raw export should stream from one executor rather than fan in.
   - New `internal/query/` (coordinator, executor, fragment, registry on NATS, budget, cache) + proto; frontend logs list to cursors.
   - **As built, after OpenObserve (2026-10-09).** A page search walks time windows newest-first and stops when the page is full, streaming rows (`POST /logs/query/stream`); counts come from the histogram, whose finished hour partitions are cached in Valkey; executors keep their cache on a volume with a stable affinity id, warm up on start and pre-fetch on upload. See ARCHITECTURE.md *Distributed query* 4f-4h.
   - Rejected: Arrow Flight (Parquet over gRPC is enough); fragment results over NATS (message-size limits, no streaming backpressure); Valkey heartbeat set for discovery (NATS micro-service discovery replaces it).

5. **Metrics** (XL)
   - Two streams: `metrics_samples(series_id UBIGINT, timestamp, value)` sorted by series; `metrics_series(series_id, metric_name, labels MAP, first/last_seen)`; `series_id = xxhash64(name+canonical labels)` at ingest.
   - Matchers compile to DuckDB predicates on the series stream, join to samples; the Go `matchAll` remains only as a parity-test oracle.
   - Postgres `metric_series_index(project, metric_name, label_name, label_value, last_seen)` for `/labels`, `/label/<n>/values`, `/series` only — never every series.
   - Compactor writes `metrics_5m` / `metrics_1h` rollups `(series_id, bucket, count, sum, min, max, last, delta)`; `promql/queryable.go` selects stream by range/step; document `rate()`-over-rollup caveat.
   - Verify DuckDB Appender MAP support; fallback `label_keys/label_values LIST`.

6. **Tiered retention & lifecycle** (L; 6a policies/keys/sampling, 6b rollup-only aging after item 5)
   - `retention_policies(scope org|project, stream, hot/warm/cold days, storage classes, sample_rate, tail_rules, rollup_only_after_days)` replaces `MaxLogsLifeDays`; per-signal quotas for spans/metrics.
   - Uses item 1's date-prefixed key layout. S3 lifecycle rules filter by prefix or tag, at most 1,000 rules per bucket, so prefix rules can express stream-, level- and date-wide transitions only; per-project or per-org schedules at thousands of projects need object tags set at PUT (gocloud `WriterOptions.BeforeWrite`) and tag-filtered rules. Never transition objects under 1 MB. `storage_class` on catalog row so the planner warns/refuses Glacier hits.
   - `ObjectStore.DeleteBatch` (AWS SDK `DeleteObjects` for `s3://`, since gocloud blob has no batch delete; worker-pool fallback elsewhere) + partition detach/drop (item 3).
   - Logs: head sampling at ingest per (project, service, level) before the JetStream publish, hash on `trace_id` for correlation. Traces: **compaction-time tail sampling** in DuckDB with a deterministic decision — keep when `error OR slow OR hash(trace_id) < rate` — so every compaction pass and every replica makes the same choice for a trace; a per-pass `random()` would keep halves of traces whose spans landed in different L0 files. The Valkey kept-set (15 min TTL) is only for late spans of traces kept by the error/slow rule. Metrics (6b): rollup-only aging once item 5's rollups exist.

7. **Self-observability** (M) — **first item to build**
   - `promhttp` on internal `METRICS_ADDR` (`:9464`), never proxied. Gauges/counters: ingest rows/bytes/rejected, writer buffer bytes, L0 upload lag, segments by level, compaction backlog, query duration/scanned bytes/segments opened, cold cache hit ratio, leaderlock holder, alert eval duration, Go runtime + DuckDB memory; JetStream stream bytes and fill ratio, consumer lag per shard, publish rejections, broker Raft health (from NATS monitoring on `:8222`).
   - `otelgin`/`otelgrpc` tracing at 1% into a reserved `system` project with a loop guard.
   - Published SLOs: ack p99 <500ms including the JetStream commit; ingest→queryable <2 min p95 for high-volume pairs (a size-flushed L0; low-volume pairs up to the 1-hour max-age, with recent rows readable from the stream by Live Tail); JetStream consumer lag <5 min; query p95 <5s for 1h windows under budget; compaction backlog <10 min. Seed dashboard + alert rules.
   - New `internal/telemetry/`.

8. **Alerting & notifications** (L)
   - Tables: `alert_rules`, `alert_states`, `notification_channels` (config AES-GCM via `SecretKeyRepository.Encrypt` — the key is `SHA-256(secret_keys.secret)`, the same DB row that signs every JWT, so this guards against log/API/UI leakage only, not against a database dump; same posture as SSO client secrets and MFA seeds today), `alert_routes`, `silences`, `notification_outbox`.
   - Evaluator in `worker`, sharded `hash(rule_id) % ALERT_EVAL_SHARDS`, each shard a `leaderlock`. PromQL via existing engine; log/span rules via existing `ExecuteAggregation`. Prometheus state machine (inactive→pending→firing→resolved), fingerprinted, persisted per eval.
   - Outbox dispatcher with backoff, `group_wait`, silences at dispatch; the outbox stays a Postgres table (transactional with rule state), not a JetStream work queue. Egress: reuse the pinned resolve-check-dial pattern (`control_plane/services/egress.go:97-134`) but **not** its policy, which allows RFC1918 on purpose because Sites live on private networks; consolidate it and `oauth/clients.go` `publicOnlyDialContext` into `internal/util/egress` with the policy as a parameter. Alerting policy = oauth's `isPublicIP` set (deny loopback, link-local, unspecified, RFC1918/ULA, CGNAT 100.64/10, multicast; unwrap IPv4-mapped IPv6); `ALERT_EGRESS_ALLOWLIST` (Site-side, validated at startup like `CONTROL_PLANE_EGRESS_ALLOWLIST`) opts private ranges back in for in-network receivers, `ALERT_EGRESS_DENYLIST` covers a service CIDR the binary cannot discover. The alert `http.Client` refuses redirects, has a 10s overall timeout and a 64 KB response cap, and surfaces only the status code to the channel-test UI. Email: no SMTP sender exists today (the invite flow hands the admin a token instead); the relay is operator config, channels hold recipients only, `net/smtp` over the same pinned dialer with STARTTLS/TLS ≥1.2. Channels: webhook, Slack, email, PagerDuty.
   - Routes Site-scoped + proxy allowlist. Authz: rules PROJECT_ADMIN, silences PROJECT_MEMBER; channel create/edit/test = project OWNER or global ADMIN while channels are project-scoped (ship project-scoped first), PROJECT_ADMIN attaches existing channels to rules; channels move to ORG_ADMIN after item 9.
   - New `features/alerting/`, frontend `features/alerts`.

9. **Organizations / teams / SCIM** (XL)
   - `organizations`, `organization_memberships (ORG_OWNER|ORG_ADMIN|ORG_MEMBER)`, `teams`, `team_memberships`, `team_project_roles`; `projects.organization_id NOT NULL`; `org_plans`. Effective role = max(direct, team, SSO mapping).
   - Migration: one `Default` org, every project attached, ADMIN→ORG_OWNER; zero behaviour change for single-tenant installs.
   - **Authentication is untouched.** GitHub/Google social login (`users/controllers/user_controller.go` `/auth/github/callback`, `/auth/google/callback`, `users/services`), OIDC SSO connectors incl. Entra group lookup (`users/sso`), MFA, personal access tokens, Obzly's own OAuth 2.0 server for MCP clients (`features/oauth`), and the control plane's central identities keep working as-is; this item changes *authorization*. SCIM is provisioning beside SSO, not a login path: sign-in stays on the OIDC connector, SCIM's bearer token is per-org and separate from user access tokens.
   - **Existing SSO group mappings migrate unchanged.** `sso_group_mappings` (global Admin / project role) and `sso_site_group_mappings` (Site role) attach to the `Default` org with their targets as they are — nobody loses access on the upgrade; org-level targets (`ORG_ADMIN` etc.) are a new mapping kind added afterwards, not a rewrite of the old ones. Add a migration test that asserts every pre-upgrade mapping still resolves to the same effective role.
   - **Open before implementation: organizations vs the control plane.** As written this item assumes a single instance. Decide whether an org spans Sites (org lives on the control plane; Sites hold projects attached to a control-plane org id, and `central_identities` / `sso_site_group_mappings` are the org's identity edge) or lives per Site (each Site has its own orgs; the control plane only federates). The recommended default is **org on the control plane, spanning Sites** — it matches how SSO and Site grants already work — which means SCIM is served by the control plane, and `org_plans` quotas are enforced per Site against a control-plane-issued org id. Recommended: **no fan-out to Sites.** Every control-plane→Site call today is a per-user assertion minted from `ResolveEffectiveGrant` (`control_plane/controllers/proxy_controller.go:90`); there is no machine identity, and `/api/v1/users/*` is denylisted on the proxy. Keep provisioning central: grants already resolve lazily at token exchange (Site tokens live ≤1h), and deactivation revokes the central identity. Fan-out would need a new service identity and proxy rule. Single-site installs (`SERVICE_MODE=all`) are the degenerate case: the instance is its own control plane.
   - SCIM 2.0 `/scim/v2/{Users,Groups,ServiceProviderConfig,Schemas,ResourceTypes}`; per-org bearer token = random secret shown once, stored as `token_prefix` + SHA-256 `token_hash` in `scim_tokens(org_id, token_prefix, token_hash, scopes, expires_at, revoked_at)` — same shape as `access_tokens` (migration 20260924090000), never AES-encrypted because it is only ever compared; Groups→teams, PATCH ops, soft-deactivate; test against Okta/Entra checklists.
   - `internal/authz.Authorize(ctx, subject, action, resource)` + `authz.Require("action")` middleware; retire `CanUserWriteToProject` call sites incrementally with a straggler-grep test; decisions in audit log.

10. **Deployment & DR** (L)
    - Helm `deploy/helm/obzly/`, replacing the three kustomize/compose shapes above: broker (StatefulSet, 3 replicas, same image, `SERVICE_MODE=broker`, PVC or instance-store NVMe, on-demand node selector, one pod per AZ, PDB `maxUnavailable: 1`; or the official NATS chart by reference) / api / querier (Deployment; cache on `emptyDir` at `/obzly-data`, bounded by `emptyDir.sizeLimit` and `COLD_CACHE_MAX_SIZE_MB`, the same shape as `ha/deployment-api.yaml`; on instance-store nodes the node configuration puts ephemeral storage on the NVMe, e.g. Karpenter `instanceStorePolicy: RAID0`, so no PVC is involved; an opt-in persistent volume for EBS-only nodes; executor id for rendezvous = pod name) / worker / control-plane; HPA, PDB, NetworkPolicy (close querier gRPC, NATS 4222/6222/8222 and metrics ports to everything but the Site's own roles), ServiceMonitor, separate readiness and liveness probes (item 1's shutdown flag), migration pre-upgrade Job, `values` presets small|medium|enterprise from item 11's capacity model; external PG/Valkey/S3 by reference, or optional in-cluster CNPG `Cluster` / Valkey operator CRs (`postgres.mode`, `valkey.mode` — see "In-cluster Postgres and Valkey via operators"). PgBouncer / per-role pool sizing guidance.
    - Multi-arch: add `linux/amd64` to `platforms:` — a native build on the existing self-hosted amd64 runner, which already builds the arm64 image under QEMU — then publish one manifest list. A GitHub-hosted native matrix (`ubuntu-24.04` + `ubuntu-24.04-arm`) is an option if the QEMU arm64 build becomes the bottleneck, not a requirement.
    - Runbooks `docs/runbooks/{backup-restore,dr,upgrade}.md`: PG PITR (pgBackRest/wal-g for external PG; CNPG Barman Cloud backup + `recovery` bootstrap for in-cluster; RPO 5 min), S3 versioning; restore order PG → `worker` → `obzly reconcile` → `api`. `reconcile` CLI: row-without-object (mark lost, alert), object-without-row (re-adopt from Parquet footer), checksum mismatch. JetStream is a buffer, not a store of record: it needs no backup, but R3 across three AZs is what protects unflushed data, and the DR runbook restores brokers empty (acked-but-unflushed data in a lost broker quorum is gone — state that as the one loss case).
    - **Key material** (in `backup-restore.md`): `secret_keys` (auto-created on first start, no env override; `users/repositories/secret_key_repository.go`) is the root of trust for every session JWT, SSO client secrets, MFA seeds, the control plane's assertion key, state tokens and (item 8) channel configs. Restores are full-database only and must complete before the binary or the migration Job first touches the database: an app start against an empty database mints a new secret, and `secret_keys` has no singleton constraint (primary key on the secret itself) and is read with an unordered `First()`, so a data-only restore on top leaves two rows and an arbitrary key. Acceptance step: `SELECT count(*) FROM secret_keys` = 1, and one SSO login and one MFA verify succeed. Backups contain the key beside the ciphertexts — encrypt them with a key held outside the database (pgBackRest `repo-cipher-type`, wal-g libsodium, CNPG object-store encryption, SSE-KMS on AWS) and treat backup-bucket read access as secret access. If the row is lost for good, the only path is re-entry: SSO client secrets, MFA re-enrolment, a new assertion key with a Site JWKS refresh, re-issued sessions. Under item 9's org-on-control-plane default the control-plane database is the identity root for every Site: same rule, restored first.
    - Weekly leader-locked integrity audit sampling N% of segments (closes the documented limitation). Expand/contract migration policy, one-version rollback boundary — enforced and observable, not just stated. The admin **Upgrade status** page (Settings; `GET /api/v1/system/upgrade-status`, `features/system/upgradestatus`, added 2026-09-30) already reports goose schema state including `ahead`, per-stream rewrite progress, the upload and small-segment backlog and a `SafeToUpgrade` verdict, per deployment (the control plane reports schema only, each Site its own data). Three changes ride with this item: (a) make the boundary checkable — goose versions are timestamps, so add a per-release marker migration into `schema_releases(release, goose_version, applied_at)` and count marker rows newer than the binary's own: 1 = warning ("mid-rollout or supported rollback" — with RollingUpdate and startup migrations, N-1 pods see this during every rollout), ≥2 = blocker; today `BuildVerdict` blocks on any `ahead`. (b) Decide whether an optional rewrite (item 2's v2→v3) blocks the next release or only warns; today any remaining rewrite blocks. (c) Rewrite failures are per-process (`datamigration/worker.go:206`) and the `worker` never serves HTTP, so an `api` replica reports zero skipped; persist failures on the catalog row (`rewrite_failed_at`, `rewrite_error`) and compute remaining/skipped from the catalog. Runbook `upgrade.md`: each Site first, reading its Upgrade status before and after, then the control plane last (it ships the UI; see the version-skew row); the Helm pre-upgrade Job is additive — startup migrations under the Valkey lock stay.

11. **Performance validation** (M)
    - `cmd/datagen`: seeded realistic 1 TB recipe (services, pods, error rates, trace fan-out, 1M series).
    - Extend `cmd/loadtest` with `ingest-steady|ingest-burst|query-mix|soak-24h`, JSON output, nightly job vs `perf/baselines/*.json` with 10% gate. Add a Spot-interruption chaos test (SIGTERM and SIGKILL mid-ingest, rows counted before and after) that checks item 1's loss contract per tier.
    - Capacity model table in ARCHITECTURE.md (per-2vCPU throughput per signal → replica counts for 5 TB/day and the 22 TB/day ceiling), refreshed each release; it measures the compression ratio and the logs/spans/metrics split that the cost tables below assume.

12. **Also**: frontend Vitest + Playwright smoke set before item 4 (pagination contract changes) (M); control-plane Site status into Valkey (S).

## Phase ordering (to be written into ROADMAP.md as "Phase 14 — Enterprise scale")

Critical path: **7 → 3 → 1 → 4 → 5 → 6b**. Sizes sum to ~14–29 engineer-months; the critical path
alone is M + L + XL + XL + XL + L ≈ 8–17 months done in sequence. The schedule is therefore bounded
by the critical path, not by headcount: ~4 quarters with 3–4 engineers holds only if B and C run
without interruption.

- **A — See and measure** (parallel, no schema/protocol breaks; the interim adds one nullable column): 7, 11, 2 (format options + sort + stats), item 1's interim uploader and shutdown/readiness work, 10 multi-arch + Helm v1 for current roles, frontend test baseline, honour `TrackTotal`.
- **B — Write path & catalog** (sequential): 3 (one-time partition migration, do it while data is small) → 1: broker role and embedded NATS first, then JetStream ingest for logs, spans and metrics (incl. the date-prefixed key layout; + 6's `DeleteBatch` in the same key change) and Live Tail on core NATS, then the Valkey queue removal. Until 1 lands, HA `api` replicas are covered by the Phase A interim uploader.
- **C — Read path**: 4 (uses the broker from B for discovery and dispatch), then 5 in parallel once fragment interface is defined; 2 promoted fields (the one `schema_version` bump); Helm adds querier.
- **D — Lifecycle & DR** (after B; 6a in parallel with C, 6b after 5): 6a policies/sampling/partition-drop, 6b rollup-only aging; 10 runbooks, `reconcile`, integrity audit, Upgrade status changes.
- **E — Enterprise track** (separate engineer from B onward): 9 orgs/teams/authz → SCIM; 8 alerting project-scoped → org-scoped channels after 9.

Acceptance criteria, one per item (each becomes the item's "done" line in ROADMAP.md):

| Item | Done when |
|---|---|
| 1 | 5 TB/day sustained 24h on N `api` replicas, p99 ack <500ms including the JetStream commit; chaos test: killing any one `api`, `worker` or broker pod (SIGTERM or SIGKILL) loses 0 acked rows; no Valkey on the ingest path; compaction throughput scales with worker count, and killing a worker mid-merge leaves the catalog consistent with its shards resumed within the lease TTL. |
| 2 | On the 1 TB `datagen` set, ZSTD files ≥25% smaller than today's; a service-filtered 1h query skips ≥80% of row groups. |
| 3 | `PlanSegments` p95 <50 ms on a 5M-row catalog; retiring a month is detach + drop, no row-by-row delete. |
| 4 | 30-day dashboard on 1 PB <10s under budget; killing an executor mid-query returns an identical result within the deadline; killing a coordinator mid-query surfaces a retryable error; adding an executor moves ≤ ~1/N of cache affinity. |
| 5 | `rate()` over 1h across 1M series p95 <5s; PromQL parity suite passes against the Go `matchAll` oracle. |
| 6 | Zero partial traces after tail sampling in a fan-out test; lifecycle transitions applied by tag and prefix rules. |
| 7 | Every SLO in item 7 has a metric and an alert rule; seed dashboard ships. |
| 8 | Rule-to-notification p95 <60s; egress tests (private IP, redirect, DNS rebinding) are refused. |
| 9 | Pre-upgrade mapping test passes; Okta and Entra SCIM checklists green. |
| 10 | Chart installs small/medium/enterprise presets; restore drill meets RPO 5 min and the key-material check; amd64 + arm64 manifest published. |
| 11 | Nightly suite runs and its 10% gate blocks a regression; capacity model refreshed. |
| 12 | Playwright smoke green before item 4 merges. |

## Cost impact — to be written into the ARCHITECTURE.md scale section

Prices: PUT $0.005/1k, GET/HEAD $0.0004/1k, DELETE free, storage $23/TB-mo Standard, $12.5 IA,
$4 Glacier IR; same-region transfer to compute free.

- **Today** at 5 TB/day raw (~0.8 TB/day Parquet at today's Snappy, ~6×; ~0.6 TB/day after item 2's
  ZSTD, 8×): ~6k PUT + 6k HEAD/day → ~$1/mo. Negligible.
- **L0 flushes by size, so request cost stays small.** With JetStream holding unflushed data, an L0
  file is written when it reaches ~64 MB or turns an hour old, never on a 5s timer. A naive 5s
  flush per (replica × stream × project) at 5,000 projects would be ~259M PUT/day ≈ $1,300/day —
  the problem the previous S3-only design had to solve with adaptive flush tiers and a per-project
  floor. Size-flushed, the 22 TB/day ceiling writes ~400k L0 files/day (mostly hourly files from
  low-volume pairs) ≈ $75/mo in requests. **Requests ≈ $7/mo at 1 TB/day, ≈ $30 at 5 TB/day and ≈
  $75 at the 1 PB ceiling, against storage of $320, $1.6k and $7.2k tiered ($23.5k Standard)** —
  about 1–2% of the S3 bill; storage dominates.
- Roadmap *reduces* the dominant cost: ZSTD −30–40%, IA/Glacier tiering, sampling, rollup-only aging.
- Real delta is **compute**: leveled compaction rewrites each byte 2–3× (worker CPU/disk); `querier`
  replicas are new. Quantified by the Phase A capacity model.
- Guardrails: L0 target size and max-age as config with a hard floor on max-age (≥5 min);
  `obzly_s3_requests_total{op}` counter (item 7) so PUT rate is visible before it is a bill.

### Detailed S3 model (write as a table in ARCHITECTURE.md)

Storage for 1 PB at 1-yr retention (≈22 TB/day raw at 8×): all-Standard ≈ $23.5k/mo; 30d Standard /
60d IA / rest Glacier IR ≈ $7.2k/mo ($7/TB-mo blend); with sampling + rollup-only aging ≈
$3.5–4.8k/mo. Hidden lines: **cold retrieval is per-GB** (IA $0.01/GB, GIR $0.03/GB → a 500 GB
Glacier scan = $15/query; planner must refuse/warn, `storage_class` on catalog row); min durations
30d/90d match the tier schedule; versioning for DR costs ~7 days of deleted data (~$160/mo) with 7d
noncurrent expiry; cross-region replication optional (~$600/mo transfer + duplicate storage at
1 TB/day), not in base plan.

Requests as a function of `R` (L0 files/s cluster-wide): L0 PUT = R × $12.96/mo; each of L0 HEAD,
compactor GET, querier GET ≈ R × $1.04/mo; L1/L2 ops <$2; query cold GETs ~$12; lifecycle <$1;
DELETE free; LIST avoided (catalog is the index). Totals: **R=1 → ~$16/mo; R=5 (the 1 PB
ceiling, size-flushed) → ~$80/mo; R=10 → ~$175/mo; a flat 5s flush with no floor (R≈3,000) →
~$39k/mo in PUTs alone.** Workers write L0 from sharded consumers, so R depends on (project, stream)
pairs and data volume, not on `api` replica count; the capacity model (item 11) re-derives it.
Rejected: S3 Express One Zone for L0 (single-AZ durability
defeats staging); on MinIO request cost is zero so `R` is only a compaction-CPU concern.

### S3 cost by ingest tier (write as a table in ARCHITECTURE.md)

Assumptions: 8× ZSTD, 1-yr retention, 30d Std → 60d IA → GIR, L0 flushed from JetStream by size
(64 MB) or at a 1-hour max-age per (project, stream), three streams per project. L0 files/day ≈
pairs × 24 + compressed bytes ÷ 64 MB. Requests ≈ `L0 files/day × $0.000186/mo` (PUT + HEAD +
compaction GET + query GET per file) plus ~$12/mo query cold reads at the TB tiers; storage ≈
$7/TB-mo blended. The last row is the storage design ceiling: 1 PB held for one year needs ≈
22 TB/day raw at 8×.

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

Takeaways for the design: (1) below ~10 GB/day the existing local-disk-only default (no
`OBJECT_STORAGE_URL`) stays the right deployment — roadmap does not change the default; below
~100 GB/day S3 is <$35/mo and compute dominates. (2) Low-volume pairs still produce small hourly
L0 files (~70 KB at 1 GB/day) → **compaction must also promote by age** (merge anything older than
~1 day into L1 regardless of size) and **never transition objects <1 MB to IA/GIR** (40 KB
per-object metadata overhead). (3) Storage is the S3 bill at every scale; the request line only
becomes material if the L0 max-age floor is removed (a flat 5s flush would make the 100-project
install ~$960/mo), which is why the floor is enforced in config.

### Compute by ingest tier (write as a table in ARCHITECTURE.md)

The compute side of the same tiers, so the S3 table above is never read as the whole bill. Method,
so the numbers can be refreshed by the Phase A capacity model (item 11) rather than trusted:

- **Measured** (ROADMAP.md Phase 7, 2-CPU test host, 200-byte synthetic lines): REST logs
  21.8–31k lines/s (~4.4–6.2 MB/s), OTLP/gRPC logs 16.7k/s (~3.3 MB/s), spans 34k/s, points 51k/s.
  Call it **~3–6 MB/s raw ingest per 2 vCPU at peak**, on today's Valkey-hop write path.
- **Planning rate for `api`: ~0.7 MB/s raw per vCPU** (≈25–45% of measured peak). The headroom
  covers bursts, the query coordinator that also runs on `api` (item 4), and the 30% on-demand
  baseline. This is the rate the 5 TB/day illustration in the Spot section already implies
  (58 MB/s ÷ 80 vCPU), so both sections agree.
- **`worker`**: leveled compaction rewrites each byte 2–3× (item 1) plus retention, rollups (5),
  tail sampling (6) and alert evaluation (8): **~3 vCPU per TB/day raw**, minimum one 2-vCPU
  replica. Compaction is bursty, so this is provisioned, not average, load.
- **`querier`** (item 4, Phase C): scales with **bytes scanned**, i.e. stored volume × query
  concurrency, not ingest — **~1 vCPU per 25 TB stored** at the 1 PB anchor, with local cache
  disk ≈ 1% of stored. Below ~100 GB/day the executor stays in-process (in `all`, or in `api`).
- **`broker`** (NATS, item 1): on-demand only, three pods across AZs (five at the ceiling). Disk =
  ingest rate × the ~1 h lag budget × 3 replicas (58 MB/s → ~630 GB at 5 TB/day; 259 MB/s → ~2.8 TB
  at 22 TB/day). CPU is modest: ~1 vCPU per 20 MB/s replicated. With the writer moving from `api`
  to `worker`, `api` gets lighter and `worker` heavier by about the same amount; the planning rates
  above are kept until item 11 measures the split.
- **Postgres**: catalog rows (~1 per 128 MB segment plus L0 churn) and `N replicas × pool`
  connections; size by connection count, not rows, until the TB tier. **Valkey** after item 1:
  locks, limits, pub/sub, cache only.
- Instances: Graviton `m7g` (`large` = 2 vCPU/8 GiB, `xlarge` = 4/16). Prices are approximate
  **us-east-1 on-demand list, Sept 2026** — verify before quoting; eu-west-1 runs ~10% higher.
  m7g.medium ≈ $30/mo, m7g.large ≈ $60/mo, m7g.xlarge ≈ $119/mo, im4gn.2xlarge ≈ $530/mo (8 vCPU,
  1.9 TB NVMe), Spot ≈ 70% off. RDS Postgres db.t4g.medium ≈ $47
  (single-AZ) / $94 (Multi-AZ), db.m7g.large ≈ $150 / $300, db.m7g.xlarge ≈ $300 / $600,
  db.m7g.2xlarge ≈ $600 / $1,200, db.m7g.4xlarge ≈ $1,200 / $2,400. ElastiCache Valkey
  cache.t4g.small ≈ $23, cache.t4g.medium ≈ $50, cache.m7g.large ≈ $114, cache.m7g.xlarge ≈ $227.

**Sizing by tier** (post-roadmap topology; "all" = one replica running every role):

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

At 10 GB/day and below the second `api` replica buys availability, not throughput: one 2-vCPU
replica is under 5% busy at 10 GB/day. The single-replica rows stay on-demand — with one
replica there is no on-demand baseline to absorb a Spot interruption (the interruption itself is
graceful: 2-min notice → SIGTERM), so the reason is availability during the replacement gap; their
embedded R1 JetStream survives a process crash and loses at most `sync_interval` on a host loss.
The multi-replica rows need the `broker` role from 10 GB/day up: that is the price of zero-loss HA
(~$100/mo at 10–100 GB/day). An install that accepts the single-node loss profile stays on one
`all` replica. Until item 1 lands, today's multi-replica overlay needs the Phase A interim uploader
(finding 1b); the "Compute w/ Spot" column below assumes item 1.

**Monthly cost by tier** (compute = the `api`/`worker`/`querier` rows above; Spot = 70% off with
30% of `api` kept on-demand; S3 from the table above; local gp3 disk for the no-S3 tiers):

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

`broker` = on-demand instances plus gp3 for the JetStream store (3 × m7g.medium + ~$10; 3 ×
m7g.large + ~$10; 3 × m7g.xlarge + ~$50 for 630 GB; 5 × im4gn.2xlarge with NVMe included). Against
the previous S3-only design the broker adds ~$100–2.7k/mo and removes ~$30–2.1k/mo of S3 requests;
what it buys is the zero-loss contract.

Not in the table: `querier` cache disk at the TB tiers when on gp3 (2 × 500 GB ≈ $80/mo at 1 TB/day;
10 × 1 TB ≈ $800/mo at 5 TB/day and 22 TB/day). Instance-store NVMe (im4gn.xlarge ≈ $265/mo
on-demand, 937 GB included) is cheaper only under Spot at the 5 TB tier and above (10 × im4gn ≈
$795/mo Spot vs 10 × m7g.xlarge + 1 TB gp3 ≈ $1,157/mo); on-demand, or at 1 TB/day, m7g.xlarge +
gp3 is the same or cheaper — which is why the sizing table keeps m7g.xlarge and the Spot section
prefers NVMe. Also not included: load balancer and NAT (a few tens of $/mo), and the control plane
(1 × m7g.large ≈ $60/mo, shared across Sites).

Takeaways: (1) **below ~100 GB/day the managed Postgres and Valkey are most of the bill**, and a
Multi-AZ database is the single biggest line — that is the first thing a small install should
decide on, not S3 (the in-cluster operator option below cuts it by ~$25–40/mo at 1–10 GB and
~$200–430/mo from 50 GB up); from 10 GB/day the broker is the next line, and it is what makes the
multi-replica tiers zero-loss. (2) From ~1 TB/day compute and S3 are the same order, and at 5 TB/day
Spot saves more per month than the broker costs, which is why keeping `api`, `worker` and `querier`
stateless (JetStream on on-demand brokers, item 4's idempotent fragments) is on the critical path. (3) Every rate above is a
planning assumption until item 11's nightly suite measures it; refresh this table with the
capacity model each release.

### In-cluster Postgres and Valkey via operators (option below ~1 TB/day)

The "Postgres + Valkey" column above assumes RDS + ElastiCache. Both can instead run in-cluster
under operators, and below ~1 TB/day that is a reasonable default. What Obzly needs from each is
small, which is what makes it safe: Postgres = a plain DSN, PG 13+ (`gen_random_uuid` is built
in), no extensions, no superuser, migrations at startup under the Valkey leader lock; Valkey = a
**single host:port** (`cache/cache.go` uses one `InitAddress`, no Sentinel/Cluster awareness).
Valkey holds leader locks (migrations, compaction/retention/upload, data migration, correlation),
rate and attempt limits (per-project ingest, login, MFA), live-tail pub/sub (until item 1 moves it
to NATS), the query concurrency limiter, caches (API keys and their last-used throttle, access-token state, the control plane's
Site tokens), MCP OAuth authorization codes and registered clients, the admin ingestion-rate
counters and, until item 1, the logs queue. All of it except the queue is TTL'd or rebuildable, so
a Valkey loss after item 1 is a hiccup: in-flight MCP OAuth flows and sessions mid-exchange retry.
`deploy/kubernetes/eks/valkey-deployment.yaml` already runs Valkey in-cluster as a single
non-persistent Deployment, so an operator is a step up there, not a change of approach.

- **Postgres — CloudNativePG**: `instances: 2` (primary + streaming replica) with anti-affinity
  across AZs (EBS is single-AZ) from the 10 GB tier, on the **on-demand** nodegroup (catalog =
  source of truth; see the Spot table); single-node installs (1–5 GB) run `instances: 1` — no zone
  failover, parity with the single-AZ RDS they replace. App DSN → the operator's `-rw` Service,
  which follows failover in seconds; set `sslmode=require` (CNPG serves TLS). Continuous WAL
  archiving to S3 via the Barman Cloud plugin gives PITR at the 5-min RPO item 10's runbook targets
  — the runbook's "pgBackRest/wal-g" step becomes "CNPG backup + `recovery` bootstrap". Set
  `max_connections` in the cluster spec with room for every `api`/`worker` replica's GORM pool (the
  connection-count hazard noted above).
- **Valkey — an operator that runs a primary + replica behind a Service that always points at the
  current primary**, so the client keeps addressing one host:port; **AOF persistence on a small
  PVC** so locks and the pre-item-1 logs queue survive a restart; on-demand nodes. Check candidates
  against that requirement before choosing: Hyperspike's valkey-operator is built around cluster
  mode, and OT-Container-Kit's replication mode fails over through Sentinel, which needs either a
  Sentinel-aware client or a primary-tracking Service. **Not Valkey Cluster mode** — the client
  would need hash-tagged keys and cluster routing; nothing in the roadmap needs it. If no operator
  fits, a single-replica StatefulSet with AOF on a PVC (today's Deployment plus persistence) is the
  fallback. AOF removes the restart-loss part of finding 1, not the RAM-SPOF and unbounded-queue
  parts — item 1 is still what fixes those. Memory stays well under 1 GB below the TB tier.
- **NATS — no operator needed**: the chart's `broker` StatefulSet (same image) or the official NATS
  chart, JetStream on a PVC (gp3) or instance NVMe, on-demand nodes, one pod per AZ. It holds only
  unflushed data, so it has no backup step; its upgrades roll one pod at a time.
- **What it costs instead**: major-version upgrades, failover drills and **restore tests** are
  yours (the operator automates failover and minor upgrades; nobody verifies backups unless you
  do). Zone failure is covered only with the second instance in another AZ. Above ~1 TB/day the
  managed default stands: after item 3 the catalog is partitioned and busy, and a managed service
  with a support contract earns its price there.
- **Helm (item 10)**: `postgres.mode: external | cnpg` and `valkey.mode: external | operator`
  values — `external` = by reference (today's README), the other = an optional CNPG `Cluster` /
  Valkey CR rendered by the chart. The operators themselves are installed separately: their CRDs
  must exist before the chart can render a CR, so the chart checks for the CRD and fails with a
  clear message instead of installing the operator. The `small` and `medium` presets default to
  in-cluster; the `enterprise` preset to external.

The operators mostly consume headroom on nodes already paid for. Replacing the "Postgres + Valkey"
column:

| Ingest/day | Managed (RDS + ElastiCache) | In-cluster via operators | Saving |
|---|---|---|---|
| 1–5 GB (`instances: 1`) | $70 | ~$20–30 node share + ~$10 gp3 | ~$30–40/mo |
| 10 GB (`instances: 2`; needs a second on-demand m7g.large beyond the Spot figure above) | $117 | ~$60 node + ~$20 share + ~$10 gp3 ≈ $90 | ~$25–30/mo |
| 50–100 GB | $350 | 2 × 2-vCPU nodes ≈ $120 + ~$15 gp3 | ~$200/mo |
| 1 TB | $714 | 2 × m7g.xlarge ≈ $240 + ~$40 gp3 | ~$430/mo — but see above |

## Spot instances (Postgres + Valkey on-demand)

| Role | Spot fit | Requirements |
|---|---|---|
| `api` | Good with ~30% on-demand baseline (separate nodegroup + topology spread) — **after item 1** (its Phase A interim at minimum) | After item 1 an `api` pod acks only after the JetStream commit and holds nothing beyond in-flight requests, so an interruption loses nothing that was acked. Until then, `flushAllSegmentsOnce` on SIGTERM is local-disk only and in `SERVICE_MODE=api` nothing uploads, so on `emptyDir` even a drained pod loses every segment it flushed — keep `api` on-demand (or `all` on a PVC) until the interim lands. Readiness→false immediately, LB deregistration ≤30s, `terminationGracePeriodSeconds` ~60s, NTH/Karpenter drain. Collectors retry 503. |
| `querier` | Ideal | Item 4 fragments must be idempotent + retried on another executor; rendezvous rebalances cache. Cache on `emptyDir` over instance-store-backed node ephemeral storage (i4i/im4gn), not an EBS PVC — an EBS-backed pod is pinned to one AZ and cannot follow a Spot replacement. |
| `broker` | **No — on-demand only** | Holds acked-but-unflushed data and Raft state. Three pods across three AZs (five at the ceiling), PDB `maxUnavailable: 1`. |
| `worker` | Good | JetStream consumers redeliver unacked messages to the next owner of the shard, so a reclaimed worker loses nothing. Already idempotent (temp-then-rename, `ReplaceSegments` tx, expiring leases). Orphans cleaned by `CleanupStaleTempFiles` + `reconcile`. Migration Job on-demand only. |
| `control-plane` | Fine, keep 1 on-demand | Stateless but query chokepoint. |
| Postgres | On-demand, Multi-AZ, PITR | Catalog = source of truth. Same rule for an in-cluster CNPG cluster: on-demand nodegroup, instances in two AZs. |
| Valkey | On-demand | Mandatory today (logs queue); after item 1 only locks/limits/caches → loss is a hiccup. In-cluster operator: AOF on PVC, on-demand nodes. |

Plan changes: (1) **no local WAL** — a node-local log disappears with the disk under Spot; JetStream
R3 on on-demand brokers is the durability layer, which is what lets every other role run on Spot.
(2) The loss contract per failure mode: R3 — zero loss for any single-node failure, including one
broker; loss only if a broker quorum is lost. Embedded R1 (`all`) — survives a process crash; a host
loss drops up to `sync_interval`. Illustrative savings at 5 TB/day: ~20 api + 10 querier + 4 worker
m7g.xlarge ≈ $4k/mo on-demand → ~$1.4–1.8k/mo with 70% Spot; more than the brokers cost.

## Existing data on deployments — migration/compat story per item

Principle: **expand/contract migrations, one-version rollback boundary, never a mass S3 rewrite** —
background rewrites only, resumable, optional. The catalog already stores `object_key` and
`schema_version` per segment, which is what makes each step cheap.

**Readiness signal:** before each upgrade, the admin **Upgrade status** page (Settings; `GET
/api/v1/system/upgrade-status`, per Site through the control plane) must report safe for every Site
and for the control plane. It already covers schema state, rewrite progress and the upload backlog;
Phase B adds Valkey logs-queue depth (for the queue-removal drain), JetStream consumer lag and stream
fill (blockers above a threshold), broker cluster health, and the catalog-partition migration state
(item 10 lists the changes to its verdict).

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

## Files to modify (docs only)

**Status (2026-09-30):** the `ARCHITECTURE.md` and `ROADMAP.md` edits below are done — a new
"Scale architecture (1 PB / 5 TB/day)" section and "Phase 14 — Enterprise scale". The deploy-doc
corrections and the runbooks stub are still to do.

- `ARCHITECTURE.md`
  - New top-level section **"Scale architecture (1 PB / 5 TB/day)"** after "High availability" containing items 1–7, 9, 11 designs above, plus subsections for the cost model (S3 model, S3 and compute tier tables), deployment options (the in-cluster operator option), Spot, and upgrade and compatibility (the migration/compat table and the Upgrade status readiness signal).
  - In "High availability" and "Storage architecture" (line ~59-60): the NATS/DataFusion cluster-mode paragraph becomes **NATS adopted** (embedded in `all`, `broker` role in HA, JetStream as the ingest buffer) and **DataFusion still rejected**, with a pointer to the new section — do not delete the history.
  - "Licensing constraint" table: add `nats-server` / `nats.go` (Apache-2.0, safe to import and embed).
  - In "High availability": correct its statement that `emptyDir` is safe because durable data lives in object storage (false for `api`-written segments until item 1's interim, finding 1b).
  - In "Multi-site: control plane and Sites": item 9's decision on organizations vs the control plane, once taken.
  - Update "Storage architecture" (leveled segments, object key layout); "Known limitation: no ongoing object-storage integrity audit" → closed by item 10's weekly audit in 14-D; the `compactor.go:45` file-order contract. In the new Scale section state item 1's durability contract explicitly: no WAL by design (JetStream is the durability layer — Spot section), ack after the JetStream commit, R3 = zero loss for any single-node failure (loss only if a broker quorum is lost), embedded R1 = survives a process crash, loses up to `sync_interval` on a host loss.
- `ROADMAP.md`
  - Phase 0: add note that the "not PB/day scale" framing is superseded by Phase 14.
  - Phase 7: tick "backup/restore" as moved to Phase 14-D; the "no WAL … accepted, documented tradeoff" bullet (≤30s bound) gets a pointer to Phase 14 item 1's loss contract, which moves durability off the node without a WAL.
  - New **"Phase 14 — Enterprise scale"** with sub-phases A–E, each item sized with dependencies and the acceptance criterion from the table above; separate numbered entries for alerting, orgs/SCIM, Helm/multi-arch, DR runbooks.
- Deploy docs: `deploy/kubernetes/eks/ha/README.md` and the `ha/deployment-api.yaml` header, `deploy/docker/docker-compose.ha.yml.example` header — the same `emptyDir` correction as "High availability"; `valkey-deployment.yaml` header (holds the logs queue until item 1).
- Optional: `docs/runbooks/README.md` stub listing the runbooks to be written in 14-D.

## Open questions for the review

Decided on 2026-09-30 and already reflected above: 1 PB means 1 PB stored at 1-year retention (its
own ~22 TB/day tier); the HA `api` gap is closed by a Phase A interim uploader; NATS JetStream
ships inside the image (embedded in `all`, `broker` role in HA) as the ingest buffer, Live Tail
transport and querier control plane, which replaced the earlier 5s/60s/300s flush tiers and their
per-tier loss contract.

Still open:

1. **Upgrade status semantics** (item 10): does an optional rewrite (item 2's v2→v3) block the next
   release or only warn; is a per-release `schema_releases` marker migration acceptable?
2. **Querier shape and instance type**: Deployment + `emptyDir` (proposed) or StatefulSet; m7g +
   gp3 (sizing table) or im4gn NVMe under Spot at 5 TB/day and above.
3. **Interim channel-creation authz** (item 8): project OWNER / global ADMIN (proposed) or Site
   ADMIN, until channels become org-scoped.
4. **Compression figures**: two figures (today's Snappy ~6×, post-ZSTD 8×, as written) or one 8×
   planning figure; item 11 measures the real ratio either way.
5. **CNPG at 1–5 GB**: `instances: 1` on the single node (proposed) or a second on-demand node for
   zone failover.
6. **Ingest mix**: 5 TB/day treated as all logs at 200 B/line (≈290k lines/s, as written) or a
   logs/spans/metrics split; item 11 measures it.
7. **NATS packaging** (item 1): `broker` mode from the one image (proposed default), with the
   official NATS chart as a supported alternative — or official chart only at the HA tiers.
8. **JetStream partitioning at the ceiling**: how many streams/shards per signal at 22 TB/day, and
   R3 or R5 on five brokers; item 11 sizes it.
9. **Embedded R1 durability**: keep JetStream's default `sync_interval` (loses up to 2 min on a host
   loss) or `sync_always` (no host-loss window, lower throughput) for single-node installs.
10. **Organizations vs the control plane** (item 9): org on the control plane spanning Sites with no
   fan-out (proposed), or per-Site orgs.

## Verification

- Documents read coherently end-to-end; every "why it breaks" cites a real file/line, re-verified
  on 2026-09-30 against `main` plus the uncommitted Upgrade status change. Provenance of the
  2026-09-30 pre-review: findings on row 1/1b, the loss contract, the WAL, the schema bump, the S3
  arithmetic, alerting egress, key material and the upgrade gate were each challenged by three
  independent verifiers and survived; the remaining corrections (catalog pruning and partition
  drop, querier gRPC exposure, SCIM fan-out, version skew, citations in rows 4 and 10, multi-arch,
  probes, the flat-flush figure, key layout, tail sampling, lifecycle rules, operators, phase
  dependencies, acceptance criteria, executor placement) were checked against the code by hand.
- Every code file/package named in the design exists today or is explicitly marked new.
- `ROADMAP.md` Phase 14 items each have size + deps + acceptance criteria; the A–E ordering matches the dependency graph (no phase depends on a later one; 6b waits for 5).
- No item requires infrastructure beyond Postgres + Valkey + S3 + the one image; NATS runs from that image (embedded or `broker` role) and is stated as stateful; the operators, ServiceMonitor and Spot drain tooling are optional.
- Arithmetic (NATS revision): broker disk = rate × 3,600 s × 3 (58 MB/s → ~630 GB; 259 MB/s → ~2.8 TB); L0 files/day = pairs × 24 + compressed ÷ 64 MB (22 TB: 15,000 × 24 + 2.8 TB ÷ 64 MB ≈ 404k → ~$75); totals ≈ $320 / $570 / $660 / $1,580 / $5,200 / $19,000; R × $12.96 per L0 file/s; 22 TB/day = 1,024 TB × 8 ÷ 365; 1,024 TB × $7 ≈ $7,170; 117 × $119 ≈ $13,900 on-demand; Spot $3,213 + $2,268 + $612 + $360 ≈ $6,450; $2,400 + $227 ≈ $2,630 for Postgres + Valkey; 5 × $530 ≈ $2,660 for brokers at the ceiling.
- Run `prettier --check` on the markdown if the repo's lint covers it (frontend lint job does not; skip otherwise).
