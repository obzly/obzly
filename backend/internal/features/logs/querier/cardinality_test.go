package logs_querier_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	logs_compactor "obzly/internal/features/logs/compactor"
	logs_core "obzly/internal/features/logs/core"
	logs_querier "obzly/internal/features/logs/querier"
	"obzly/internal/storage"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// cardinalityFixture: two days of logs over tens of thousands of message
// patterns and thousands of pods, skewed so a few patterns dominate, in
// compacted segments with their per-minute counts - the shape that made
// grouped counts read every group of every hour back into Go.
type cardinalityFixture struct {
	projectID uuid.UUID
	querier   *logs_querier.LogQuerier
	files     []string
	from, to  time.Time
}

const (
	cardinalityPatterns = 30_000
	cardinalityPods     = 4_000
	cardinalityFiles    = 8
	cardinalityRows     = 80_000 // per file
)

// A top N of a grouped count over high-cardinality data reads only N rows
// back from the merge, and matches a plain GROUP BY over the raw files.
func Test_TopNAggregation_OverManyPatterns_ReadsOnlyTheTopRowsAndMatchesTheRawCount(t *testing.T) {
	f := seedCardinalityFixture(t)
	from, to := f.from.Add(7*time.Minute+13*time.Second), f.to.Add(-3*time.Minute)

	before := f.querier.MergedRows()
	started := time.Now()
	response, err := f.querier.ExecuteAggregation(context.Background(), f.projectID, &logs_core.AggregationRequestDTO{
		TimeRange: &logs_core.TimeRangeDTO{From: &from, To: &to}, GroupField: logs_core.PatternHashField, Limit: 10,
	})
	require.NoError(t, err)
	elapsed := time.Since(started)
	merged := f.querier.MergedRows() - before
	t.Logf("top 10 of %d patterns: %d rows merged into Go in %s", cardinalityPatterns, merged, elapsed)

	want := f.naiveTopN(t, "pattern_hash", from, to, 10)
	got := map[string]int64{}
	var order []string
	for _, bucket := range response.Buckets {
		got[bucket.GroupKey] = bucket.Count
		order = append(order, bucket.GroupKey)
	}
	require.Equal(t, want.keys, order)
	require.Equal(t, want.counts, got)
	require.LessOrEqual(t, merged, int64(20), "only the top rows reach Go")
}

