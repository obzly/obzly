package spans_compactor

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"

	spans_core "obzly/internal/features/spans/core"
	"obzly/internal/storage/segments"
	"obzly/internal/util/logger"

	"github.com/google/uuid"
)

// Span rollups, built like logs_1m: each compacted (L1 or cold) spans
// segment gets spans_1m (per-minute stats), spans_1m_edges (per-minute
// service calls) and spans_1m_traces (per-minute trace membership), read
// from the raw segment, and spans_1h and spans_1h_traces derived from its
// spans_1m and spans_1m_traces files. A merge gets its own rollups and the
// sources' become orphans to delete. L0 is read raw. Existing segments are rolled up newest
// first, a budget per pass, so an upgrade backfills over a few passes.
const (
	// rollupBudget / rollupTimeBudget: segments rolled up per project per
	// pass, and when a pass stops starting new ones (a cold segment can be a
	// download of a GB).
	rollupBudget     = 10
	rollupTimeBudget = time.Minute
	// derivedBudget / derivedTimeBudget: segments given their hourly tiers
	// per project per pass; a per-minute rollup file is small.
	derivedBudget     = 50
	derivedTimeBudget = 30 * time.Second
	// joinRows: spans joined at once for spans_1m_edges and spans_1m_traces.
	// A cold segment of 20M spans joined whole spilled 7 GB and failed at
	// the compactor's 1 GB; it's joined in parts of whole traces instead.
	joinRows = 2_000_000
)

// statsQuery is spans_1m: per minute, service, operation, kind, status and
// whether the span is a root, the span count and duration sum, a duration
// histogram (one row per bucket) and each group's latest k8s placement.
var statsQuery = `SELECT project_id, timestamp, service_name, name, kind, status_code, root, duration_bucket,
		count(*)::BIGINT AS cnt, sum(duration_ms) AS duration_sum,
		arg_max(k8s_namespace, start_time) FILTER (WHERE k8s_namespace IS NOT NULL) AS k8s_namespace,
		max(start_time) FILTER (WHERE k8s_namespace IS NOT NULL) AS k8s_namespace_at,
		arg_max(k8s_deployment, start_time) FILTER (WHERE k8s_deployment IS NOT NULL) AS k8s_deployment,
		max(start_time) FILTER (WHERE k8s_deployment IS NOT NULL) AS k8s_deployment_at,
		arg_max(k8s_node, start_time) FILTER (WHERE k8s_node IS NOT NULL) AS k8s_node,
		max(start_time) FILTER (WHERE k8s_node IS NOT NULL) AS k8s_node_at
	FROM (SELECT project_id, time_bucket(INTERVAL '1 minute', start_time::TIMESTAMP) AS timestamp,
		service_name, name, kind, status_code, parent_span_id = '' AS root, start_time,
		` + spans_core.SpanDurationMsSQL + ` AS duration_ms,
		` + spans_core.DurationBucketSQL(spans_core.SpanDurationMsSQL) + ` AS duration_bucket,
		json_extract_string(resource_attributes, '` + spans_core.K8sNamespacePath + `') AS k8s_namespace,
		json_extract_string(resource_attributes, '` + spans_core.K8sDeploymentPath + `') AS k8s_deployment,
		json_extract_string(resource_attributes, '` + spans_core.K8sNodePath + `') AS k8s_node
		FROM ` + segments.RollupSourcePlaceholder + `)
	GROUP BY 1, 2, 3, 4, 5, 6, 7, 8 ORDER BY 2`

