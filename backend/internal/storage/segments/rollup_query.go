package segments

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// RollupSourcePlaceholder marks where a rollup query reads the source
// segment (any number of times); RollupPartsPlaceholder where a combining
// query reads the parts' rows.
const (
	RollupSourcePlaceholder = "{{rollup_source}}"
	RollupPartsPlaceholder  = "{{rollup_parts}}"
)

// BuildRollupQuery writes stream's rollup of one segment of sourceStream:
// query's rows, with the segment (read as sourceStream's current schema,
// legacy files included) at each RollupSourcePlaceholder. The result has a
// timestamp column; it's a final-level segment whose stats name record as
// its source, nil when the query returns no rows. joins: the query holds
// the segment in memory (a self-join), so it takes a sort's share of the
// compactor's memory.
func (c *Compactor) BuildRollupQuery(
	record *SegmentRecord, sourceStream, stream, query, dataDir string, joins bool,
) (*Segment, error) {
	return c.BuildRollupQueries(record, sourceStream, stream, []string{query}, "", dataDir, joins)
}

// BuildRollupQueries is BuildRollupQuery over parts: each query writes its
// rows on its own (a part of a join too big to hold at once), and combine
// (at RollupPartsPlaceholder; "" for all rows by timestamp) merges them.
func (c *Compactor) BuildRollupQueries(
	record *SegmentRecord, sourceStream, stream string, queries []string, combine, dataDir string, joins bool,
) (*Segment, error) {
	source, sourceArgs := SourceExpr(sourceStream, []ResolvedFile{{Path: record.Path, Legacy: record.IsLegacy()}})
	segmentDir := filepath.Join(dataDir, stream, record.ProjectID.String())
	if err := os.MkdirAll(segmentDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create rollup directory: %w", err)
	}
	outputPath := filepath.Join(segmentDir, fmt.Sprintf("%d_%s.parquet", time.Now().UnixNano(), uuid.NewString()[:8]))

	var parts []string
	defer func() {
		for _, part := range parts {
			_ = os.Remove(part)
		}
	}()
	for i, query := range queries {
		reads := strings.Count(query, RollupSourcePlaceholder)
		if reads == 0 {
			return nil, fmt.Errorf("%s rollup query doesn't read its source", stream)
		}
		var args []any
		for range reads {
			args = append(args, sourceArgs...)
		}
		path := outputPath
		if len(queries) > 1 {
			path = fmt.Sprintf("%s.part%d", outputPath, i)
			parts = append(parts, path)
		}
		statement := compactionCopySQL(strings.ReplaceAll(query, RollupSourcePlaceholder, source), path)
		if err := c.execGated(joins, statement, args...); err != nil {
			_ = os.Remove(outputPath)
			return nil, fmt.Errorf("failed to build %s rollup of segment %s: %w", stream, record.ID, err)
		}
	}
	if len(parts) > 0 {
		if combine == "" {
			combine = "SELECT * FROM " + RollupPartsPlaceholder + " ORDER BY timestamp"
		}
		files, args := ParquetFilesExpr(parts)
		statement := compactionCopySQL(strings.Replace(combine, RollupPartsPlaceholder, files, 1), outputPath)
		if err := c.execGated(true, statement, args...); err != nil {
			_ = os.Remove(outputPath)
			return nil, fmt.Errorf("failed to combine %s rollup parts of segment %s: %w", stream, record.ID, err)
		}
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
