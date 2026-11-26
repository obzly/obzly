package segments_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func Test_IsIDToken(t *testing.T) {
	for term, want := range map[string]bool{
		"6ac2e65bcf50f676fdd207d8":             true,
		"123456789012":                         true,
		"8e74b59e-31a7-4e60-81f3-f858eb4655fc": true,
		"f858eb4655fc":                         true,
		"Authentication":                       false, // no digit
		"abc123":                               false, // too short
		"purchaseId=6ac2e65bcf50f676fdd207d8":  false, // two tokens
		"8e74b59e-31a7":                        false,
	} {
		require.Equal(t, want, segments.IsIDToken(term), term)
	}
}

// A filter built from a file says yes to every ID-like token in it, no to
// one that isn't there and to a fragment of a longer token; SkipByTokens
// drops a segment without the token and keeps one without a filter.
func Test_TokenBloom_BuildAndSkip(t *testing.T) {
	dir := t.TempDir()
	path := writeMessages(t, dir, "with.parquet",
		"GET /trans/payout?purchaseId=6ac2e65bcf50f676fdd207d8",
		"request 8e74b59e-31a7-4e60-81f3-f858eb4655fc done",
		"order 123456789012 shipped",
	)
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	built, err := segments.BuildTokenBloom(context.Background(), db, "logs", path)
	require.NoError(t, err)
	require.True(t, built)
	withFilter := &segments.SegmentRecord{ID: uuid.New(), ProjectID: uuid.New(), Stream: "logs", Path: path,
		Stats: &segments.SegmentStats{TokenBloom: true}}
	noFilter := &segments.SegmentRecord{ID: uuid.New(), ProjectID: uuid.New(), Stream: "logs",
		Path: filepath.Join(dir, "other.parquet")}
	skip := func(token string) []*segments.SegmentRecord {
		return segments.SkipByTokens(context.Background(), nil, dir,
			[]*segments.SegmentRecord{withFilter, noFilter}, []string{token})
	}

	for _, present := range []string{
		"6ac2e65bcf50f676fdd207d8", "8e74b59e-31a7-4e60-81f3-f858eb4655fc", "f858eb4655fc", "123456789012",
	} {
		require.Len(t, skip(present), 2, present)
	}
	for _, absent := range []string{"aaaabbbbcccc1234", "e65bcf50f676"} {
		require.Equal(t, []*segments.SegmentRecord{noFilter}, skip(absent), absent)
	}
	built, err = segments.BuildTokenBloom(context.Background(), db, "spans", path)
	require.NoError(t, err)
	require.False(t, built, "spans keep no token filters")
}

// A cold segment holds tens of millions of IDs: listing them DISTINCT ran
// out of the builder's memory on a 1 GB file.
func Test_TokenBloom_FileWithMoreIDsThanMemory_BuildsAFilterMissingNone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cold.parquet")
	gen, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	_, err = gen.Exec(
		fmt.Sprintf(`COPY (SELECT 'request ' || md5(i::VARCHAR) || ' order ord' || (i * 7919)::VARCHAR || 'x'
		AS message FROM range(3000000) t(i)) TO '%s' (FORMAT PARQUET)`, path),
	)
	require.NoError(t, err)
	var joined string
	require.NoError(t, gen.QueryRow(`SELECT string_agg(md5(i::VARCHAR), ',') FROM range(0, 3000000, 300000) t(i)`).
		Scan(&joined))
	sample := strings.Split(joined, ",")
	_ = gen.Close()
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	_, err = segments.ConfigureMemory(db, "96MB", dir)
	require.NoError(t, err)
	require.NoError(t, segments.SetHandleLimits(db, "", 2))

	built, err := segments.BuildTokenBloom(context.Background(), db, "logs", path)

	require.NoError(t, err)
	require.True(t, built)
	record := &segments.SegmentRecord{ID: uuid.New(), ProjectID: uuid.New(), Stream: "logs", Path: path,
		Stats: &segments.SegmentStats{TokenBloom: true}}
	for _, token := range sample {
		require.Len(t, segments.SkipByTokens(context.Background(), nil, dir,
			[]*segments.SegmentRecord{record}, []string{token}), 1, token)
	}
}

func writeMessages(t *testing.T, dir, name string, messages ...string) string {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	path := filepath.Join(dir, name)
	values := ""
	for i, message := range messages {
		if i > 0 {
			values += ", "
		}
		values += fmt.Sprintf("('%s')", message)
	}
	_, err = db.Exec(fmt.Sprintf("COPY (SELECT * FROM (VALUES %s) v(message)) TO '%s' (FORMAT PARQUET)", values, path))
	require.NoError(t, err)
	return path
}
