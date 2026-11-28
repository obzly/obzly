# Ingest lanes: one busy project across several workers

Status: **built** (2026-11-23), Phase 2 of [ingest shard balancing](INGEST_SHARD_BALANCING.md):
`internal/broker/lanes.go`, `lane_controller.go`, and the sink in `internal/ingest/sink.go`. Where the
build differs from the design below, the section says so.

## Problem

A project lives in one shard per signal (`FNV(project) % 64`), and a shard is owned by one worker.
Phase 1 moves busy shards to workers with room, but it can't split one: a project whose load is more
than one worker can take stays capped at that worker, however many others there are. The balancer
counts these as `obzly_ingest_unsplittable_shards`.

Where the cap sits today:

| | Per project (one signal) | Source |
|---|---|---|
| Homelab, 3 old cores per worker | about 3,900 log lines/s | stress lab, 2026-11-20 |
| CPU cost per signal | logs ~1.6, spans ~0.28, metrics ~0.2–0.35 CPU s/MB | balancer weights, 2026-11-22 |
| US production project | about 1,900 log lines/s | read-only aggregate, 2026-11-22 |

So no deployment needs this yet: the US project would hit the cap at several times its current rate
on cloud cores, and a bigger worker raises the cap with no code at all. Lanes are for the project
that outgrows the largest sensible worker.

## Idea

A busy project gets **K lanes** (K = 1 for every project today). The receivers spread its batches over
K shards instead of one; Phase 1 then places those shards on different workers like any others. Each
lane's owner writes its own L0 files for the project. Nothing downstream needs batches in order:

- logs and spans are sorted at query time, and repeated ids are dropped when files merge;
- metric samples are appended and sorted by timestamp; delta counters are already turned into running
  totals at the receiver, before the broker;
- Live Tail, rate limiting and project checks all happen at the receiver.

## Design

### Lane shards

Lane 0 is the project's shard today, `base = FNV(project) % 64`. Lane i is `(base + 17·i) % 64`
(17 is coprime with 64, so K ≤ 64 lanes never collide with each other). The subject format doesn't
change: `ingest.<signal>.<shard>.<project>`.

### Publishing

`broker.PublishIngest` picks the lane per published message from the project's lane count, in a
per-project cycle where each lane appears as often as its weight (smooth weighted round-robin; equal
weights are plain round-robin). A request split into several messages spreads over the lanes too.
Each project has its own position in its cycle: with one counter shared by every project, two
projects publishing in turn could each land on one lane.

Lane counts live in Postgres, `ingest_lanes` (signal, project, K; only projects with K ≥ 2 have a
row). Every replica (`broker.LaneCache`) rereads the table every 30 s and keeps what it last read
while Postgres can't be read. A project not in it means K = 1. Until 2026-11-26 they lived only in a
Valkey hash (`obzly:ingest:lanes:<signal>`): in a node-kill drill Valkey came back empty, a busy
project dropped back to one lane, and its shard built a 919-batch backlog that took about 20 minutes
to drain while the controller worked its way up again. Only the controller's timers (when a load
went above or below its target) stay in Valkey: losing them restarts them, nothing more.

### Writing: one buffer per project, shared by its lanes on a worker

The one real change on the workers. The sink (`internal/ingest/sink.go`) kept one buffer per project
touched only by "its" shard's goroutine, and each signal's durable writer buffers and flushes a
project's rows as one. Two lanes of one project on the same worker (which Phase 1 may do when that
worker has room, and does whenever few workers are left) would have mixed two shards' rows and acks.

The design proposed keying the writers' buffers by (project, lane). The build keeps the writers as
they are and makes the sink's project buffer shared instead:

- A project buffer has its own lock and the set of shards carrying it. A batch's rows are appended
  before its ack is added, and a flush takes the acks before it flushes the writer, so every ack a
  flush covers has its rows in the segments it writes; a flush landing mid-batch splits that batch
  over two segments and its ack waits for the second.
- Releasing a shard flushes the project's buffer, acking the other lane's batches too (their rows
  are durable by then), and keeps the buffer while another of the worker's shards still carries it.
- With K = 1 a buffer has one shard, as before.

### Compaction stays with lane 0

