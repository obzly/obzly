package logs_querier_test

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"testing"
	"time"

	logs_compactor "obzly/internal/features/logs/compactor"
	logs_core "obzly/internal/features/logs/core"
	logs_ingester "obzly/internal/features/logs/ingester"
	logs_querier "obzly/internal/features/logs/querier"
	logs_uploader "obzly/internal/features/logs/uploader"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tieredFixture: four segments over the same three hours of logs (levels,
// services, pods and message patterns at random seconds, and a rollout 70
// minutes in): the first three compacted, the last L0.
type tieredFixture struct {
	projectID uuid.UUID
	querier   *logs_querier.LogQuerier
	compactor *logs_compactor.LogCompactor
	segments  []*segments.SegmentRecord
	base      time.Time
}

// tieredSnapshot: what each count returns, exact ones (equal to the raw
// logs) apart from deploy markers (minute precision from any rollup).
type tieredSnapshot struct {
	exact   map[string]any
	deploys *logs_core.DeploysResponseDTO
}

// Grouped counts, histograms and pattern changes give the raw logs' answer
// from logs_1m alone, with the smaller tiers, and with a mix of segments
// with and without each tier (backfill in progress) - aligned to hours,
// minutes or neither.
func Test_CountsFromRollupTiers_MatchTheRawLogs(t *testing.T) {
	f := seedTieredFixture(t)
	raw := f.snapshot(t)

	require.NoError(t, f.compactor.RollupSegments(f.projectID))
	require.Len(t, f.rollups(t, logs_core.StreamLogsRollup), 3)
	require.Len(t, f.rollups(t, logs_core.StreamLogsPodRollup), 3)
	require.Len(t, f.rollups(t, logs_core.StreamLogsPatternRollup), 3)

	f.dropRollups(t, logs_core.StreamLogsPodRollup)
	f.dropRollups(t, logs_core.StreamLogsPatternRollup)
	perMinute := f.snapshot(t)
	f.assertSame(t, raw.exact, perMinute.exact, "logs_1m only")

	require.NoError(t, f.compactor.RollupSegments(f.projectID))
	require.Len(t, f.rollups(t, logs_core.StreamLogsPodRollup), 3)
	tiered := f.snapshot(t)
	f.assertSame(t, raw.exact, tiered.exact, "every tier")
	assert.Equal(t, perMinute.deploys, tiered.deploys, "deploys, every tier")
	require.NotEmpty(t, tiered.deploys.Markers)

	// Segment 1 has only logs_1m, segment 2 only the derived tiers.
	f.dropRollups(t, logs_core.StreamLogsPodRollup, f.segments[1])
	f.dropRollups(t, logs_core.StreamLogsPatternRollup, f.segments[1])
	f.dropRollups(t, logs_core.StreamLogsRollup, f.segments[2])
	mixed := f.snapshot(t)
	f.assertSame(t, raw.exact, mixed.exact, "a mix of tiers")
	assert.Equal(t, perMinute.deploys, mixed.deploys, "deploys, a mix of tiers")
}

// Keys past the IN-list limit are a semi-join, with the counts the unkeyed
// count has for them (a week's keyed pattern count took 15 s as an IN list).
func Test_GroupWindows_ManyKeys_CountTheSameAsWithoutKeys(t *testing.T) {
	f := seedTieredFixture(t)
	require.NoError(t, f.compactor.RollupSegments(f.projectID))
	request := func(keys []string) *logs_core.GroupWindowsResponseDTO {
		response, err := f.querier.ExecuteGroupWindows(context.Background(), f.projectID,
			&logs_core.GroupWindowsRequestDTO{
				GroupField: logs_core.PatternHashField, Keys: keys,
				Windows:    []logs_core.CountWindowDTO{{From: f.base, To: f.base.Add(3 * time.Hour)}},
				Selections: []logs_core.GroupSelectionDTO{{Where: "w0 > 0", Limit: 1000}},
			})
		require.NoError(t, err)
		return response
	}
	counts := func(response *logs_core.GroupWindowsResponseDTO) map[string]int64 {
		result := map[string]int64{}
		for _, row := range response.Selections[0].Rows {
			result[row.Key] = row.Counts[0]
		}
		return result
	}

	keys := manyPatternKeys()
	all, keyed := counts(request(nil)), counts(request(keys))

	require.Len(t, keyed, 2)
	for _, key := range keys[:2] {
		require.Positive(t, keyed[key])
		require.Equal(t, all[key], keyed[key])
	}
}

// Whole hours grouped by pattern come from logs_1h_patterns alone: with the
// raw files and logs_1m gone, the counts are unchanged.
func Test_GroupWindows_WholeHoursByPattern_ReadOnlyTheHourlyCounts(t *testing.T) {
	f := seedTieredFixture(t)
	require.NoError(t, (&segments.SegmentRepository{}).DeleteByIDs([]uuid.UUID{f.segments[3].ID}))
	request := func() *logs_core.GroupWindowsRequestDTO {
		return &logs_core.GroupWindowsRequestDTO{
			Query:      condition("level", logs_core.ConditionOperatorEquals, "error"),
			GroupField: logs_core.JoinGroupFields(logs_core.PatternHashField, "service_name"),
			Windows: []logs_core.CountWindowDTO{
				{From: f.base, To: f.base.Add(time.Hour - time.Nanosecond)},
				{From: f.base.Add(time.Hour), To: f.base.Add(3*time.Hour - time.Nanosecond)},
			},
			Selections: []logs_core.GroupSelectionDTO{{OrderBy: "w0 DESC", Limit: 1000}},
		}
	}
	want, err := f.querier.ExecuteGroupWindows(context.Background(), f.projectID, request())
	require.NoError(t, err)
	require.NotEmpty(t, want.Selections[0].Rows)

	require.NoError(t, f.compactor.RollupSegments(f.projectID))
	f.removeFiles(t, logs_core.StreamLogsRollup)

	got, err := f.querier.ExecuteGroupWindows(context.Background(), f.projectID, request())
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// Minute-aligned histograms by pod come from logs_1m_pods alone.
func Test_Histogram_ByPod_ReadsOnlyThePodCounts(t *testing.T) {
	f := seedTieredFixture(t)
	require.NoError(t, (&segments.SegmentRepository{}).DeleteByIDs([]uuid.UUID{f.segments[3].ID}))
	from, to := f.base.Add(13*time.Minute), f.base.Add(158*time.Minute-time.Nanosecond)
	want := f.histogram(t, condition("k8s_namespace_name", logs_core.ConditionOperatorEquals, "shop"),
		"k8s_pod_name", 5*time.Minute, from, to)
	require.NotEmpty(t, want)

	require.NoError(t, f.compactor.RollupSegments(f.projectID))
	f.removeFiles(t, logs_core.StreamLogsRollup)

	assert.Equal(t, want, f.histogram(t, condition("k8s_namespace_name", logs_core.ConditionOperatorEquals, "shop"),
		"k8s_pod_name", 5*time.Minute, from, to))
}

// Each derived tier sums logs_1m's counts over its own dimensions, names the
// raw segment as its source, and holds every log.
func Test_RollupSegments_DerivesTiersFromPerMinuteCounts_WithTheSameSums(t *testing.T) {
	f := seedRollupFixture(t)
	require.NoError(t, f.compactor.RollupSegments(f.projectID))
	perMinute := singleRollup(t, f.projectID, f.source, logs_core.StreamLogsRollup)
	pods := singleRollup(t, f.projectID, f.source, logs_core.StreamLogsPodRollup)
	hourly := singleRollup(t, f.projectID, f.source, logs_core.StreamLogsPatternRollup)

	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	rows := func(query string, path string) [][]any {
		t.Helper()
		result, err := db.Query(query, path)
		require.NoError(t, err)
		defer func() { _ = result.Close() }()
		columns, err := result.Columns()
		require.NoError(t, err)
		var all [][]any
		for result.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			require.NoError(t, result.Scan(pointers...))
			all = append(all, values)
		}
		return all
	}
	const podColumns = "timestamp, level, service_name, k8s_namespace_name, k8s_container_name, k8s_pod_name"
	wantPods := rows("SELECT "+podColumns+", sum(cnt)::BIGINT FROM read_parquet(?) GROUP BY ALL ORDER BY ALL",
		perMinute.Path)
	assert.Equal(t, wantPods, rows("SELECT "+podColumns+", cnt FROM read_parquet(?) ORDER BY ALL", pods.Path))
	assert.Equal(t, int64(len(wantPods)), pods.RowCount)

	wantHourly := rows(`SELECT time_bucket(INTERVAL '1 hour', timestamp), level, service_name, pattern_hash,
		sum(cnt)::BIGINT FROM read_parquet(?) GROUP BY ALL ORDER BY ALL`, perMinute.Path)
	assert.Equal(t, wantHourly, rows(
		"SELECT timestamp, level, service_name, pattern_hash, cnt FROM read_parquet(?) ORDER BY ALL", hourly.Path))
	assert.Less(t, len(wantHourly), int(perMinute.RowCount))
	for _, row := range wantHourly {
		at, ok := row[0].(time.Time)
		require.True(t, ok)
		assert.Equal(t, at.Truncate(time.Hour), at)
	}

	for _, path := range []string{pods.Path, hourly.Path} {
		total := rows("SELECT sum(cnt)::BIGINT FROM read_parquet(?)", path)
		assert.Equal(t, int64(600), total[0][0], path)
	}
}

// Tiers missing from a segment that already has logs_1m (written before
// they existed) are built from it, leaving logs_1m alone.
func Test_RollupSegments_SegmentWithOnlyPerMinuteCounts_GetsTheDerivedTiers(t *testing.T) {
	f := seedRollupFixture(t)
	require.NoError(t, f.compactor.RollupSegments(f.projectID))
	perMinute := singleRollup(t, f.projectID, f.source, logs_core.StreamLogsRollup)
	repo := &segments.SegmentRepository{}
	for _, stream := range []string{logs_core.StreamLogsPodRollup, logs_core.StreamLogsPatternRollup} {
		_, err := repo.DeleteByProject(f.projectID, stream)
		require.NoError(t, err)
	}

	require.NoError(t, f.compactor.RollupSegments(f.projectID))

	assert.Equal(t, perMinute.ID, singleRollup(t, f.projectID, f.source, logs_core.StreamLogsRollup).ID)
	singleRollup(t, f.projectID, f.source, logs_core.StreamLogsPodRollup)
	singleRollup(t, f.projectID, f.source, logs_core.StreamLogsPatternRollup)
}

// A logs_1m file only in object storage (another worker built it) is
// downloaded for the derived tiers, and the download removed afterwards.
func Test_RollupSegments_PerMinuteCountsOnlyInObjectStorage_DerivesFromADownload(t *testing.T) {
	f := seedRollupFixture(t)
	require.NoError(t, f.compactor.RollupSegments(f.projectID))
	perMinute := singleRollup(t, f.projectID, f.source, logs_core.StreamLogsRollup)
	store, err := segments.NewObjectStore(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, logs_uploader.NewLogUploaderForStream(store, logs_core.StreamLogsRollup).
		Upload(context.Background(), perMinute))
	require.NoError(t, os.Remove(perMinute.Path))
	repo := &segments.SegmentRepository{}
	for _, stream := range []string{logs_core.StreamLogsPodRollup, logs_core.StreamLogsPatternRollup} {
		_, err := repo.DeleteByProject(f.projectID, stream)
		require.NoError(t, err)
	}
	compactor, err := logs_compactor.NewLogCompactor(t.TempDir(), logs_compactor.WithObjectStore(store))
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })

	require.NoError(t, compactor.RollupSegments(f.projectID))

	singleRollup(t, f.projectID, f.source, logs_core.StreamLogsPodRollup)
	singleRollup(t, f.projectID, f.source, logs_core.StreamLogsPatternRollup)
	assert.NoFileExists(t, perMinute.Path)
}

