package logs_querier

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	logs_ingester "obzly/internal/features/logs/ingester"
	"obzly/internal/query"
	"obzly/internal/storage/segments"
	test_utils "obzly/internal/util/testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Every search answers the same whether it runs as one in-process query or
// as fragments over several executors merged by the coordinator - top-N
// groups included, which a per-fragment limit would get wrong.
func Test_Searches_SpreadOverExecutors_MatchInProcess(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	projectID := seedUploadedSegments(t, now)
	inProcess, err := NewLogQuerier(WithQueryDir(filepath.Join(t.TempDir(), "query")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = inProcess.Close() })
	spread, err := NewLogQuerier(WithQueryDir(filepath.Join(t.TempDir(), "query")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = spread.Close() })
	records, err := spread.segmentRepo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	// Segment ids are random: pick executor ids that split them.
	var executors []query.Executor
	for attempt := 0; len(query.Assign(records, executors)) < 2; attempt++ {
		executors = nil
		for i := range 3 {
			executors = append(executors, query.NewLocalExecutor(fmt.Sprintf("e%d-%d", i, attempt), spread.db, nil, ""))
		}
	}
	spread.coordinator, err = query.NewCoordinator(filepath.Join(t.TempDir(), "spread"), spread.db,
		query.NewLocalExecutor("local", spread.db, nil, ""), func() []query.Executor { return executors },
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	timeRange := &logs_core.TimeRangeDTO{From: &from, To: &to}

	for _, request := range []*logs_core.AggregationRequestDTO{
		{TimeRange: timeRange},
		{TimeRange: timeRange, GroupField: "level", Limit: 2},
		{TimeRange: timeRange, GroupField: "service", Limit: 3},
		{TimeRange: timeRange, BucketMs: 60_000},
		{TimeRange: timeRange, BucketMs: 60_000, GroupField: "level"},
	} {
		want, err := inProcess.ExecuteAggregation(context.Background(), projectID, request)
		require.NoError(t, err)
		got, err := spread.ExecuteAggregation(context.Background(), projectID, request)
		require.NoError(t, err)
		require.NotEmpty(t, want.Buckets)
		require.Equal(t, want, got, "aggregation %+v", request)
	}

	wantPatterns, err := inProcess.ExecutePatterns(
		projectID,
		&logs_core.PatternsRequestDTO{TimeRange: timeRange, Limit: 2},
	)
	require.NoError(t, err)
	gotPatterns, err := spread.ExecutePatterns(projectID, &logs_core.PatternsRequestDTO{TimeRange: timeRange, Limit: 2})
	require.NoError(t, err)
	require.Len(t, gotPatterns.Patterns, 2)
	for i := range wantPatterns.Patterns {
		require.Equal(t, wantPatterns.Patterns[i].Hash, gotPatterns.Patterns[i].Hash)
		require.Equal(t, wantPatterns.Patterns[i].Count, gotPatterns.Patterns[i].Count)
	}

	wantFields, err := inProcess.DiscoverFields(projectID)
	require.NoError(t, err)
	gotFields, err := spread.DiscoverFields(projectID)
	require.NoError(t, err)
	require.Equal(t, wantFields, gotFields)

	page := &logs_core.LogQueryRequestDTO{TimeRange: timeRange, Limit: 7, Offset: 5}
	wantPage, err := inProcess.ExecuteQuery(projectID, page)
	require.NoError(t, err)
	gotPage, err := spread.ExecuteQuery(projectID, page)
	require.NoError(t, err)
	require.Equal(t, wantPage.Total, gotPage.Total)
	require.Equal(t, wantPage.Logs, gotPage.Logs)
}

// Over the scan budget a search still returns its page with the count
// skipped, while a histogram is refused.
func Test_ScanBudget_SkipsTheCountButRefusesAggregations(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	projectID := seedUploadedSegments(t, now)
	q, err := NewLogQuerier(WithQueryDir(filepath.Join(t.TempDir(), "query")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = q.Close() })
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	timeRange := &logs_core.TimeRangeDTO{From: &from, To: &to}
	level := &logs_core.QueryNode{
		Type: logs_core.QueryNodeTypeCondition,
		Condition: &logs_core.ConditionNode{
			Field:    "level",
			Operator: logs_core.ConditionOperatorEquals,
			Value:    "error",
		},
	}

	page, err := q.ExecuteQuery(projectID, &logs_core.LogQueryRequestDTO{
		Query: level, TimeRange: timeRange, Limit: 10, ScanBudget: 1})
	require.NoError(t, err)
	require.Len(t, page.Logs, 10)
	require.True(t, page.CountSkipped)
	require.EqualValues(t, logs_core.TotalNotCounted, page.Total)

	_, err = q.ExecuteAggregation(context.Background(), projectID, &logs_core.AggregationRequestDTO{
		TimeRange: timeRange, BucketMs: 60_000, ScanBudget: 1})
	require.ErrorIs(t, err, query.ErrScanBudget)

	counted, err := q.ExecuteQuery(
		projectID,
		&logs_core.LogQueryRequestDTO{Query: level, TimeRange: timeRange, Limit: 10},
	)
	require.NoError(t, err)
	require.False(t, counted.CountSkipped, "no budget, as for background work")
	require.Positive(t, counted.Total)
}

// seedUploadedSegments writes six segments of 30 logs (three levels, four
// services, three message templates, minutes apart) marked uploaded, so the
// coordinator dispatches them.
func seedUploadedSegments(t *testing.T, now time.Time) uuid.UUID {
	t.Helper()
	// These segments are marked uploaded: hold the lock the uploader tests
	// take before resetting every uploaded logs segment.
	test_utils.AcquireExclusiveGlobalQueue(t, "logs-uploaded-queue")
	ingester, err := logs_ingester.NewLogIngester(t.TempDir(), segments.WithMaxRows(1_000_000))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })
	projectID := uuid.New()
	levels := []logs_core.LogLevel{logs_core.LogLevelInfo, logs_core.LogLevelInfo, logs_core.LogLevelError}
	templates := []string{"user %d signed in", "payment %d failed", "cache miss for key %d"}
	for segment := range 6 {
		var batch []*logs_core.LogItem
		for i := range 30 {
			n := segment*30 + i
			batch = append(batch, &logs_core.LogItem{
				ID:        uuid.New(),
				ProjectID: projectID,
				Timestamp: now.Add(-time.Duration(n) * 17 * time.Second),
				Level:     levels[(n+segment)%len(levels)],
				Message:   fmt.Sprintf(templates[(n*segment+i)%len(templates)], n),
				Fields: map[string]any{
					"service":                      fmt.Sprintf("svc-%d", (n*7+segment)%4),
					fmt.Sprintf("key_%d", segment): n,
				},
			})
		}
		require.NoError(t, ingester.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: batch}))
		_, err = ingester.FlushAll()
		require.NoError(t, err)
	}
	repo := &segments.SegmentRepository{}
	records, err := repo.ListByProject(projectID, "logs")
	require.NoError(t, err)
	require.Len(t, records, 6)
	for _, record := range records {
		require.NoError(t, repo.MarkUploaded(record.ID, "logs/"+record.ID.String()+".parquet"))
	}
	return projectID
}

