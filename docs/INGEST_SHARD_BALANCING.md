# Ingest shard balancing

Status: **Phase 1 built** (load-aware placement, `internal/broker/balance*.go`), shipping in the
current stability batch. Phase 2 (hot projects across lanes) is a sketch for a later release.

## Problem

Workers split the ingest shards by **count**, not by load. Every signal (logs, spans, metrics)
has 64 shards, a project lives in exactly one of them (`FNV(project) % 64`), and each worker holds
up to `ceil(64 / live workers)` shard leases per signal (`broker.ShardRunner.fairShare`). That is
even when many small projects spread over the shards. With a few busy projects it is luck: whichever
worker happens to hold a busy project's shard does all of its work.

Seen in the homelab (2026-11-22), 2 workers with a 3-core limit each, four busy shards:

| Worker | Busy shards it held | Rows written | CPU |
|---|---|---|---|
| A | logs (project 1), metrics, spans | 1,517 + 2,040 + 2,633 /s | 3.0 cores, at its limit |
| B | logs (project 2) | 2,933 /s | 2.2 cores |

Every stream sat at its 10 GB cap with clients retrying 429s, while worker B had room. Adding a third
worker happened to move the metrics and spans shards to it: metrics 2,040 → 4,130 rows/s, spans 2,633 →
3,950 rows/s, every worker around 2 cores. The next restart could put them back together.

A second, related limit: one project is one shard on one goroutine, so a single project can never
use more than one worker. The US production project already ingests about 1,900 lines/s; the lab
measured a ceiling near 3,900 lines/s per project on its cores. Phase 2 addresses that.

## How it works today

- `broker.ShardRunner`, one per signal on every `worker` / `all` replica, registers in a Valkey
  sorted set (`obzly:ingest:workers:<signal>`) every tick (5 s) and holds shard leases
  (`leaderlock`, key `ingest-<signal>-shard-<n>`, 30 s TTL, renewed every tick).
- Each tick it renews its leases, gives back shards beyond the fair share, and claims free shards,
  starting at an offset from its member id, until it holds its fair share.
- An owned shard runs one durable pull consumer (`<signal>-shard-NN`) on one goroutine. Giving a
  shard away (`stopShard` → `ShardHandler.Release`) flushes, uploads and acks everything buffered
  first, then frees the lease; the next owner continues from the same durable consumer. So moving a
  shard is already safe and lossless; it costs one early L0 flush per project in the shard and a few
  seconds without consumption.
- Compaction follows the ingest lease (`broker.OwnsProject`): the worker that writes a project's L0
  files also compacts them. A move moves both.

## Goals and non-goals

Goals:

1. Spread **load**, not shard counts, across workers, counting all three signals together.
2. Converge within a few minutes after a worker joins, leaves or a project's traffic changes, without
   moving shards back and forth.
3. Keep the lease as the only authority on ownership: balancing decides who *should* own a shard,
   leases still decide who *does*. A bug in the balancer can cause poor placement, never two owners.
4. Behave exactly like today when Valkey is unreachable or the feature is off.

Non-goals (Phase 1): splitting one project across workers (Phase 2), changing the shard count,
balancing compaction separately from ingest, or balancing queriers (they already use rendezvous
hashing).

## Phase 1: load-aware placement

### What counts as load

Each owned shard keeps an exponentially weighted moving average (half-life 60 s) of the **payload
bytes it handles per second**, measured in `consumeShard` where every fetched message's size is
already summed. Bytes are known before any work happens, independent of how fast this worker is,
and comparable across workers. CPU time per shard is not measurable per goroutine, and wall time per
shard inflates on a saturated worker.

Signals cost different CPU per byte (a log line is hashed for its pattern and its fields extracted;
a span or metric sample mostly moves into Parquet). A per-signal weight converts bytes to comparable
load. Measured in the homelab (2026-11-22) from workers each holding one busy shard: logs about 1.6
CPU seconds per MB, spans about 0.28, metrics 0.2 to 0.35, so the weights are logs 1.0, spans 0.2,
metrics 0.2. They are constants, not settings.