// A segment without per-minute counts (L0) gets no derived tiers either.
func Test_RollupSegments_SegmentWithoutPerMinuteCounts_GetsNoDerivedTiers(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute).Add(-time.Hour)
	projectID, _ := seedProject(t, []*logs_core.LogItem{
		{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "cache flushed"},
		{Timestamp: now.Add(time.Minute), Level: logs_core.LogLevelError, Message: "cache flushed"},
	})
	compactor, err := logs_compactor.NewLogCompactor(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })

	require.NoError(t, compactor.RollupSegments(projectID))

	for _, stream := range logs_core.LogRollupStreams {
		rollups, err := (&segments.SegmentRepository{}).ListByProject(projectID, stream)
		require.NoError(t, err)
		assert.Empty(t, rollups, stream)
	}
}

// Derived tiers whose source is gone are deleted the next pass, files
// included, and deleting the project deletes every tier.
func Test_RollupSegments_WhenSourceIsGone_DeletesTheDerivedTiers(t *testing.T) {
	f := seedRollupFixture(t)
	require.NoError(t, f.compactor.RollupSegments(f.projectID))
	pods := singleRollup(t, f.projectID, f.source, logs_core.StreamLogsPodRollup)
	hourly := singleRollup(t, f.projectID, f.source, logs_core.StreamLogsPatternRollup)

	require.NoError(t, (&segments.SegmentRepository{}).DeleteByIDs([]uuid.UUID{f.source.ID}))
	require.NoError(t, f.compactor.RollupSegments(f.projectID))

	for _, stream := range logs_core.LogRollupStreams {
		rollups, err := (&segments.SegmentRepository{}).ListByProject(f.projectID, stream)
		require.NoError(t, err)
		assert.Empty(t, rollups, stream)
	}
	assert.NoFileExists(t, pods.Path)
	assert.NoFileExists(t, hourly.Path)

	g := seedRollupFixture(t)
	require.NoError(t, g.compactor.RollupSegments(g.projectID))
	singleRollup(t, g.projectID, g.source, logs_core.StreamLogsPatternRollup)
	_, err := g.compactor.DeleteAllForProject(g.projectID)
	require.NoError(t, err)
	for _, stream := range logs_core.LogRollupStreams {
		rollups, err := (&segments.SegmentRepository{}).ListByProject(g.projectID, stream)
		require.NoError(t, err)
		assert.Empty(t, rollups, stream)
	}
}

