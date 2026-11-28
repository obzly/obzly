package segments

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// UploadedCheck breaks down VerifyUploaded, so callers can report an
// unrecoverable loss loudly.
type UploadedCheck struct {
	Verified      int // local copy gone, remote copy present - nothing to do
	Failures      int // transient (network, etc.) - worth retrying next pass
	Unrecoverable int // both copies gone while the row remains - nothing to restore from
}

// VerifyUploaded checks uploaded segments whose local copy is gone for the
// loss nothing else catches: the remote copy gone too. A merge or retention
// deletes the row before the files, so a segment whose row is gone by the
// time its object is found missing was merged away, not lost.
func VerifyUploaded(
	ctx context.Context, store *ObjectStore, repo *SegmentRepository, uploaded []*SegmentRecord, timeout time.Duration,
) (UploadedCheck, error) {
	var result UploadedCheck
	var failureErrs []error
	var unrecoverableIDs []string

	for _, record := range uploaded {
		if _, err := os.Stat(record.Path); err == nil {
			continue
		}

		existsCtx, cancel := context.WithTimeout(ctx, timeout)
		exists, err := store.Exists(existsCtx, record.ObjectKey)
		cancel()
		if err == nil && !exists {
			var cataloged bool
			cataloged, err = repo.Exists(record.ID)
			if err == nil && !cataloged {
				continue
			}
		}
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
			"failed to reconcile %d of %d segments: %w", result.Failures, len(uploaded), errors.Join(failureErrs...),
		)
	}
	return result, nil
}
