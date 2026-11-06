package logs_querier_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	logs_ingester "obzly/internal/features/logs/ingester"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The ingester stores each log's pattern hash; segments written before
// that column existed compute it at read time - filters, pattern counts
// and returned rows agree across both.
func Test_PatternHash_StoredAtIngest_AgreesWithOlderSegmentsWithoutIt(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	logins := func(offset time.Duration, users ...string) []*logs_core.LogItem {
		var items []*logs_core.LogItem
		for _, user := range users {
			items = append(items, &logs_core.LogItem{Timestamp: now.Add(offset), Level: logs_core.LogLevelInfo,
				Message: "user " + user + " logged in"})
		}
		return items
	}
	projectID, querier := seedProject(t, logins(-time.Minute, "1", "2"))
	stripPatternHash(t, projectID)

	ingester, err := logs_ingester.NewLogIngester(t.TempDir(), segments.WithMaxRows(1_000_000))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })
	newer := logins(0, "3")
	newer = append(newer, &logs_core.LogItem{Timestamp: now, Level: logs_core.LogLevelInfo, Message: "cache flushed"})
	for _, item := range newer {
		item.ProjectID, item.ID = projectID, uuid.New()
	}
	require.NoError(t, ingester.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: newer}))
	_, err = ingester.FlushAll()
	require.NoError(t, err)
	requirePatternHashStored(t, projectID, 2)

	hash := logs_core.PatternHash("user 1 logged in")
	from, to := now.Add(-time.Hour), now.Add(time.Hour)

	request := queryRequest(condition(logs_core.PatternHashField, logs_core.ConditionOperatorEquals, hash), from, to)
	request.Limit = 10
	matched, err := querier.ExecuteQuery(projectID, request)
	require.NoError(t, err)
	assert.Len(t, matched.Logs, 3, "both segments")
	for _, log := range matched.Logs {
		assert.Equal(t, hash, log.PatternHash)
	}

	patterns, err := querier.ExecutePatterns(projectID, &logs_core.PatternsRequestDTO{
		TimeRange: &logs_core.TimeRangeDTO{From: &from, To: &to},
	})
	require.NoError(t, err)
	require.Len(t, patterns.Patterns, 2)
	assert.Equal(t, logs_core.LogPatternDTO{Hash: hash, Pattern: "user <num> logged in",
		Sample: patterns.Patterns[0].Sample, Count: 3}, patterns.Patterns[0])

	grouped := aggregationRequest(nil, from, to)
	grouped.GroupField = logs_core.PatternHashField
	buckets, err := querier.ExecuteAggregation(context.Background(), projectID, grouped)
	require.NoError(t, err)
	require.Len(t, buckets.Buckets, 2)
	assert.Equal(t, hash, buckets.Buckets[0].GroupKey)
	assert.Equal(t, int64(3), buckets.Buckets[0].Count)
}

func Test_PatternHash_OfALongMessage_IsTheHashOfItsFirstCharacters(t *testing.T) {
	head := strings.Repeat("ü", logs_core.PatternMaxChars-4) + " id "
	assert.Equal(t, logs_core.PatternHash(head), logs_core.PatternHash(head+"12345 and much more"))
	assert.NotEqual(t, logs_core.PatternHash(head[:len(head)-4]), logs_core.PatternHash(head))
}

// stripPatternHash rewrites projectID's segments without the pattern_hash
// column - as segments written before it existed.
func stripPatternHash(t *testing.T, projectID uuid.UUID) {
	t.Helper()
	records, err := (&segments.SegmentRepository{}).ListByProject(projectID, "logs")
	require.NoError(t, err)
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	for _, record := range records {
		stripped := record.Path + ".old"
		_, err := db.Exec("COPY (SELECT * EXCLUDE (pattern_hash) FROM read_parquet(?)) TO '"+stripped+
			"' (FORMAT PARQUET)", record.Path)
		require.NoError(t, err)
		require.NoError(t, os.Rename(stripped, record.Path))
	}
}

// requirePatternHashStored checks the project's newest segment holds every
// row's hash in its pattern_hash column.
func requirePatternHashStored(t *testing.T, projectID uuid.UUID, rows int) {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	result, err := db.Query("SELECT message, pattern_hash FROM read_parquet(?)", newestSegment(t, projectID).Path)
	require.NoError(t, err)
	defer func() { _ = result.Close() }()
	count := 0
	for result.Next() {
		var message, hash string
		require.NoError(t, result.Scan(&message, &hash))
		assert.Equal(t, logs_core.PatternHash(message), hash, message)
		count++
	}
	assert.Equal(t, rows, count)
}