func seedTieredFixture(t *testing.T) tieredFixture {
	t.Helper()
	base := time.Now().UTC().Truncate(time.Hour).Add(-6 * time.Hour)
	ingester, err := logs_ingester.NewLogIngester(t.TempDir(), segments.WithMaxRows(1_000_000))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })

	random := rand.New(rand.NewSource(11)) //nolint:gosec // test data
	levels := []logs_core.LogLevel{logs_core.LogLevelInfo, logs_core.LogLevelWarn, logs_core.LogLevelError}
	type pod struct{ namespace, name, container, service string }
	pods := []pod{
		{"shop", "checkout-7d9f8b6c5-x2b4k", "app", "checkout"},
		{"shop", "checkout-7d9f8b6c5-q8z7w", "app", "checkout"},
		{"shop", "cart-0", "app", "cart"},
		{"ops", "worker-5c6d7f8g9-mnbvc", "sidecar", "worker"},
	}
	rollout := pod{"shop", "checkout-5b6c7d8f9-h2j4k", "app", "checkout"}
	messages := []func() string{
		func() string { return fmt.Sprintf("request %d took %dms", random.Intn(1000), random.Intn(500)) },
		func() string { return "cache flushed" },
		func() string { return fmt.Sprintf("user %d signed in", random.Intn(100)) },
		func() string { return fmt.Sprintf("payment failed for order %d", random.Intn(10_000)) },
	}

	projectID := uuid.New()
	for range 4 {
		var items []*logs_core.LogItem
		for range 500 {
			offset := time.Duration(random.Int63n(int64(3 * time.Hour)))
			from := pods[random.Intn(len(pods))]
			if offset > 70*time.Minute && random.Intn(4) == 0 {
				from = rollout
			}
			items = append(items, &logs_core.LogItem{
				ID: uuid.New(), ProjectID: projectID, Timestamp: base.Add(offset),
				Level:   levels[random.Intn(len(levels))],
				Message: messages[random.Intn(len(messages))](),
				Fields: map[string]any{
					"service_name": from.service, "k8s_namespace_name": from.namespace,
					"k8s_pod_name": from.name, "k8s_container_name": from.container,
				},
			})
		}
		require.NoError(t, ingester.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: items}))
		_, err = ingester.FlushAll()
		require.NoError(t, err)
	}

	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, logs_core.StreamLogs)
	require.NoError(t, err)
	require.Len(t, records, 4)
	sort.Slice(records, func(i, j int) bool { return records[i].Path < records[j].Path })
	require.NoError(t, repo.SetLevel([]uuid.UUID{records[0].ID, records[1].ID, records[2].ID}, segments.LevelCompacted))

	querier, err := logs_querier.NewLogQuerier()
	require.NoError(t, err)
	t.Cleanup(func() { _ = querier.Close() })
	compactor, err := logs_compactor.NewLogCompactor(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })
	return tieredFixture{projectID, querier, compactor, records, base}
}