// hourQuery is spans_1h, from a spans_1m file.
var hourQuery = `SELECT project_id, time_bucket(INTERVAL '1 hour', timestamp::TIMESTAMP) AS timestamp,
		service_name, name, kind, status_code, root, duration_bucket,
		sum(cnt)::BIGINT AS cnt, sum(duration_sum) AS duration_sum,
		arg_max(k8s_namespace, k8s_namespace_at) AS k8s_namespace, max(k8s_namespace_at) AS k8s_namespace_at,
		arg_max(k8s_deployment, k8s_deployment_at) AS k8s_deployment,
		max(k8s_deployment_at) AS k8s_deployment_at,
		arg_max(k8s_node, k8s_node_at) AS k8s_node, max(k8s_node_at) AS k8s_node_at
	FROM ` + segments.RollupSourcePlaceholder + `
	GROUP BY 1, 2, 3, 4, 5, 6, 7, 8 ORDER BY 2`

// edgesCombine sums the parts' rows of an edge.
var edgesCombine = `SELECT project_id, timestamp, src, dst, peer, db_system, messaging_system, duration_bucket,
		sum(cnt)::BIGINT AS cnt, sum(err_cnt)::BIGINT AS err_cnt, sum(duration_sum) AS duration_sum
	FROM ` + segments.RollupPartsPlaceholder + ` GROUP BY ALL ORDER BY timestamp`

// rawTier is a rollup built from the raw segment: its queries for a
// segment of that many rows, how parts combine, and whether they join.
type rawTier struct {
	stream  string
	queries func(rows int64) []string
	combine string
	joins   bool
}

// tracesCombine sums the parts' rows of a minute, service and error flag.
var tracesCombine = `SELECT project_id, timestamp, service_name, errors, duration_bucket, sum(cnt)::BIGINT AS cnt
	FROM ` + segments.RollupPartsPlaceholder + ` GROUP BY ALL ORDER BY timestamp`

// derivedTier is a rollup built from another rollup of the same source.
type derivedTier struct {
	from, stream, query string
}

// tracesHourQuery is spans_1h_traces, from a spans_1m_traces file.
var tracesHourQuery = `SELECT project_id, time_bucket(INTERVAL '1 hour', timestamp::TIMESTAMP) AS timestamp,
		service_name, errors, duration_bucket, sum(cnt)::BIGINT AS cnt
	FROM ` + segments.RollupSourcePlaceholder + `
	GROUP BY 1, 2, 3, 4, 5 ORDER BY 2`

var derivedTiers = []derivedTier{
	{from: spans_core.StreamSpansRollup, stream: spans_core.StreamSpansHourRollup, query: hourQuery},
	{from: spans_core.StreamSpansTraceRollup, stream: spans_core.StreamSpansTraceHourRollup, query: tracesHourQuery},
}

var rawTiers = []rawTier{
	{stream: spans_core.StreamSpansRollup, queries: func(int64) []string { return []string{statsQuery} }},
	{stream: spans_core.StreamSpansEdgeRollup, queries: joinParts(edgesQuery), combine: edgesCombine, joins: true},
	{stream: spans_core.StreamSpansTraceRollup, queries: joinParts(tracesQuery), combine: tracesCombine, joins: true},
}

