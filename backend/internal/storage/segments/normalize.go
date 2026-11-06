package segments

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"obzly/internal/util/attrnames"

	duckdb "github.com/marcboeker/go-duckdb/v2"
)

// Segment schema versions. v1 stores OTel names as sent (dotted keys,
// "resource." prefix, uppercase levels); v2 stores attrnames-normalized names
// and lowercase levels; v3 logs add promoted-field columns, which v2 files
// read as NULL, so 2 and 3 merge freely. Queries read v1 through
// LegacyReplacements until the data migration rewrites them.
const (
	SchemaVersionLegacy  = 1
	SchemaVersionNamed   = 2
	CurrentSchemaVersion = 3
)

// ResolvedFile is a readable segment file and whether it predates
// normalized names.
type ResolvedFile struct {
	Path   string
	Legacy bool
	// MinTime/MaxTime: the rows' time range, when known (tail files).
	MinTime, MaxTime time.Time
}

// LegacyReplacements are the per-stream column rewrites that make a
// version 1 file read like a version 2 one (and that the migration applies
// when rewriting it).
var LegacyReplacements = map[string]string{
	"logs":    "obzly_norm_fields(fields) AS fields, lower(level) AS level",
	"metrics": "obzly_norm_name(metric_name) AS metric_name, obzly_norm_fields(labels) AS labels",
	"spans": "obzly_norm_fields(attributes) AS attributes, " +
		"obzly_norm_fields(resource_attributes) AS resource_attributes, obzly_norm_events(events) AS events",
}

// SourceExpr is the FROM expression for a set of files: current files are
// read directly, legacy ones through LegacyReplacements, unioned by name.
func SourceExpr(stream string, files []ResolvedFile) (string, []any) {
	var current, legacy []string
	for _, file := range files {
		if file.Legacy {
			legacy = append(legacy, file.Path)
		} else {
			current = append(current, file.Path)
		}
	}
	if anchor := anchorFor(stream); anchor != "" && len(files) > 0 {
		current = append(current, anchor)
	}
	if len(legacy) == 0 {
		return ParquetFilesExpr(current)
	}

	legacyExpr, legacyArgs := ParquetFilesExpr(legacy)
	wrapped := "(SELECT * REPLACE (" + LegacyReplacements[stream] + ") FROM " + legacyExpr + ")"
	if len(current) == 0 {
		return wrapped, legacyArgs
	}
	currentExpr, currentArgs := ParquetFilesExpr(current)
	return "(SELECT * FROM " + currentExpr + " UNION ALL BY NAME SELECT * FROM " + wrapped + ")",
		append(currentArgs, legacyArgs...)
}

// RegisterNormalizationFunctions adds obzly_norm_fields, obzly_norm_events,
// obzly_norm_name and obzly_series_id to a DuckDB database (visible to all
// its connections).
func RegisterNormalizationFunctions(db *sql.DB) error {
	conn, err := db.Conn(context.Background())
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	functions := map[string]func(string) string{
		"obzly_norm_fields": normalizeJSONObject,
		"obzly_norm_events": normalizeJSONEvents,
		"obzly_norm_name":   attrnames.NormalizeMetricName,
	}
	for name, fn := range functions {
		if err := duckdb.RegisterScalarUDF(conn, name, &stringFunction{fn}); err != nil {
			return fmt.Errorf("failed to register %s: %w", name, err)
		}
	}
	if err := duckdb.RegisterScalarUDF(conn, "obzly_series_id", seriesIDFunction{}); err != nil {
		return fmt.Errorf("failed to register obzly_series_id: %w", err)
	}
	return nil
}

type stringFunction struct {
	fn func(string) string
}

func (f *stringFunction) Config() duckdb.ScalarFuncConfig {
	varchar, _ := duckdb.NewTypeInfo(duckdb.TYPE_VARCHAR)
	return duckdb.ScalarFuncConfig{InputTypeInfos: []duckdb.TypeInfo{varchar}, ResultTypeInfo: varchar}
}

func (f *stringFunction) Executor() duckdb.ScalarFuncExecutor {
	return duckdb.ScalarFuncExecutor{RowExecutor: func(values []driver.Value) (any, error) {
		text, ok := values[0].(string)
		if !ok {
			return values[0], nil
		}
		return f.fn(text), nil
	}}
}

// normalizeJSONObject renames a JSON object's keys; anything else is kept.
func normalizeJSONObject(text string) string {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") {
		return text
	}
	var fields map[string]any
	if json.Unmarshal([]byte(trimmed), &fields) != nil {
		return text
	}
	out, err := json.Marshal(attrnames.NormalizeMap(fields))
	if err != nil {
		return text
	}
	return string(out)
}

// normalizeJSONEvents renames the attribute keys of each span event.
func normalizeJSONEvents(text string) string {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "[") {
		return text
	}
	var events []map[string]any
	if json.Unmarshal([]byte(trimmed), &events) != nil {
		return text
	}
	for _, event := range events {
		if attributes, ok := event["attributes"].(map[string]any); ok {
			event["attributes"] = attrnames.NormalizeMap(attributes)
		}
	}
	out, err := json.Marshal(events)
	if err != nil {
		return text
	}
	return string(out)
}