func (f tieredFixture) snapshot(t *testing.T) tieredSnapshot {
	t.Helper()
	ctx := context.Background()
	exact := map[string]any{}
	for name, request := range f.windowRequests() {
		response, err := f.querier.ExecuteGroupWindows(ctx, f.projectID, request)
		require.NoError(t, err, name)
		exact["windows: "+name] = response
	}

	ragged := f.base.Add(17*time.Minute + 23*time.Second)
	end := f.base.Add(3 * time.Hour)
	errorsOnly := condition("level", logs_core.ConditionOperatorEquals, "error")
	type shape struct {
		query  *logs_core.QueryNode
		group  string
		bucket time.Duration
		from   time.Time
		to     time.Time
	}
	shapes := map[string]shape{
		"patterns, hourly, whole hours":   {nil, logs_core.PatternHashField, time.Hour, f.base, end},
		"patterns, hourly, ragged":        {nil, logs_core.PatternHashField, time.Hour, ragged, end.Add(-11 * time.Second)},
		"pattern and level, 2 h, ragged":  {nil, logs_core.JoinGroupFields(logs_core.PatternHashField, "level"), 2 * time.Hour, ragged, end},
		"errors by pattern, hourly":       {errorsOnly, logs_core.PatternHashField, time.Hour, f.base.Add(time.Minute), end},
		"patterns, 15 min":                {nil, logs_core.PatternHashField, 15 * time.Minute, ragged, end},
		"services, 5 min, ragged":         {nil, "service_name", 5 * time.Minute, ragged, ragged.Add(97*time.Minute + 5*time.Second)},
		"pods, 1 min":                     {nil, "k8s_pod_name", time.Minute, f.base.Add(30 * time.Minute), f.base.Add(50 * time.Minute)},
		"one namespace, hourly, no group": {condition("k8s_namespace_name", logs_core.ConditionOperatorEquals, "ops"), "", time.Hour, ragged, end},
		"pattern and pod, hourly":         {nil, logs_core.JoinGroupFields(logs_core.PatternHashField, "k8s_pod_name"), time.Hour, ragged, end},
	}
	for name, s := range shapes {
		exact["histogram: "+name] = f.histogram(t, s.query, s.group, s.bucket, s.from, s.to)
	}

	changes, err := f.querier.ExecutePatternChanges(ctx, f.projectID, &logs_core.PatternChangesRequestDTO{
		Current:   &logs_core.TimeRangeDTO{From: new(f.base.Add(2 * time.Hour)), To: new(end)},
		Baseline:  &logs_core.TimeRangeDTO{From: new(ragged), To: new(f.base.Add(2 * time.Hour))},
		MinCount:  1,
		CountOnly: true,
	})
	require.NoError(t, err)
	exact["pattern changes"] = changes

	deploys, err := f.querier.ExecuteDeploys(ctx, f.projectID, &logs_core.DeploysRequestDTO{
		TimeRange: &logs_core.TimeRangeDTO{From: new(f.base), To: new(end)},
	})
	require.NoError(t, err)
	return tieredSnapshot{exact: exact, deploys: deploys}
}

