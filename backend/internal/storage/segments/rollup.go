package segments

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// RollupStreams are the metric rollup streams and their bucket widths.
var RollupStreams = map[string]time.Duration{
	"metrics_5m": 5 * time.Minute,
	"metrics_1h": time.Hour,
}

// BuildGroupedRollup writes stream's rollup of one segment of sourceStream:
// SELECT selectList FROM the segment (read as sourceStream's current schema,
// legacy files included) GROUP BY groupBy. The result has a timestamp
// column; it's a final-level segment whose stats name record as its
// source, nil when record has no rows.
func (c *Compactor) BuildGroupedRollup(
	record *SegmentRecord, sourceStream, stream, selectList, groupBy, dataDir string,
) (*Segment, error) {
	source, args := SourceExpr(sourceStream, []ResolvedFile{{Path: record.Path, Legacy: record.IsLegacy()}})
	query := "SELECT " + selectList + " FROM " + source + " GROUP BY " + groupBy

	segmentDir := filepath.Join(dataDir, stream, record.ProjectID.String())
	if err := os.MkdirAll(segmentDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create rollup directory: %w", err)
	}
	outputPath := filepath.Join(segmentDir, fmt.Sprintf("%d_%s.parquet", time.Now().UnixNano(), uuid.NewString()[:8]))
	if _, err := c.db.Exec(compactionCopySQL(query, outputPath), args...); err != nil {
		_ = os.Remove(outputPath)
		return nil, fmt.Errorf("failed to build %s rollup of segment %s: %w", stream, record.ID, err)
	}

	var rows int64
	var minTime, maxTime *time.Time
	if err := c.db.QueryRow("SELECT count(*), min(timestamp), max(timestamp) FROM read_parquet(?)", outputPath).
		Scan(&rows, &minTime, &maxTime); err != nil {
		_ = os.Remove(outputPath)
		return nil, fmt.Errorf("failed to read rollup of segment %s: %w", record.ID, err)
	}
	if rows == 0 || minTime == nil || maxTime == nil {
		_ = os.Remove(outputPath)
		return nil, nil
	}
	info, err := os.Stat(outputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat rollup: %w", err)
	}
	return &Segment{
		Path:          outputPath,
		ProjectID:     record.ProjectID,
		Stream:        stream,
		RowCount:      int(rows),
		SizeBytes:     info.Size(),
		RawSizeBytes:  info.Size(),
		MinTime:       minTime.UTC(),
		MaxTime:       maxTime.UTC(),
		CreatedAt:     time.Now().UTC(),
		SchemaVersion: CurrentSchemaVersion,
		Stats:         &SegmentStats{Source: record.ID.String()},
		Level:         LevelCold,
	}, nil
}

// BuildRollup writes stream's rollup of one metrics segment: per series
// and bucket, the first, last, min and max samples with their timestamps
// (so a chart drawn from them looks like one drawn from every sample, and
// max/min/last-over-time stay exact) plus count and sum. The result is a
// final-level segment whose stats name record as its source; nil when
// record has no rows.
func (c *Compactor) BuildRollup(record *SegmentRecord, stream, dataDir string) (*Segment, error) {
	width, ok := RollupStreams[stream]
	if !ok {
		return nil, fmt.Errorf("%q is not a rollup stream", stream)
	}
	source, args := SourceExpr("metrics", []ResolvedFile{{Path: record.Path, Legacy: record.IsLegacy()}})
	present, err := columnsOf(c.db, source, args)
	if err != nil {
		return nil, err
	}
	seriesID := "obzly_series_id(metric_name, labels)"
	if present["series_id"] {
		seriesID = "coalesce(series_id, " + seriesID + ")"
	}
	query := fmt.Sprintf(`SELECT project_id, %s AS series_id, metric_name, labels,
		max_by(type, timestamp) AS type,
		time_bucket(INTERVAL '%d seconds', timestamp::TIMESTAMP) AS bucket,
		count(*) AS count, sum(value) AS sum,
		min(timestamp) AS first_ts, arg_min(value, timestamp) AS first_value,
		max(timestamp) AS last_ts, arg_max(value, timestamp) AS last_value,
		arg_min(timestamp, value) AS min_ts, min(value) AS min_value,
		arg_max(timestamp, value) AS max_ts, max(value) AS max_value
		FROM %s
		GROUP BY project_id, 2, metric_name, labels, bucket
		ORDER BY metric_name, series_id, bucket`, seriesID, int(width.Seconds()), source)

	segmentDir := filepath.Join(dataDir, stream, record.ProjectID.String())
	if err := os.MkdirAll(segmentDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create rollup directory: %w", err)
	}
	outputPath := filepath.Join(segmentDir, fmt.Sprintf("%d_%s.parquet", time.Now().UnixNano(), uuid.NewString()[:8]))
	if _, err := c.db.Exec(compactionCopySQL(query, outputPath), args...); err != nil {
		return nil, fmt.Errorf("failed to build %s rollup of segment %s: %w", stream, record.ID, err)
	}

	var rows int64
	var minTime, maxTime *time.Time
	if err := c.db.QueryRow("SELECT count(*), min(first_ts), max(last_ts) FROM read_parquet(?)", outputPath).
		Scan(&rows, &minTime, &maxTime); err != nil {
		return nil, fmt.Errorf("failed to read rollup of segment %s: %w", record.ID, err)
	}
	if rows == 0 || minTime == nil || maxTime == nil {
		_ = os.Remove(outputPath)
		return nil, nil
	}
	stats, err := computeStats(c.db, stream, "read_parquet(?)", []any{outputPath})
	if err != nil {
		return nil, err
	}
	if stats == nil {
		stats = &SegmentStats{}
	}
	stats.Source = record.ID.String()
	info, err := os.Stat(outputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat rollup: %w", err)
	}
	return &Segment{
		Path:          outputPath,
		ProjectID:     record.ProjectID,
		Stream:        stream,
		RowCount:      int(rows),
		SizeBytes:     info.Size(),
		RawSizeBytes:  info.Size(),
		MinTime:       minTime.UTC(),
		MaxTime:       maxTime.UTC(),
		CreatedAt:     time.Now().UTC(),
		SchemaVersion: CurrentSchemaVersion,
		Stats:         stats,
		Level:         LevelCold,
	}, nil
}
