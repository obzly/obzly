package segments

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"obzly/internal/telemetry"
	"obzly/internal/util/logger"
)

// ErrNotUploadedYet: the segment is on the disk of the replica that wrote it
// and hasn't reached object storage yet.
var ErrNotUploadedYet = errors.New("not uploaded yet by the replica that wrote it")

// resolveConcurrency: cold segments are fetched this many at a time. One by
// one, a week of them on a freshly started pod outlasted a 30 s query.
const resolveConcurrency = 8

// ResolveFiles maps each record to a locally readable file, downloading cold
// segments from store (nil for local-disk-only) into cacheDir.
//
// An unresolvable record is skipped rather than failing the query: the
// catalog can still reference a segment that is gone (an interrupted delete,
// a restored data directory), and losing its rows beats failing every query.
func ResolveFiles(ctx context.Context, store *ObjectStore, cacheDir string, records []*SegmentRecord) []ResolvedFile {
	return ResolveFilesReading(ctx, store, cacheDir, records, nil)
}

// ResolveFilesReading is ResolveFiles for a query reading only columns (nil:
// all); a large cold segment is fetched column by column (partial.go).
func ResolveFilesReading(
	ctx context.Context, store *ObjectStore, cacheDir string, records []*SegmentRecord, columns []string,
) []ResolvedFile {
	if len(records) > 0 {
		telemetry.QuerySegmentsPerRequest.WithLabelValues(records[0].Stream).Observe(float64(len(records)))
	}

	type resolved struct {
		path, source string
		err          error
	}
	results := make([]resolved, len(records))
	var wg sync.WaitGroup
	slots := make(chan struct{}, resolveConcurrency)
	for i, record := range records {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			path, source, err := resolveFileReading(ctx, store, cacheDir, record, columns)
			results[i] = resolved{path, source, err}
		}()
	}
	wg.Wait()

	files := make([]ResolvedFile, 0, len(records))
	for i, record := range records {
		path, source, err := results[i].path, results[i].source, results[i].err
		telemetry.QuerySegmentReads.WithLabelValues(record.Stream, source).Inc()
		if errors.Is(err, ErrNotUploadedYet) {
			// Expected for a few seconds after another replica writes a
			// segment; AppendTail serves its rows from JetStream meanwhile.
			logger.GetLogger().Debug("skipping a segment not uploaded yet",
				"stream", record.Stream, "segmentId", record.ID, "projectId", record.ProjectID)
			continue
		}
		if err != nil {
			logger.GetLogger().Error(
				"skipping unreadable segment",
				"stream", record.Stream, "segmentId", record.ID, "projectId", record.ProjectID, "error", err,
			)
			continue
		}
		telemetry.QueryScannedBytes.WithLabelValues(record.Stream).Add(float64(record.SizeBytes))
		files = append(files, ResolvedFile{Path: path, Legacy: record.IsLegacy()})
	}
	return files
}

// EnsureLocalFiles puts every record's file at its catalog path (the same on
// every replica), downloading ones another replica uploaded. False when one
// is neither local nor uploaded yet, so the caller skips it this pass.
func EnsureLocalFiles(ctx context.Context, store *ObjectStore, records []*SegmentRecord) (bool, error) {
	for _, record := range records {
		if _, err := os.Stat(record.Path); err == nil {
			continue
		}
		if store == nil || record.ObjectKey == "" {
			return false, nil
		}
		// Download is not atomic; the rename keeps a partial file from
		// passing for the segment. CleanupStaleTempFiles sweeps leftovers.
		tempPath := record.Path + ".tmp"
		if err := store.Download(ctx, record.ObjectKey, tempPath); err != nil {
			_ = os.Remove(tempPath)
			return false, fmt.Errorf("failed to download segment %s: %w", record.ID, err)
		}
		if err := os.Rename(tempPath, record.Path); err != nil {
			_ = os.Remove(tempPath)
			return false, fmt.Errorf("failed to place downloaded segment %s: %w", record.ID, err)
		}
	}
	return true, nil
}

// resolveFileReading is resolveFile, fetching a large cold segment column
// by column when columns are few (partial.go).
func resolveFileReading(
	ctx context.Context, store *ObjectStore, cacheDir string, record *SegmentRecord, columns []string,
) (string, string, error) {
	if columns != nil && store != nil && record.ObjectKey != "" {
		if _, err := os.Stat(record.Path); err != nil {
			whole := filepath.Join(cacheDir, record.Stream, record.ProjectID.String(), record.ID.String()+".parquet")
			if info, err := os.Stat(whole); err != nil || (record.SizeBytes > 0 && info.Size() != record.SizeBytes) {
				path, err := resolvePartial(ctx, store, cacheDir, record, columns)
				if err == nil {
					return path, telemetry.SourcePartial, nil
				}
				if !errors.Is(err, errWholeFile) {
					logger.GetLogger().Warn("column-only read failed - downloading the segment whole",
						"segmentId", record.ID, "error", err)
				}
			}
		}
	}
	return resolveFile(ctx, store, cacheDir, record)
}

func resolveFile(
	ctx context.Context,
	store *ObjectStore,
	cacheDir string,
	record *SegmentRecord,
) (string, string, error) {
	if _, err := os.Stat(record.Path); err == nil {
		return record.Path, telemetry.SourceLocal, nil
	}

	if store == nil {
		return "", telemetry.SourceMissing, fmt.Errorf(
			"segment %s is not on local disk and no object storage is configured to fetch it from",
			record.ID,
		)
	}
	if record.ObjectKey == "" {
		return "", telemetry.SourcePending, fmt.Errorf("segment %s: %w", record.ID, ErrNotUploadedYet)
	}

	cachePath := filepath.Join(cacheDir, record.Stream, record.ProjectID.String(), record.ID.String()+".parquet")
	if info, err := os.Stat(cachePath); err == nil {
		if record.SizeBytes <= 0 || info.Size() == record.SizeBytes {
			// mtime is the cache's recency (EvictColdCache): a file read
			// now must not be the first evicted while the query opens it.
			now := time.Now()
			_ = os.Chtimes(cachePath, now, now)
			return cachePath, telemetry.SourceCache, nil
		}
		// A copy cut short by a restart mid-download fails every query; refetch it.
		logger.GetLogger().Warn("replacing cached segment of the wrong size",
			"segmentId", record.ID, "size", info.Size(), "want", record.SizeBytes)
		_ = os.Remove(cachePath)
	}

	if err := store.Download(ctx, record.ObjectKey, cachePath); err != nil {
		return "", telemetry.SourceMissing, fmt.Errorf(
			"failed to download segment %s from object storage: %w", record.ID, err,
		)
	}

	return cachePath, telemetry.SourceObjectStore, nil
}