func (f tieredFixture) windowRequests() map[string]*logs_core.GroupWindowsRequestDTO {
	ragged := f.base.Add(17*time.Minute + 23*time.Second)
	end := f.base.Add(3 * time.Hour)
	top := []logs_core.GroupSelectionDTO{{OrderBy: "w0 DESC", Limit: 1000}}
	hourly := []logs_core.CountWindowDTO{
		{From: f.base, To: f.base.Add(time.Hour)},
		{From: f.base.Add(time.Hour), To: f.base.Add(2 * time.Hour)},
		{From: f.base.Add(2 * time.Hour), To: end},
	}
	return map[string]*logs_core.GroupWindowsRequestDTO{
		"patterns, current against baseline": {
			GroupField: logs_core.PatternHashField,
			Windows: []logs_core.CountWindowDTO{
				{From: ragged.Add(time.Hour), To: end}, {From: f.base, To: ragged.Add(time.Hour)},
			},
			Selections: []logs_core.GroupSelectionDTO{
				{OrderBy: "w0 DESC", Limit: 1000}, {Where: "w1 = 0"},
			},
		},
		"pattern, service and level per hour": {
			GroupField: logs_core.JoinGroupFields(logs_core.PatternHashField, "service_name", "level"),
			Windows:    hourly, Selections: top,
		},
		"services, errors then all": {
			GroupField: "service_name",
			Windows: []logs_core.CountWindowDTO{
				{From: f.base, To: f.base.Add(90 * time.Minute), Levels: []string{"error"}},
				{From: f.base.Add(90*time.Minute + time.Second), To: end},
			},
			Selections: top,
		},
		"workloads, ragged": {
			GroupField: logs_core.WorkloadField,
			Windows:    []logs_core.CountWindowDTO{{From: ragged, To: end.Add(-time.Minute - 3*time.Second)}},
			Selections: top,
		},
		"pods in one namespace": {
			Query:      condition("k8s_namespace_name", logs_core.ConditionOperatorEquals, "shop"),
			GroupField: "k8s_pod_name", Windows: hourly, Selections: top,
		},
		"level, service and pattern sets": {
			GroupSets: []string{"level", "service_name", logs_core.PatternHashField},
			Windows:   []logs_core.CountWindowDTO{{From: ragged, To: end}}, Selections: top,
		},
		"level and namespace sets": {
			GroupSets: []string{"level", "k8s_namespace_name"},
			Windows:   []logs_core.CountWindowDTO{{From: ragged, To: end}}, Selections: top,
		},
		"two patterns of one service": {
			Query:      condition("service_name", logs_core.ConditionOperatorEquals, "checkout"),
			GroupField: logs_core.PatternHashField,
			Keys:       []string{logs_core.PatternHash("cache flushed"), logs_core.PatternHash("user 7 signed in")},
			Windows:    hourly, Selections: top,
		},
		"patterns keyed past the IN-list limit": {
			GroupField: logs_core.PatternHashField,
			Keys:       manyPatternKeys(),
			Windows:    hourly, Selections: top,
		},
		"pattern and pod": {
			GroupField: logs_core.JoinGroupFields(logs_core.PatternHashField, "k8s_pod_name"),
			Windows:    []logs_core.CountWindowDTO{{From: ragged, To: end}}, Selections: top,
		},
	}
}

