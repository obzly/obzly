// Package telemetry is Obzly's own Prometheus instrumentation (ARCHITECTURE.md "Scale
// architecture"). Metrics are package-level vars on a private registry, served only on
// the internal METRICS_ADDR listener, never the public port or the control-plane proxy.
//
// It imports nothing else from this module, to avoid cycles. Label values are bounded
// sets (stream, signal, op, route template, leader-lock name), never a project, user or
// segment id.
package telemetry

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const namespace = "obzly"

// Result label values shared by several metrics.
const (
	ResultOK      = "ok"
	ResultError   = "error"
	ResultSkipped = "skipped"
	// IngestPublishes results besides ok.
	ResultFull        = "full"
	ResultUnavailable = "unavailable"
)

// Signal label values.
const (
	SignalLogs    = "logs"
	SignalSpans   = "spans"
	SignalMetrics = "metrics"
)

// Where a query fragment ran (QueryFragments' where label), and fragment
// results besides ok/error.
const (
	QueryWhereLocal  = "local"
	QueryWhereRemote = "remote"
	ResultEmpty      = "empty"
	ResultCancelled  = "cancelled"
)

// Where a query found a segment file (SegmentReads' source label).
const (
	SourceLocal       = "local"
	SourceCache       = "cache"
	SourceObjectStore = "object_store"
	// SourcePartial: the needed columns of a cold segment (partial.go).
	SourcePartial = "partial"
	SourceMissing = "missing"
	// SourcePending: written on another replica and not uploaded yet; its
	// rows are still in JetStream, which the query tail serves.
	SourcePending = "pending"
)

var Registry = prometheus.NewRegistry()

var factory = promauto.With(Registry)