A worker's **capacity** is its CPU limit (cgroup `cpu.max`, else `GOMAXPROCS`). A worker with twice
the cores targets twice the load.

Every shard counts at least a small **floor** (1 KB/s). Idle shards spread by count through their
own rule (below), so new projects still land on different workers as today. A first build used
16 KB/s: the 192 floors (3 MB/s) outweighed the lab's whole weighted load and pulled placement back
towards plain counts.

### The shared view

Each worker publishes, every tick, one Valkey hash per worker, `obzly:ingest:load:<member>`, with a
30 s TTL (the lease TTL), holding its capacity and, per owned shard, `signal/shard → bytes/s and
owned-since`. Membership stays the existing sorted set (one per signal today; one combined set
`obzly:ingest:workers` is added for the balancer, scored by heartbeat). A worker missing for a lease
TTL drops out of both, as today.

Reading the view is one pipeline per tick: the member set, then each member's hash. With tens of
workers and 192 shards that is a few KB.

### Planning

A process-level **balancer** owns the decisions for all three runners on the replica (today each
runner decides alone, which would treat "one busy logs shard here, one busy metrics shard there" as
balanced per signal while the worker is overloaded overall). The planner is a pure function, so it is
tested exhaustively without Valkey:

- **Input**: live workers with capacity, every owned shard's load and owner, unowned shards, pending
  handoffs, and the time of each worker's last move.
