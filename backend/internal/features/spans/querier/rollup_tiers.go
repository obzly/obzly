package spans_querier

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	spans_core "obzly/internal/features/spans/core"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

// Span stats read the largest rollup that can answer them, per segment, as
// logs counts do (logs_querier's rollup_tiers.go): a segment's whole hours
// from spans_1h, its other whole minutes from spans_1m, the partial minutes
// at the edges raw. A segment with no rollup yet (L0, or a backfill not done)
// and the JetStream tail are read raw over the whole range.

// rangeKind is the part of a query's ranges one input reads.
type rangeKind int

const (
	wholeRange rangeKind = iota
	minuteEdges
	hourEdges
	wholeMinutes
	minutesOutsideHours
	wholeHours
)

// interval is [start, end), or [start, end] with includeEnd.
type interval struct {
	start, end time.Time
	includeEnd bool
}

// rollupTiers are the rollup streams that may answer a query ("": none).
type rollupTiers struct {
	minute, hour string
}

// statsInput is one input of a stats query: a stream, its records (and, for
// the raw whole-range input, the tail) and the part of the ranges it reads.
type statsInput struct {
	stream  string
	records []*segments.SegmentRecord
	tail    []segments.ResolvedFile
	rollup  bool
	kind    rangeKind
}

// tiersFor are tiers' streams whose width divides grain (the query's time
// buckets, 0 for none): a rollup row must fall in one bucket.
func tiersFor(tiers rollupTiers, grain time.Duration) rollupTiers {
	if grain <= 0 {
		return tiers
	}
	if grain%time.Minute != 0 {
		return rollupTiers{}
	}
	if grain%time.Hour != 0 {
		tiers.hour = ""
	}
	return tiers
}

// statsInputs plans ranges' inputs: the raw segments and tail, split per
// segment among tiers. The first input is always the raw one over the whole
// ranges, holding the tail; the others read only what their kind covers.
func (q *SpanQuerier) statsInputs(
	projectID uuid.UUID,
	ranges []interval,
	hints segments.PruningHints,
	tiers rollupTiers,
) ([]statsInput, error) {
	from, to := rangesBounds(ranges)
	records, err := q.segmentRepo.PlanSegments(projectID, spans_core.StreamSpans, from, to, hints)
	if err != nil {
		return nil, fmt.Errorf("failed to list segments for project: %w", err)
	}
	tail := segments.AppendTail(context.Background(), q.tail, projectID, spans_core.StreamSpans, records, from, to,
		nil)
	if tiers.minute == "" && tiers.hour == "" {
		return []statsInput{{stream: spans_core.StreamSpans, records: records, tail: tail, kind: wholeRange}}, nil
	}

	plan := func(stream string, from time.Time) (map[string]*segments.SegmentRecord, error) {
		if stream == "" {
			return nil, nil
		}
		rollups, err := q.segmentRepo.PlanSegments(projectID, stream, from, to, segments.PruningHints{})
		if err != nil {
			return nil, fmt.Errorf("failed to list %s rollups: %w", stream, err)
		}
		return q.readableRollups(rollups), nil
	}
	// A rollup's rows start at their minute or hour, before from.
	minutes, err := plan(tiers.minute, from.Truncate(time.Minute))
	if err != nil {
		return nil, err
	}
	hours, err := plan(tiers.hour, from.Truncate(time.Hour))
	if err != nil {
		return nil, err
	}

	inputs := []statsInput{{stream: spans_core.StreamSpans, tail: tail, kind: wholeRange}}
	index := map[rangeKind]int{wholeRange: 0}
	add := func(stream string, kind rangeKind, record *segments.SegmentRecord) {
		i, ok := index[kind]
		if !ok {
			i = len(inputs)
			index[kind] = i
			inputs = append(inputs, statsInput{stream: stream, rollup: stream != spans_core.StreamSpans, kind: kind})
		}
		inputs[i].records = append(inputs[i].records, record)
	}
	for _, record := range records {
		id := record.ID.String()
		minute, hour := minutes[id], hours[id]
		switch {
		case minute == nil && hour == nil:
			add(spans_core.StreamSpans, wholeRange, record)
		case hour == nil:
			add(spans_core.StreamSpans, minuteEdges, record)
			add(tiers.minute, wholeMinutes, minute)
		case minute == nil:
			add(spans_core.StreamSpans, hourEdges, record)
			add(tiers.hour, wholeHours, hour)
		default:
			add(spans_core.StreamSpans, minuteEdges, record)
			add(tiers.minute, minutesOutsideHours, minute)
			add(tiers.hour, wholeHours, hour)
		}
	}

	// An input other than the first opens only the files its ranges reach: a
	// range ending at now has a zero-width edge there, which would otherwise
	// open every segment of the range.
	kept := inputs[:1]
	for _, input := range inputs[1:] {
		input.records = recordsInRanges(input.records, rangesOf(input.kind, ranges), input.rollup)
		if len(input.records) > 0 {
			kept = append(kept, input)
		}
	}
	return kept, nil
}

