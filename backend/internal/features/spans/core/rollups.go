package spans_core

import "fmt"

// Span rollups (spans_compactor builds them, spans_querier reads them): each
// compacted spans segment gets per-minute stats, one rollup segment per
// source, like logs_1m. spans_1h is derived from each spans_1m file (same
// source). spans_1m_edges holds the Service Map's service-to-service calls
// and untraced peers, and spans_1m_traces each root span's trace membership
// (its services, whether it has an error), both found within the segment;
// spans_1h_traces is derived from each spans_1m_traces file.
const (
	StreamSpans                = "spans"
	StreamSpansRollup          = "spans_1m"
	StreamSpansHourRollup      = "spans_1h"
	StreamSpansEdgeRollup      = "spans_1m_edges"
	StreamSpansTraceRollup     = "spans_1m_traces"
	StreamSpansTraceHourRollup = "spans_1h_traces"
)

// Durations are kept as a log-scale histogram: durationBucketsPerDoubling
// buckets per doubling (each about 9% wide), so a quantile merged from any
// number of rows is within about 4.5% of the exact one. Durations below a
// microsecond share the lowest bucket.
const durationBucketsPerDoubling = 8

// SpanDurationMsSQL is a raw span's duration in milliseconds.
const SpanDurationMsSQL = "(epoch_ns(end_time) - epoch_ns(start_time)) / 1000000.0"

// K8sNamespacePath/K8sDeploymentPath/K8sNodePath address the resource
// attributes the k8sattributes Collector processor sets (k8s.namespace.name,
// k8s.deployment.name, k8s.node.name), as normalized at ingest.
const (
	K8sNamespacePath  = `$.k8s_namespace_name`
	K8sDeploymentPath = `$.k8s_deployment_name`
	K8sNodePath       = `$.k8s_node_name`
)

// PeerNameSQL names the peer a client span calls, by the attributes OTel
// instrumentations set (normalized at ingest), most specific first.
const PeerNameSQL = "COALESCE(" +
	"NULLIF(json_extract_string(attributes, '$.peer_service'), ''), " +
	"NULLIF(json_extract_string(attributes, '$.server_address'), ''), " +
	"NULLIF(json_extract_string(attributes, '$.net_peer_name'), ''), " +
	"NULLIF(json_extract_string(attributes, '$.db_system'), ''), " +
	"NULLIF(json_extract_string(attributes, '$.messaging_system'), ''))"

// SpanRollupStreams are built by the spans compactor, uploaded and deleted
// with the spans stream.
var SpanRollupStreams = []string{
	StreamSpansRollup, StreamSpansHourRollup, StreamSpansEdgeRollup, StreamSpansTraceRollup,
	StreamSpansTraceHourRollup,
}

// DurationBucketSQL is the histogram bucket of a duration in milliseconds.
func DurationBucketSQL(durationMs string) string {
	return fmt.Sprintf("CAST(floor(log2(greatest(%s, 0.001)) * %d) AS SMALLINT)", durationMs,
		durationBucketsPerDoubling)
}

// DurationBucketValueSQL is the duration a bucket stands for: its
// geometric middle, in milliseconds.
func DurationBucketValueSQL(bucket string) string {
	return fmt.Sprintf("pow(2, (%s + 0.5) / %d)", bucket, durationBucketsPerDoubling)
}