// The Patterns tab's comparison - now against 24 h earlier - is decided in
// the merge: the top, new, spiking and dropping patterns and how many of
// each match a full count of every pattern, and only the listed rows reach Go.
func Test_GroupWindows_PatternComparison_MatchesAFullCountAndReadsOnlyTheListedRows(t *testing.T) {
	f := seedCardinalityFixture(t)
	end := f.to.Add(-time.Minute - time.Nanosecond)
	from := end.Add(-3*time.Hour + 17*time.Second)
	windows := []logs_core.CountWindowDTO{{From: from, To: end}, {From: from.Add(-24 * time.Hour), To: end.Add(-24 * time.Hour)}}
	type kind struct {
		where   string
		args    []any
		order   string
		limit   int
		matches func(c []int64) bool
		less    func(a, b []int64) bool
	}
	byCurrent := func(a, b []int64) bool { return a[0] > b[0] }
	kinds := []kind{
		{"length(key) > 0 AND w0 > 0", nil, "", 30, func(c []int64) bool { return c[0] > 0 }, byCurrent},
		{"length(key) > 0 AND w1 = 0 AND w0 >= ?", []any{5}, "", 2000,
			func(c []int64) bool { return c[1] == 0 && c[0] >= 5 }, byCurrent},
		{"length(key) > 0 AND w1 > 0 AND w0 >= ? AND w0 >= 2 * w1", []any{5}, "w0 / w1 DESC", 30,
			func(c []int64) bool { return c[1] > 0 && c[0] >= 5 && c[0] >= 2*c[1] },
			func(a, b []int64) bool { return float64(a[0])/float64(a[1]) > float64(b[0])/float64(b[1]) }},
		{"length(key) > 0 AND w1 >= ? AND 2 * w0 <= w1", []any{5}, "w1 DESC", 30,
			func(c []int64) bool { return c[1] >= 5 && 2*c[0] <= c[1] }, func(a, b []int64) bool { return a[1] > b[1] }},
	}
	request := &logs_core.GroupWindowsRequestDTO{GroupField: logs_core.PatternHashField, Windows: windows}
	for _, k := range kinds {
		request.Selections = append(request.Selections,
			logs_core.GroupSelectionDTO{Where: k.where, WhereArgs: k.args, OrderBy: k.order, Limit: k.limit})
	}

	before := f.querier.MergedRows()
	started := time.Now()
	response, err := f.querier.ExecuteGroupWindows(context.Background(), f.projectID, request)
	require.NoError(t, err)
	merged := f.querier.MergedRows() - before
	t.Logf("pattern comparison: %d rows merged into Go in %s", merged, time.Since(started))

	full := f.naiveWindows(t, "pattern_hash", windows)
	var totals [2]int64
	for _, counts := range full {
		totals[0], totals[1] = totals[0]+counts[0], totals[1]+counts[1]
	}
	require.Equal(t, totals[:], response.Totals)
	rowsWanted := int64(1)
	for i, k := range kinds {
		var keys []string
		for key, counts := range full {
			if key != "" && k.matches(counts) {
				keys = append(keys, key)
			}
		}
		sort.Slice(keys, func(a, b int) bool {
			ca, cb := full[keys[a]], full[keys[b]]
			if k.less(ca, cb) != k.less(cb, ca) {
				return k.less(ca, cb)
			}
			return keys[a] < keys[b]
		})
		got := response.Selections[i]
		require.Equal(t, int64(len(keys)), got.Matched, "selection %d", i)
		want := keys[:min(len(keys), k.limit)]
		require.Len(t, got.Rows, len(want), "selection %d", i)
		for j, row := range got.Rows {
			require.Equal(t, want[j], row.Key, "selection %d row %d", i, j)
			require.Equal(t, full[row.Key], row.Counts, "selection %d row %d", i, j)
		}
		rowsWanted += 1 + int64(len(want))
	}
	require.NotZero(t, response.Selections[2].Matched, "the fixture has spikes")
	require.Equal(t, rowsWanted, merged, "the totals, each selection's count and its rows - nothing else")
}

// Explain this spike counts each dimension on its own (ReplicaSet from the
// pod name) in one pass; values and totals match a count per dimension.
func Test_GroupWindows_GroupSets_CountEachFieldOnItsOwn(t *testing.T) {
	f := seedCardinalityFixture(t)
	end := f.to.Add(-time.Nanosecond)
	windows := []logs_core.CountWindowDTO{
		{From: end.Add(-2*time.Hour + 9*time.Second), To: end}, {From: end.Add(-6 * time.Hour), To: end.Add(-2 * time.Hour)},
	}
	sets := []string{"service_name", "k8s_pod_name", logs_core.ReplicaSetField}
	response, err := f.querier.ExecuteGroupWindows(context.Background(), f.projectID, &logs_core.GroupWindowsRequestDTO{
		GroupSets: sets, Windows: windows,
		Selections: []logs_core.GroupSelectionDTO{{Limit: 10_000}, {Limit: 12}},
	})
	require.NoError(t, err)

	want := map[string][]int64{}
	for i, expr := range []string{"service_name", "k8s_pod_name", `regexp_replace(k8s_pod_name, '-[^-]+$', '')`} {
		for key, counts := range f.naiveWindows(t, expr, windows) {
			want[fmt.Sprint(i)+logs_core.GroupKeySeparator+key] = counts
		}
	}
	got := map[string][]int64{}
	for _, row := range response.Selections[0].Rows {
		got[row.Key] = row.Counts
	}
	require.Equal(t, want, got)
	require.Len(t, response.Selections[1].Rows, 12)
	var lines int64
	for key, counts := range want {
		if key[0] == '0' {
			lines += counts[0]
		}
	}
	require.Equal(t, lines, response.Totals[0], "every line once, not once per set")
}

