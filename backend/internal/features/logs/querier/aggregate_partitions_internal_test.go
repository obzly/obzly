package logs_querier

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/query/partcache"
	"obzly/internal/storage"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The partitioned path returns what one query over the range does; it
// stores finished partitions, and a later run reads them instead of
// scanning (a planted count shows up in the result).
func Test_AggregateByPartition_MatchesTheUncachedResultAndReusesFinishedPartitions(t *testing.T) {
	dir := t.TempDir()
	projectID := uuid.New()
	// Neither end on the hour grid, and a fixed minute, so 47 whole hours
	// lie between them whatever the time.
	to := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Hour).Add(10 * time.Minute)
	from := to.Add(-2 * 24 * time.Hour).Add(17 * time.Minute)
	records := writeLogSegments(t, dir, projectID, from, to, 4, 300)
	require.NoError(t, storage.GetDb().Create(records).Error)
	request := func() *logs_core.AggregationRequestDTO {
		return &logs_core.AggregationRequestDTO{
			TimeRange: &logs_core.TimeRangeDTO{From: &from, To: &to}, BucketMs: 30 * 60 * 1000, GroupField: "level",
		}
	}
	uncached, err := NewLogQuerier(WithQueryDir(filepath.Join(dir, "plain")))
	require.NoError(t, err)
	defer func() { _ = uncached.Close() }()
	store := &partcache.Memory{}
	cached, err := NewLogQuerier(WithQueryDir(filepath.Join(dir, "cached")),
		WithPartitionCache(partcache.New(store, time.Hour, 5*time.Minute)))
	require.NoError(t, err)
	defer func() { _ = cached.Close() }()

	want, err := uncached.ExecuteAggregation(context.Background(), projectID, request())
	require.NoError(t, err)
	first, err := cached.ExecuteAggregation(context.Background(), projectID, request())
	require.NoError(t, err)

	require.NotEmpty(t, want.Buckets)
	require.Equal(t, counts(want), counts(first))
	require.Len(t, store.Values, 47, "whole hours inside the range; the two partial edge hours aren't stored")

	var plantedKey string
	for key := range store.Values {
		plantedKey = key
		break
	}
	var stored []partitionCount
	require.NoError(t, json.Unmarshal(store.Values[plantedKey], &stored))
	require.NotEmpty(t, stored)
	stored[0].Count += 1000
	store.Values[plantedKey], _ = json.Marshal(stored)

	second, err := cached.ExecuteAggregation(context.Background(), projectID, request())
	require.NoError(t, err)
	require.Equal(t, total(want)+1000, total(second), "the stored partition was read, not rescanned")
	require.True(t, strings.HasPrefix(plantedKey, "query_partition:logs:"+projectID.String()))
}

func Test_PartitionGrid_CoversTheRangeOnTheEpochGrid(t *testing.T) {
	from := time.Date(2026, 10, 9, 10, 17, 0, 0, time.UTC)
	to := time.Date(2026, 10, 9, 13, 0, 0, 0, time.UTC)

	parts := partitionGrid(from, to, time.Hour)

	require.Len(t, parts, 4, "10:00, 11:00, 12:00 and the 13:00 hour holding to")
	require.Equal(t, time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC), parts[0].start)
	require.Equal(t, time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC), parts[3].end)
}

// An open range must not start the partition grid at 1970 (~500k hourly
// partitions, each a cache key and an OR clause).
func Test_AggregateByPartition_OpenRange_PartitionsOnlyWhereSegmentsAre(t *testing.T) {
	dir := t.TempDir()
	projectID := uuid.New()
	to := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Hour).Add(10 * time.Minute)
	from := to.Add(-3 * time.Hour)
	records := writeLogSegments(t, dir, projectID, from, to, 2, 100)
	require.NoError(t, storage.GetDb().Create(records).Error)
	store := &partcache.Memory{}
	querier, err := NewLogQuerier(WithQueryDir(filepath.Join(dir, "cached")),
		WithPartitionCache(partcache.New(store, time.Hour, 5*time.Minute)))
	require.NoError(t, err)
	defer func() { _ = querier.Close() }()

	response, err := querier.ExecuteAggregation(context.Background(), projectID, &logs_core.AggregationRequestDTO{
		BucketMs: 1, GroupField: "level",
	})
	require.NoError(t, err)

	require.EqualValues(t, 200, total(response))
	require.LessOrEqual(t, len(store.Values), 6, "the hours from the oldest segment to now, not since 1970")
	for _, bucket := range response.Buckets {
		require.Zero(t, bucket.Timestamp.Nanosecond()%int(time.Second), "1 ms buckets floored at 1 s")
	}
}

func Test_PartitionedAggregationFits_TooManyPartitions_FallsBack(t *testing.T) {
	now := time.Now().UTC()
	records := []*segments.SegmentRecord{{MinTime: now.Add(-3 * time.Hour), MaxTime: now}}
	request := &logs_core.AggregationRequestDTO{BucketMs: 60_000}
	open := time.Unix(0, 0).UTC()

	require.True(t, partitionedAggregationFits(request, open, noUpperBound, nil, records),
		"clamped to the segments' span")
	require.False(t, partitionedAggregationFits(request, open, noUpperBound,
		[]segments.ResolvedFile{{Path: "pending.parquet"}}, records),
		"pending rows have no known span: the open range stays, past the cap")
}

func Test_MergeRanges_JoinsTouchingRangesOnly(t *testing.T) {
	at := func(hour int) time.Time { return time.Date(2026, 10, 9, hour, 0, 0, 0, time.UTC) }

	merged := mergeRanges([]timeRange{
		{start: at(3), end: at(4), includeEnd: true},
		{start: at(1), end: at(2)},
		{start: at(2), end: at(3)},
		{start: at(6), end: at(7)},
	})

	require.Equal(t, []timeRange{
		{start: at(1), end: at(4), includeEnd: true},
		{start: at(6), end: at(7)},
	}, merged)
}

func counts(response *logs_core.AggregationResponseDTO) map[string]int64 {
	result := map[string]int64{}
	for _, bucket := range response.Buckets {
		result[bucket.Timestamp.UTC().Format(time.RFC3339)+"|"+bucket.GroupKey] += bucket.Count
	}
	return result
}

func total(response *logs_core.AggregationResponseDTO) int64 {
	var sum int64
	for _, bucket := range response.Buckets {
		sum += bucket.Count
	}
	return sum
}
