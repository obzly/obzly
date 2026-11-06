package logs_querier

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/storage"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func Test_SearchWindows_NewestFirst_CoverTheRangeFromTheTopAndGrow(t *testing.T) {
	to := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	from := to.Add(-7 * 24 * time.Hour)
	records := []*segments.SegmentRecord{{MinTime: from, MaxTime: to}}

	windows := searchWindows(from, to, records, to, true)

	require.Equal(t, searchWindow{From: to.Add(-5 * time.Minute), To: to, IncludeTo: true}, windows[0])
	require.Equal(t, 20*time.Minute, windows[1].To.Sub(windows[1].From))
	require.Equal(t, from, windows[len(windows)-1].From)
	for i := 1; i < len(windows); i++ {
		require.Equal(t, windows[i-1].From, windows[i].To, "contiguous")
		require.False(t, windows[i].IncludeTo)
	}
	require.LessOrEqual(t, len(windows), 8, "a week in a handful of steps")
}

func Test_SearchWindows_OldestFirst_CoverTheRangeFromTheBottom(t *testing.T) {
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	records := []*segments.SegmentRecord{{MinTime: from, MaxTime: to}}

	windows := searchWindows(from, to, records, to, false)

	require.Equal(t, searchWindow{From: from, To: from.Add(5 * time.Minute)}, windows[0])
	last := windows[len(windows)-1]
	require.Equal(t, to, last.To)
	require.True(t, last.IncludeTo)
	for i := 1; i < len(windows); i++ {
		require.Equal(t, windows[i-1].To, windows[i].From)
	}
}

// "No end" is year 2200: windows start at the newest data, not there.
func Test_SearchWindows_OpenEnded_StartAtTheNewestData(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	records := []*segments.SegmentRecord{{MinTime: now.Add(-time.Hour), MaxTime: now.Add(-time.Minute)}}

	windows := searchWindows(time.Unix(0, 0).UTC(), noUpperBound, records, now, true)

	require.Equal(t, noUpperBound, windows[0].To, "the range's own end is still covered")
	require.Equal(t, now.Add(searchEdgeSlack).Add(-firstSearchWindow), windows[0].From)
	require.Equal(t, time.Unix(0, 0).UTC(), windows[len(windows)-1].From)
}

// Reading window by window returns exactly what one query over the whole
// range does, in both directions and at an offset; emitted rows add up to
// the page; a page the newest window fills stops there.
func Test_SelectPartitioned_MatchesASingleQueryAndStopsEarly(t *testing.T) {
	dir := t.TempDir()
	projectID := uuid.New()
	to := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	from := to.Add(-7 * 24 * time.Hour)
	records := writeLogSegments(t, dir, projectID, from, to, 6, 400)
	q, err := NewLogQuerier(WithQueryDir(filepath.Join(dir, "query")))
	require.NoError(t, err)
	defer func() { _ = q.Close() }()
	where := "project_id = ? AND timestamp >= ? AND timestamp <= ?"
	args := []any{projectID.String(), from, to}

	for _, ascending := range []bool{false, true} {
		for _, offset := range []int{0, 37} {
			want, err := q.selectMatching(context.Background(), nil, records, where, args, ascending, 50, offset, false, nil)
			require.NoError(t, err)
			var emitted []logs_core.LogItemDTO
			got, err := q.selectPartitioned(context.Background(), pageRequest{
				ordered: records, whereClause: where, whereArgs: args,
				ascending: ascending, limit: 50, offset: offset, from: from, to: to,
			}, func(logs []logs_core.LogItemDTO, _ logs_core.SearchWindowDTO) error {
				emitted = append(emitted, logs...)
				return nil
			})
			require.NoError(t, err)

			require.False(t, got.partial)
			require.Equal(t, ids(want), ids(got.logs), "ascending=%v offset=%d", ascending, offset)
			require.Equal(t, ids(got.logs), ids(emitted)[:len(got.logs)])
		}
	}

	windowsRead := 0
	_, err = q.selectPartitioned(context.Background(), pageRequest{
		ordered: records, whereClause: where, whereArgs: args, limit: 1, from: from, to: to,
	}, func([]logs_core.LogItemDTO, logs_core.SearchWindowDTO) error {
		windowsRead++
		return nil
	})
	require.NoError(t, err)
	require.Less(t, windowsRead, len(searchWindows(from, to, records, time.Now().UTC(), true)),
		"the walk stops once one row is found")
}