// readableRollups maps a source segment id to its rollup, one per source
// (the newest), keeping only rollups this process can read.
func (q *SpanQuerier) readableRollups(rollups []*segments.SegmentRecord) map[string]*segments.SegmentRecord {
	sort.Slice(rollups, func(i, j int) bool { return rollups[i].CreatedAt.After(rollups[j].CreatedAt) })
	bySource := map[string]*segments.SegmentRecord{}
	for _, rollup := range rollups {
		if rollup.Stats == nil || rollup.Stats.Source == "" || bySource[rollup.Stats.Source] != nil ||
			!q.readable(rollup) {
			continue
		}
		bySource[rollup.Stats.Source] = rollup
	}
	return bySource
}

func (q *SpanQuerier) readable(record *segments.SegmentRecord) bool {
	if q.objectStore != nil && record.ObjectKey != "" {
		return true
	}
	_, err := os.Stat(record.Path)
	return err == nil
}

// rangesOf is the part of ranges an input of kind reads.
func rangesOf(kind rangeKind, ranges []interval) []interval {
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
func splitWhole(ranges []interval, unit time.Duration) (inner, edges []interval) {
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
		inner = append(inner, interval{start: first, end: last})
		if r.start.Before(first) {
			edges = append(edges, interval{start: r.start, end: first})
		}
		if last.Before(end) {
			edges = append(edges, interval{start: last, end: r.end, includeEnd: r.includeEnd})
		}
	}
	return inner, edges
}

// recordsInRanges keeps the records with rows in ranges. A rollup's times
// are its rows' bucket starts, inside the (aligned) ranges they answer.
func recordsInRanges(records []*segments.SegmentRecord, ranges []interval, rollup bool) []*segments.SegmentRecord {
	var kept []*segments.SegmentRecord
	for _, record := range records {
		for _, r := range ranges {
			startsBefore := record.MinTime.Before(r.end) || (r.includeEnd && record.MinTime.Equal(r.end))
			if rollup {
				startsBefore = record.MinTime.Before(r.end)
			}
			if startsBefore && !record.MaxTime.Before(r.start) {
				kept = append(kept, record)
				break
			}
		}
	}
	return kept
}

// rangePredicate is column within any of ranges, with its arguments.
func rangePredicate(column string, ranges []interval) (string, []any) {
	if len(ranges) == 0 {
		return "FALSE", nil
	}
	parts := make([]string, 0, len(ranges))
	args := make([]any, 0, 2*len(ranges))
	for _, r := range ranges {
		op := "<"
		if r.includeEnd {
			op = "<="
		}
		parts = append(parts, "("+column+" >= ? AND "+column+" "+op+" ?)")
		args = append(args, r.start, r.end)
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

func rangesBounds(ranges []interval) (time.Time, time.Time) {
	from, to := ranges[0].start, ranges[0].end
	for _, r := range ranges[1:] {
		if r.start.Before(from) {
			from = r.start
		}
		if r.end.After(to) {
			to = r.end
		}
	}
	return from, to
}