// RollupSegments deletes orphaned rollups, builds the missing spans_1m,
// spans_1m_edges and spans_1m_traces rollups of up to rollupBudget
// compacted segments, newest first, starting none after rollupTimeBudget,
// then the missing hourly tiers of up to derivedBudget segments within
// derivedTimeBudget. Run it after the
// pass's merges are uploaded: a merge is invisible to queries until then.
func (c *SpanCompactor) RollupSegments(projectID uuid.UUID) error {
	records, err := c.segmentRepo.ListByProject(projectID, streamSpans)
	if err != nil {
		return fmt.Errorf("failed to list segments: %w", err)
	}
	present := make(map[string]bool, len(records))
	for _, record := range records {
		present[record.ID.String()] = true
	}

	built := make(map[string]map[string]*segments.SegmentRecord, len(spans_core.SpanRollupStreams))
	var orphans []*segments.SegmentRecord
	for _, stream := range spans_core.SpanRollupStreams {
		rollups, err := c.segmentRepo.ListByProject(projectID, stream)
		if err != nil {
			return fmt.Errorf("failed to list %s rollups: %w", stream, err)
		}
		bySource, streamOrphans := rollupsBySource(rollups, present)
		built[stream] = bySource
		orphans = append(orphans, streamOrphans...)
	}
	if len(orphans) > 0 {
		if err := c.deleteSegments(orphans); err != nil {
			return fmt.Errorf("failed to delete orphaned span rollups: %w", err)
		}
	}

	sort.Slice(records, func(i, j int) bool { return records[i].MaxTime.After(records[j].MaxTime) })
	started := time.Now()
	done := 0
	for _, record := range records {
		if done >= rollupBudget || (done > 0 && time.Since(started) > rollupTimeBudget) {
			break
		}
		if record.Level < segments.LevelCompacted {
			continue
		}
		var missing []rawTier
		for _, tier := range rawTiers {
			if built[tier.stream][record.ID.String()] == nil {
				missing = append(missing, tier)
			}
		}
		if len(missing) == 0 {
			continue
		}
		done++
		rollups, err := c.rollUp(record, missing)
		if err != nil {
			logger.GetLogger().Warn("Failed to build a span segment's rollups - trace views read it raw",
				"segmentId", record.ID, "error", err)
		}
		for stream, rollup := range rollups {
			built[stream][record.ID.String()] = rollup
		}
	}

	started = time.Now()
	done = 0
	for _, record := range records {
		if done >= derivedBudget || (done > 0 && time.Since(started) > derivedTimeBudget) {
			break
		}
		derivedAny := false
		for _, tier := range derivedTiers {
			minute := built[tier.from][record.ID.String()]
			if minute == nil || built[tier.stream][record.ID.String()] != nil {
				continue
			}
			derivedAny = true
			if err := c.deriveHours(minute, tier); err != nil {
				logger.GetLogger().Warn("Failed to derive a span segment's hourly rollup - trace views read its "+
					"per-minute one", "segmentId", record.ID, "stream", tier.stream, "error", err)
			}
		}
		if derivedAny {
			done++
		}
	}
	return nil
}

// rollUp builds and records one segment's missing raw tiers, by stream
// (none when it has no rows or isn't downloadable yet). A file downloaded
// for it is removed afterwards: workers keep only what they wrote or merged.
func (c *SpanCompactor) rollUp(
	record *segments.SegmentRecord, tiers []rawTier,
) (map[string]*segments.SegmentRecord, error) {
	_, statErr := os.Stat(record.Path)
	ready, err := segments.EnsureLocalFiles(context.Background(), c.objectStore, []*segments.SegmentRecord{record})
	if err != nil {
		return nil, err
	}
	if !ready {
		return nil, nil // another replica's segment, not uploaded yet: a later pass
	}
	if statErr != nil {
		defer func() { _ = os.Remove(record.Path) }()
	}

	created := map[string]*segments.SegmentRecord{}
	for _, tier := range tiers {
		rollup, err := c.compactor.BuildRollupQueries(record, streamSpans, tier.stream, tier.queries(record.RowCount),
			tier.combine, c.dataDir, tier.joins)
		if err != nil {
			return created, err
		}
		if rollup == nil {
			continue
		}
		row := segments.FromSegment(*rollup)
		if err := c.segmentRepo.Create(row); err != nil {
			_ = os.Remove(rollup.Path)
			return created, fmt.Errorf("failed to record %s rollup: %w", tier.stream, err)
		}
		created[tier.stream] = row
	}
	return created, nil
}

// deriveHours builds and records tier from one per-minute rollup,
// downloading it (and removing the download afterwards) when it isn't local.
func (c *SpanCompactor) deriveHours(minute *segments.SegmentRecord, tier derivedTier) error {
	_, statErr := os.Stat(minute.Path)
	ready, err := segments.EnsureLocalFiles(context.Background(), c.objectStore, []*segments.SegmentRecord{minute})
	if err != nil {
		return err
	}
	if !ready {
		return nil // another replica's rollup, not uploaded yet: a later pass
	}
	if statErr != nil {
		defer func() { _ = os.Remove(minute.Path) }()
	}

	segment, err := c.compactor.BuildRollupQuery(minute, tier.from, tier.stream, tier.query, c.dataDir, false)
	if err != nil || segment == nil {
		return err
	}
	segment.Stats = &segments.SegmentStats{Source: minute.Stats.Source}
	if err := c.segmentRepo.Create(segments.FromSegment(*segment)); err != nil {
		_ = os.Remove(segment.Path)
		return fmt.Errorf("failed to record %s rollup: %w", tier.stream, err)
	}
	return nil
}