`broker.OwnsProject` stays `owns(base shard)`: the owner of lane 0 compacts the whole project, all
lanes' files. Other lanes' L0 files are uploaded by their writers before they ack, so compaction finds
them in object storage (`EnsureLocalFiles` downloads them; one not uploaded yet is left for the next
pass, as today). The cost: those files are read from object storage rather than local disk. If that
becomes the bottleneck, compaction can follow lanes too, as a later change.

### Reading pending rows (the tail)

Queries add a project's not-yet-uploaded batches from JetStream (`broker.PendingIngestRange`) by its
exact subject. With lanes, the filter becomes `ingest.<signal>.*.<project>`: one wildcard on the
shard token. Sequences across lanes interleave in the one stream, which the tail already reads in
sequence order. Direct get by `next_by_subj` takes the wildcard (checked in the build).

Found in the lab build: the tail decided which of its delta files were written by comparing them to
the project's oldest pending sequence, which assumes the project's batches are acked oldest first.
Lanes ack on their own, so a lane writing its batches while another still had older ones pending
left them in the tail, counted twice. Each delta now records its sequence range per subject, and a
refresh reads each subject's oldest pending sequence (`broker.FirstPendingSeqs`, exact subjects):
a delta is kept, re-read for its pending batches only, or dropped per lane.

### Deciding K

A leader-locked task on the workers (`ingest-lanes`, every minute) sets each project's K per signal:

- **Load**: each worker's sink already sees every batch per project; the balancer adds per-project
  weighted bytes/s (1-minute half-life) to its Valkey report, top 10 projects per signal only.
- **Lane target**: half a worker's capacity, in weighted bytes/s. A worker's capacity in bytes comes
  from the balancer's own figures (its load ÷ its CPU use, smoothed); before there is data, 0.6 MB/s
  per core (the lab's logs cost).
- **Grow** K to `ceil(load / lane target)`, at most 8, when the project's load has been above
  K × target for 10 minutes. **Shrink** by one when it has been below (K − 1) × target × 0.6 for 30
  minutes. At most one change per project per 10 minutes.
- **Behind**: each worker also reports, per shard, how many seconds it needs to drain its
  undelivered batches at its current rate (JetStream gives every delivered message its consumer's
  pending count). The worker holding the controller's lease publishes, every balancer tick (5 s),
  each shard 2 s or more behind and by how much (`obzly:ingest:lanes:backlog:<signal>`,
  "15:72,32:4", expiring after 30 s); receivers reread it every 10 s and weight a project's lanes by
  it: a lane keeping up weighs 20, one b seconds behind 20 / (1 + b/10), so 10 s behind halves its
  share and 30 s quarters it; one 60 s or more behind gets nothing while another lane is under 60 s,
  so a backlogged lane drains at its full rate (with every lane that far behind, the least behind get
  the most). Until 2026-11-26 receivers only skipped lanes 60 s behind (published once a minute), so a
  lane filling up kept its full 1/K share until it crossed that line.
  Once every lane of the busiest project on a behind shard is behind (with K = 1, its one shard),
  that project gets one lane more (after the same 10 minutes, one step at a time), whatever its load
  says, and a project doesn't shrink while any lane of it is behind. Found in the lab: a 48 h metrics backfill filled its 10 GiB
  stream on one shard while the worker used 2 of its 3 cores and the project's load read 0.64 MiB/s
  against a 0.9 MiB/s target. Load is bytes × a per-signal cost estimate, and a shard is bounded by
  its one fetch-write loop as well as by CPU, so the estimate alone never saw it. A shard that drains
  nothing (its uploads failing) reports 0: more lanes wouldn't help it. Found in the lab next: with
  round-robin alone, the backlogged lane kept getting its share of new batches (at real time, many
  small ones), so it drained only by the margin between its rate and that share: 9,500 batches over
  about six hours. And because it stayed behind, the controller took the project from 2 to 6 lanes,
  none of which helped it; one lane behind among others no longer grows K.
- **Only when every live worker can write lanes**: the balancer report carries a capability flag.
  Workers older than the balancer publish no report at all, so the task also counts the shard runners
  registered for each signal (every version registers them) and changes nothing while there are
  more runners than capable workers.
- **Where it runs**: in the balancer of whichever worker holds the `ingest-lanes` lease, once a
  minute. So lanes need `INGEST_SHARD_BALANCE=load`.

A K going down needs no coordination: batches stop going to the dropped lane, and its shard drains.
Found in the lab build: the dropped lane's worker held its last batches, under the L0 target size,
until max-age (an hour by default), so going from 2 lanes to 1 hid about 60,000 lines for that
long. The sink now flushes a buffer only extra lanes carry once it has had no batch for a minute.

### Placement

Lane shards are ordinary shards with their own load, and the busiest moves to the worker with most
room. On top of that, a worker holding two or more lanes of one project hands its highest one to the
least loaded worker holding none of that project's lanes (the balancer reads the lane counts each
tick; `obzly_ingest_shard_moves_total{reason="lanes"}`), and shedding never sends a lane to a worker
that holds another lane of its project, so the two don't undo each other. Loads aren't consulted for
this: in the lab a backlogged metrics project's second lane stayed on its first lane's worker, which
by bytes × weight was the least loaded, and the project went no faster (two lanes on one worker are
correct, just not faster). Same cooldown and minimum shard age as a shed.