var (
	BuildInfo = factory.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "build_info",
		Help:      "Always 1; labels carry the running version and SERVICE_MODE.",
	}, []string{"version", "mode"})

	HTTPRequestDuration = factory.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "http_request_duration_seconds",
		Help:      "HTTP request latency by route template and status code.",
		Buckets:   latencyBuckets,
	}, []string{"method", "route", "status"})

	GRPCRequestDuration = factory.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "grpc_request_duration_seconds",
		Help:      "OTLP gRPC request latency by method and status code.",
		Buckets:   latencyBuckets,
	}, []string{"method", "code"})

	IngestAcceptedRows = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "ingest_accepted_rows_total",
		Help:      "Log lines, spans and metric samples accepted by a receiver.",
	}, []string{"signal"})

	IngestRejectedRows = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "ingest_rejected_rows_total",
		Help:      "Items rejected individually inside an otherwise accepted batch (validation).",
	}, []string{"signal"})

	IngestThrottledRows = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "ingest_throttled_rows_total",
		Help:      "Rows in batches refused with 429 by a project's rate limit; the sender retries them.",
	}, []string{"signal"})

	RateLimitLocalDecisions = factory.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "rate_limit_local_decisions_total",
		Help:      "Rate-limit decisions made by a replica's own bucket because Valkey was unreachable.",
	})

	ValkeyUnreachable = factory.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "valkey_unreachable_total",
		Help:      "Times a Valkey call got no reply, after which request-path calls skip Valkey for 2 s.",
	})

	IngestShardLeaseUnconfirmed = factory.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "ingest_shard_leases_unconfirmed",
		Help:      "Shards a worker keeps consuming while Valkey can't renew their leases; compaction waits for them.",
	}, []string{"signal"})

	OTLPDeltaSamples = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "otlp_delta_samples_total",
		Help:      "OTLP delta-temporality metric samples made cumulative (ok), or refused with a retryable error because Valkey failed (error).",
	}, []string{"result"})

	WriterAppendedRows = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "writer_appended_rows_total",
		Help:      "Rows appended to this process's in-memory segment writer.",
	}, []string{"stream"})

	WriterAppendedBytes = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "writer_appended_bytes_total",
		Help:      "Estimated raw bytes appended to this process's in-memory segment writer.",
	}, []string{"stream"})

	WriterBufferedRows = factory.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "writer_buffered_rows",
		Help:      "Rows held in RAM awaiting a segment flush; lost on a hard kill.",
	}, []string{"stream"})

	WriterBufferedBytes = factory.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "writer_buffered_bytes",
		Help:      "Estimated raw bytes held in RAM awaiting a segment flush.",
	}, []string{"stream"})

	SegmentFlushes = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "segment_flushes_total",
		Help:      "Segment flushes by result. A failed flush drops its rows.",
	}, []string{"stream", "result"})

	SegmentFlushedRows = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "segment_flush_rows_total",
		Help:      "Rows in flushes by result; result=error counts rows lost to a failed flush.",
	}, []string{"stream", "result"})

	SegmentFlushDuration = factory.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "segment_flush_duration_seconds",
		Help:      "Time to write one buffered project to a Parquet segment.",
		Buckets:   latencyBuckets,
	}, []string{"stream"})

	SegmentFlushSize = factory.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "segment_flush_size_bytes",
		Help:      "Size of freshly flushed Parquet segment files.",
		Buckets:   prometheus.ExponentialBuckets(4*1024, 4, 10), // 4 KiB .. 1 GiB
	}, []string{"stream"})

	// The oldest row's time in RAM before its flush: how long ingested data
	// waited to become queryable on this replica.
	SegmentFlushLag = factory.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "segment_flush_lag_seconds",
		Help:      "Age of the oldest row in a buffer when it was flushed (ingest-to-queryable on this replica).",
		Buckets:   []float64{1, 5, 10, 20, 30, 45, 60, 120, 300, 600, 1800},
	}, []string{"stream"})

	ObjectStoreRequests = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "object_store_requests_total",
		Help:      "Object storage (S3/GCS/Azure/file) requests by operation and result; the request-cost guardrail.",
	}, []string{"op", "result"})

	ObjectStoreDuration = factory.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "object_store_request_duration_seconds",
		Help:      "Object storage request latency by operation.",
		Buckets:   latencyBuckets,
	}, []string{"op"})

	QueryMemorySandboxBytes = factory.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "query_memory_sandbox_bytes",
		Help:      "Memory limits of the SQL-mode DuckDB handles running now, as the memory gate admitted them.",
	})

	// QueryMemoryAdmissions counts SQL-mode queries by how the memory gate
	// admitted them: full (their usual cap), reduced (less, spilling more),
	// waited (after others finished) or refused (429).
	QueryMemoryAdmissions = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "query_memory_admissions_total",
		Help:      "SQL-mode queries by how the memory gate admitted them: full, reduced, waited or refused.",
	}, []string{"outcome"})

	// CompactionRepeatedRows counts rows merges dropped as copies of another
	// row's id: batches the broker redelivered after they were persisted.
	CompactionRepeatedRows = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "compaction_repeated_rows_total",
		Help:      "Rows dropped by merges as repeats of another row's id (batches redelivered after a restart).",
	}, []string{"stream"})

	ObjectStoreBytes = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "object_store_bytes_total",
		Help:      "Bytes moved to (upload) or from (download) object storage.",
	}, []string{"op"})

	QuerySegmentReads = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "query_segment_reads_total",
		Help:      "Segments a query opened, by where the file was found; object_store is a cold-cache miss.",
	}, []string{"stream", "source"})

	QueryPartialReads = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "query_partial_reads_total",
		Help:      "Cold segments a query read column by column instead of downloading whole: fetched (columns downloaded) or cached (already there).",
	}, []string{"stream", "result"})

	QueryScannedBytes = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "query_scanned_bytes_total",
		Help:      "Compressed bytes of the segment files queries opened.",
	}, []string{"stream"})

	QuerySegmentsPruned = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "query_segments_pruned_total",
		Help:      "Segments a query skipped without opening because their catalog stats ruled them out.",
	}, []string{"stream"})

	QuerySegmentsPerRequest = factory.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "query_segments_per_request",
		Help:      "Segments the catalog returned for one query's time window.",
		Buckets:   prometheus.ExponentialBuckets(1, 4, 9), // 1 .. 65536
	}, []string{"stream"})

	// Distributed query. where: QueryWhereLocal (this process) or QueryWhereRemote (a
	// SERVICE_MODE=querier executor).
	QueryPlannedBytes = factory.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "query_planned_bytes",
		Help:      "Compressed bytes of the segments one coordinated query planned to read.",
		Buckets:   prometheus.ExponentialBuckets(1<<20, 4, 12), // 1 MiB .. 4 TiB
	}, []string{"stream"})

	QueryRejected = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "query_rejected_total",
		Help:      "Queries refused before reading anything, by reason (scan_budget).",
	}, []string{"stream", "reason"})

	LogsSampledOut = factory.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "logs_sampled_out_total",
		Help:      "Logs dropped at ingest by a project's log sampling rules.",
	})

	// Integrity audit.
	IntegrityAuditSampled = factory.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "integrity_audit_sampled",
		Help:      "Segments the last integrity audit checked against object storage.",
	})
	IntegrityAuditFailures = factory.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "integrity_audit_failures",
		Help:      "Sampled segments the last integrity audit found missing, resized or unreadable.",
	})
	IntegrityAuditLastRun = factory.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "integrity_audit_last_run_timestamp_seconds",
		Help:      "When the last integrity audit finished (Unix seconds).",
	})

	AuthzDecisions = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "authz_decisions_total",
		Help:      "Access decisions by action and result (allow, deny).",
	}, []string{"action", "result"})

	// Alerting. result: sent, retried, failed, silenced.
	AlertNotifications = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "alert_notifications_total",
		Help:      "Alert notification delivery attempts by result.",
	}, []string{"result"})

	AlertNotificationLatency = factory.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "alert_notification_latency_seconds",
		Help:      "From a notification being queued (the evaluation that fired it) to its delivery.",
		Buckets:   []float64{1, 2, 5, 10, 20, 30, 60, 120, 300},
	})

	// Trace tail sampling.
	SpansSampledOut = factory.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "spans_sampled_out_total",
		Help:      "Spans dropped by trace tail sampling when a day's spans merged into cold files.",
	})

	// PromQL push-down, by result: pushed, fallback (storage couldn't answer exactly) or
	// unsupported (nothing to push).
	PromQLPushdown = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "promql_pushdown_total",
		Help:      "PromQL queries by whether a range function was evaluated where the samples are.",
	}, []string{"result"})

	QueryFragments = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "query_fragments_total",
		Help:      "Query fragments a coordinator ran, by where and result (ok, empty, error).",
	}, []string{"where", "result"})

	QueryFragmentDuration = factory.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "query_fragment_duration_seconds",
		Help:      "Time to run one fragment and have its result, by where.",
		Buckets:   prometheus.ExponentialBuckets(0.005, 2, 14), // 5 ms .. 41 s
	}, []string{"where"})

	QueryFragmentRetries = factory.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "query_fragment_retries_total",
		Help:      "Fragments re-sent elsewhere after an executor failed them.",
	})

	QuerySingleFragments = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "query_single_fragments_total",
		Help: "Whole-query (single fragment) requests by placement: remote, or in process because of " +
			"no_executor, no_upload, over_bound or fallback (the remote run failed).",
	}, []string{"placement"})

	QueryShippedFiles = factory.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "query_shipped_files_total",
		Help:      "Local files (query-time tail, segments not uploaded yet) shipped to an executor.",
	})

	QueryExecutors = factory.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "query_executors",
		Help:      "Query executors this coordinator currently sees (NATS discovery).",
	})

	// Executor side (SERVICE_MODE=querier).
	// Partitioned search: windows a page search read before its page was
	// full (or the range ran out).
	SearchWindows = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "search_windows_total",
		Help:      "Time windows page searches read, by stream (newest first; the walk stops once the page is full).",
	}, []string{"stream"})

	TokenFilterSkips = factory.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "token_filter_skipped_segments_total",
		Help:      "Segments an ID search skipped because their token filter lacks the ID (never downloaded or scanned).",
	})

	QueryPartitionCache = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "query_partition_cache_total",
		Help:      "Aggregation time partitions by result cache outcome (hit, miss, error).",
	}, []string{"result"})

	QueryExecutorFragments = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "query_executor_fragments_total",
		Help:      "Fragments this executor ran, by result (ok, empty, error).",
	}, []string{"result"})

	QueryExecutorRunning = factory.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "query_executor_running_fragments",
		Help:      "Fragments this executor is running now; more wait for a slot.",
	})

	QueryExecutorResultBytes = factory.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "query_executor_result_bytes_total",
		Help:      "Result bytes this executor sent to coordinators over gRPC.",
	})

	LeaderTaskRuns = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "leader_task_runs_total",
		Help:      "Leader-locked background passes; skipped = another replica holds the lock.",
	}, []string{"task", "result"})

	LeaderTaskDuration = factory.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "leader_task_duration_seconds",
		Help:      "Duration of leader-locked passes this replica ran.",
		Buckets:   []float64{0.01, 0.1, 0.5, 1, 5, 15, 30, 60, 120, 300, 600, 1800},
	}, []string{"task"})

	LeaderTaskLastSuccess = factory.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "leader_task_last_success_timestamp_seconds",
		Help:      "Unix time this replica last completed the pass without error.",
	}, []string{"task"})

	LeaderLockHeld = factory.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "leader_lock_held",
		Help:      "1 while this replica holds the named leader lock.",
	}, []string{"task"})

	JetStreamStreamBytes = factory.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "jetstream_stream_bytes",
		Help:      "Bytes held in a JetStream ingest stream, awaiting segment writers (cluster-wide).",
	}, []string{"stream"})

	JetStreamStreamMaxBytes = factory.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "jetstream_stream_max_bytes",
		Help:      "An ingest stream's byte limit; at the limit new batches are rejected.",
	}, []string{"stream"})

	JetStreamStreamMessages = factory.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "jetstream_stream_messages",
		Help:      "Batches held in a JetStream ingest stream (cluster-wide).",
	}, []string{"stream"})

	JetStreamReplicasLagging = factory.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "jetstream_stream_replicas_lagging",
		Help:      "Replicas of an ingest stream that are offline or not current; one more lost broker loses quorum.",
	}, []string{"stream"})

	IngestPublishes = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "ingest_publishes_total",
		Help:      "Receiver batches published to JetStream; result ok, full (429) or unavailable (503).",
	}, []string{"signal", "result"})

	IngestHeldMessages = factory.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "ingest_held_messages",
		Help:      "JetStream batches this worker holds unacked until their rows are in an uploaded segment.",
	}, []string{"signal"})

	IngestOwnedShards = factory.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "ingest_owned_shards",
		Help:      "Ingest shards this worker consumes.",
	}, []string{"signal"})

	IngestShardLoad = factory.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "ingest_shard_load_bytes",
		Help:      "Weighted payload bytes per second of each ingest shard this worker owns (load-aware balancing).",
	}, []string{"signal", "shard"})

	IngestWorkerLoadRatio = factory.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "ingest_worker_load_ratio",
		Help:      "This worker's ingest load against its target share (1 is even).",
	})

	IngestShardMoves = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "ingest_shard_moves_total",
		Help:      "Ingest shards this worker handed away or claimed (shed: by load, release: idle by count, lanes: a second lane of one project to another worker, claim).",
	}, []string{"reason"})

	IngestProjectsWithLanes = factory.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "ingest_projects_with_lanes",
		Help:      "Projects whose batches spread over more than one ingest lane (set by the lane controller).",
	}, []string{"signal"})

	IngestLaneChanges = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "ingest_lane_changes_total",
		Help:      "Lane count changes the lane controller made.",
	}, []string{"direction"})

	IngestStepDuration = factory.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "ingest_step_duration_seconds",
		Help: "Time a worker spends on each step of writing ingest batches: fetch (waiting on JetStream), " +
			"append (decoding one batch), flush (writing one project's segments), upload, ack, and wait " +
			"(a shard held until queued uploads finish).",
		Buckets: latencyBuckets,
	}, []string{"signal", "step"})

	IngestShardBehind = factory.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "ingest_shard_behind_seconds",
		Help:      "How long an owned ingest shard needs to drain its undelivered batches at its current rate.",
	}, []string{"signal", "shard"})

	IngestUnsplittableShards = factory.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "ingest_unsplittable_shards",
		Help:      "Shards on this overloaded worker busier than any other worker's room: moving one only moves the overload.",
	})

	IngestTerminatedMessages = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "ingest_terminated_messages_total",
		Help:      "JetStream batches dropped after failing to be written for longer than the poison age.",
	}, []string{"signal"})

	CatalogPartitionsDropped = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "catalog_partitions_dropped_total",
		Help:      "Past month partitions of the segment catalog dropped once empty.",
	}, []string{"stream"})

	CatalogSegments = factory.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "catalog_segments",
		Help:      "Segments in the catalog (cluster-wide).",
	}, []string{"stream"})

	// Small segments are what the compactor has yet to merge.
	CatalogSmallSegments = factory.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "catalog_small_segments",
		Help:      "Segments under the compaction size threshold (compaction backlog, cluster-wide).",
	}, []string{"stream"})

	CatalogPendingUpload = factory.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "catalog_pending_upload_segments",
		Help:      "Segments not yet uploaded to object storage (cluster-wide; 0 without object storage).",
	}, []string{"stream"})

	CatalogOldestPendingUploadAge = factory.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "catalog_oldest_pending_upload_age_seconds",
		Help:      "Age of the oldest segment awaiting upload (upload lag, cluster-wide).",
	}, []string{"stream"})
)

var latencyBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}

func init() {
	Registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
}
