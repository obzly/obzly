package logs_querier

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/query"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

// Deploy markers: a Deployment rollout starts a new ReplicaSet, whose pods
// are named <replicaset>-<suffix>, and a ReplicaSet is <deployment>-<hash>.
// So the first time a ReplicaSet's pods log, inside the range and not at
// its start, is when that rollout began - read from the pods' first log
// (per-minute counts where a segment has them, else its raw logs).
const (
	// deployStartSlack: a ReplicaSet already logging this soon after the
	// range starts was running before it, not rolled out in it.
	deployStartSlack = 2 * time.Minute
	maxDeployMarkers = 50
)

// podHashSuffix matches a generated name part: the last segment of a pod
// or ReplicaSet name (5-10 lowercase letters and digits).
var podHashSuffix = regexp.MustCompile(`-[a-z0-9]{5,10}$`)

// ExecuteDeploys returns the rollouts that began in request.TimeRange,
// oldest first, among the logs matching request.Query when it only uses
// the per-minute counts' dimensions (otherwise across the project).
func (q *LogQuerier) ExecuteDeploys(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.DeploysRequestDTO,
) (*logs_core.DeploysResponseDTO, error) {
	response := &logs_core.DeploysResponseDTO{Markers: []logs_core.DeployMarkerDTO{}}
	from, to := resolveTimeRange(request.TimeRange)
	filter := request.Query
	if !onRollupFields(filter) {
		filter = nil
	}
	records, err := q.segmentRepo.PlanSegments(projectID, q.stream, from, to, hintsFor(filter))
	if err != nil {
		return nil, fmt.Errorf("failed to list segments for project: %w", err)
	}
	tail, ordered := q.searchInputs(ctx, projectID, records, from, to, false)
	if len(tail) == 0 && len(ordered) == 0 {
		return response, nil
	}

	firstSeen := map[[2]string]time.Time{}
	add := func(namespace, pod string, at time.Time) {
		key := [2]string{namespace, pod}
		if seen, ok := firstSeen[key]; !ok || at.Before(seen) {
			firstSeen[key] = at
		}
	}

	var uncovered []*segments.SegmentRecord
	var rollupRecords []*segments.SegmentRecord
	if q.stream == logs_core.StreamLogs {
		rollups, err := q.segmentRepo.PlanSegments(projectID, logs_core.StreamLogsRollup, from, to,
			segments.PruningHints{})
		if err != nil {
			return nil, fmt.Errorf("failed to list log rollups: %w", err)
		}
		bySource := q.readableRollups(rollups)
		for _, record := range ordered {
			if rollup := bySource[record.ID.String()]; rollup != nil {
				rollupRecords = append(rollupRecords, rollup)
			} else {
				uncovered = append(uncovered, record)
			}
		}
	} else {
		uncovered = ordered
	}

	raw := newWhereBuilder(projectID)
	if err := q.firstSeenPods(ctx, q.stream, raw, projectID, filter, from, to, tail, uncovered, add); err != nil {
		return nil, err
	}
	if len(rollupRecords) > 0 {
		rollup := newWhereBuilder(projectID)
		rollup.rollup = true
		// A per-minute count's timestamp is its minute's start.
		if err := q.firstSeenPods(ctx, logs_core.StreamLogsRollup, rollup, projectID, filter,
			from.Truncate(time.Minute), to, nil, rollupRecords, add); err != nil {
			return nil, err
		}
	}

	response.Markers = deployMarkers(firstSeen, from)
	return response, nil
}

// firstSeenPods hands add each pod's first log time in stream's records.
func (q *LogQuerier) firstSeenPods(
	ctx context.Context,
	stream string,
	builder *whereBuilder,
	projectID uuid.UUID,
	filter *logs_core.QueryNode,
	from, to time.Time,
	tail []segments.ResolvedFile,
	records []*segments.SegmentRecord,
	add func(namespace, pod string, at time.Time),
) error {
	if len(records) == 0 && len(tail) == 0 {
		return nil
	}
	querySQL, queryArgs := builder.Build(filter)
	where := "project_id = ? AND timestamp >= ? AND timestamp <= ? AND COALESCE(k8s_pod_name, '') <> ''"
	args := append([]any{projectID.String(), from, to}, queryArgs...)
	if querySQL != "" {
		where += " AND " + querySQL
	}
	grouped := "SELECT COALESCE(k8s_namespace_name, '') AS namespace, k8s_pod_name AS pod, " +
		"min(timestamp) AS first_seen FROM " + query.SourcePlaceholder + " WHERE " + where + " GROUP BY 1, 2"
	err := q.coordinator.Query(ctx, query.Request{
		Stream:       stream,
		Segments:     records,
		LocalFiles:   tail,
		FragmentSQL:  grouped,
		FragmentArgs: args,
		FinalSQL: "SELECT namespace, pod, min(first_seen) FROM " + query.ResultsPlaceholder +
			" GROUP BY 1, 2",
		SingleSQL:  grouped,
		SingleArgs: args,
	}, func(rows *sql.Rows) error {
		for rows.Next() {
			var namespace, pod string
			var at time.Time
			if err := rows.Scan(&namespace, &pod, &at); err != nil {
				return fmt.Errorf("failed to scan pod first log: %w", err)
			}
			add(namespace, pod, at.UTC())
		}
		return nil
	})
	if errors.Is(err, query.ErrNoData) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to read pod first logs: %w", err)
	}
	return nil
}

// deployMarkers groups pods into ReplicaSets and returns those whose first
// log came after the range's start, newest maxDeployMarkers, oldest first.
func deployMarkers(firstSeen map[[2]string]time.Time, from time.Time) []logs_core.DeployMarkerDTO {
	type replicaSet struct {
		namespace, name string
	}
	started := map[replicaSet]time.Time{}
	pods := map[replicaSet]int{}
	for key, at := range firstSeen {
		if !podHashSuffix.MatchString(key[1]) {
			continue // a StatefulSet's ordinal pod, or not a generated name
		}
		set := replicaSet{namespace: key[0], name: podHashSuffix.ReplaceAllString(key[1], "")}
		if seen, ok := started[set]; !ok || at.Before(seen) {
			started[set] = at
		}
		pods[set]++
	}

	markers := []logs_core.DeployMarkerDTO{}
	for set, at := range started {
		if !at.After(from.Add(deployStartSlack)) || !podHashSuffix.MatchString(set.name) {
			continue // running before the range, or not a Deployment's ReplicaSet
		}
		markers = append(markers, logs_core.DeployMarkerDTO{
			Time:       at,
			Namespace:  set.namespace,
			Workload:   podHashSuffix.ReplaceAllString(set.name, ""),
			ReplicaSet: set.name,
			Pods:       pods[set],
		})
	}
	sort.Slice(markers, func(i, j int) bool {
		if !markers[i].Time.Equal(markers[j].Time) {
			return markers[i].Time.Before(markers[j].Time)
		}
		return strings.Compare(markers[i].ReplicaSet, markers[j].ReplicaSet) < 0
	})
	if len(markers) > maxDeployMarkers {
		markers = markers[len(markers)-maxDeployMarkers:]
	}
	return markers
}