// writeLogSegments writes files of rows spread evenly over [from, to],
// each file covering an interleaved slice of the range.
func writeLogSegments(
	t *testing.T, dir string, projectID uuid.UUID, from, to time.Time, files, rowsPerFile int,
) []*segments.SegmentRecord {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	step := to.Sub(from) / time.Duration(files*rowsPerFile)
	var records []*segments.SegmentRecord
	for f := range files {
		path := filepath.Join(dir, fmt.Sprintf("seg-%d.parquet", f))
		_, err := db.Exec(fmt.Sprintf(`COPY (
			SELECT 'id-%d-' || lpad(r::VARCHAR, 6, '0') AS id, '%s' AS project_id,
			       (TIMESTAMP '%s' + to_microseconds(((r * %d + %d) * %d)::BIGINT))::TIMESTAMP_NS AS timestamp,
			       'info' AS level, 'm' || r AS message, '' AS client_ip, '' AS trace_id, '' AS span_id,
			       '{}' AS fields
			FROM range(%d) t(r)) TO '%s' (FORMAT PARQUET)`,
			f, projectID, from.Format("2006-01-02 15:04:05"), files, f, step.Microseconds(), rowsPerFile, path))
		require.NoError(t, err)
		records = append(records, &segments.SegmentRecord{
			ID: uuid.New(), ProjectID: projectID, Stream: "logs", Path: path, RowCount: int64(rowsPerFile),
			MinTime: from, MaxTime: to, SchemaVersion: segments.CurrentSchemaVersion,
		})
	}
	return records
}

func ids(logs []logs_core.LogItemDTO) []string {
	result := make([]string, len(logs))
	for i, log := range logs {
		result[i] = log.ID
	}
	return result
}

// contains() matches the text literally: % and _ in a search are not
// wildcards (they were escaped for LIKE before).
func Test_Contains_MatchesPercentAndUnderscoreLiterally(t *testing.T) {
	dir := t.TempDir()
	projectID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	path := filepath.Join(dir, "literal.parquet")
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	_, err = db.Exec(fmt.Sprintf(`COPY (
		SELECT 'id-' || i AS id, '%s' AS project_id, TIMESTAMP '%s' AS timestamp, 'info' AS level,
		       m AS message, '' AS client_ip, '' AS trace_id, '' AS span_id, '{}' AS fields
		FROM (VALUES (1, 'disk 100%% full'), (2, 'disk 1000 full'), (3, 'a_b'), (4, 'axb')) v(i, m))
		TO '%s' (FORMAT PARQUET)`, projectID, now.Format("2006-01-02 15:04:05"), path))
	require.NoError(t, err)
	_ = db.Close()
	require.NoError(t, storage.GetDb().Create(&segments.SegmentRecord{
		ID: uuid.New(), ProjectID: projectID, Stream: "logs", Path: path, RowCount: 4,
		MinTime: now, MaxTime: now, SchemaVersion: segments.CurrentSchemaVersion,
	}).Error)
	q, err := NewLogQuerier(WithQueryDir(filepath.Join(dir, "query")))
	require.NoError(t, err)
	defer func() { _ = q.Close() }()
	from, to := now.Add(-time.Minute), now.Add(time.Minute)
	search := func(text string) []string {
		response, err := q.ExecuteQuery(projectID, &logs_core.LogQueryRequestDTO{
			TimeRange: &logs_core.TimeRangeDTO{From: &from, To: &to},
			Query: &logs_core.QueryNode{Type: logs_core.QueryNodeTypeCondition, Condition: &logs_core.ConditionNode{
				Field: "message", Operator: logs_core.ConditionOperatorContains, Value: text,
			}},
		})
		require.NoError(t, err)
		var messages []string
		for _, log := range response.Logs {
			messages = append(messages, log.Message)
		}
		return messages
	}

	require.Equal(t, []string{"disk 100% full"}, search("0%"))
	require.Equal(t, []string{"a_b"}, search("_"))
}