- **Target**: each worker's share of total load in proportion to its capacity.
- **Shed**: a worker sheds one shard when its load exceeds target × 1.25 *and* the excess is above
  256 KB/s of weighted load (noise; about 0.4 of a homelab core of log work). It picks the shard whose move best evens out the spread: the largest shard no
  bigger than its excess, else none (a single shard bigger than the excess would only move the
  overload; that is Phase 2's case). The receiver is the worker furthest below its target that can
  take it without going above target × 1.25 itself.
- **Limits against flapping**: one shed per worker per 2 minutes, a shard moves only after its owner
  has held it for 10 minutes (so at most once per 10 minutes), and a move happens only if it shrinks
  the largest overload by at least 10% of the target.
- **Idle shards by count**: loads alone never move an idle shard, so a worker joining a quiet cluster
  would get none. A worker holding more than `ceil(64 / workers)` shards of a signal hands its idle
  ones (at most 8 a tick, no cooldown: they buffer nothing) to the workers holding fewest.
- **Claim**: a free shard (new, given back, or its owner gone) is claimed by the worker with the most
  room below target; others leave it for 2 ticks. After that anyone claims it, so a stale view
  never leaves a shard without an owner for long.
- **Count guard**: no worker holds more than 3 × `ceil(64 / workers)` shards of one signal, so a
  wrong load figure can't pile every idle shard onto one worker.

### Directed handoff

Releasing a lease lets *any* worker claim the shard first. A handoff names the receiver:

1. The shedding worker writes `obzly:ingest:balance:handoff:<scope>:<signal>:<shard> =
   <receiver>|<load>` with a 30 s TTL. The receiver starts the shard's average from that load:
   starting from zero, it looked idle for minutes and could have been handed more.
2. It stops the shard as today (flush, upload, ack) and releases the lease.
3. Other workers skip a free shard with a handoff naming someone else; the receiver claims it on its
   next tick (at most 5 s) and deletes the handoff.
4. If the receiver doesn't claim it before the TTL (it died, or Valkey lost the key), the shard is an
   ordinary free shard again and the claim rule applies.

Ownership never depends on the handoff key: the receiver still has to win the lease, and a worker
that ignores handoffs (an older version during an upgrade) can only take the shard first, which
places it badly but safely.

### Cost of a move

The shard pauses while its owner flushes and uploads its buffered rows (seconds to tens of seconds on
a slow store), its projects get one L0 file earlier than they would have, and the receiver opens the
durable consumer. Compaction moves with it. With the limits above a stable site moves nothing; a
worker joining moves a handful of shards over a few minutes.

### Settings

- `INGEST_SHARD_BALANCE`: `load` (default) or `count` (today's behaviour, the kill switch).
- Everything else (half-life, tolerance, floor, cooldowns) is a constant with the measured reason
  next to it, as elsewhere in the code.

### Failure modes

| Failure | Behaviour |
|---|---|
| Valkey unreachable | As today: keep the shards held, claim nothing new; no sheds. |
| A worker's load report is stale or missing | Its shards count at their last figure until its hash expires (30 s), then the worker is gone and its shards free up when their leases expire. |
| Receiver dies mid-handoff | The handoff expires in 30 s; the shard is claimed by the normal rule. |
| Two workers' views disagree | Leases decide; at worst one extra move, limited by the cooldowns. |
| Mixed versions during an upgrade | Old workers keep count-based fair shares and ignore handoffs; new ones respect old workers' leases. Placement is suboptimal until the rollout ends, never unsafe. |
| One shard bigger than any worker's room | Left where it is (moving it only moves the overload); counted in a metric and logged once. Phase 2. |

### Observability

- `obzly_ingest_shard_load_bytes{signal,shard}` (owner only), `obzly_ingest_worker_load_ratio` (load
  ÷ target), `obzly_ingest_shard_moves_total{reason="shed|claim|lost"}`,
  `obzly_ingest_unsplittable_shards`.
- Logs: `Handing ingest shard to another worker` with the shard, its load and both workers' load
  ratios; `Ingest shard is busier than any worker's room` once per shard per hour.
- The planned Site health page shows the load ratio per worker and the busiest shards.

## Phase 2 (sketch): one busy project across several workers

Designed in full in [INGEST_LANES.md](INGEST_LANES.md); the sketch below is the starting point.

A project above a threshold (about 2,500 rows/s of one signal) gets **lanes**: the `api` publishes
its batches round-robin to `ingest.<signal>.<shard>.<project>` for K shards derived from
`(project, lane)`, K from 2 to 8 by load, stored per project in Postgres and cached by the
receivers. Nothing downstream needs order: logs and spans are sorted at query time and repeated ids
are dropped at merge; metric samples are appended and sorted by timestamp, and delta counters are
already turned into running totals at the receiver, before the broker. Each lane's owner writes its
own L0 files for the project (segments already carry their writer). Compaction stays with the
project's primary shard, which already merges files other workers uploaded. The tail that serves
not-yet-uploaded rows reads every lane's subjects. Lanes multiply a project's L0 files, so K grows
only with load. This needs its own design pass and sign-off.

## Testing

- **Planner unit tests** (pure function): idle → even counts; the lab's four busy shards on 2 and 3
  workers → each worker within 25% of target; a worker joining or leaving → bounded moves; noise and
  oscillating loads → no flapping; one oversized shard → left in place and reported; mixed
  capacities → proportional loads.
- **Integration tests** with real Valkey and the embedded broker: three runners with injected loads
  converge, honour handoffs, recover when a receiver dies mid-handoff, and fall back to holding
  shards when Valkey is down; no message lost or written twice across a move.
- **Lab**: the 2026-11-22 setup (two logs projects, traces, metrics) on 2 workers: before/after rows
  per second per signal, CPU per worker, moves over an hour; a rollout and a worker kill under load.
- **Weights**: CPU seconds per GB for each signal, measured with one busy shard of each on one worker.

## Decisions (signed off 2026-11-23)

1. Load = payload bytes per second with per-signal weights measured in the lab (not CPU sampling).
2. Balancing across all three signals per worker, through one process-level balancer.
3. Directed handoffs through a Valkey key, leases staying the only ownership authority.
4. The thresholds above. The build changed three after measuring: the noise floor 512 → 256 KB/s
   and the idle floor 16 → 1 KB/s (see above), and the separate 5-minute minimum age folded into
   the 10-minute minimum hold; idle shards spread by their own count rule.
5. On by default with `INGEST_SHARD_BALANCE=count` as the switch back.
6. Phase 2 (lanes for busy projects) as a separate design and release.
