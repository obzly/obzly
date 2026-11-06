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

	telemetry.RegisterDuckDB("test_handle", db)
	require.Positive(t, duckDBMemory(t, "test_handle"), "an in-memory table uses DuckDB memory")

	telemetry.UnregisterDuckDB(db)
	require.Equal(t, -1.0, duckDBMemory(t, "test_handle"), "unregistered handles are not reported")
}

// duckDBMemory returns obzly_duckdb_memory_bytes for handle, or -1 when the
// series is absent.
func duckDBMemory(t *testing.T, handle string) float64 {
	t.Helper()
	families, err := telemetry.Registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != "obzly_duckdb_memory_bytes" {
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
