// Package spans_uploader is the spans stream's cold-tier upload path (see
// logs_uploader).
package spans_uploader

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"obzly/internal/storage/segments"
)

const streamSpans = "spans"

const perSegmentTimeout = 30 * time.Second

type SpanUploader struct {
	store       *segments.ObjectStore
	segmentRepo *segments.SegmentRepository

	// DeleteLocalAfterUpload reclaims local disk once a segment is durably
	// uploaded; off by default.
	DeleteLocalAfterUpload bool
}

func NewSpanUploader(store *segments.ObjectStore) *SpanUploader {
	return &SpanUploader{store: store, segmentRepo: &segments.SegmentRepository{}}
}

// UploadPending uploads up to limit not-yet-uploaded segments; one
// segment's failure does not stop the rest.
func (u *SpanUploader) UploadPending(ctx context.Context, limit int) (int, error) {
	pending, err := u.segmentRepo.ListPendingUploadForLocalWriter(streamSpans, limit)
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
func (u *SpanUploader) Upload(ctx context.Context, record *segments.SegmentRecord) error {
	return u.uploadOne(ctx, record)
}

func (u *SpanUploader) uploadOne(ctx context.Context, record *segments.SegmentRecord) error {
	key := segments.ObjectKeyFor(record)

	uploadCtx, cancel := context.WithTimeout(ctx, perSegmentTimeout)
	defer cancel()
	if err := u.store.Upload(uploadCtx, key, record.Path); err != nil {
		return err
	}

	// Verify the object landed before marking it durable (a successful
	// upload call does not guarantee it).
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
	segments.NotifyUploaded(&uploaded)

	if !u.DeleteLocalAfterUpload {
		return nil
	}

	if err := os.Remove(record.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("uploaded and recorded, but failed to reclaim local disk space: %w", err)
	}

	return nil
}

// PendingCount is how many segments (up to limit) await upload.
func (u *SpanUploader) PendingCount(limit int) (int, error) {
	pending, err := u.segmentRepo.ListPendingUploadForLocalWriter(streamSpans, limit)
	if err != nil {
		return 0, fmt.Errorf("failed to list pending segments: %w", err)
	}
	return len(pending), nil
}

type ReconcileResult struct {
	Verified      int
	Failures      int
	Unrecoverable int
}

// ReconcileSegments checks that uploaded segments missing locally still
// exist in object storage, and errors on any lost from both.
func (u *SpanUploader) ReconcileSegments(ctx context.Context, limit int) (ReconcileResult, error) {
	var result ReconcileResult

	uploaded, err := u.segmentRepo.ListUploaded(streamSpans, limit)
	if err != nil {
		return result, fmt.Errorf("failed to list uploaded segments: %w", err)
	}

	var failureErrs []error
	var unrecoverableIDs []string

	for _, record := range uploaded {
		if _, err := os.Stat(record.Path); err == nil {
			continue
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
