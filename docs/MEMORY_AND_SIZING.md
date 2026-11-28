# Memory, pod sizing and large queries

How an Obzly replica uses memory, what protects it from being OOM-killed, the caveats, recommended
pod sizes, and what to do when users often run large queries.

## How a replica uses memory

A replica's memory is Go's heap plus a set of DuckDB databases ("handles"). Each handle has its own
`memory_limit` and spills to disk past it (up to `QUERIER_SPILL_MAX_SIZE` per handle, on the data
volume):

| Handle | Where | Cap |
| --- | --- | --- |
| Search handles, one per signal (`querier_logs`, `querier_spans`, ...) | `api`, `all` | 40% of the container each |
| Search handles | `worker` | 1/16 of the container each (with a floor and a ceiling) |
| Executor | `querier` | 60% of the container |
| SQL mode, one per running query (`logs_sql_sandbox`); also the AI usage pages and web analytics | `api`, `all`; `worker` for web analytics | the search cap (web analytics: its own budget), or less from the memory gate |
| Writer, tail mergers, token filters, compactor | every storage replica | fixed: 1 GB (compactor), 384 MB, 256 MB |

The caps add up to more than the container, deliberately: handles rarely fill their caps at the same
time, and capping each one lower would make every query spill and slow down. At startup each replica
logs `DuckDB memory budget` with every cap and warns when the sum is over the container limit.

The handle that can multiply is SQL mode's: each query opens a new DuckDB and **copies the whole
time window into it** before running the statement. So its memory follows the window, not the
query: in the lab a 30-minute window of about 6.5 million lines (3,600 lines per second) took an
API pod from 0.3 GB to 2.7 GB, about 350 bytes per line, before spilling. Before the memory gate,
one such query beside ingest traffic OOM-killed a 4 GiB API pod, and every request on it failed.

## What protects a replica

**The memory gate (`QUERY_MEMORY_FRACTION`, default `0.6`).** SQL-mode queries are admitted within
that share of the container:

- The long-lived handles count at what they actually use (re-read every 2 s), so ordinary searches,
  Volume, Patterns, the health map, traces and metrics are never held back.
- A new SQL-mode query gets what is free, up to its usual cap. When less than its cap is free it runs
  with less and spills more (slower, but the pod stays up).
- With less than 256 MB free it waits up to 10 s for other SQL queries to finish, then answers
  `429` (`TOO_MANY_CONCURRENT_QUERIES`, "try again in a moment"). One SQL query is always admitted
  when none is running.
- `QUERY_MEMORY_FRACTION=0` turns the gate off (every SQL query gets its full cap, as before).

**Go's soft memory limit.** Unless you set `GOMEMLIMIT` yourself, Obzly keeps it at 90% of the
container minus what DuckDB holds, and never below a quarter of the container. So Go collects garbage
harder exactly while DuckDB is busy. Setting `GOMEMLIMIT` turns this off. A `broker` keeps it at
90% of its container: recovering a full ingest stream after a restart reads it block by block, and
without a limit Go let the heap reach twice what was live (in the lab all three brokers, 2 GiB each,
were OOM-killed on every restart with 5.6 GB pending).

**Errors that say what to do.** A SQL-mode query that needs more memory than it can get even
spilling answers `422 QUERY_NEEDS_MORE_MEMORY` ("narrow the time range"). A mistake in the SQL
itself (an unknown column, a syntax error) answers `400` with DuckDB's message. Neither is a `500`
any more.

**Measured in the lab** (4 GiB `api` pod, a project ingesting 3,600 lines per second, 30-minute
SQL windows over cold segments):

| | No gate | Gate `0.75`, chunks read into memory | Gate `0.75`, chunks streamed | Gate `0.6`, chunks streamed (default) |
| --- | --- | --- | --- | --- |
| Queries at once | 1 | 3 | 3 | 3 |
| Outcome | pod OOM-killed | pod OOM-killed | 1 full, 1 reduced, 1 `429` | 1 full, 1 reduced, 1 `429` |
| Peak non-reclaimable memory | over 4 GiB | over 4 GiB | 3.9 GB | 3.2 GB |
| Peak Go heap | - | 1.7 GB | 0.5 GB | 0.5-0.75 GB |

Go's heap had been the hidden part: fetching only the needed columns of a large cold segment read
each column chunk (up to hundreds of MB) into memory, eight at a time per query. Those chunks now
stream straight into the cache file.

## Caveats

