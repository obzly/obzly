package telemetry_test

import (
	"database/sql"
	"testing"

	"obzly/internal/telemetry"

	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/stretchr/testify/require"
)

func Test_DuckDBCollector_ReportsRegisteredHandlesUntilUnregistered(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE t AS SELECT range AS i, repeat('x', 100) AS s FROM range(100000)`)
	require.NoError(t, err)

	telemetry.RegisterDuckDB("test_handle", db, 256<<20)
	require.Positive(t, duckDBMemory(t, "test_handle"), "an in-memory table uses DuckDB memory")
	require.Equal(t, float64(256<<20), duckDBGauge(t, "obzly_duckdb_memory_limit_bytes", "test_handle"))

	telemetry.UnregisterDuckDB(db)
	require.Equal(t, -1.0, duckDBMemory(t, "test_handle"), "unregistered handles are not reported")
	require.Equal(t, -1.0, duckDBGauge(t, "obzly_duckdb_memory_limit_bytes", "test_handle"))
}

func Test_DuckDBCollector_HandleThatCantAnswer_ReportsItsLastReading(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE t AS SELECT range AS i, repeat('x', 100) AS s FROM range(100000)`)
	require.NoError(t, err)
	telemetry.RegisterDuckDB("test_busy_handle", db, 0)
	defer telemetry.UnregisterDuckDB(db)
	before := duckDBMemory(t, "test_busy_handle")
	require.Positive(t, before)

	require.NoError(t, db.Close()) // every query now fails, as one past the scrape timeout does

	require.Equal(t, before, duckDBMemory(t, "test_busy_handle"), "the series stays, at its last reading")
}

func Test_DuckDBHandles_GroupsRegisteredHandlesByKindAndLimit(t *testing.T) {
	var dbs []*sql.DB
	for _, limit := range []int64{64 << 20, 64 << 20, 128 << 20} {
		db, err := sql.Open("duckdb", "")
		require.NoError(t, err)
		defer db.Close()
		telemetry.RegisterDuckDB("test_kind", db, limit)
		dbs = append(dbs, db)
	}
	defer func() {
		for _, db := range dbs {
			telemetry.UnregisterDuckDB(db)
		}
	}()

	var kinds []telemetry.DuckDBHandleKind
	for _, kind := range telemetry.DuckDBHandles() {
		if kind.Handle == "test_kind" {
			kinds = append(kinds, kind)
		}
	}
	require.Equal(t, []telemetry.DuckDBHandleKind{
		{Handle: "test_kind", Count: 2, LimitBytes: 64 << 20},
		{Handle: "test_kind", Count: 1, LimitBytes: 128 << 20},
	}, kinds)
}

// duckDBMemory returns obzly_duckdb_memory_bytes for handle, or -1 when the
// series is absent.
func duckDBMemory(t *testing.T, handle string) float64 {
	t.Helper()
	return duckDBGauge(t, "obzly_duckdb_memory_bytes", handle)
}

// duckDBGauge returns the metric's value for handle, or -1 when absent.
func duckDBGauge(t *testing.T, metric, handle string) float64 {
	t.Helper()
	families, err := telemetry.Registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != metric {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "handle" && label.GetValue() == handle {
					return metric.GetGauge().GetValue()
				}
			}
		}
	}
	return -1
}
