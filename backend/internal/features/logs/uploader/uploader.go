// Package logs_uploader is the logs stream's cold-tier upload path: it pushes
// local segments to object storage and marks them uploaded in stream_segments.
package logs_uploader

import (
	logs_core "obzly/internal/features/logs/core"

	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"obzly/internal/storage/segments"
	"obzly/internal/util/logger"
)

// perSegmentTimeout bounds each Upload/Exists/Download within the batch
// context, so one hung connection can't eat the whole batch's time.
const perSegmentTimeout = 30 * time.Second

type LogUploader struct {
	store       *segments.ObjectStore
	segmentRepo *segments.SegmentRepository
	stream      string

	// DeleteLocalAfterUpload frees local disk once a segment is uploaded (hot to
	// cold tiering). When off, object storage is only a durable backup.
	DeleteLocalAfterUpload bool

	// blooms builds each segment's token filter before its upload (nil:
	// none - tests, streams without filters).
	blooms *segments.TokenBlooms
}

// SetTokenBlooms uploads a token filter with every segment.
func (u *LogUploader) SetTokenBlooms(blooms *segments.TokenBlooms) {
	u.blooms = blooms
}

func NewLogUploader(store *segments.ObjectStore) *LogUploader {
	return NewLogUploaderForStream(store, logs_core.StreamLogs)
}

func NewLogUploaderForStream(store *segments.ObjectStore, stream string) *LogUploader {
	return &LogUploader{store: store, segmentRepo: &segments.SegmentRepository{}, stream: stream}
}

// UploadPending uploads up to limit pending segments and returns how many
// succeeded. A failure doesn't stop the batch (oldest-first order would let
// one broken segment block all behind it); it stays pending and is retried.
func (u *LogUploader) UploadPending(ctx context.Context, limit int) (int, error) {
	pending, err := u.segmentRepo.ListPendingUploadForLocalWriter(u.stream, limit)
	if err != nil {
		return 0, fmt.Errorf("failed to list pending segments: %w", err)
	}

	uploaded := 0
	var failures []error
	for _, record := range pending {
		if err := u.uploadOne(ctx, record); err != nil {
			failures = append(failures, fmt.Errorf("segment %s: %w", record.ID, err))
			continue
		}
		uploaded++
	}

	if len(failures) > 0 {
		return uploaded, fmt.Errorf("failed to upload %d of %d pending segments: %w",
			len(failures), len(pending), errors.Join(failures...))
	}
	return uploaded, nil
}

// Upload pushes one segment now, verifies it and marks it uploaded.
func (u *LogUploader) Upload(ctx context.Context, record *segments.SegmentRecord) error {
	return u.uploadOne(ctx, record)
}

func (u *LogUploader) uploadOne(ctx context.Context, record *segments.SegmentRecord) error {
	key := segments.ObjectKeyFor(record)

	// The filter goes up first, so a segment marked uploaded with one never
	// lacks it. A failed filter only means searches scan the segment.
	withBloom := u.uploadTokenBloom(ctx, record, key)

	uploadCtx, cancel := context.WithTimeout(ctx, perSegmentTimeout)
	defer cancel()
	if err := u.store.Upload(uploadCtx, key, record.Path); err != nil {
		return err
	}

	// Verify the object exists before MarkUploaded: a nil Upload doesn't prove it
	// is retrievable (silent provider failures, eventual consistency), and
	// reconciliation only checks segments whose local copy is gone.
	existsCtx, cancel := context.WithTimeout(ctx, perSegmentTimeout)
	defer cancel()
	exists, err := u.store.Exists(existsCtx, key)
	if err != nil {
		return fmt.Errorf("uploaded but failed to verify it landed: %w", err)
	}
	if !exists {
		return fmt.Errorf(
			"uploaded %q but a follow-up existence check found nothing there - not marking durable, will retry next pass",
			key,
		)
	}

	if err := u.segmentRepo.MarkUploaded(record.ID, key); err != nil {
		return fmt.Errorf("uploaded but failed to record it (will re-upload next pass): %w", err)
	}
	uploaded := *record
	uploaded.ObjectKey = key
	if withBloom {
		if err := u.segmentRepo.MarkTokenBloom(record.ID); err == nil {
			stats := segments.SegmentStats{}
			if record.Stats != nil {
				stats = *record.Stats
			}
			stats.TokenBloom = true
			uploaded.Stats = &stats
		}
	}
	segments.NotifyUploaded(&uploaded)

	if !u.DeleteLocalAfterUpload {
		return nil
	}

	if err := os.Remove(record.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("uploaded and recorded, but failed to reclaim local disk space: %w", err)
	}

	return nil
}

func (u *LogUploader) uploadTokenBloom(ctx context.Context, record *segments.SegmentRecord, key string) bool {
	if u.blooms == nil {
		return false
	}
	built, err := u.blooms.Build(ctx, record.Stream, record.Path)
	if err != nil {
		logger.GetLogger().Warn("Failed to build a segment's token filter - searches will scan it",
			"segmentId", record.ID, "error", err)
		return false
	}
	if !built {
		return false
	}
	uploadCtx, cancel := context.WithTimeout(ctx, perSegmentTimeout)
	defer cancel()
	if err := u.store.Upload(uploadCtx, key+segments.BloomSuffix, segments.BloomPath(record.Path)); err != nil {
		logger.GetLogger().Warn("Failed to upload a segment's token filter - searches will scan it",
			"segmentId", record.ID, "error", err)
		return false
	}
	return true
}

