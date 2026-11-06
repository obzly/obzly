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

	covered, err := q.seriesCovered(projectID, request.ActiveSince)
	if err != nil {
		return nil, err
	}
	if covered {
		records, err := q.segmentRepo.PlanSegments(projectID, seriesStream, request.FirstSeenSince,
			maxRepresentableTime, segments.PruningHints{MetricNames: request.MetricNames})
		if err != nil {
			return nil, fmt.Errorf("failed to list series segments for project: %w", err)
		}
		where := "project_id = ? AND metric_name IN (" + names + ")"
		args := append([]any{projectID.String()}, nameArgs...)
		if err := q.lifetimesInto(ctx, byID, query.Request{Stream: seriesStream, Segments: records},
			"first_seen", "last_seen", where, args, request.ActiveSince); err != nil {
			return nil, fmt.Errorf("failed to list series lifetimes: %w", err)
		}
		// The query-time tail's samples have no series rows yet.
		tail := segments.AppendTail(context.Background(), q.tail, projectID, "metrics", nil,
			request.ActiveSince, now, nil)
		if len(tail) > 0 {
			if err := q.lifetimesInto(ctx, byID, query.Request{LocalFiles: tail}, "timestamp", "timestamp",
				where+" AND timestamp >= ?", append(args, request.ActiveSince), request.ActiveSince); err != nil {
				return nil, fmt.Errorf("failed to list tail series lifetimes: %w", err)
			}
		}
	} else {
		var hints segments.PruningHints
		hints.MetricNames = request.MetricNames
		records, err := q.segmentRepo.PlanSegments(projectID, "metrics", request.ActiveSince, now, hints)
		if err != nil {
			return nil, fmt.Errorf("failed to list segments for project: %w", err)
		}
		tail := segments.AppendTail(context.Background(), q.tail, projectID, "metrics", records,
			request.ActiveSince, now, nil)
		where := "project_id = ? AND timestamp >= ? AND metric_name IN (" + names + ")"
		args := append([]any{projectID.String(), request.ActiveSince}, nameArgs...)
		if err := q.lifetimesInto(ctx, byID, query.Request{Segments: records, LocalFiles: tail},
			"timestamp", "timestamp", where, args, request.ActiveSince); err != nil {
			return nil, fmt.Errorf("failed to list series lifetimes from samples: %w", err)
		}
	}

	result := make([]metrics_core.SeriesLifetimeDTO, 0, len(byID))
	for _, lifetime := range byID {
		result = append(result, *lifetime)
	}
	return result, nil
}

// lifetimesInto merges each series' first/last seen in request's files into
// byID (keyed by name and labels), keeping series last seen at or after since.
func (q *MetricQuerier) lifetimesInto(
	ctx context.Context,
	byID map[string]*metrics_core.SeriesLifetimeDTO,
	request query.Request,
	firstColumn, lastColumn, where string,
	args []any,
	since time.Time,
) error {
	fragment := "SELECT series_id, any_value(metric_name) AS metric_name, any_value(labels) AS labels, " +
		"min(" + firstColumn + ") AS first_seen, max(" + lastColumn + ") AS last_seen FROM " +
		query.SourcePlaceholder + " WHERE " + where + " GROUP BY series_id"
	final := "SELECT series_id, any_value(metric_name), any_value(labels), min(first_seen), max(last_seen) FROM " +
		query.ResultsPlaceholder + " GROUP BY series_id HAVING max(last_seen) >= ?"
	request.FragmentSQL = fragment
	request.FragmentArgs = args
	request.FinalSQL = final
	// No SingleSQL: one fragment runs this same merge over itself.
	request.FinalArgs = []any{since}
	_, err := q.run(ctx, request, func(rows *sql.Rows) error {
		for rows.Next() {
			var seriesID uint64
			var metricName, labelsJSON string
			var firstSeen, lastSeen time.Time
			if err := rows.Scan(&seriesID, &metricName, &labelsJSON, &firstSeen, &lastSeen); err != nil {
				return fmt.Errorf("failed to scan series lifetime: %w", err)
			}
			key := metricName + "\x00" + labelsJSON
			if existing, ok := byID[key]; ok {
				if firstSeen.Before(existing.FirstSeen) {
					existing.FirstSeen = firstSeen.UTC()
				}
				if lastSeen.After(existing.LastSeen) {
					existing.LastSeen = lastSeen.UTC()
				}
				continue
			}
			byID[key] = &metrics_core.SeriesLifetimeDTO{
				MetricName: metricName, Labels: decodeLabels(labelsJSON),
				FirstSeen: firstSeen.UTC(), LastSeen: lastSeen.UTC(),
			}
		}
		return nil
	})
	return err
}
