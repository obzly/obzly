package metrics_querier

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	"obzly/internal/query"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

// labelsChunkSeries bounds the series one labels query collects: past it
// the labels are read in series_id slices, so the distinct labels held at
// once stay a few tens of MB on top of the scan. A var for tests.
var labelsChunkSeries = 50_000

// lifetimeSource is one set of files series lifetimes are read from: times
// reads first/last seen, labels the same files that can hold a row at or
// after since (fewer, for the series stream).
type lifetimeSource struct {
	times, labels           query.Request
	firstColumn, lastColumn string
	where                   string
	args                    []any
}

// seenWindow is a series' first and last seen.
type seenWindow struct {
	first, last time.Time
}

// ListSeriesLifetimes returns every series of request.MetricNames seen at or
// after ActiveSince with its first and last seen. Until the series stream
// covers ActiveSince it reads samples, so first seen is then no earlier.
func (q *MetricQuerier) ListSeriesLifetimes(
	ctx context.Context,
	projectID uuid.UUID,
	request *metrics_core.SeriesLifetimesRequestDTO,
) ([]metrics_core.SeriesLifetimeDTO, error) {
	if len(request.MetricNames) == 0 {
		return nil, nil
	}
	now := time.Now().UTC()
	names := strings.TrimSuffix(strings.Repeat("?, ", len(request.MetricNames)), ", ")
	nameArgs := make([]any, len(request.MetricNames))
	for i, name := range request.MetricNames {
		nameArgs[i] = name
	}
	byID := map[string]*metrics_core.SeriesLifetimeDTO{}
	hints := segments.PruningHints{MetricNames: request.MetricNames}

	covered, err := q.seriesCovered(projectID, request.ActiveSince)
	if err != nil {
		return nil, err
	}
	if covered {
		records, err := q.segmentRepo.PlanSegments(projectID, seriesStream, request.FirstSeenSince,
			maxRepresentableTime, hints)
		if err != nil {
			return nil, fmt.Errorf("failed to list series segments for project: %w", err)
		}
		// last_seen is the series stream's row time: only these can hold an active series.
		active, err := q.segmentRepo.PlanSegments(projectID, seriesStream, request.ActiveSince,
			maxRepresentableTime, hints)
		if err != nil {
			return nil, fmt.Errorf("failed to list series segments for project: %w", err)
		}
		where := "project_id = ? AND metric_name IN (" + names + ")"
		args := append([]any{projectID.String()}, nameArgs...)
		if err := q.lifetimesInto(ctx, byID, lifetimeSource{
			times:       query.Request{Stream: seriesStream, Segments: records},
			labels:      query.Request{Stream: seriesStream, Segments: active},
			firstColumn: "first_seen", lastColumn: "last_seen", where: where, args: args,
		}, request.ActiveSince); err != nil {
			return nil, fmt.Errorf("failed to list series lifetimes: %w", err)
		}
		// The query-time tail's samples have no series rows yet.
		tail := segments.AppendTail(context.Background(), q.tail, projectID, "metrics", nil,
			request.ActiveSince, now, nil)
		if len(tail) > 0 {
			tailRequest := query.Request{LocalFiles: tail}
			if err := q.lifetimesInto(ctx, byID, lifetimeSource{
				times: tailRequest, labels: tailRequest, firstColumn: "timestamp", lastColumn: "timestamp",
				where: where + " AND timestamp >= ?", args: append(args, request.ActiveSince),
			}, request.ActiveSince); err != nil {
				return nil, fmt.Errorf("failed to list tail series lifetimes: %w", err)
			}
		}
	} else {
		records, err := q.segmentRepo.PlanSegments(projectID, "metrics", request.ActiveSince, now, hints)
		if err != nil {
			return nil, fmt.Errorf("failed to list segments for project: %w", err)
		}
		tail := segments.AppendTail(context.Background(), q.tail, projectID, "metrics", records,
			request.ActiveSince, now, nil)
		samples := query.Request{Segments: records, LocalFiles: tail}
		if err := q.lifetimesInto(ctx, byID, lifetimeSource{
			times: samples, labels: samples, firstColumn: "timestamp", lastColumn: "timestamp",
			where: "project_id = ? AND timestamp >= ? AND metric_name IN (" + names + ")",
			args:  append([]any{projectID.String(), request.ActiveSince}, nameArgs...),
		}, request.ActiveSince); err != nil {
			return nil, fmt.Errorf("failed to list series lifetimes from samples: %w", err)
		}
	}

	result := make([]metrics_core.SeriesLifetimeDTO, 0, len(byID))
	for _, lifetime := range byID {
		result = append(result, *lifetime)
	}
	return result, nil
}

