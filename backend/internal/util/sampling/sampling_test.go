package sampling

import (
	"database/sql"
	"fmt"
	"testing"

	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/stretchr/testify/require"
)

// Go and DuckDB put every id in the same bucket, so log head sampling and
// trace tail sampling agree on a trace.
func Test_Bucket_MatchesDuckDB(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	for i := range 200 {
		id := fmt.Sprintf("%032x", uint64(i)*0x9e3779b97f4a7c15)
		var want int64
		require.NoError(t, db.QueryRow("SELECT ("+SQLBucket("?")+")::BIGINT", id).Scan(&want))
		require.Equal(t, want, Bucket(id), id)
	}
}
