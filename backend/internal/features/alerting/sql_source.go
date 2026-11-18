package alerting

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"obzly/internal/util/attrnames"
)

const (
	// maxSQLSeries: rows (series) a logs_sql rule may return, after keeping
	// each label set's latest row.
	maxSQLSeries = 100
	// maxSQLLabelLength bounds one label value taken from a SQL column.
	maxSQLLabelLength = 200
	// sqlValueColumn names the column a logs_sql rule compares, when present.
	sqlValueColumn = "value"
)

// sqlSamples turns a logs_sql rule's result into series: one per row, its
// value the "value" column (else the last numeric column) and its labels
// the other columns as strings. With a time column only each label set's
// latest row counts, so a per-minute GROUP BY compares the newest minute.
func sqlSamples(columns []string, rows [][]any) ([]sample, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	valueColumn := sqlValueColumnIndex(columns, rows)
	if valueColumn < 0 {
		return nil, fmt.Errorf("the query must return a number: a column named %s, or a numeric column",
			sqlValueColumn)
	}
	timeColumn := -1
	for i := range columns {
		if i != valueColumn && everyNonNull(rows, i, isTimeValue) {
			timeColumn = i
			break
		}
	}

	type latest struct {
		sample sample
		at     time.Time
	}
	var order []string
	series := map[string]*latest{}
	for _, row := range rows {
		value, ok := sqlNumber(row[valueColumn], true)
		if !ok {
			continue
		}
		labels := Labels{}
		for i, name := range columns {
			if i == valueColumn || i == timeColumn {
				continue
			}
			labels[sqlLabelName(name, i)] = truncate(sqlLabelValue(row[i]), maxSQLLabelLength)
		}
		var at time.Time
		if timeColumn >= 0 {
			at, _ = sqlTime(row[timeColumn])
		}
		key := fingerprint(labels)
		existing, seen := series[key]
		if !seen {
			order = append(order, key)
			series[key] = &latest{sample: sample{labels: labels, value: value}, at: at}
			continue
		}
		if timeColumn >= 0 && at.After(existing.at) {
			existing.sample.value, existing.at = value, at
		}
	}

	if len(order) > maxSQLSeries {
		return nil, fmt.Errorf("the query returns %d series; a SQL rule may watch at most %d "+
			"(ORDER BY the value and LIMIT it)", len(order), maxSQLSeries)
	}
	samples := make([]sample, 0, len(order))
	for _, key := range order {
		samples = append(samples, series[key].sample)
	}
	return samples, nil
}

// sqlValueColumnIndex is the column named "value", else the last column
// whose every value is a number; -1 when there is none.
func sqlValueColumnIndex(columns []string, rows [][]any) int {
	for i, name := range columns {
		if strings.EqualFold(name, sqlValueColumn) {
			return i
		}
	}
	for i := len(columns) - 1; i >= 0; i-- {
		if everyNonNull(rows, i, func(v any) bool { _, ok := sqlNumber(v, false); return ok }) {
			return i
		}
	}
	return -1
}

// everyNonNull reports whether column has a value and test holds for all
// of them (NULLs are skipped).
func everyNonNull(rows [][]any, column int, test func(any) bool) bool {
	seen := false
	for _, row := range rows {
		if column >= len(row) || row[column] == nil {
			continue
		}
		if !test(row[column]) {
			return false
		}
		seen = true
	}
	return seen
}

// sqlNumber reads a DuckDB number. Numeric text counts only for the column
// named "value": elsewhere "200" or "404" is a label, not a measurement.
func sqlNumber(value any, allowText bool) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case *big.Int:
		// HUGEINT, e.g. sum() of an integer column.
		if typed == nil {
			return 0, false
		}
		f, _ := new(big.Float).SetInt(typed).Float64()
		return f, true
	case interface{ Float64() float64 }:
		// duckdb.Decimal
		return typed.Float64(), true
	case string:
		if !allowText {
			return 0, false
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return f, err == nil
	}
	return 0, false
}

// isTimeValue: SQL mode returns TIMESTAMP and DATE values as RFC 3339 text.
func isTimeValue(value any) bool {
	_, ok := sqlTime(value)
	return ok
}

func sqlTime(value any) (time.Time, bool) {
	switch typed := value.(type) {
	case time.Time:
		return typed, true
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, typed)
		return parsed, err == nil
	}
	return time.Time{}, false
}

// sqlLabelName makes a column name a valid label name ("count_star()" ->
// "count_star__"); an unnamed column becomes column_<n>.
func sqlLabelName(column string, index int) string {
	name := attrnames.Normalize(column)
	if name == "" {
		return fmt.Sprintf("column_%d", index+1)
	}
	if name[0] >= '0' && name[0] <= '9' {
		return "_" + name
	}
	return name
}

func sqlLabelValue(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}
