package segments

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"obzly/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrSegmentsGone: a segment being merged was deleted meanwhile (project
// deletion, retention), so the merge result must be discarded.
var ErrSegmentsGone = errors.New("segments were deleted during compaction")

// SegmentRepository is the Postgres catalog of segment files. It knows nothing
// about Parquet or DuckDB; callers record Writer's segments via FromSegment.
type SegmentRepository struct{}

// Create persists one segment row, generating record.ID if unset.
func (r *SegmentRepository) Create(record *SegmentRecord) error {
	if record.ID == uuid.Nil {
		record.ID = uuid.New()
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	stampLocalWriter(record)

	return storage.GetDb().Create(record).Error
}

// ListByTimeRange returns a project/stream's segments overlapping [from, to],
// oldest first, without pruning hints (for callers like the SQL sandbox).
func (r *SegmentRepository) ListByTimeRange(
	projectID uuid.UUID,
	stream string,
	from, to time.Time,
) ([]*SegmentRecord, error) {
	return r.PlanSegments(projectID, stream, from, to, PruningHints{})
}

// ListPendingUpload returns up to limit not-yet-uploaded segments of stream.
func (r *SegmentRepository) ListPendingUpload(stream string, limit int) ([]*SegmentRecord, error) {
	var records []*SegmentRecord

	err := storage.GetDb().
		Where("stream = ? AND uploaded_at IS NULL", stream).
		Order("created_at ASC").
		Limit(limit).
		Find(&records).Error

	return records, err
}

// ListPendingUploadForLocalWriter is the uploader's queue: up to limit
// not-yet-uploaded segments whose files are on this process's disk, oldest
// first (plus rows with no writer_id if SetLocalWriter allowed it). Another
// replica's rows would fail every pass and starve this replica's uploads.
// Without SetLocalWriter it behaves like ListPendingUpload.
func (r *SegmentRepository) ListPendingUploadForLocalWriter(stream string, limit int) ([]*SegmentRecord, error) {
	writer := currentLocalWriter.Load()
	if writer == nil {
		return r.ListPendingUpload(stream, limit)
	}

	var records []*SegmentRecord
	err := storage.GetDb().
		Where("stream = ? AND uploaded_at IS NULL", stream).
		Where("writer_id = ? OR (? AND writer_id IS NULL)", writer.id, writer.uploadsUnowned).
		Order("created_at ASC").
		Limit(limit).
		Find(&records).Error

	return records, err
}

// ListUploaded returns up to limit uploaded segments of stream, most recently
// uploaded first, for the reconciliation worker: a fresh upload's local copy
// going missing is the likelier sign of a real problem.
func (r *SegmentRepository) ListUploaded(stream string, limit int) ([]*SegmentRecord, error) {
	var records []*SegmentRecord

	err := storage.GetDb().
		Where("stream = ? AND uploaded_at IS NOT NULL", stream).
		Order("uploaded_at DESC").
		Limit(limit).
		Find(&records).Error

	return records, err
}

// ListUploadedSince returns every uploaded segment, of any stream, with rows
// at or after since, newest first - what a querier warms its cache with.
func (r *SegmentRepository) ListUploadedSince(since time.Time) ([]*SegmentRecord, error) {
	var records []*SegmentRecord
	err := storage.GetDb().
		Where("uploaded_at IS NOT NULL AND object_key <> '' AND max_time >= ?", since).
		Order("max_time DESC").
		Find(&records).Error
	return records, err
}

// MarkUploaded records that a segment has been pushed to object storage
// under objectKey.
func (r *SegmentRepository) MarkUploaded(id uuid.UUID, objectKey string) error {
	now := time.Now().UTC()

	return storage.GetDb().
		Model(&SegmentRecord{}).
		Where("id = ?", id).
		Updates(map[string]any{"uploaded_at": now, "object_key": objectKey}).
		Error
}

// ListUploadedWithoutTokenBloom returns up to limit uploaded segments of
// stream without a token filter, newest first - the backfill's queue.
func (r *SegmentRepository) ListUploadedWithoutTokenBloom(stream string, limit int) ([]*SegmentRecord, error) {
	var records []*SegmentRecord
	err := storage.GetDb().
		Where("stream = ? AND uploaded_at IS NOT NULL AND object_key <> ''", stream).
		Where("stats IS NULL OR stats->>'token_bloom' IS NULL").
		Order("max_time DESC").
		Limit(limit).
		Find(&records).Error
	return records, err
}

// MarkTokenBloom records that id's token filter is in object storage.
func (r *SegmentRepository) MarkTokenBloom(id uuid.UUID) error {
	return r.setTokenBloom(id, true)
}

// MarkNoTokenBloom records that id gets no filter (its backfill failed):
// searches scan it, and the backfill doesn't retry it every pass.
func (r *SegmentRepository) MarkNoTokenBloom(id uuid.UUID) error {
	return r.setTokenBloom(id, false)
}

func (r *SegmentRepository) setTokenBloom(id uuid.UUID, present bool) error {
	return storage.GetDb().Exec(`UPDATE stream_segments
		SET stats = jsonb_set(coalesce(stats, '{}'::jsonb), '{token_bloom}', to_jsonb(?::boolean)) WHERE id = ?`,
		present, id).Error
}

// SetLevel relabels segments (e.g. a lone old L0 promoted to L1, which needs
// no rewrite).
func (r *SegmentRepository) SetLevel(ids []uuid.UUID, level int) error {
	if len(ids) == 0 {
		return nil
	}
	return storage.GetDb().Model(&SegmentRecord{}).Where("id IN ?", ids).Update("level", level).Error
}

// RecordCompactionAttempt counts a merge of ids before it runs, since a
// crash during it can't count it afterwards. A merge that succeeds replaces
// the rows, so only failed attempts stay counted.
func (r *SegmentRepository) RecordCompactionAttempt(ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	return storage.GetDb().Model(&SegmentRecord{}).Where("id IN ?", ids).
		UpdateColumn("compaction_attempts", gorm.Expr("compaction_attempts + 1")).Error
}

// MarkRollupsAged flags the rollups built from sourceIDs as aged, before
// retention retires those sources.
func (r *SegmentRepository) MarkRollupsAged(projectID uuid.UUID, sourceIDs []string) error {
	if len(sourceIDs) == 0 {
		return nil
	}
	streams := make([]string, 0, len(RollupStreams))
	for stream := range RollupStreams {
		streams = append(streams, stream)
	}
	return storage.GetDb().Exec(
		"UPDATE stream_segments SET stats = jsonb_set(stats, '{aged}', 'true') "+
			"WHERE project_id = ? AND stream IN ? AND stats->>'source' IN ?",
		projectID, streams, sourceIDs,
	).Error
}

// Exists reports whether a segment row is still in the catalog (a merge or
// a deletion removes it).
func (r *SegmentRepository) Exists(id uuid.UUID) (bool, error) {
	var count int64
	err := storage.GetDb().Model(&SegmentRecord{}).Where("id = ?", id).Count(&count).Error
	return count > 0, err
}

// DeleteByIDs removes segment catalog rows only; callers delete the Parquet
// files and uploaded objects themselves.
func (r *SegmentRepository) DeleteByIDs(ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}

	return storage.GetDb().Where("id IN ?", ids).Delete(&SegmentRecord{}).Error
}