// The quota cutoff's slim rows sort and page exactly like full ones.
func Test_ExecuteQuery_TimestampsOnly_PagesLikeFullRows(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	projectID := seedUploadedSegments(t, now)
	q, err := NewLogQuerier(WithQueryDir(filepath.Join(t.TempDir(), "query")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = q.Close() })
	request := func(slim bool) *logs_core.LogQueryRequestDTO {
		return &logs_core.LogQueryRequestDTO{Limit: 2, Offset: 57, SortOrder: "asc", TimestampsOnly: slim}
	}

	full, err := q.ExecuteQuery(projectID, request(false))
	require.NoError(t, err)
	slim, err := q.ExecuteQuery(projectID, request(true))
	require.NoError(t, err)

	require.Len(t, slim.Logs, 2)
	for i := range full.Logs {
		require.Equal(t, full.Logs[i].ID, slim.Logs[i].ID)
		require.Equal(t, full.Logs[i].Timestamp, slim.Logs[i].Timestamp)
		require.Empty(t, slim.Logs[i].Message)
	}
}

func Test_NewLogQuerier_WithMemoryLimit_CapsItsDuckDB(t *testing.T) {
	q, err := NewLogQuerier(WithQueryDir(filepath.Join(t.TempDir(), "query")), WithMemoryLimit("1GB"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = q.Close() })

	var limit string
	require.NoError(t, q.db.QueryRow("SELECT current_setting('memory_limit')").Scan(&limit))

	require.Contains(t, limit, "953.6 MiB")
}

// A capped querier's SQL sandbox gets the cap too, set before the sandbox
// turns off external access.
func Test_ExecuteSQLQuery_WithMemoryLimit_RunsInACappedSandbox(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	projectID := seedUploadedSegments(t, now)
	q, err := NewLogQuerier(WithQueryDir(filepath.Join(t.TempDir(), "query")), WithMemoryLimit("512MB"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = q.Close() })
	from, to := now.Add(-time.Hour), now.Add(time.Hour)

	response, err := q.ExecuteSQLQuery(projectID, &logs_core.ExecuteSQLQueryRequestDTO{
		SQL:       "SELECT current_setting('memory_limit') AS mem, count(*) AS n FROM logs",
		TimeRange: &logs_core.TimeRangeDTO{From: &from, To: &to},
	})

	require.NoError(t, err)
	require.Len(t, response.Rows, 1)
	require.Equal(t, "488.2 MiB", response.Rows[0][0])
	require.EqualValues(t, 180, response.Rows[0][1])
}