// Past the search deadline the walk stops with what it found and how far it
// got, rather than failing or running on window after window.
func Test_SelectPartitioned_PastTheDeadline_ReturnsAPartialPage(t *testing.T) {
	dir := t.TempDir()
	projectID := uuid.New()
	to := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	from := to.Add(-7 * 24 * time.Hour)
	records := writeLogSegments(t, dir, projectID, from, to, 2, 50)
	q, err := NewLogQuerier(WithQueryDir(filepath.Join(dir, "query")))
	require.NoError(t, err)
	defer func() { _ = q.Close() }()
	previous := searchDeadline
	searchDeadline = time.Nanosecond
	t.Cleanup(func() { searchDeadline = previous })

	got, err := q.selectPartitioned(context.Background(), pageRequest{
		ordered: records, whereClause: "project_id = ?", whereArgs: []any{projectID.String()},
		limit: 10, from: from, to: to,
	}, nil)

	require.NoError(t, err)
	require.True(t, got.partial)
	require.Equal(t, to, got.searched, "nothing searched yet")
	require.Empty(t, got.logs)
}

// An ID-like search term matches as a whole token, and a segment whose
// token filter lacks it is never read: the other file here isn't even
// Parquet once its filter is built, so reading it would fail the search.
func Test_IDSearch_MatchesWholeTokensAndSkipsSegmentsWithoutThem(t *testing.T) {
	dir := t.TempDir()
	projectID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	segment := func(name string, messages ...string) *segments.SegmentRecord {
		path := filepath.Join(dir, name)
		values := ""
		for i, message := range messages {
			if i > 0 {
				values += ", "
			}
			values += fmt.Sprintf("('%s-%d', '%s')", name, i, message)
		}
		_, err := db.Exec(fmt.Sprintf(`COPY (
			SELECT id, '%s' AS project_id, TIMESTAMP '%s' AS timestamp, 'info' AS level, message,
			       '' AS client_ip, '' AS trace_id, '' AS span_id, '{}' AS fields
			FROM (VALUES %s) v(id, message)) TO '%s' (FORMAT PARQUET)`,
			projectID, now.Format("2006-01-02 15:04:05"), values, path))
		require.NoError(t, err)
		built, err := segments.BuildTokenBloom(context.Background(), db, "logs", path)
		require.NoError(t, err)
		require.True(t, built)
		record := &segments.SegmentRecord{ID: uuid.New(), ProjectID: projectID, Stream: "logs", Path: path,
			RowCount: int64(len(messages)), MinTime: now, MaxTime: now, SchemaVersion: segments.CurrentSchemaVersion,
			Stats: &segments.SegmentStats{TokenBloom: true}}
		require.NoError(t, storage.GetDb().Create(record).Error)
		return record
	}
	segment("with-id.parquet",
		"GET /trans/payout?purchaseId=6ac2e65bcf50f676fdd207d8",
		`getRuleRejectionLog { "purchaseId" : "6ac2e65bcf50f676fdd207d8" }`,
		"x6ac2e65bcf50f676fdd207d8y inside a longer token",
	)
	other := segment("without-id.parquet", "nothing to see 999988887777aaaa")
	require.NoError(t, os.WriteFile(other.Path, []byte("not parquet"), 0o644))
	q, err := NewLogQuerier(WithQueryDir(filepath.Join(dir, "query")))
	require.NoError(t, err)
	defer func() { _ = q.Close() }()
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	search := func(text string) []string {
		response, err := q.ExecuteQuery(projectID, &logs_core.LogQueryRequestDTO{
			TimeRange: &logs_core.TimeRangeDTO{From: &from, To: &to},
			Query: &logs_core.QueryNode{Type: logs_core.QueryNodeTypeCondition, Condition: &logs_core.ConditionNode{
				Field: "message", Operator: logs_core.ConditionOperatorContains, Value: text,
			}},
		})
		require.NoError(t, err, text)
		var messages []string
		for _, log := range response.Logs {
			messages = append(messages, log.Message)
		}
		return messages
	}

	require.ElementsMatch(t, []string{
		"GET /trans/payout?purchaseId=6ac2e65bcf50f676fdd207d8",
		`getRuleRejectionLog { "purchaseId" : "6ac2e65bcf50f676fdd207d8" }`,
	}, search("6ac2e65bcf50f676fdd207d8"))
	require.Empty(t, search("e65bcf50f676"), "a fragment of a longer token isn't a token")
}
