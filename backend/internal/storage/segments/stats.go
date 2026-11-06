package segments

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// maxStatsValues caps each distinct-value list. A segment with more
// distinct values than this records the list as unknown (nil), which only
// means queries cannot skip it.
const maxStatsValues = 256

// SegmentStats is what a segment contains, stored in stream_segments.stats
// so queries can skip whole files. A nil list means unknown (predates stats,
// a merged input lacked them, or over maxStatsValues) and never prunes.
// Values exclude NULLs: equality and IN never match NULL.
type SegmentStats struct {
	// Services: distinct service_name (logs, ai_logs, spans).
	Services []string `json:"services"`
	// Levels: distinct lower(level) (logs, ai_logs) - levels are matched
	// case-insensitively.
	Levels []string `json:"levels"`
	// MetricNames: distinct metric_name (metrics).
	MetricNames []string `json:"metric_names"`
	// Source: for a derived segment (metrics_5m/metrics_1h rollups), the
	// id of the segment it was built from.
	Source string `json:"source,omitempty"`
	// Aged: the rollup's source was retired by retention, so it is the only
	// data left for its time.
	Aged bool `json:"aged,omitempty"`
	// PromotedFull (logs): promoted-field bits set on every row, so queries can
	// read those fields from their columns alone (ResolvePromotedFields).
	PromotedFull int64 `json:"promoted_full,omitempty"`
	// TokenBloom (logs, ai_logs): a token filter was uploaded next to the
	// file (tokenbloom.go), so an ID search can skip it.
	TokenBloom bool `json:"token_bloom,omitempty"`
}

// statsColumn is one SegmentStats list and the SQL expression it collects.
type statsColumn struct {
	column string
	expr   string
	field  func(*SegmentStats) *[]string
}

var (
	servicesStat = statsColumn{"service_name", "service_name", func(s *SegmentStats) *[]string { return &s.Services }}
	levelsStat   = statsColumn{"level", "lower(level)", func(s *SegmentStats) *[]string { return &s.Levels }}
	metricsStat  = statsColumn{"metric_name", "metric_name", func(s *SegmentStats) *[]string { return &s.MetricNames }}
)

// statsColumnsByStream is which lists each stream records.
var statsColumnsByStream = map[string][]statsColumn{
	"logs":           {servicesStat, levelsStat},
	"ai_logs":        {servicesStat, levelsStat},
	"spans":          {servicesStat},
	"metrics":        {metricsStat},
	"metrics_series": {metricsStat},
	"metrics_5m":     {metricsStat},
	"metrics_1h":     {metricsStat},
}

// Value stores SegmentStats as JSONB.
func (s SegmentStats) Value() (driver.Value, error) {
	encoded, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return string(encoded), nil
}

// Scan reads SegmentStats from JSONB.
func (s *SegmentStats) Scan(value any) error {
	var raw []byte
	switch v := value.(type) {
	case nil:
		return nil
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("unsupported stats type %T", value)
	}
	return json.Unmarshal(raw, s)
}

// computeStats collects the stream's stats lists from source (a table name
// or a read_parquet expression), using only the columns it has. Returns nil
// for a stream that records no stats.
func computeStats(db *sql.DB, stream, source string, args []any) (*SegmentStats, error) {
	wanted := statsColumnsByStream[stream]
	if len(wanted) == 0 {
		return nil, nil
	}
	present, err := columnsOf(db, source, args)
	if err != nil {
		return nil, err
	}

	stats := &SegmentStats{}
	for _, stat := range wanted {
		if !present[stat.column] {
			continue
		}
		values, err := distinctValues(db, stat.expr, source, args)
		if err != nil {
			return nil, err
		}
		*stat.field(stats) = values
	}
	if present["promoted_mask"] {
		if err := db.QueryRow("SELECT coalesce(bit_and(coalesce(promoted_mask, 0)), 0) FROM "+source, args...).
			Scan(&stats.PromotedFull); err != nil {
			return nil, fmt.Errorf("failed to collect promoted stats: %w", err)
		}
	}
	return stats, nil
}

// mergeStats is the stats of a file holding every input's rows: the union
// of each list, unknown if any input's is unknown or the union is too big.
func mergeStats(stream string, inputs []*SegmentStats) *SegmentStats {
	wanted := statsColumnsByStream[stream]
	if len(wanted) == 0 {
		return nil
	}

	merged := &SegmentStats{}
	for _, stat := range wanted {
		union := []string{}
		for _, input := range inputs {
			if input == nil || *stat.field(input) == nil {
				union = nil
				break
			}
			for _, value := range *stat.field(input) {
				if !slices.Contains(union, value) {
					union = append(union, value)
				}
			}
		}
		if len(union) > maxStatsValues {
			union = nil
		}
		if union != nil {
			slices.Sort(union)
		}
		*stat.field(merged) = union
	}
	merged.PromotedFull = -1
	for _, input := range inputs {
		if input == nil {
			merged.PromotedFull = 0
			break
		}
		merged.PromotedFull &= input.PromotedFull
	}
	if merged.PromotedFull == -1 {
		merged.PromotedFull = 0
	}
	return merged
}

// distinctValues returns expr's distinct non-NULL values in source, sorted,
// or nil when there are more than maxStatsValues of them.
func distinctValues(db *sql.DB, expr, source string, args []any) ([]string, error) {
	query := fmt.Sprintf(
		`SELECT DISTINCT CAST(%s AS VARCHAR) AS v FROM %s WHERE %s IS NOT NULL ORDER BY v LIMIT %d`,
		expr, source, expr, maxStatsValues+1,
	)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to collect segment stats: %w", err)
	}
	defer func() { _ = rows.Close() }()

	values := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(values) > maxStatsValues {
		return nil, nil
	}
	return values, nil
}

// columnsOf returns the column names of source.
func columnsOf(db *sql.DB, source string, args []any) (map[string]bool, error) {
	rows, err := db.Query("DESCRIBE SELECT * FROM "+source, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to describe segment columns: %w", err)
	}
	defer func() { _ = rows.Close() }()

	columnNames, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	if len(columnNames) == 0 || !strings.EqualFold(columnNames[0], "column_name") {
		return nil, errors.New("unexpected DESCRIBE output")
	}

	present := map[string]bool{}
	for rows.Next() {
		values := make([]any, len(columnNames))
		pointers := make([]any, len(columnNames))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		if name, ok := values[0].(string); ok {
			present[name] = true
		}
	}
	return present, rows.Err()
}