// Health map: pods become workloads in SQL, and error and warning lines are
// windows of their own; the biggest keys match a full count.
func Test_GroupWindows_WorkloadsAndLevels_MatchAFullCount(t *testing.T) {
	f := seedCardinalityFixture(t)
	end := f.to.Add(-time.Nanosecond)
	from := end.Add(-5 * time.Hour)
	windows := []logs_core.CountWindowDTO{
		{From: from, To: end}, {From: from, To: end, Levels: []string{"error"}}, {From: from, To: end, Levels: []string{"warn"}},
	}
	before := f.querier.MergedRows()
	response, err := f.querier.ExecuteGroupWindows(context.Background(), f.projectID, &logs_core.GroupWindowsRequestDTO{
		GroupField: logs_core.JoinGroupFields("k8s_namespace_name", logs_core.WorkloadField), Windows: windows,
		Selections: []logs_core.GroupSelectionDTO{{Where: "w0 > 0", Limit: 100}},
	})
	require.NoError(t, err)
	merged := f.querier.MergedRows() - before

	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT k8s_namespace_name || chr(31) || regexp_replace(k8s_pod_name, '-[^-]+-[^-]+$', '') AS k,
		count(*), count(*) FILTER (WHERE level = 'error'), count(*) FILTER (WHERE level = 'warn')
		FROM read_parquet(?) WHERE timestamp >= ? AND timestamp <= ? GROUP BY k ORDER BY 2 DESC, k LIMIT 100`,
		f.files, from, end)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var want []logs_core.GroupCountsDTO
	for rows.Next() {
		row := logs_core.GroupCountsDTO{Counts: make([]int64, 3)}
		require.NoError(t, rows.Scan(&row.Key, &row.Counts[0], &row.Counts[1], &row.Counts[2]))
		want = append(want, row)
	}
	require.Equal(t, want, response.Selections[0].Rows)
	require.Equal(t, int64(7*300), response.Selections[0].Matched, "7 namespaces x 300 workloads")
	require.Equal(t, int64(100+2), merged)
}

// Pattern changes (the Anomalies badge and panel) classify in the merge:
// counts, totals and the listed patterns match a full count, and only the
// listed rows reach Go.
func Test_PatternChanges_OverManyPatterns_MatchAFullCount(t *testing.T) {
	f := seedCardinalityFixture(t)
	curTo := f.to.Add(-time.Nanosecond)
	curFrom := curTo.Add(-6 * time.Hour)
	baseFrom, baseTo := curFrom.Add(-24*time.Hour), curFrom.Add(-time.Nanosecond)

	before := f.querier.MergedRows()
	started := time.Now()
	counted, err := f.querier.ExecutePatternChanges(context.Background(), f.projectID, &logs_core.PatternChangesRequestDTO{
		Current:   &logs_core.TimeRangeDTO{From: &curFrom, To: &curTo},
		Baseline:  &logs_core.TimeRangeDTO{From: &baseFrom, To: &baseTo},
		CountOnly: true,
	})
	require.NoError(t, err)
	t.Logf("pattern changes, count only: %d rows merged into Go in %s", f.querier.MergedRows()-before, time.Since(started))
	require.LessOrEqual(t, f.querier.MergedRows()-before, int64(4))
	listed, err := f.querier.ExecutePatternChanges(context.Background(), f.projectID, &logs_core.PatternChangesRequestDTO{
		Current:  &logs_core.TimeRangeDTO{From: &curFrom, To: &curTo},
		Baseline: &logs_core.TimeRangeDTO{From: &baseFrom, To: &baseTo},
		Limit:    20,
	})
	require.NoError(t, err)

	// The classification as it ran in Go over every pattern before.
	full := f.naiveWindows(t, "pattern_hash", []logs_core.CountWindowDTO{
		{From: curFrom, To: curTo}, {From: baseFrom, To: baseTo},
	})
	scale := float64(curTo.Sub(curFrom)) / float64(baseTo.Sub(baseFrom))
	var newCount, spikeCount, dropCount int
	var curTotal, baseTotal int64
	var added, rising []string
	for hash, counts := range full {
		now, before := counts[0], counts[1]
		curTotal, baseTotal = curTotal+now, baseTotal+before
		expected := float64(before) * scale
		switch {
		case before == 0 && now >= 5:
			newCount++
			added = append(added, hash)
		case before > 0 && now >= 5 && float64(now) >= 2*expected:
			spikeCount++
			rising = append(rising, hash)
		case expected >= 5 && float64(now) <= 0.5*expected:
			dropCount++
		}
	}
	require.Equal(t, newCount, counted.NewCount)
	require.Equal(t, spikeCount, counted.SpikeCount)
	require.Equal(t, dropCount, counted.DropCount)
	require.Equal(t, curTotal, counted.CurrentTotal)
	require.Equal(t, baseTotal, counted.BaselineTotal)
	require.NotZero(t, newCount+spikeCount, "the fixture has changes")

	sort.Slice(added, func(i, j int) bool {
		if full[added[i]][0] != full[added[j]][0] {
			return full[added[i]][0] > full[added[j]][0]
		}
		return added[i] < added[j]
	})
	ratio := func(hash string) float64 { return float64(full[hash][0]) / (float64(full[hash][1]) * scale) }
	sort.Slice(rising, func(i, j int) bool {
		if ratio(rising[i]) != ratio(rising[j]) {
			return ratio(rising[i]) > ratio(rising[j])
		}
		if full[rising[i]][0] != full[rising[j]][0] {
			return full[rising[i]][0] > full[rising[j]][0]
		}
		return rising[i] < rising[j]
	})
	var got []string
	for _, change := range listed.Changes {
		if change.Change != logs_core.PatternChangeDrop {
			got = append(got, change.Hash)
		}
	}
	want := append(append([]string{}, added[:min(len(added), 20)]...), rising[:min(len(rising), 20)]...)
	require.Equal(t, want, got)
}

func seedCardinalityFixture(t *testing.T) cardinalityFixture {
	t.Helper()
	dir := t.TempDir()
	projectID := uuid.New()
	to := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	from := to.Add(-48 * time.Hour)
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	span := to.Sub(from) / cardinalityFiles
	var files []string
	var records []*segments.SegmentRecord
	for i := range cardinalityFiles {
		path := filepath.Join(dir, fmt.Sprintf("seg-%d.parquet", i))
		start := from.Add(time.Duration(i) * span)
		// u in [0, 1) from a hash of the row; cubed, low pattern ids dominate.
		_, err := db.Exec(fmt.Sprintf(`COPY (
			SELECT id, project_id, timestamp, level, 'msg ' || pattern AS message, '' AS client_ip,
			       '' AS trace_id, '' AS span_id, '{}' AS fields, service_name, k8s_namespace_name,
			       k8s_pod_name, 'main' AS k8s_container_name, 'p' || lpad(pattern::VARCHAR, 6, '0') AS pattern_hash
			FROM (
				SELECT 'id-%d-' || r AS id, '%s' AS project_id,
				       (TIMESTAMP '%s' + to_microseconds((r * %d)::BIGINT))::TIMESTAMP_NS AS timestamp,
				       ['info', 'info', 'warn', 'error'][1 + (r %% 4)::INT] AS level,
				       floor(%d * pow((hash(r * 31 + %d) %% 1000003) / 1000003.0, 3))::BIGINT AS pattern,
				       'svc-' || (hash(r) %% 40) AS service_name,
				       'ns-' || (hash(r + 1) %% 7) AS k8s_namespace_name,
				       'app' || (hash(r + 2) %% %d) %% 300 || '-5c8b6d7f9-' ||
				         substr('bcdfghjklmnpqrstvwxz2456789', 1 + (hash(r + 2) %% %d %% 27)::INT, 1) ||
				         substr('bcdfghjklmnpqrstvwxz2456789', 1 + (hash(r + 2) %% %d // 27 %% 27)::INT, 1) ||
				         'xkq' AS k8s_pod_name
				FROM range(%d) t(r)
			)
		) TO '%s' (FORMAT PARQUET)`,
			i, projectID, start.Format("2006-01-02 15:04:05"), span.Microseconds()/cardinalityRows,
			cardinalityPatterns, i, cardinalityPods, cardinalityPods, cardinalityPods, cardinalityRows, path))
		require.NoError(t, err)
		files = append(files, path)
		records = append(records, &segments.SegmentRecord{
			ID: uuid.New(), ProjectID: projectID, Stream: logs_core.StreamLogs, Path: path,
			RowCount: cardinalityRows, MinTime: start, MaxTime: start.Add(span),
			SchemaVersion: segments.CurrentSchemaVersion, Level: segments.LevelCompacted,
		})
	}
	require.NoError(t, storage.GetDb().Create(records).Error)

	compactor, err := logs_compactor.NewLogCompactor(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = compactor.Close() })
	for range 2 {
		require.NoError(t, compactor.RollupSegments(projectID))
	}
	rollups, err := (&segments.SegmentRepository{}).ListByProject(projectID, logs_core.StreamLogsRollup)
	require.NoError(t, err)
	require.Len(t, rollups, cardinalityFiles)

	querier, err := logs_querier.NewLogQuerier(logs_querier.WithQueryDir(filepath.Join(dir, "query")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = querier.Close() })
	return cardinalityFixture{projectID, querier, files, from, to}
}

// naiveCount is a reference top N: one GROUP BY over every raw file.
type naiveCount struct {
	keys   []string
	counts map[string]int64
}

func (f cardinalityFixture) naiveTopN(t *testing.T, keyExpr string, from, to time.Time, n int) naiveCount {
	t.Helper()
	return f.naiveQuery(t, fmt.Sprintf(`SELECT %s AS k, count(*) AS c FROM read_parquet(?)
		WHERE timestamp >= ? AND timestamp <= ? GROUP BY k ORDER BY c DESC, k ASC LIMIT %d`, keyExpr, n),
		from, to)
}

// naiveWindows is every key's count in each window, from the raw files.
func (f cardinalityFixture) naiveWindows(
	t *testing.T, keyExpr string, windows []logs_core.CountWindowDTO,
) map[string][]int64 {
	t.Helper()
	var filters []string
	var args []any
	for i, window := range windows {
		filter := "timestamp >= ? AND timestamp <= ?"
		args = append(args, window.From, window.To)
		if len(window.Levels) > 0 {
			filter += " AND level = ?"
			args = append(args, window.Levels[0])
		}
		filters = append(filters, fmt.Sprintf("count(*) FILTER (WHERE %s) AS c%d", filter, i))
	}
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	rows, err := db.Query("SELECT "+keyExpr+" AS k, "+strings.Join(filters, ", ")+
		" FROM read_parquet(?) GROUP BY k", append(args, f.files)...)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	result := map[string][]int64{}
	for rows.Next() {
		var key string
		counts := make([]int64, len(windows))
		dest := []any{&key}
		for i := range counts {
			dest = append(dest, &counts[i])
		}
		require.NoError(t, rows.Scan(dest...))
		var any int64
		for _, count := range counts {
			any += count
		}
		if any > 0 {
			result[key] = counts
		}
	}
	require.NoError(t, rows.Err())
	return result
}

func (f cardinalityFixture) naiveQuery(t *testing.T, query string, args ...any) naiveCount {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	rows, err := db.Query(query, append([]any{f.files}, args...)...)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	result := naiveCount{counts: map[string]int64{}}
	for rows.Next() {
		var key string
		var count int64
		require.NoError(t, rows.Scan(&key, &count))
		result.keys = append(result.keys, key)
		result.counts[key] = count
	}
	require.NoError(t, rows.Err())
	return result
}