### Settings

- `INGEST_LANES`: `auto` (default once built) or `off` (every project K = 1).
- `INGEST_MAX_LANES`: 8. Everything else is a constant with its measured reason.

### Failure modes

| Failure | Behaviour |
|---|---|
| Valkey unreachable or restarted empty | Lane counts are in Postgres, so nothing changes; receivers keep the last backlog figures for 30 s, then weight every lane equally. The controller (its lease is in Valkey) pauses while Valkey is unreachable. |
| Postgres unreachable | Every replica keeps the lane counts it last read; a replica starting meanwhile uses K = 1. The controller changes nothing. |
| No controller runs | The backlog key expires within 30 s and receivers go back to plain round-robin. |
| A lane's worker dies | Its shard's lease expires and another worker takes it, as any shard. |
| Workers on mixed versions | The task keeps every K at 1 until all report the capability. |
| K shrinks with batches pending on the dropped lane | Its shard keeps being consumed until empty. |
| One worker holds every lane (few workers left) | Correct (buffers per shard and project); just no faster than today. |

### Observability

- `obzly_ingest_projects_with_lanes{signal}` (set by the controller), `obzly_ingest_lane_changes_total{direction}`.
- `obzly_ingest_shard_behind_seconds{signal,shard}`: seconds each owned shard needs to drain.
- `obzly_ingest_step_duration_seconds{signal,step}`: where a worker's time goes (fetch, append,
  flush, upload, ack, wait).
- Logs: `Ingest lanes changed` with project, signal, old and new K, the load, and whether every lane
  of it was behind (`lanesBehind`).
- The Site health page lists projects with lanes and their load per lane.

## Testing

- Unit: lane shard mapping; the controller's grow, shrink and hysteresis; growth only when every lane
  is behind, no shrink while any is, and which project a behind shard picks; receivers skipping a
  lane that is behind (and using all when every one is), giving a lane filling up fewer batches, and
  ignoring a healthy shard's noise; two projects in turn each spreading over their lanes; the backlog
  in Valkey, published every tick; the capability gate; lane counts surviving the Valkey keys being
  lost (a new receiver spreads over every lane at once, the controller keeps them) and a receiver
  keeping its counts while Postgres can't be read.
- Sink and writers: two shards of one project on one worker, interleaved, each flushing and acking
  only its own rows; K = 1 identical to today.
- Integration (real Valkey and broker): a project at K = 3 across three runners: every batch written
  once, the tail sees pending rows of every lane, compaction merges files from three writers.
- Lab: one logs project at about three times one worker's rate on three workers: throughput and CPU
  per worker before and after, a rollout and a worker kill with lanes active, K shrinking when the
  generator slows.

## Decisions (signed off 2026-11-23: build now)

1. Lanes as extra shards of the same subject space (`(base + 17·i) % 64`), not a new stream.
2. Per-message round-robin at the receivers, lane counts in Valkey with K = 1 as the safe default.
   Since 2026-11-26: lane counts in Postgres, and the round-robin weighted by each lane's backlog.
3. Lanes of one project on one worker share its sink buffer (the design said writer buffers keyed
   by shard and project; the build found the shared buffer simpler and as safe); compaction stays
   with lane 0.
4. The tail reads all lanes through a wildcard on the shard token.
5. K set automatically from per-project load (grow after 10 min, shrink after 30, max 8), only when
   every worker can write lanes; `INGEST_LANES=off` to disable.
6. **When to build**: now, ahead of need. No deployment needs it yet (the recommendation was to wait
   for the unsplittable-shard metric to fire), but building it now keeps the design and the lab
   evidence together.