// lifetimesInto merges each series' first/last seen in source into byID
// (keyed by name and labels), keeping series last seen at or after since.
// Times and labels are separate queries: grouping samples with
// any_value(labels) held every series' labels in each thread's hash table
// (not spillable) next to the labels column's decoded pages (one per row
// group, tens of MB), and an inventory build ran out of a worker's 383 MiB.
func (q *MetricQuerier) lifetimesInto(
	ctx context.Context,
	byID map[string]*metrics_core.SeriesLifetimeDTO,
	source lifetimeSource,
	since time.Time,
) error {
	times, err := q.seriesTimes(ctx, source, since)
	if err != nil || len(times) == 0 {
		return err
	}
	chunks := (len(times) + labelsChunkSeries - 1) / labelsChunkSeries
	for chunk := range chunks {
		where := source.where + " AND " + source.lastColumn + " >= ?"
		if chunks > 1 {
			where += fmt.Sprintf(" AND series_id %% %d = %d", chunks, chunk)
		}
		request := source.labels
		request.FragmentSQL = "SELECT DISTINCT series_id, metric_name, labels FROM " + query.SourcePlaceholder +
			" WHERE " + where
		request.FragmentArgs = append(append([]any{}, source.args...), since)
		request.FinalSQL = "SELECT DISTINCT series_id, metric_name, labels FROM " + query.ResultsPlaceholder
		request.SingleSQL, request.SingleArgs = request.FragmentSQL, request.FragmentArgs
		_, err := q.run(ctx, request, func(rows *sql.Rows) error {
			for rows.Next() {
				var seriesID uint64
				var metricName, labelsJSON string
				if err := rows.Scan(&seriesID, &metricName, &labelsJSON); err != nil {
					return fmt.Errorf("failed to scan series labels: %w", err)
				}
				window, ok := times[seriesID]
				if !ok {
					continue
				}
				mergeLifetime(byID, metricName, labelsJSON, window)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// seriesTimes is each series' first and last seen in source's times files,
// for the series last seen at or after since. Fixed-size state only, so the
// labels column isn't read and the aggregation can spill.
func (q *MetricQuerier) seriesTimes(
	ctx context.Context, source lifetimeSource, since time.Time,
) (map[uint64]seenWindow, error) {
	request := source.times
	request.FragmentSQL = "SELECT series_id, min(" + source.firstColumn + ") AS first_seen, max(" +
		source.lastColumn + ") AS last_seen FROM " + query.SourcePlaceholder + " WHERE " + source.where +
		" GROUP BY series_id"
	request.FragmentArgs = source.args
	request.FinalSQL = "SELECT series_id, min(first_seen), max(last_seen) FROM " + query.ResultsPlaceholder +
		" GROUP BY series_id HAVING max(last_seen) >= ?"
	request.FinalArgs = []any{since}
	request.SingleSQL = request.FragmentSQL + " HAVING max(" + source.lastColumn + ") >= ?"
	request.SingleArgs = append(append([]any{}, source.args...), since)
	times := map[uint64]seenWindow{}
	_, err := q.run(ctx, request, func(rows *sql.Rows) error {
		for rows.Next() {
			var seriesID uint64
			var window seenWindow
			if err := rows.Scan(&seriesID, &window.first, &window.last); err != nil {
				return fmt.Errorf("failed to scan series lifetime: %w", err)
			}
			times[seriesID] = window
		}
		return nil
	})
	return times, err
}

// mergeLifetime adds one series' window to byID, widening an existing entry.
func mergeLifetime(
	byID map[string]*metrics_core.SeriesLifetimeDTO, metricName, labelsJSON string, window seenWindow,
) {
	key := metricName + "\x00" + labelsJSON
	if existing, ok := byID[key]; ok {
		if window.first.Before(existing.FirstSeen) {
			existing.FirstSeen = window.first.UTC()
		}
		if window.last.After(existing.LastSeen) {
			existing.LastSeen = window.last.UTC()
		}
		return
	}
	byID[key] = &metrics_core.SeriesLifetimeDTO{
		MetricName: metricName, Labels: decodeLabels(labelsJSON),
		FirstSeen: window.first.UTC(), LastSeen: window.last.UTC(),
	}
}