- **The gate covers SQL mode, not everything.** Search handles are bounded by their own caps, which
  are larger than an even share. A replica can still run out of memory when several search handles
  fill their caps at once beside Go; it is much less likely, and the startup budget warning tells you
  how far over the container the caps are.
- **Reduced SQL queries are slower.** Under memory pressure a SQL query runs with less memory and
  spills to the data volume. The volume needs room for it: `QUERIER_SPILL_MAX_SIZE` (default
  `8GB`) per running SQL query, beside segments and the cache.
- **The window is copied, then queried.** Filtering in the `WHERE` clause doesn't shrink what is
  copied: only the time range does. Copying may take up to 120 s and the statement itself 15 s;
  past that the query answers `408`.
- **Page cache counts in the pod's memory usage** but is reclaimed before an OOM kill. Watch the
  container's working set (`container_memory_working_set_bytes`), not `container_memory_usage_bytes`.
- **Requests below limits overcommit the node.** When pods' requests are far below their limits, a
  node can run out of memory while every pod is under its limit, and the kernel kills one anyway (the
  lab's k3s node did this at 8 GB).
- **The AI usage pages and web analytics use the same sandbox,** admitted by the same gate (web
  analytics on the `worker`, against the worker's memory). Under contention they also answer `429`.

## Recommended pod sizes

The chart's defaults suit a small Site. Size up when the startup budget warning is far over the
limit, when `obzly_query_memory_admissions_total{outcome="reduced"}` or `"refused"` grows, or
when a pod is OOM-killed.

| Tier | Chart default (limit) | Frequent SQL mode or large windows | Notes |
| --- | --- | --- | --- |
| `api` | 2 GiB, 2 CPU | 4-8 GiB, 2-4 CPU, requests = limits | SQL mode runs on the `api` replica that receives the request; more memory buys bigger windows before spilling |
| `querier` | 2 GiB, 2 CPU | 4 GiB, 2-4 CPU | Runs search fragments; scale out with `querier.autoscaling` |
| `worker` | 3 GiB, 1 CPU | 4-6 GiB, 2+ CPU | Ingest, compaction, rollups and web analytics; at least 2 CPU for a busy project |
| `broker` | 2 GiB, 1 CPU request | 2 GiB, 1-2 CPU, requests = limits | Memory follows the ingest buffer, not queries. A starved broker misses heartbeats and stream leaders move while ingest stalls: give brokers Guaranteed QoS on busy nodes |

A rough guide for SQL mode: about 350 bytes per line in the window before it spills. A one-hour
window of a project ingesting 3,600 lines per second (13 million lines) needs about 4.5 GB to run
without spilling: give `api` 8 GiB, or expect that query to spill and take longer.

Give the data volume (`emptyDir.sizeLimit` or the PVC) room for the cache plus
`QUERIER_SPILL_MAX_SIZE` for each SQL query you expect at once, and prefer local NVMe for it.

## When users often run large queries

- **Use the built-in views for counts over days.** Volume & noise, Patterns, the health map and
  dashboards read hourly and per-minute rollups and cached partitions; SQL mode reads raw logs.
- **Keep SQL windows short and aggregate.** Pick the range you need (the window is what is copied),
  return counts and groups rather than rows (results are capped at 10,000 rows), and move repeated
  questions to a dashboard panel, whose finished hours are cached (`QUERY_CACHE_TTL_HOURS`).
- **Give `api` replicas the memory, with requests equal to limits,** on nodes with headroom, and
  scale out with `api.autoscaling`: each replica has its own gate, so more replicas run more SQL
  queries at once.
- **Cap the scan:** `QUERY_MAX_SCAN_GB` refuses a query over that much stored data before it reads
  anything.
- **Watch the gate:**
  - `obzly_query_memory_admissions_total{outcome}`: `full` is healthy; a growing share of `reduced`
    means SQL queries spill (add memory); any `refused` means users got a `429`.
  - `obzly_query_memory_sandbox_bytes` and `obzly_duckdb_memory_bytes{handle="logs_sql_sandbox"}`:
    what SQL mode holds now.
  - `obzly_duckdb_memory_bytes` by handle and `go_memstats_heap_inuse_bytes`: where the rest goes.
- **Tune `QUERY_MEMORY_FRACTION`** only with those numbers. DuckDB overshoots its memory limits by
  about 10% and Go needs about 0.5 GB on a busy `api` replica, so the default leaves room for both.
  Raise it towards `0.7` on large pods (8 GiB and up) whose Go heap stays small when SQL queries
  are often reduced; lower it when a replica is still OOM-killed.
