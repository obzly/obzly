package logs_querier

import (
	"fmt"
	"time"

	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/storage/segments"
	"obzly/internal/util/attrnames"

	"github.com/google/uuid"
)

// Counts read the smallest rollup that can answer them, per segment: a
// segment whose tier isn't built yet (a backfill) falls back to logs_1m,
// then to its raw logs. Each segment's time is read from exactly one input:
// whole hours from logs_1h_patterns, whole minutes from a per-minute
// rollup, the partial minutes at the edges raw.

// rangeKind is the part of a time range one input of a count reads.
type rangeKind int

const (
	wholeRange rangeKind = iota
	minuteEdges
	hourEdges
	wholeMinutes
	minutesOutsideHours
	wholeHours
)

// podRollupFields are logs_1m_pods' dimensions, patternRollupFields
// logs_1h_patterns'.
var (
	podRollupFields = map[string]bool{
		"level":              true,
		"service_name":       true,
		"k8s_namespace_name": true,
		"k8s_container_name": true,
		"k8s_pod_name":       true,
	}
	patternRollupFields = map[string]bool{
		"level":                    true,
		"service_name":             true,
		logs_core.PatternHashField: true,
	}
)

// countTiers are the rollups that may answer a count: per-minute streams by
// preference, and whether logs_1h_patterns may answer whole hours.
type countTiers struct {
	minute []string
	hourly bool
}

// tieredSource is one input of a count: a stream, its records (and, for
// the raw whole-range input, the tail) and the part of each range it reads.
type tieredSource struct {
	stream  string
	records []*segments.SegmentRecord
	tail    []segments.ResolvedFile
	rollup  bool
	kind    rangeKind
}

// countTiersFor picks the tiers for a count on fields (normalized, all
// logs_1m dimensions). Without the pattern, logs_1m_pods: its rows per
// minute are a few dozen where logs_1m's can be thousands. With it, whole
// hours from logs_1h_patterns when hours may be merged (buckets of whole
// hours, or none) and the fields are its own.
func countTiersFor(fields []string, hoursMerge bool) countTiers {
	if fieldsIn(fields, podRollupFields) {
		return countTiers{minute: []string{logs_core.StreamLogsPodRollup, logs_core.StreamLogsRollup}}
	}
	return countTiers{
		minute: []string{logs_core.StreamLogsRollup},
		hourly: hoursMerge && fieldsIn(fields, patternRollupFields),
	}
}

// tieredSources splits ordered and tail among tiers, per segment. The first
// source is always the raw one over the whole ranges, holding the tail.
func (q *LogQuerier) tieredSources(
	projectID uuid.UUID,
	from, to time.Time,
	tail []segments.ResolvedFile,
	ordered []*segments.SegmentRecord,
	tiers countTiers,
) ([]tieredSource, error) {
	plan := func(stream string, from time.Time) (map[string]*segments.SegmentRecord, error) {
		rollups, err := q.segmentRepo.PlanSegments(projectID, stream, from, to, segments.PruningHints{})
		if err != nil {
			return nil, fmt.Errorf("failed to list %s rollups: %w", stream, err)
		}
		return q.readableRollups(rollups), nil
	}
	// A rollup's rows start at their minute or hour, before from.
	minute := make([]map[string]*segments.SegmentRecord, len(tiers.minute))
	for i, stream := range tiers.minute {
		bySource, err := plan(stream, from.Truncate(time.Minute))
		if err != nil {
			return nil, err
		}
		minute[i] = bySource
	}
	var hours map[string]*segments.SegmentRecord
	if tiers.hourly {
		bySource, err := plan(logs_core.StreamLogsPatternRollup, from.Truncate(time.Hour))
		if err != nil {
			return nil, err
		}
		hours = bySource
	}

	type sourceKey struct {
		stream string
		kind   rangeKind
	}
	sources := []tieredSource{{stream: q.stream, tail: tail, kind: wholeRange}}
	index := map[sourceKey]int{{q.stream, wholeRange}: 0}
	add := func(stream string, kind rangeKind, record *segments.SegmentRecord) {
		key := sourceKey{stream, kind}
		i, ok := index[key]
		if !ok {
			i = len(sources)
			index[key] = i
			sources = append(sources, tieredSource{stream: stream, rollup: stream != q.stream, kind: kind})
		}
		sources[i].records = append(sources[i].records, record)
	}
	for _, record := range ordered {
		id := record.ID.String()
		var minuteRollup *segments.SegmentRecord
		minuteStream := ""
		for i, bySource := range minute {
			if rollup := bySource[id]; rollup != nil {
				minuteRollup, minuteStream = rollup, tiers.minute[i]
				break
			}
		}
		hourRollup := hours[id]
		switch {
		case minuteRollup == nil && hourRollup == nil:
			add(q.stream, wholeRange, record)
		case hourRollup == nil:
			add(q.stream, minuteEdges, record)
			add(minuteStream, wholeMinutes, minuteRollup)
		case minuteRollup == nil:
			add(q.stream, hourEdges, record)
			add(logs_core.StreamLogsPatternRollup, wholeHours, hourRollup)
		default:
			add(q.stream, minuteEdges, record)
			add(minuteStream, minutesOutsideHours, minuteRollup)
			add(logs_core.StreamLogsPatternRollup, wholeHours, hourRollup)
		}
	}
	return sources, nil
}

