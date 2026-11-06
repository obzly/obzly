package segments

import (
	"database/sql"
	"fmt"
	"strings"
)

// parquetCopyOptions is how every segment file is written: ZSTD, row groups of
// 262,144 rows (enough for min/max pruning after compaction) and V2 data
// pages, about 48% smaller than DuckDB's defaults on logs. Old Snappy files
// stay readable. format_test.go fails if a go-duckdb bump ignores them.
const parquetCopyOptions = "FORMAT PARQUET, COMPRESSION ZSTD, COMPRESSION_LEVEL 3, " +
	"ROW_GROUP_SIZE 262144, PARQUET_VERSION V2"

// compactionCopyOptions also bounds the row group buffer for wide rows
// (262,144 rows of ~1 KB logs is ~256 MB). DuckDB rejects
// ROW_GROUP_SIZE_BYTES unless preserve_insertion_order is off, which it is
// only on the compactor's handle.
const compactionCopyOptions = parquetCopyOptions + ", ROW_GROUP_SIZE_BYTES '64MB'"

// CompactionSortColumns orders the rows of every compacted file, per stream,
// so row-group min/max stats can skip data. Nothing relies on file order -
// queries always ORDER BY. project_id is constant per file, so not a key.
// Rows are sorted within hour buckets of CompactionTimeColumn so a row group
// covers about an hour; sorting by service first made each row group of a
// multi-day file span all of it.
var CompactionSortColumns = map[string][]string{
	"logs":    {"service_name", "timestamp"},
	"ai_logs": {"service_name", "timestamp"},
	"spans":   {"service_name", "start_time"},
	// Name plus canonical label JSON keeps each series together.
	"metrics": {"metric_name", "series_id", "timestamp"},
}

// DeduplicatingMerges are streams whose merges collapse rows instead of
// keeping them all (any level): a metrics_series row per series, with the
// earliest first_seen and latest last_seen of its inputs. %s is the source.
var DeduplicatingMerges = map[string]string{
	"metrics_series": "SELECT project_id, series_id, any_value(metric_name) AS metric_name, " +
		"any_value(labels) AS labels, max_by(type, last_seen) AS type, min(first_seen) AS first_seen, " +
		"max(last_seen) AS last_seen FROM %s GROUP BY project_id, series_id ORDER BY metric_name, series_id",
}

// CompactionTimeColumn is each stream's row time, the first sort key in
// hour buckets.
var CompactionTimeColumn = map[string]string{
	"logs":    "timestamp",
	"ai_logs": "timestamp",
	"spans":   "start_time",
	"metrics": "timestamp",
}

// copyToParquetSQL builds the COPY statement that writes query's rows to
// path with parquetCopyOptions.
func copyToParquetSQL(query, path string) string {
	return fmt.Sprintf(`COPY (%s) TO '%s' (%s)`, query, path, parquetCopyOptions)
}

// compactionCopySQL is copyToParquetSQL for the compactor's handle.
func compactionCopySQL(query, path string) string {
	return fmt.Sprintf(`COPY (%s) TO '%s' (%s)`, query, path, compactionCopyOptions)
}

// sortedSelect returns a SELECT over fromExpr ordered by the sort columns
// fromExpr has: older segments lack some, and ORDER BY a missing column fails.
// fillSeriesID computes series_id for current-version metrics rows written
// before the column existed (legacy names aren't normalized, so their hash
// wouldn't match).
func sortedSelect(db *sql.DB, stream, fromExpr string, args []any, fillSeriesID bool) (string, error) {
	query := "SELECT * FROM " + fromExpr
	wanted := CompactionSortColumns[stream]
	if len(wanted) == 0 {
		return query, nil
	}

	present, err := columnsOf(db, fromExpr, args)
	if err != nil {
		return "", err
	}
	if fillSeriesID && stream == "metrics" && present["metric_name"] && present["labels"] {
		const computed = "obzly_series_id(metric_name, labels)"
		if present["series_id"] {
			query = "SELECT * REPLACE (coalesce(series_id, " + computed + ") AS series_id) FROM " + fromExpr
		} else {
			query = "SELECT *, " + computed + " AS series_id FROM " + fromExpr
		}
		present["series_id"] = true
	}

	var orderBy []string
	if timeColumn := CompactionTimeColumn[stream]; present[timeColumn] {
		orderBy = append(orderBy, `date_trunc('hour', "`+timeColumn+`")`)
	}
	for _, column := range wanted {
		if present[column] {
			orderBy = append(orderBy, `"`+column+`"`)
		}
	}
	if len(orderBy) == 0 {
		return query, nil
	}
	return query + " ORDER BY " + strings.Join(orderBy, ", "), nil
}