// rollupsBySource maps a source segment id to its rollup and returns as
// orphans the rollups whose source is gone, and a source's older duplicates
// (two passes racing).
func rollupsBySource(
	rollups []*segments.SegmentRecord, present map[string]bool,
) (map[string]*segments.SegmentRecord, []*segments.SegmentRecord) {
	sort.Slice(rollups, func(i, j int) bool { return rollups[i].CreatedAt.After(rollups[j].CreatedAt) })
	bySource := make(map[string]*segments.SegmentRecord, len(rollups))
	var orphans []*segments.SegmentRecord
	for _, rollup := range rollups {
		source := ""
		if rollup.Stats != nil {
			source = rollup.Stats.Source
		}
		if !present[source] || bySource[source] != nil {
			orphans = append(orphans, rollup)
			continue
		}
		bySource[source] = rollup
	}
	return bySource, orphans
}

// joinParts is a joining tier's queries for a segment of rows spans: one
// per joinRows, each over the traces hashing to its part.
func joinParts(query func(parts, part int) string) func(rows int64) []string {
	return func(rows int64) []string {
		parts := int(max((rows+joinRows-1)/joinRows, 1))
		queries := make([]string, parts)
		for part := range parts {
			queries[part] = query(parts, part)
		}
		return queries
	}
}

// edgesQuery is spans_1m_edges, or the part of it whose traces hash to
// part of parts: per minute of the child span, calls from a parent's
// service to its child's (peer false), and client spans with no child
// naming an untraced peer (peer true, with a duration histogram). A parent
// or child in another segment isn't seen; segments hold whole minutes of
// ingest, so that's the few calls straddling a boundary. One row with no
// calls (cnt 0) marks a segment without any as rolled up, so it isn't
// rebuilt every pass; readers skip it. The joined spans hold no JSON: a
// client span's peer is named up front.
func edgesQuery(parts, part int) string {
	inPart := partFilter(parts, part)
	client := "kind IN ('CLIENT', 'PRODUCER')"
	return `WITH s AS (SELECT project_id, trace_id, span_id, parent_span_id, service_name, status_code, start_time,
			` + spans_core.SpanDurationMsSQL + ` AS duration_ms,
			CASE WHEN ` + client + ` THEN ` + spans_core.PeerNameSQL + ` END AS peer,
			CASE WHEN ` + client + ` THEN NULLIF(json_extract_string(attributes, '$.db_system'), '') END AS db_system,
			CASE WHEN ` + client + ` THEN NULLIF(json_extract_string(attributes, '$.messaging_system'), '') END
				AS messaging_system
			FROM ` + segments.RollupSourcePlaceholder + ` WHERE ` + inPart + `)
	SELECT * FROM (
		SELECT c.project_id, time_bucket(INTERVAL '1 minute', c.start_time::TIMESTAMP) AS timestamp,
			p.service_name AS src, c.service_name AS dst, false AS peer, '' AS db_system, '' AS messaging_system,
			CAST(NULL AS SMALLINT) AS duration_bucket, count(*)::BIGINT AS cnt,
			(count(*) FILTER (WHERE c.status_code = 'ERROR'))::BIGINT AS err_cnt, sum(c.duration_ms) AS duration_sum
		FROM s c JOIN s p ON c.trace_id = p.trace_id AND c.parent_span_id = p.span_id
		WHERE c.parent_span_id != ''
		GROUP BY 1, 2, 3, 4
		UNION ALL BY NAME
		SELECT c.project_id, time_bucket(INTERVAL '1 minute', c.start_time::TIMESTAMP) AS timestamp,
			c.service_name AS src, c.peer AS dst, true AS peer, coalesce(c.db_system, '') AS db_system,
			coalesce(c.messaging_system, '') AS messaging_system,
			` + spans_core.DurationBucketSQL("c.duration_ms") + ` AS duration_bucket, count(*)::BIGINT AS cnt,
			(count(*) FILTER (WHERE c.status_code = 'ERROR'))::BIGINT AS err_cnt, sum(c.duration_ms) AS duration_sum
		FROM s c
		WHERE c.peer IS NOT NULL AND c.peer <> c.service_name
			AND NOT EXISTS (SELECT 1 FROM s child WHERE child.trace_id = c.trace_id AND child.parent_span_id = c.span_id)
		GROUP BY 1, 2, 3, 4, 5, 6, 7, 8
		UNION ALL BY NAME
		SELECT any_value(project_id) AS project_id,
			time_bucket(INTERVAL '1 minute', min(start_time)::TIMESTAMP) AS timestamp, '' AS src, '' AS dst,
			false AS peer, '' AS db_system, '' AS messaging_system, CAST(NULL AS SMALLINT) AS duration_bucket,
			0::BIGINT AS cnt, 0::BIGINT AS err_cnt, 0.0 AS duration_sum
		FROM s
	) ORDER BY timestamp`
}