// rangesOf is the part of ranges an input of kind reads.
func rangesOf(kind rangeKind, ranges []timeRange) []timeRange {
	switch kind {
	case minuteEdges:
		_, edges := splitWhole(ranges, time.Minute)
		return edges
	case hourEdges:
		_, edges := splitWhole(ranges, time.Hour)
		return edges
	case wholeMinutes:
		inner, _ := splitWhole(ranges, time.Minute)
		return inner
	case minutesOutsideHours:
		inner, _ := splitWhole(ranges, time.Minute)
		_, outside := splitWhole(inner, time.Hour)
		return outside
	case wholeHours:
		inner, _ := splitWhole(ranges, time.Hour)
		return inner
	}
	return ranges
}

// splitWhole splits ranges into the whole units (minutes or hours) they
// hold, which rollups of that grain answer exactly, and the partial units
// at their edges.
func splitWhole(ranges []timeRange, unit time.Duration) (inner, edges []timeRange) {
	for _, r := range ranges {
		end := r.end
		if r.includeEnd {
			end = end.Add(time.Nanosecond)
		}
		first := r.start.Truncate(unit)
		if first.Before(r.start) {
			first = first.Add(unit)
		}
		last := end.Truncate(unit)
		if !first.Before(last) {
			edges = append(edges, r)
			continue
		}
		inner = append(inner, timeRange{start: first, end: last})
		if r.start.Before(first) {
			edges = append(edges, timeRange{start: r.start, end: first})
		}
		if last.Before(end) {
			edges = append(edges, timeRange{start: last, end: r.end, includeEnd: r.includeEnd})
		}
	}
	return inner, edges
}

// queryFields are the normalized fields node's conditions use.
func queryFields(node *logs_core.QueryNode) []string {
	switch {
	case node == nil:
		return nil
	case node.Condition != nil:
		return []string{attrnames.Normalize(node.Condition.Field)}
	case node.Logic != nil:
		var fields []string
		for i := range node.Logic.Children {
			fields = append(fields, queryFields(&node.Logic.Children[i])...)
		}
		return fields
	}
	return nil
}

// countField is a group field as the rollups hold it: the pod-derived
// fields are k8s_pod_name.
func countField(field string) string {
	field = attrnames.Normalize(field)
	if field == logs_core.WorkloadField || field == logs_core.ReplicaSetField {
		return "k8s_pod_name"
	}
	return field
}

func fieldsIn(fields []string, allowed map[string]bool) bool {
	for _, field := range fields {
		if !allowed[field] {
			return false
		}
	}
	return true
}