func (f tieredFixture) histogram(
	t *testing.T, query *logs_core.QueryNode, groupField string, bucket time.Duration, from, to time.Time,
) []logs_core.AggregationBucketDTO {
	t.Helper()
	request := aggregationRequest(query, from, to)
	request.GroupField, request.BucketMs, request.Limit = groupField, bucket.Milliseconds(), 10_000
	response, err := f.querier.ExecuteAggregation(context.Background(), f.projectID, request)
	require.NoError(t, err)
	return response.Buckets
}

func (f tieredFixture) rollups(t *testing.T, stream string) []*segments.SegmentRecord {
	t.Helper()
	rollups, err := (&segments.SegmentRepository{}).ListByProject(f.projectID, stream)
	require.NoError(t, err)
	return rollups
}

// dropRollups deletes stream's catalog rows built from sources (all when
// none are given).
func (f tieredFixture) dropRollups(t *testing.T, stream string, sources ...*segments.SegmentRecord) {
	t.Helper()
	wanted := map[string]bool{}
	for _, source := range sources {
		wanted[source.ID.String()] = true
	}
	var ids []uuid.UUID
	for _, rollup := range f.rollups(t, stream) {
		if len(sources) == 0 || wanted[rollup.Stats.Source] {
			ids = append(ids, rollup.ID)
		}
	}
	require.NotEmpty(t, ids)
	require.NoError(t, (&segments.SegmentRepository{}).DeleteByIDs(ids))
}

// removeFiles deletes the raw segments' files and stream's.
func (f tieredFixture) removeFiles(t *testing.T, stream string) {
	t.Helper()
	records := append(f.rollups(t, stream), f.segments[:3]...)
	for _, record := range records {
		require.NoError(t, os.Remove(record.Path))
	}
}

func (f tieredFixture) assertSame(t *testing.T, want, got map[string]any, state string) {
	t.Helper()
	for name := range want {
		assert.Equal(t, want[name], got[name], state+" - "+name)
	}
}

func singleRollup(
	t *testing.T, projectID uuid.UUID, source *segments.SegmentRecord, stream string,
) *segments.SegmentRecord {
	t.Helper()
	rollups, err := (&segments.SegmentRepository{}).ListByProject(projectID, stream)
	require.NoError(t, err)
	require.Len(t, rollups, 1, stream)
	require.NotNil(t, rollups[0].Stats)
	assert.Equal(t, source.ID.String(), rollups[0].Stats.Source, stream)
	return rollups[0]
}

// manyPatternKeys is two patterns the fixture logs and 40 it doesn't: past
// the 32 values an IN list may have, so the keys are a semi-join.
func manyPatternKeys() []string {
	keys := []string{logs_core.PatternHash("cache flushed"), logs_core.PatternHash("user 7 signed in")}
	for i := range 40 {
		keys = append(keys, logs_core.PatternHash(fmt.Sprintf("never logged %c", 'a'+i)))
	}
	return keys
}
