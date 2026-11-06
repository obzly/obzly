package segments

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"obzly/internal/storage"

	"github.com/google/uuid"
)

// ErrSegmentUnreadable: a legacy segment is neither on disk nor in object
// storage, so it can't be migrated (queries skip it too).
var ErrSegmentUnreadable = errors.New("segment file is missing locally and in object storage")

// MigrateLegacySegment rewrites one version 1 segment to the current
// version: read it (downloading it if it was reclaimed from disk), write the
// normalized copy, swap the catalog rows atomically, then drop the old file
// and object. A segment deleted meanwhile (retention, project deletion,
// compaction) is left alone.
func MigrateLegacySegment(
	ctx context.Context,
	repo *SegmentRepository,
	compactor *Compactor,
	store *ObjectStore,
	dataDir string,
	record *SegmentRecord,
) error {
	source := record.Path
	if _, err := os.Stat(source); err != nil {
		if store == nil || record.ObjectKey == "" {
			return ErrSegmentUnreadable
		}
		source = filepath.Join(os.TempDir(), "obzly-migrate-"+record.ID.String()+".parquet")
		if err := store.Download(ctx, record.ObjectKey, source); err != nil {
			return fmt.Errorf("failed to download segment: %w", err)
		}
		defer func() { _ = os.Remove(source) }()
	}

	segment, err := compactor.RewriteLegacySegment(record, source, dataDir)
	if err != nil {
		return err
	}
	rewritten := FromSegment(*segment)
	if err := repo.ReplaceSegments([]uuid.UUID{record.ID}, rewritten); err != nil {
		_ = compactor.DeleteSegmentFiles([]*SegmentRecord{rewritten})
		if errors.Is(err, ErrSegmentsGone) {
			return nil
		}
		return fmt.Errorf("failed to record rewritten segment: %w", err)
	}

	if err := compactor.DeleteSegmentFiles([]*SegmentRecord{record}); err != nil {
		return fmt.Errorf("failed to delete old segment file: %w", err)
	}
	if store != nil && record.ObjectKey != "" {
		if record.Stats != nil && record.Stats.TokenBloom {
			_ = store.Delete(ctx, record.ObjectKey+BloomSuffix)
		}
		if err := store.Delete(ctx, record.ObjectKey); err != nil {
			return fmt.Errorf("failed to delete old segment object: %w", err)
		}
	}
	return nil
}

// ListLegacy returns up to limit version 1 segments of a stream, oldest
// first, skipping the given ids (ones that failed this run).
func (r *SegmentRepository) ListLegacy(stream string, limit int, skip []uuid.UUID) ([]*SegmentRecord, error) {
	var records []*SegmentRecord
	query := storage.GetDb().
		Where("stream = ? AND schema_version = ?", stream, SchemaVersionLegacy).
		Order("created_at ASC").
		Limit(limit)
	if len(skip) > 0 {
		query = query.Where("id NOT IN ?", skip)
	}
	return records, query.Find(&records).Error
}

// RelabelNamed moves a stream's version 2 segments to the current version:
// version 3 only adds columns version 2 files read as NULL, so no rewrite.
func (r *SegmentRepository) RelabelNamed(stream string) (int64, error) {
	result := storage.GetDb().Model(&SegmentRecord{}).
		Where("stream = ? AND schema_version = ?", stream, SchemaVersionNamed).
		Update("schema_version", CurrentSchemaVersion)
	return result.RowsAffected, result.Error
}

// MarkRewriteFailed records that rewriting a legacy segment failed, so
// every replica's Upgrade status counts it (the worker skips it until it
// restarts).
func (r *SegmentRepository) MarkRewriteFailed(stream string, id uuid.UUID, cause error) error {
	return storage.GetDb().Model(&SegmentRecord{}).
		Where("stream = ? AND id = ?", stream, id).
		Updates(map[string]any{"rewrite_failed_at": time.Now().UTC(), "rewrite_error": cause.Error()}).Error
}

// CountRewriteFailed counts a stream's legacy segments whose rewrite failed.
func (r *SegmentRepository) CountRewriteFailed(stream string) (int64, error) {
	var failed int64
	err := storage.GetDb().Model(&SegmentRecord{}).
		Where("stream = ? AND schema_version = ? AND rewrite_failed_at IS NOT NULL", stream, SchemaVersionLegacy).
		Count(&failed).Error
	return failed, err
}

// CountBySchemaVersion returns how many segments of a stream are legacy and in total.
func (r *SegmentRepository) CountBySchemaVersion(stream string) (legacy, total int64, err error) {
	row := storage.GetDb().Model(&SegmentRecord{}).
		Select("COUNT(*) FILTER (WHERE schema_version = ?), COUNT(*)", SchemaVersionLegacy).
		Where("stream = ?", stream).
		Row()
	err = row.Scan(&legacy, &total)
	return legacy, total, err
}
