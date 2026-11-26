package segments

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"obzly/internal/util/logger"
)

// ErrMergeNotPublished: a merge's upload failed, so it was discarded and its
// sources kept; the next pass merges them again.
var ErrMergeNotPublished = errors.New("merged segment not uploaded")

// Publisher uploads a merged segment before it replaces its sources: a worker's
// disk is not durable (an emptyDir, a spot node), and queriers can't read it.
type Publisher interface {
	// Publish uploads a segment with no catalog row yet and fills in its
	// object key, upload time and stats as MarkUploaded would.
	Publish(ctx context.Context, record *SegmentRecord) error
	// Published finishes once its row exists (notify, reclaim local disk).
	Published(record *SegmentRecord) error
}

// CommitMerge replaces sources with merged in the catalog, then deletes the
// sources' data with discard. With a publisher, merged is uploaded first: the
// sources were deleted at once while the merge lived only on this worker until
// a later upload, and a restart in between lost its rows. False when the merge
// was discarded: a source was deleted meanwhile, or (ErrMergeNotPublished) the
// upload failed.
func CommitMerge(
	ctx context.Context, repo *SegmentRepository, publisher Publisher, discard func([]*SegmentRecord) error,
	sources []*SegmentRecord, merged *SegmentRecord,
) (bool, error) {
	if merged.ID == uuid.Nil {
		merged.ID = uuid.New()
	}
	if publisher != nil {
		if err := publisher.Publish(ctx, merged); err != nil {
			return false, errors.Join(
				fmt.Errorf("%w: %w", ErrMergeNotPublished, err),
				discard([]*SegmentRecord{merged}),
			)
		}
	}

	ids := make([]uuid.UUID, len(sources))
	for i, source := range sources {
		ids[i] = source.ID
	}
	if err := repo.ReplaceSegments(ids, merged); err != nil {
		// Discard the merge; its rows are either still in the sources or were
		// deliberately deleted.
		if cleanupErr := discard([]*SegmentRecord{merged}); cleanupErr != nil {
			return false, fmt.Errorf("failed to discard merged segment: %w", cleanupErr)
		}
		if errors.Is(err, ErrSegmentsGone) {
			return false, nil
		}
		return false, fmt.Errorf("failed to record merged segment: %w", err)
	}

	if publisher != nil {
		if err := publisher.Published(merged); err != nil {
			logger.GetLogger().Warn("Merged segment is uploaded and recorded, but finishing up failed",
				"segmentId", merged.ID, "error", err)
		}
	}
	if err := discard(sources); err != nil {
		return true, fmt.Errorf("failed to clean up merged-away segments: %w", err)
	}
	return true, nil
}

// UploadVerified uploads record's file to key and checks it landed: a nil
// Upload doesn't prove the object is retrievable (silent provider failures,
// eventual consistency).
func UploadVerified(
	ctx context.Context, store *ObjectStore, key string, record *SegmentRecord, existsTimeout time.Duration,
) error {
	uploadCtx, cancel := context.WithTimeout(ctx, TransferTimeout(record.SizeBytes))
	defer cancel()
	if err := store.Upload(uploadCtx, key, record.Path); err != nil {
		return err
	}

	existsCtx, cancel := context.WithTimeout(ctx, existsTimeout)
	defer cancel()
	exists, err := store.Exists(existsCtx, key)
	if err != nil {
		return fmt.Errorf("uploaded but failed to verify it landed: %w", err)
	}
	if !exists {
		return fmt.Errorf(
			"uploaded %q but a follow-up existence check found nothing there - not marking durable, will retry next pass",
			key,
		)
	}
	return nil
}
