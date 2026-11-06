package segments

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"obzly/internal/storage"
	"obzly/internal/telemetry"

	"github.com/google/uuid"
)

// PruningHints narrow a query plan to segments that can hold a matching row.
// Each list is the values a matching row's field must be one of: nil means no
// constraint, empty means nothing matches. Only pass filters every matching
// row must satisfy (not trace-level ones).
type PruningHints struct {
	Services    []string
	Levels      []string
	MetricNames []string
}

// hintColumn pairs a PruningHints list with its SegmentStats JSON key.
type hintColumn struct {
	key    string
	values []string
}

// planRow is one candidate segment plus how many segments overlapped the
// window before stats pruning.
type planRow struct {
	SegmentRecord `gorm:"embedded"`
	Candidates    int64 `gorm:"column:candidates"`
}

// Intersect narrows hints by values: a single-valued field must be in both
// sets. nil on either side is no constraint.
func Intersect(current, values []string) []string {
	if current == nil {
		return values
	}
	if values == nil {
		return current
	}
	kept := []string{}
	for _, value := range current {
		if slices.Contains(values, value) {
			kept = append(kept, value)
		}
	}
	return kept
}

// PlanSegments returns a project's stream segments overlapping [from, to]
// whose stats don't rule out hints, oldest first. Pruning runs in the catalog
// query. Segments without stats, unknown lists and legacy (v1) segments are
// always kept. Skips count in obzly_query_segments_pruned_total.
func (r *SegmentRepository) PlanSegments(
	projectID uuid.UUID,
	stream string,
	from, to time.Time,
	hints PruningHints,
) ([]*SegmentRecord, error) {
	columns := hints.columns()
	for _, column := range columns {
		if len(column.values) == 0 {
			// Contradictory filters: no row can match anywhere.
			return []*SegmentRecord{}, nil
		}
	}

	window := "project_id = ? AND stream = ? AND min_time <= ? AND max_time >= ?"
	windowArgs := []any{projectID, stream, to, from}
	if len(columns) == 0 {
		var records []*SegmentRecord
		err := storage.GetDb().Where(window, windowArgs...).Order("min_time ASC").Find(&records).Error
		return records, err
	}

	conditions := make([]string, 0, len(columns))
	hintArgs := make([]any, 0, len(columns))
	for _, column := range columns {
		// One typed placeholder per value: Postgres can't infer the element
		// type of bare parameters inside ARRAY[...].
		placeholders := strings.TrimSuffix(strings.Repeat("?::TEXT,", len(column.values)), ",")
		conditions = append(conditions, fmt.Sprintf(
			"(stats IS NULL OR jsonb_typeof(stats->'%s') IS DISTINCT FROM 'array' "+
				"OR jsonb_exists_any(stats->'%s', ARRAY[%s]))",
			column.key, column.key, placeholders,
		))
		for _, value := range column.values {
			hintArgs = append(hintArgs, value)
		}
	}
	query := fmt.Sprintf(`
		SELECT * FROM (
			SELECT *, count(*) OVER () AS candidates,
			       (schema_version = %d OR (%s)) AS may_match
			FROM stream_segments
			WHERE %s
		) planned
		WHERE may_match
		ORDER BY min_time ASC`,
		SchemaVersionLegacy, strings.Join(conditions, " AND "), window,
	)
	var rows []planRow
	if err := storage.GetDb().Raw(query, append(hintArgs, windowArgs...)...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to plan segments: %w", err)
	}

	records := make([]*SegmentRecord, len(rows))
	for i := range rows {
		records[i] = &rows[i].SegmentRecord
	}
	candidates := int64(len(rows))
	if len(rows) > 0 {
		candidates = rows[0].Candidates
	} else if err := storage.GetDb().Model(&SegmentRecord{}).Where(window, windowArgs...).
		Count(&candidates).Error; err != nil {
		return nil, fmt.Errorf("failed to count candidate segments: %w", err)
	}
	if pruned := candidates - int64(len(rows)); pruned > 0 {
		telemetry.QuerySegmentsPruned.WithLabelValues(stream).Add(float64(pruned))
	}
	return records, nil
}

func (h PruningHints) columns() []hintColumn {
	var columns []hintColumn
	for _, column := range []hintColumn{
		{"services", h.Services},
		{"levels", h.Levels},
		{"metric_names", h.MetricNames},
	} {
		if column.values != nil {
			columns = append(columns, column)
		}
	}
	return columns
}
