package logs_querier_test

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"

	logs_compactor "obzly/internal/features/logs/compactor"
	logs_core "obzly/internal/features/logs/core"
	logs_querier "obzly/internal/features/logs/querier"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rollupFixture: two hours of logs over a few levels, services and message
// patterns, at random seconds, in one compacted segment.
type rollupFixture struct {
	projectID uuid.UUID
	querier   *logs_querier.LogQuerier
	compactor *logs_compactor.LogCompactor
	source    *segments.SegmentRecord
	base      time.Time
}

func seedRollupFixture(t *testing.T) rollupFixture {
	t.Helper()
	base := time.Now().UTC().Truncate(time.Hour).Add(-4 * time.Hour)
	random := rand.New(rand.NewSource(7)) //nolint:gosec // test data
	levels := []logs_core.LogLevel{logs_core.LogLevelInfo, logs_core.LogLevelWarn, logs_core.LogLevelError}
	var items []*logs_core.LogItem
	for range 600 {
		offset := time.Duration(random.Int63n(int64(2 * time.Hour)))
		items = append(items, &logs_core.LogItem{
			Timestamp: base.Add(offset), Level: levels[random.Intn(len(levels))],
			Message: fmt.Sprintf("request %d took %dms", random.Intn(1000), random.Intn(500)),
			Fields: map[string]any{
				"service_name":       []string{"api", "web"}[random.Intn(2)],
				"k8s_namespace_name": "shop",
			},
		})
		if random.Intn(5) == 0 {
			items[len(items)-1].Message = "cache flushed"
		}
	}
	projectID, querier := seedProject(t, items)

	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, logs_core.StreamLogs)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.NoError(t, repo.SetLevel([]uuid.UUID{records[0].ID}, segments.LevelCompacted))

	compactor, err := logs_compactor.NewLogCompactor(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })
	return rollupFixture{projectID, querier, compactor, records[0], base}
}

func (f rollupFixture) histogram(
	t *testing.T, query *logs_core.QueryNode, groupField string, bucket time.Duration, from, to time.Time,
) []logs_core.AggregationBucketDTO {
	t.Helper()
	request := aggregationRequest(query, from, to)
	request.GroupField, request.BucketMs, request.Limit = groupField, bucket.Milliseconds(), 10_000
	response, err := f.querier.ExecuteAggregation(context.Background(), f.projectID, request)
	require.NoError(t, err)
	return response.Buckets
}

func (f rollupFixture) rollups(t *testing.T) []*segments.SegmentRecord {
	t.Helper()
	rollups, err := (&segments.SegmentRepository{}).ListByProject(f.projectID, logs_core.StreamLogsRollup)
	require.NoError(t, err)
	return rollups
}

// Histograms from the per-minute counts match the raw logs exactly -
// minute-aligned or not, filtered, and grouped by each dimension.
func Test_Histogram_FromPerMinuteCounts_MatchesTheRawLogs(t *testing.T) {
	f := seedRollupFixture(t)
	hash := logs_core.PatternHash("cache flushed")
	type shape struct {
		query  *logs_core.QueryNode
		group  string
		bucket time.Duration
		from   time.Time
		to     time.Time
	}
	ragged := f.base.Add(17*time.Minute + 23*time.Second)
	shapes := map[string]shape{
		"levels, whole hours":       {nil, "level", time.Minute, f.base, f.base.Add(2 * time.Hour)},
		"levels, ragged edges":      {nil, "level", 5 * time.Minute, ragged, ragged.Add(83*time.Minute + 7*time.Second)},
		"service, level filter":     {condition("level", logs_core.ConditionOperatorEquals, "error"), "service_name", 10 * time.Minute, f.base, f.base.Add(2 * time.Hour)},
		"patterns":                  {nil, logs_core.PatternHashField, 30 * time.Minute, f.base, f.base.Add(2 * time.Hour)},
		"pattern filter, no groups": {condition(logs_core.PatternHashField, logs_core.ConditionOperatorEquals, hash), "", time.Minute, ragged, f.base.Add(2 * time.Hour)},
		"message filter (raw only)": {condition("message", logs_core.ConditionOperatorContains, "took 1"), "level", time.Minute, f.base, f.base.Add(2 * time.Hour)},
		"10-second buckets (raw)":   {nil, "level", 10 * time.Second, ragged, ragged.Add(10 * time.Minute)},
	}
	raw := map[string][]logs_core.AggregationBucketDTO{}
	for name, s := range shapes {
		raw[name] = f.histogram(t, s.query, s.group, s.bucket, s.from, s.to)
		require.NotEmpty(t, raw[name], name)
	}

	require.NoError(t, f.compactor.RollupSegments(f.projectID))
	require.Len(t, f.rollups(t), 1)

	for name, s := range shapes {
		assert.Equal(t, raw[name], f.histogram(t, s.query, s.group, s.bucket, s.from, s.to), name)
	}
}

// Whole minutes come from the counts alone: with the raw file gone, a
// minute-aligned histogram still has every log.
func Test_Histogram_WholeMinutes_ReadOnlyThePerMinuteCounts(t *testing.T) {
	f := seedRollupFixture(t)
	from, to := f.base, f.base.Add(2*time.Hour-time.Nanosecond)
	want := f.histogram(t, nil, "level", time.Hour, from, to)

	require.NoError(t, f.compactor.RollupSegments(f.projectID))
	require.NoError(t, os.Remove(f.source.Path))

	assert.Equal(t, want, f.histogram(t, nil, "level", time.Hour, from, to))
}

// A source merged away or deleted leaves its counts orphaned; the next
// pass deletes them, and deleting the project deletes them all.
func Test_RollupSegments_DeletesOrphans_AndProjectDeletionDeletesAll(t *testing.T) {
	f := seedRollupFixture(t)
	require.NoError(t, f.compactor.RollupSegments(f.projectID))
	rollups := f.rollups(t)
	require.Len(t, rollups, 1)
	assert.Equal(t, f.source.ID.String(), rollups[0].Stats.Source)

	require.NoError(t, f.compactor.RollupSegments(f.projectID))
	assert.Len(t, f.rollups(t), 1, "built once")

	require.NoError(t, (&segments.SegmentRepository{}).DeleteByIDs([]uuid.UUID{f.source.ID}))
	require.NoError(t, f.compactor.RollupSegments(f.projectID))
	assert.Empty(t, f.rollups(t), "orphan deleted")
	assert.NoFileExists(t, rollups[0].Path)

	g := seedRollupFixture(t)
	require.NoError(t, g.compactor.RollupSegments(g.projectID))
	require.Len(t, g.rollups(t), 1)
	_, err := g.compactor.DeleteAllForProject(g.projectID)
	require.NoError(t, err)
	assert.Empty(t, g.rollups(t))
}

// A segment written before pattern_hash was stored gets its counts' hashes
// computed from the messages - the same hashes the raw logs group by.
func Test_RollupSegments_OfAnOlderSegmentWithoutStoredPatterns_MatchesTheRawLogs(t *testing.T) {
	f := seedRollupFixture(t)
	stripPatternHash(t, f.projectID)
	from, to := f.base, f.base.Add(2*time.Hour-time.Nanosecond)
	want := f.histogram(t, nil, logs_core.PatternHashField, 30*time.Minute, from, to)

	require.NoError(t, f.compactor.RollupSegments(f.projectID))
	require.Len(t, f.rollups(t), 1)

	assert.Equal(t, want, f.histogram(t, nil, logs_core.PatternHashField, 30*time.Minute, from, to))
}
