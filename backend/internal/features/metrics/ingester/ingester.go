// Package metrics_ingester is the metrics stream's write path onto Parquet
// segments, mirroring spans_ingester.
package metrics_ingester

import (
	"encoding/json"
	"errors"
	"fmt"

	metrics_core "obzly/internal/features/metrics/core"
	"obzly/internal/storage/segments"
	"obzly/internal/util/attrnames"
	"obzly/internal/util/logger"

	"math"
	"obzly/internal/storage"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MetricSchema is the metrics stream's Parquet schema; column order must
// match rowFor. Labels is JSON with sorted keys, so equal label sets are
// byte-identical and the querier groups series by string equality.
var MetricSchema = segments.Schema{
	Stream: "metrics",
	Columns: []segments.ColumnDef{
		{Name: "id", Type: "VARCHAR"},
		{Name: "project_id", Type: "VARCHAR"},
		{Name: "metric_name", Type: "VARCHAR"},
		{Name: "labels", Type: "VARCHAR"},
		{Name: "value", Type: "DOUBLE"},
		// TIMESTAMP_NS: DuckDB's TIMESTAMP silently truncates to microseconds.
		{Name: "timestamp", Type: "TIMESTAMP_NS"},
		{Name: "type", Type: "VARCHAR"},
		// segments.SeriesID(metric_name, labels); NULL in older files until
		// compaction fills it.
		{Name: "series_id", Type: "UBIGINT"},
	},
}

// MetricSeriesSchema is the metrics_series stream: one row per series a
// flush saw, with when it was first and last seen;
// compaction keeps one row per series (segments.DeduplicatingMerges).
// last_seen is the row time, so retention drops a series once it has been
// quiet for the retention period.
var MetricSeriesSchema = segments.Schema{
	Stream: StreamSeries,
	Columns: []segments.ColumnDef{
		{Name: "project_id", Type: "VARCHAR"},
		{Name: "series_id", Type: "UBIGINT"},
		{Name: "metric_name", Type: "VARCHAR"},
		{Name: "labels", Type: "VARCHAR"},
		{Name: "type", Type: "VARCHAR"},
		{Name: "first_seen", Type: "TIMESTAMP_NS"},
		{Name: "last_seen", Type: "TIMESTAMP_NS"},
	},
}

const StreamSeries = "metrics_series"

type MetricIngester struct {
	writer      *segments.Writer
	segmentRepo *segments.SegmentRepository
	// series buffers each project's distinct series until its next flush;
	// nil for a sample-only ingester (the query-time tail).
	seriesWriter *segments.Writer
	mu           sync.Mutex
	series       map[uuid.UUID]map[uint64]*seriesEntry
}

type seriesEntry struct {
	metricName, labels, metricType string
	firstSeen, lastSeen            time.Time
}

// NewMetricIngester writes samples, and each flush's series to the
// metrics_series stream.
func NewMetricIngester(dataDir string, opts ...segments.Option) (*MetricIngester, error) {
	ingester, err := NewSampleOnlyIngester(dataDir, opts...)
	if err != nil {
		return nil, err
	}
	ingester.seriesWriter, err = segments.NewWriter(dataDir, MetricSeriesSchema,
		segments.WithMaxRows(math.MaxInt), segments.WithMaxBytes(math.MaxInt64))
	if err != nil {
		_ = ingester.writer.Close()
		return nil, fmt.Errorf("failed to create metrics series writer: %w", err)
	}
	ingester.series = map[uuid.UUID]map[uint64]*seriesEntry{}
	return ingester, nil
}

// NewSampleOnlyIngester writes samples only (the query-time tail, whose
// files are never catalogued).
func NewSampleOnlyIngester(dataDir string, opts ...segments.Option) (*MetricIngester, error) {
	writer, err := segments.NewWriter(dataDir, MetricSchema, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create metrics segment writer: %w", err)
	}

	return &MetricIngester{writer: writer, segmentRepo: &segments.SegmentRepository{}}, nil
}

// StoreSamplesBatch buffers samples per project and flushes any project
// whose buffer crosses a threshold.
func (i *MetricIngester) StoreSamplesBatch(entries map[uuid.UUID][]*metrics_core.MetricSample) error {
	for projectID, batchSamples := range entries {
		for _, sample := range batchSamples {
			row, rowBytes, err := rowFor(projectID, sample)
			if err != nil {
				return fmt.Errorf("failed to convert metric sample %s: %w", sample.MetricName, err)
			}

			i.trackSeries(projectID, row, sample.Timestamp)
			segment, err := i.writer.Append(projectID, row, rowBytes, sample.Timestamp)
			if err != nil {
				return fmt.Errorf("failed to append metric sample %s: %w", sample.MetricName, err)
			}
			if segment != nil {
				if err := i.recordSegment(*segment); err != nil {
					return err
				}
				// A size-triggered flush writes that project's series too.
				if _, err := i.FlushSeries(projectID); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func (i *MetricIngester) FlushStale() ([]segments.Segment, error) {
	flushed, err := i.writer.FlushStale(0)
	// Segments written before a failed project still need their catalog rows.
	if err := errors.Join(err, i.recordSegments(flushed)); err != nil {
		return flushed, err
	}
	return flushed, i.flushAllSeries()
}

func (i *MetricIngester) FlushAll() ([]segments.Segment, error) {
	flushed, err := i.writer.FlushAll()
	// Segments written before a failed project still need their catalog rows.
	if err := errors.Join(err, i.recordSegments(flushed)); err != nil {
		return flushed, err
	}
	return flushed, i.flushAllSeries()
}

// FlushSeries writes the series one project's samples used since its last
// series flush to a metrics_series segment and records it; nil if none (or
// a sample-only ingester).
func (i *MetricIngester) FlushSeries(projectID uuid.UUID) (*segments.SegmentRecord, error) {
	if i.seriesWriter == nil {
		return nil, nil
	}
	i.mu.Lock()
	buffered := i.series[projectID]
	delete(i.series, projectID)
	i.mu.Unlock()
	if len(buffered) == 0 {
		return nil, nil
	}

	for seriesID, entry := range buffered {
		row := []any{
			projectID.String(), seriesID, entry.metricName, entry.labels, entry.metricType,
			entry.firstSeen, entry.lastSeen,
		}
		rowBytes := int64(len(entry.metricName) + len(entry.labels) + 48)
		if _, err := i.seriesWriter.Append(projectID, row, rowBytes, entry.lastSeen); err != nil {
			return nil, fmt.Errorf("failed to buffer metric series: %w", err)
		}
	}
	segment, err := i.seriesWriter.FlushProject(projectID)
	if err != nil || segment == nil {
		return nil, err
	}
	if err := recordCoverage(projectID); err != nil {
		return nil, err
	}
	// The index only serves label/name autocomplete: a row Postgres
	// rejects must not fail the batch, which would then be redelivered
	// (and its samples written again) forever.
	if err := updateLabelIndex(projectID, buffered); err != nil {
		logger.GetLogger().Warn("failed to update the metric label index", "projectId", projectID, "error", err)
	}
	record := segments.FromSegment(*segment)
	if err := i.segmentRepo.Create(record); err != nil {
		return nil, fmt.Errorf("failed to record segment metadata for %s: %w", segment.Path, err)
	}
	return record, nil
}

// FlushProject flushes one project's rows and records the segment; nil if
// nothing was buffered.
func (i *MetricIngester) FlushProject(projectID uuid.UUID) (*segments.SegmentRecord, error) {
	segment, err := i.writer.FlushProject(projectID)
	if err != nil || segment == nil {
		return nil, err
	}
	record := segments.FromSegment(*segment)
	if err := i.segmentRepo.Create(record); err != nil {
		return nil, fmt.Errorf("failed to record segment metadata for %s: %w", segment.Path, err)
	}
	return record, nil
}

// WriteFile writes one project's buffered rows to a Parquet file that is
// not recorded in the catalog (the query-time tail); nil if none.
func (i *MetricIngester) WriteFile(projectID uuid.UUID) (*segments.Segment, error) {
	return i.writer.FlushProject(projectID)
}

// CleanupStaleTempFiles removes "*.parquet.tmp" files left by a crash
// mid-flush; call once at startup, before ingestion resumes.
func (i *MetricIngester) CleanupStaleTempFiles() (int, error) {
	return i.writer.CleanupStaleTempFiles()
}

func (i *MetricIngester) recordSegments(flushed []segments.Segment) error {
	for _, segment := range flushed {
		if err := i.recordSegment(segment); err != nil {
			return err
		}
	}
	return nil
}

func (i *MetricIngester) recordSegment(segment segments.Segment) error {
	if err := i.segmentRepo.Create(segments.FromSegment(segment)); err != nil {
		return fmt.Errorf("failed to record segment metadata for %s: %w", segment.Path, err)
	}
	return nil
}

func (i *MetricIngester) Close() error {
	if i.seriesWriter != nil {
		if err := i.flushAllSeries(); err != nil {
			return err
		}
		if err := i.seriesWriter.Close(); err != nil {
			return err
		}
	}
	return i.writer.Close()
}

// trackSeries notes row's series (rowFor's columns) for the next series
// flush.
func (i *MetricIngester) trackSeries(projectID uuid.UUID, row []any, at time.Time) {
	if i.seriesWriter == nil {
		return
	}
	seriesID, _ := row[7].(uint64)
	i.mu.Lock()
	defer i.mu.Unlock()
	project, ok := i.series[projectID]
	if !ok {
		project = map[uint64]*seriesEntry{}
		i.series[projectID] = project
	}
	entry, ok := project[seriesID]
	if !ok {
		metricName, _ := row[2].(string)
		labels, _ := row[3].(string)
		metricType, _ := row[6].(string)
		project[seriesID] = &seriesEntry{metricName: metricName, labels: labels, metricType: metricType,
			firstSeen: at, lastSeen: at}
		return
	}
	if at.Before(entry.firstSeen) {
		entry.firstSeen = at
	}
	if at.After(entry.lastSeen) {
		entry.lastSeen = at
		entry.metricType, _ = row[6].(string)
	}
}

func (i *MetricIngester) flushAllSeries() error {
	if i.seriesWriter == nil {
		return nil
	}
	i.mu.Lock()
	projects := make([]uuid.UUID, 0, len(i.series))
	for projectID := range i.series {
		projects = append(projects, projectID)
	}
	i.mu.Unlock()
	for _, projectID := range projects {
		if _, err := i.FlushSeries(projectID); err != nil {
			return err
		}
	}
	return nil
}

// recordCoverage notes when projectID's series stream started (its first
// series flush): samples with later timestamps all have series rows.
func recordCoverage(projectID uuid.UUID) error {
	err := storage.GetDb().Exec(
		"INSERT INTO metric_series_coverage (project_id, since) VALUES (?, NOW()) ON CONFLICT (project_id) DO NOTHING",
		projectID,
	).Error
	if err != nil {
		return fmt.Errorf("failed to record metric series coverage: %w", err)
	}
	return nil
}

func rowFor(projectID uuid.UUID, sample *metrics_core.MetricSample) ([]any, int64, error) {
	labelsJSON := "{}"
	if len(sample.Labels) > 0 {
		encoded, err := json.Marshal(attrnames.NormalizeStringMap(sample.Labels))
		if err != nil {
			return nil, 0, fmt.Errorf("failed to marshal labels: %w", err)
		}
		labelsJSON = string(encoded)
	}

	metricType := sample.Type
	if metricType == "" {
		metricType = metrics_core.MetricTypeGauge
	}

	metricName := attrnames.NormalizeMetricName(sample.MetricName)
	row := []any{
		sample.ID.String(),
		projectID.String(),
		metricName,
		labelsJSON,
		sample.Value,
		sample.Timestamp,
		string(metricType),
		segments.SeriesID(metricName, labelsJSON),
	}

	rowBytes := int64(len(sample.MetricName) + len(labelsJSON) + 64)

	return row, rowBytes, nil
}