// BackfillTokenBlooms gives up to limit uploaded segments without a token
// filter one, newest first (compaction may never rewrite them). Files not on
// local disk are downloaded into workDir and removed after. Returns the count.
func (u *LogUploader) BackfillTokenBlooms(ctx context.Context, limit int, workDir string) (int, error) {
	if u.blooms == nil {
		return 0, nil
	}
	records, err := u.segmentRepo.ListUploadedWithoutTokenBloom(u.stream, limit)
	if err != nil {
		return 0, fmt.Errorf("failed to list segments without token filters: %w", err)
	}
	done := 0
	for _, record := range records {
		if ctx.Err() != nil {
			break
		}
		if err := u.backfillOne(ctx, record, workDir); err != nil {
			if ctx.Err() != nil {
				break // out of time: the next pass retries it
			}
			logger.GetLogger().Warn("Failed to backfill a segment's token filter - searches will scan it",
				"segmentId", record.ID, "error", err)
			_ = u.segmentRepo.MarkNoTokenBloom(record.ID)
			continue
		}
		done++
	}
	return done, nil
}

func (u *LogUploader) backfillOne(ctx context.Context, record *segments.SegmentRecord, workDir string) error {
	path := record.Path
	if _, err := os.Stat(path); err != nil {
		path = filepath.Join(workDir, record.ID.String()+".parquet")
		defer func() { _ = os.Remove(path) }()
		downloadCtx, cancel := context.WithTimeout(ctx, perSegmentTimeout*4)
		defer cancel()
		if err := u.store.Download(downloadCtx, record.ObjectKey, path); err != nil {
			return err
		}
	}
	defer func() {
		if path != record.Path {
			_ = os.Remove(segments.BloomPath(path))
		}
	}()
	built, err := u.blooms.Build(ctx, record.Stream, path)
	if err != nil || !built {
		return err
	}
	uploadCtx, cancel := context.WithTimeout(ctx, perSegmentTimeout)
	defer cancel()
	if err := u.store.Upload(uploadCtx, record.ObjectKey+segments.BloomSuffix, segments.BloomPath(path)); err != nil {
		return err
	}
	return u.segmentRepo.MarkTokenBloom(record.ID)
}

// PendingCount returns how many segments still await upload.
func (u *LogUploader) PendingCount(limit int) (int, error) {
	pending, err := u.segmentRepo.ListPendingUploadForLocalWriter(u.stream, limit)
	if err != nil {
		return 0, fmt.Errorf("failed to list pending segments: %w", err)
	}
	return len(pending), nil
}

// ReconcileResult breaks down ReconcileSegments, so callers can report an
// unrecoverable loss loudly.
type ReconcileResult struct {
	Verified      int // local copy gone, remote copy present - nothing to do
	Failures      int // transient (network, etc.) - worth retrying next pass
	Unrecoverable int // both local and remote copies gone - nothing to restore from
}

// ReconcileSegments checks up to limit uploaded segments (newest upload
// first) whose local copy is gone, for the loss nothing else catches: the
// remote copy gone too (Unrecoverable). It doesn't restore local copies:
// queries read through the cache and compaction downloads what it merges.
func (u *LogUploader) ReconcileSegments(ctx context.Context, limit int) (ReconcileResult, error) {
	var result ReconcileResult

	uploaded, err := u.segmentRepo.ListUploaded(u.stream, limit)
	if err != nil {
		return result, fmt.Errorf("failed to list uploaded segments: %w", err)
	}

	var failureErrs []error
	var unrecoverableIDs []string

	for _, record := range uploaded {
		if _, err := os.Stat(record.Path); err == nil {
			continue // local copy present, nothing to reconcile
		}

		existsCtx, cancel := context.WithTimeout(ctx, perSegmentTimeout)
		exists, err := u.store.Exists(existsCtx, record.ObjectKey)
		cancel()
		if err != nil {
			result.Failures++
			failureErrs = append(failureErrs, fmt.Errorf("segment %s: checking existence: %w", record.ID, err))
			continue
		}
		if !exists {
			result.Unrecoverable++
			unrecoverableIDs = append(unrecoverableIDs, record.ID.String())
			continue
		}

		result.Verified++
	}

	if result.Unrecoverable > 0 {
		return result, fmt.Errorf(
			"%d segment(s) missing from BOTH local disk and object storage - unrecoverable data loss, ids: %v",
			result.Unrecoverable, unrecoverableIDs,
		)
	}
	if result.Failures > 0 {
		return result, fmt.Errorf(
			"failed to reconcile %d of %d segments: %w",
			result.Failures,
			len(uploaded),
			errors.Join(failureErrs...),
		)
	}
	return result, nil
}
