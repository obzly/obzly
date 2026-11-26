package logs_querier

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/storage/segments"

	duckdb "github.com/marcboeker/go-duckdb/v2"
	"github.com/stretchr/testify/require"
)

// A sandbox opened while the replica is short of memory gets what the gate
// has free, not its usual cap, and gives it back when it closes.
func Test_LogsSandbox_GateShortOfMemory_GetsWhatIsFreeAndGivesItBack(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	projectID := seedUploadedSegments(t, now)
	q, err := NewLogQuerier(WithQueryDir(filepath.Join(t.TempDir(), "query")), WithMemoryLimit("512MB"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = q.Close() })
	gate := segments.NewMemoryGate(1_000_000_000, 256<<20, time.Second)
	gate.SetInUse(600_000_000)
	segments.SetQueryMemoryGate(gate)
	t.Cleanup(func() { segments.SetQueryMemoryGate(nil) })

	sandbox, err := q.openLogsSandbox(context.Background(), projectID, now.Add(-time.Hour), now.Add(time.Hour), tableShape{}, 0)

	require.NoError(t, err)
	var memory string
	require.NoError(t, sandbox.db.QueryRow("SELECT current_setting('memory_limit')").Scan(&memory))
	require.Equal(t, "381.4 MiB", memory, "400 MB free of the gate, not the 512 MB cap")
	require.EqualValues(t, 400_000_000, gate.Admitted())
	sandbox.close()
	require.Zero(t, gate.Admitted())
}

func Test_UserSQLError_DuckDBOutOfMemory_IsErrSQLOutOfMemory(t *testing.T) {
	err := userSQLError(&duckdb.Error{Type: duckdb.ErrorTypeOutOfMemory, Msg: "Out of Memory Error: failed to allocate"})

	require.True(t, errors.Is(err, logs_core.ErrSQLOutOfMemory))
}