// ReplaceSegments swaps merged-away segments for their merged result in one
// transaction. If any source row is already gone it changes nothing and
// returns ErrSegmentsGone - otherwise a merge that overlaps a deletion
// would write the deleted rows back.
func (r *SegmentRepository) ReplaceSegments(sourceIDs []uuid.UUID, merged *SegmentRecord) error {
	if merged.ID == uuid.Nil {
		merged.ID = uuid.New()
	}
	if merged.CreatedAt.IsZero() {
		merged.CreatedAt = time.Now().UTC()
	}
	stampLocalWriter(merged)

	return storage.GetDb().Transaction(func(tx *gorm.DB) error {
		result := tx.Where("id IN ?", sourceIDs).Delete(&SegmentRecord{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != int64(len(sourceIDs)) {
			return ErrSegmentsGone
		}
		return tx.Create(merged).Error
	})
}

// DeleteByProject removes every segment row of a project/stream and returns
// them so the caller can delete their files. It repeats until nothing is
// left: a compaction committing while the first DELETE waits on its row
// locks inserts a merged row that DELETE's snapshot can't see.
func (r *SegmentRepository) DeleteByProject(projectID uuid.UUID, stream string) ([]*SegmentRecord, error) {
	var all []*SegmentRecord
	for range 5 {
		var deleted []*SegmentRecord
		err := storage.GetDb().
			Clauses(clause.Returning{}).
			Where("project_id = ? AND stream = ?", projectID, stream).
			Delete(&deleted).Error
		if err != nil {
			return all, err
		}
		if len(deleted) == 0 {
			return all, nil
		}
		all = append(all, deleted...)
	}
	return all, nil
}

// ListByProject returns every segment of a project/stream, oldest first.
// Queries should prefer ListByTimeRange.
func (r *SegmentRepository) ListByProject(projectID uuid.UUID, stream string) ([]*SegmentRecord, error) {
	var records []*SegmentRecord

	err := storage.GetDb().
		Where("project_id = ? AND stream = ?", projectID, stream).
		Order("min_time ASC").
		Find(&records).Error

	return records, err
}

// QuotaMeasure is the stream_segments column a quota counts.
type QuotaMeasure string

const (
	QuotaRows  QuotaMeasure = "row_count"
	QuotaBytes QuotaMeasure = "size_bytes"
)

// ListOldestCovering returns a project/stream's oldest segments (min_time
// order, as ListByProject) up to the first whose measure brings their sum
// to amount: what a quota pass deletes, without loading every row.
func (r *SegmentRepository) ListOldestCovering(
	projectID uuid.UUID, stream string, measure QuotaMeasure, amount int64,
) ([]*SegmentRecord, error) {
	if measure != QuotaRows && measure != QuotaBytes {
		return nil, fmt.Errorf("unknown quota measure %q", measure)
	}
	column := string(measure)
	var records []*SegmentRecord
	err := storage.GetDb().
		Where("project_id = ? AND stream = ? AND id IN (?)", projectID, stream, gorm.Expr(
			"SELECT id FROM (SELECT id, SUM("+column+") OVER (ORDER BY min_time, id) - "+column+
				" AS before FROM stream_segments WHERE project_id = ? AND stream = ?) ordered WHERE before < ?",
			projectID, stream, amount,
		)).
		Order("min_time ASC, id ASC").
		Find(&records).Error
	return records, err
}

// ListNewest returns up to limit of a project/stream's segments with the
// newest rows, newest first.
func (r *SegmentRepository) ListNewest(projectID uuid.UUID, stream string, limit int) ([]*SegmentRecord, error) {
	var records []*SegmentRecord
	err := storage.GetDb().
		Where("project_id = ? AND stream = ?", projectID, stream).
		Order("max_time DESC").
		Limit(limit).
		Find(&records).Error
	return records, err
}

// StreamStats is the row-count/size/time-range summary of a set of segments,
// computed as one SQL aggregate.
type StreamStats struct {
	RowCount     int64
	SizeBytes    int64
	RawSizeBytes int64
	// Zero time.Time if there are no matching segments.
	OldestTime time.Time
	NewestTime time.Time
}

// StreamBacklog counts a stream's segments still waiting on a background
// worker: not yet uploaded, or small enough to be compacted. Read from the
// catalog, so it is the same from any replica.
type StreamBacklog struct {
	Total                 int64
	PendingUpload         int64
	SmallSegments         int64
	OldestPendingUploadAt *time.Time
}

func (r *SegmentRepository) CountBacklog(stream string, smallThresholdBytes int64) (StreamBacklog, error) {
	var backlog StreamBacklog
	err := storage.GetDb().Raw(`
		SELECT count(*) AS total,
		       count(*) FILTER (WHERE uploaded_at IS NULL) AS pending_upload,
		       count(*) FILTER (WHERE size_bytes < ?) AS small_segments,
		       min(created_at) FILTER (WHERE uploaded_at IS NULL) AS oldest_pending_upload_at
		FROM stream_segments
		WHERE stream = ?`, smallThresholdBytes, stream).Scan(&backlog).Error
	return backlog, err
}

func (r *SegmentRepository) AggregateStats(stream string) (StreamStats, error) {
	return r.aggregate("stream = ?", stream)
}

// AggregateStatsForProject summarizes every segment for one project's stream.
func (r *SegmentRepository) AggregateStatsForProject(projectID uuid.UUID, stream string) (StreamStats, error) {
	return r.aggregate("project_id = ? AND stream = ?", projectID, stream)
}

func (r *SegmentRepository) aggregate(whereSQL string, args ...any) (StreamStats, error) {
	var row struct {
		RowCount     int64
		SizeBytes    int64
		RawSizeBytes int64
		OldestTime   sql.NullTime
		NewestTime   sql.NullTime
	}

	err := storage.GetDb().Model(&SegmentRecord{}).
		Select(
			"COALESCE(SUM(row_count), 0) AS row_count, COALESCE(SUM(size_bytes), 0) AS size_bytes, "+
				"COALESCE(SUM(raw_size_bytes), 0) AS raw_size_bytes, "+
				"MIN(min_time) AS oldest_time, MAX(max_time) AS newest_time",
		).
		Where(whereSQL, args...).
		Scan(&row).Error
	if err != nil {
		return StreamStats{}, fmt.Errorf("failed to aggregate segment stats: %w", err)
	}

	stats := StreamStats{RowCount: row.RowCount, SizeBytes: row.SizeBytes, RawSizeBytes: row.RawSizeBytes}
	if row.OldestTime.Valid {
		stats.OldestTime = row.OldestTime.Time
	}
	if row.NewestTime.Valid {
		stats.NewestTime = row.NewestTime.Time
	}

	return stats, nil
}