// tracesQuery is spans_1m_traces, or the part of it whose traces hash to
// part of parts: per minute of a root span's start, its root duration's
// histogram bucket and whether its trace has an ERROR span, the number of
// root spans (service_name NULL) and of root spans per service in their
// trace. A trace's spans in another segment aren't seen (as in
// spans_1m_edges: the few traces straddling a boundary). A segment without
// roots gets a cnt 0 row, so it isn't rebuilt every pass.
func tracesQuery(parts, part int) string {
	return `WITH s AS (SELECT project_id, trace_id, parent_span_id, service_name, status_code, start_time,
			` + spans_core.SpanDurationMsSQL + ` AS duration_ms
			FROM ` + segments.RollupSourcePlaceholder + ` WHERE ` + partFilter(parts, part) + `),
		errs AS (SELECT DISTINCT trace_id FROM s WHERE status_code = 'ERROR'),
		r AS (SELECT project_id, trace_id, time_bucket(INTERVAL '1 minute', start_time::TIMESTAMP) AS timestamp,
			` + spans_core.DurationBucketSQL("duration_ms") + ` AS duration_bucket,
			coalesce(trace_id IN (SELECT trace_id FROM errs), false) AS errors
			FROM s WHERE parent_span_id = '')
	SELECT * FROM (
		SELECT project_id, timestamp, NULL::VARCHAR AS service_name, errors, duration_bucket, count(*)::BIGINT AS cnt
		FROM r GROUP BY 1, 2, 3, 4, 5
		UNION ALL BY NAME
		SELECT r.project_id, r.timestamp, p.service_name, r.errors, r.duration_bucket, count(*)::BIGINT AS cnt
		FROM r JOIN (SELECT DISTINCT trace_id, service_name FROM s) p ON p.trace_id = r.trace_id
		GROUP BY 1, 2, 3, 4, 5
		UNION ALL BY NAME
		SELECT any_value(project_id) AS project_id,
			time_bucket(INTERVAL '1 minute', min(start_time)::TIMESTAMP) AS timestamp, NULL::VARCHAR AS service_name,
			false AS errors, CAST(NULL AS SMALLINT) AS duration_bucket, 0::BIGINT AS cnt
		FROM s
	) ORDER BY timestamp`
}

// partFilter keeps the spans of the traces hashing to part of parts.
func partFilter(parts, part int) string {
	if parts <= 1 {
		return "TRUE"
	}
	return fmt.Sprintf("hash(trace_id) %% %d = %d", parts, part)
}
