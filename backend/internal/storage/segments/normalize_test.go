package segments

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_SourceExpr_ReadsLegacyFilesWithNormalizedNames(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	require.NoError(t, RegisterNormalizationFunctions(db))

	dir := t.TempDir()
	legacy := filepath.Join(dir, "legacy.parquet")
	current := filepath.Join(dir, "current.parquet")
	_, err = db.Exec(
		`COPY (SELECT 'ERROR' AS level, '{"resource.k8s.pod.name":"p1","log.iostream":"stderr"}' AS fields) TO '` + legacy + `' (FORMAT PARQUET)`,
	)
	require.NoError(t, err)
	_, err = db.Exec(
		`COPY (SELECT 'info' AS level, '{"k8s_pod_name":"p2"}' AS fields) TO '` + current + `' (FORMAT PARQUET)`,
	)
	require.NoError(t, err)

	fromExpr, args := SourceExpr("logs", []ResolvedFile{{Path: current}, {Path: legacy, Legacy: true}})
	rows, err := db.Query(`SELECT level, json_extract_string(fields, '$.k8s_pod_name') AS pod, fields FROM `+
		fromExpr+` ORDER BY pod`, args...)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	type row struct{ level, pod, fields string }
	var got []row
	for rows.Next() {
		var r row
		require.NoError(t, rows.Scan(&r.level, &r.pod, &r.fields))
		got = append(got, r)
	}
	require.Len(t, got, 2)
	assert.Equal(t, "error", got[0].level, "legacy levels read lowercase")
	assert.Equal(t, "p1", got[0].pod, "legacy resource.k8s.pod.name reads as k8s_pod_name")
	var fields map[string]string
	require.NoError(t, json.Unmarshal([]byte(got[0].fields), &fields))
	assert.Equal(t, map[string]string{"k8s_pod_name": "p1", "log_iostream": "stderr"}, fields)
	assert.Equal(t, row{"info", "p2", `{"k8s_pod_name":"p2"}`}, got[1])
}

func Test_NormalizeJSONEvents(t *testing.T) {
	assert.JSONEq(t, `[{"name":"e","attributes":{"http_method":"GET"}}]`,
		normalizeJSONEvents(`[{"name":"e","attributes":{"http.method":"GET"}}]`))
	assert.Equal(t, "not json", normalizeJSONObject("not json"))
}
