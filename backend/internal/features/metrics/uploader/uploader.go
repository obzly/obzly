// Package metrics_uploader is the metrics streams' cold-tier upload path,
// mirroring logs_uploader.
package metrics_uploader

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"obzly/internal/storage/segments"
)

// Streams: samples, their series and the rollups.
var Streams = []string{"metrics", "metrics_series", "metrics_5m", "metrics_1h"}

// perSegmentTimeout bounds one existence check so a hung call can't stall
// the whole batch; uploads get segments.TransferTimeout.
const perSegmentTimeout = 30 * time.Second

type MetricUploader struct {
	store       *segments.ObjectStore
	segmentRepo *segments.SegmentRepository

	// DeleteLocalAfterUpload reclaims local disk once a segment is durably
	// uploaded (off by default).
	DeleteLocalAfterUpload bool
}

func NewMetricUploader(store *segments.ObjectStore) *MetricUploader {
	return &MetricUploader{store: store, segmentRepo: &segments.SegmentRepository{}}
}

// UploadPending uploads up to limit not-yet-uploaded segments; one
// segment's failure doesn't stop the rest of the batch.
func (u *MetricUploader) UploadPending(ctx context.Context, limit int) (int, error) {
	pending, err := listPerStream(func(stream string) ([]*segments.SegmentRecord, error) {
		return u.segmentRepo.ListPendingUploadForLocalWriter(stream, limit)
	})
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
func (u *MetricUploader) Upload(ctx context.Context, record *segments.SegmentRecord) error {
	return u.uploadOne(ctx, record)
}

func (u *MetricUploader) uploadOne(ctx context.Context, record *segments.SegmentRecord) error {
	key := segments.ObjectKeyFor(record)
	if err := segments.UploadVerified(ctx, u.store, key, record, perSegmentTimeout); err != nil {
		return err
	}
	if err := u.segmentRepo.MarkUploaded(record.ID, key); err != nil {
		return fmt.Errorf("uploaded but failed to record it (will re-upload next pass): %w", err)
	}
	uploaded := *record
	uploaded.ObjectKey = key
	return u.Published(&uploaded)
}

// Publish uploads a segment before its catalog row exists (a merge, before
// it replaces its sources).
func (u *MetricUploader) Publish(ctx context.Context, record *segments.SegmentRecord) error {
	record.ObjectKey = segments.ObjectKeyFor(record)
	if err := segments.UploadVerified(ctx, u.store, record.ObjectKey, record, perSegmentTimeout); err != nil {
		return err
	}
	now := time.Now().UTC()
	record.UploadedAt = &now
	return nil
}

// Published tells queriers about an uploaded, recorded segment and frees its
// local disk if configured.
func (u *MetricUploader) Published(record *segments.SegmentRecord) error {
	segments.NotifyUploaded(record)
	if !u.DeleteLocalAfterUpload {
		return nil
	}
	if err := os.Remove(record.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("uploaded and recorded, but failed to reclaim local disk space: %w", err)
	}
	return nil
}

func (u *MetricUploader) PendingCount(limit int) (int, error) {
	pending, err := listPerStream(func(stream string) ([]*segments.SegmentRecord, error) {
		return u.segmentRepo.ListPendingUploadForLocalWriter(stream, limit)
	})
	if err != nil {
		return 0, fmt.Errorf("failed to list pending segments: %w", err)
	}
	return len(pending), nil
}

type ReconcileResult = segments.UploadedCheck

// ReconcileSegments checks that uploaded segments missing locally still
// exist in object storage, without downloading them.
func (u *MetricUploader) ReconcileSegments(ctx context.Context, limit int) (ReconcileResult, error) {
	uploaded, err := listPerStream(func(stream string) ([]*segments.SegmentRecord, error) {
		return u.segmentRepo.ListUploaded(stream, limit)
	})
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("failed to list uploaded segments: %w", err)
	}
	return segments.VerifyUploaded(ctx, u.store, u.segmentRepo, uploaded, perSegmentTimeout)
}

func listPerStream(list func(stream string) ([]*segments.SegmentRecord, error)) ([]*segments.SegmentRecord, error) {
	var all []*segments.SegmentRecord
	for _, stream := range Streams {
		records, err := list(stream)
		if err != nil {
			return nil, err
		}
		all = append(all, records...)
	}
	return all, nil
}
