package segments

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"obzly/internal/storage"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
)

// Reconcile findings (`obzly reconcile`, ARCHITECTURE.md "Deployment and DR").
const (
	// The catalog row's object is gone and no local copy is left: the rows
	// are lost; Apply moves the row to lost_segments so queries skip it.
	FindingLost = "lost"
	// The object is gone but this process still has the file: Apply
	// uploads it again.
	FindingReuploadable = "reuploadable"
	// The object's size differs from the catalog's: needs a person.
	FindingSizeMismatch = "size-mismatch"
	// An object no catalog row knows, uploaded after AdoptSince (the
	// database restore point): Apply adds the row from the file. Without
	// AdoptSince, orphans are only reported.
	FindingOrphan = "orphan"
	// An object no catalog row knows, uploaded before AdoptSince: retired
	// by retention or compaction but never deleted (common for keys from
	// before deletes covered objects). Adopting it would bring retired data
	// back, so it's only reported.
	FindingOrphanOld = "orphan-old"
	// An orphan overlapping cataloged data of its project - probably a
	// compaction output whose inputs the restored catalog still lists.
	// Adopting it would double those rows, so it's only reported.
	FindingOrphanOverlapping = "orphan-overlapping"
	// An orphan that can't be adopted: unknown layout or stream, or an
	// unreadable file.
	FindingOrphanUnadoptable = "orphan-unadoptable"
)

// ReconcileOptions: what to check and whether to change anything.
type ReconcileOptions struct {
	Apply   bool
	Streams []string
	// Only these projects (all when empty).
	Projects []uuid.UUID
	// Orphans newer than this are skipped: an upload, or a compaction
	// deleting its inputs, may still be finishing.
	OrphanMinAge time.Duration
	// The database restore point: only orphans uploaded since may be
	// adopted. Zero: none are.
	AdoptSince time.Time
	// Where adopted segments' files are cached once read (LOGS_DATA_DIR).
	DataDir     string
	Concurrency int
}

type ReconcileFinding struct {
	Kind      string `json:"kind"`
	Stream    string `json:"stream"`
	SegmentID string `json:"segmentId,omitempty"`
	ObjectKey string `json:"objectKey"`
	Detail    string `json:"detail,omitempty"`
	Fixed     bool   `json:"fixed"`
}

type ReconcileReport struct {
	Applied       bool               `json:"applied"`
	RowsChecked   int                `json:"rowsChecked"`
	ObjectsListed int                `json:"objectsListed"`
	Findings      []ReconcileFinding `json:"findings"`
}

type orphanSegment struct {
	key       string
	stream    string
	id        uuid.UUID
	projectID uuid.UUID
	level     int
	size      int64
	modTime   time.Time
}

// Reconcile compares the catalog with object storage, both ways. Without
// Apply it only reports.
func Reconcile(ctx context.Context, store *ObjectStore, opts ReconcileOptions) (*ReconcileReport, error) {
	if len(opts.Streams) == 0 {
		opts.Streams = PartitionedStreams
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 16
	}
	report := &ReconcileReport{Applied: opts.Apply, Findings: []ReconcileFinding{}}
	var mu sync.Mutex
	add := func(finding ReconcileFinding) {
		mu.Lock()
		report.Findings = append(report.Findings, finding)
		mu.Unlock()
	}
	for _, stream := range opts.Streams {
		if err := reconcileRows(ctx, store, opts, stream, report, add); err != nil {
			return report, err
		}
		if err := reconcileObjects(ctx, store, opts, stream, report, add); err != nil {
			return report, err
		}
	}
	return report, nil
}

// reconcileRows checks every uploaded row's object.
func reconcileRows(
	ctx context.Context, store *ObjectStore, opts ReconcileOptions, stream string,
	report *ReconcileReport, add func(ReconcileFinding),
) error {
	var rows []SegmentRecord
	query := storage.GetDb().Where("stream = ? AND uploaded_at IS NOT NULL AND object_key <> ''", stream)
	if len(opts.Projects) > 0 {
		query = query.Where("project_id IN ?", opts.Projects)
	}
	if err := query.Find(&rows).Error; err != nil {
		return fmt.Errorf("failed to list %s segments: %w", stream, err)
	}
	report.RowsChecked += len(rows)
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(opts.Concurrency)
	for i := range rows {
		row := &rows[i]
		group.Go(func() error {
			size, found, err := store.Size(groupCtx, row.ObjectKey)
			if err != nil {
				return err
			}
			finding := ReconcileFinding{Stream: stream, SegmentID: row.ID.String(), ObjectKey: row.ObjectKey}
			switch {
			case found && size != row.SizeBytes:
				finding.Kind = FindingSizeMismatch
				finding.Detail = fmt.Sprintf("object %d bytes, catalog %d", size, row.SizeBytes)
			case found:
				return nil
			case fileExists(row.Path):
				finding.Kind = FindingReuploadable
				if opts.Apply {
					if err := store.Upload(groupCtx, row.ObjectKey, row.Path); err != nil {
						finding.Detail = err.Error()
					} else {
						finding.Fixed = true
					}
				}
			default:
				finding.Kind = FindingLost
				finding.Detail = fmt.Sprintf("%d rows, %s to %s", row.RowCount,
					row.MinTime.UTC().Format(time.RFC3339), row.MaxTime.UTC().Format(time.RFC3339))
				if opts.Apply {
					if err := markLost(row, "object missing"); err != nil {
						return err
					}
					finding.Fixed = true
				}
			}
			add(finding)
			return nil
		})
	}
	return group.Wait()
}

// reconcileObjects finds objects no catalog row knows and adopts those it
// safely can.
func reconcileObjects(
	ctx context.Context, store *ObjectStore, opts ReconcileOptions, stream string,
	report *ReconcileReport, add func(ReconcileFinding),
) error {
	known, err := knownSegmentIDs(stream)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-opts.OrphanMinAge)
	var orphans []orphanSegment
	prefixes := []string{stream + "/"}
	if len(opts.Projects) > 0 {
		prefixes = prefixes[:0]
		for _, project := range opts.Projects {
			prefixes = append(prefixes, stream+"/"+project.String()+"/")
		}
	}
	visit := func(object ObjectInfo) error {
		report.ObjectsListed++
		orphan, ok := parseObjectKey(object.Key)
		if ok && known[orphan.id] {
			return nil
		}
		if object.ModTime.After(cutoff) {
			return nil
		}
		if !ok || orphan.stream != stream {
			add(ReconcileFinding{Kind: FindingOrphanUnadoptable, Stream: stream, ObjectKey: object.Key,
				Detail: "not a segment key"})
			return nil
		}
		orphan.size, orphan.modTime = object.Size, object.ModTime
		orphans = append(orphans, orphan)
		return nil
	}
	for _, prefix := range prefixes {
		if err := store.List(ctx, prefix, visit); err != nil {
			return err
		}
	}
	for _, orphan := range orphans {
		switch {
		case opts.AdoptSince.IsZero():
			add(ReconcileFinding{Kind: FindingOrphan, Stream: stream, SegmentID: orphan.id.String(),
				ObjectKey: orphan.key, Detail: "uploaded " + orphan.modTime.UTC().Format(time.RFC3339) +
					"; pass the restore point (--adopt-since) to adopt"})
		case orphan.modTime.Before(opts.AdoptSince):
			add(ReconcileFinding{Kind: FindingOrphanOld, Stream: stream, SegmentID: orphan.id.String(),
				ObjectKey: orphan.key, Detail: "uploaded " + orphan.modTime.UTC().Format(time.RFC3339)})
		default:
			add(adoptOrphan(ctx, store, opts, orphan))
		}
	}
	return nil
}

func adoptOrphan(
	ctx context.Context,
	store *ObjectStore,
	opts ReconcileOptions,
	orphan orphanSegment,
) ReconcileFinding {
	finding := ReconcileFinding{Kind: FindingOrphan, Stream: orphan.stream, SegmentID: orphan.id.String(),
		ObjectKey: orphan.key}
	timeColumn, ok := CompactionTimeColumn[orphan.stream]
	if !ok {
		finding.Kind, finding.Detail = FindingOrphanUnadoptable, "no row time column for this stream"
		return finding
	}
	record, err := describeOrphan(ctx, store, opts, orphan, timeColumn)
	if err != nil {
		finding.Kind, finding.Detail = FindingOrphanUnadoptable, err.Error()
		return finding
	}
	finding.Detail = fmt.Sprintf("%d rows, %s to %s", record.RowCount,
		record.MinTime.Format(time.RFC3339), record.MaxTime.Format(time.RFC3339))
	var overlapping int64
	if err := storage.GetDb().Model(&SegmentRecord{}).
		Where("project_id = ? AND stream = ? AND min_time <= ? AND max_time >= ?",
			record.ProjectID, record.Stream, record.MaxTime, record.MinTime).
		Count(&overlapping).Error; err != nil {
		finding.Kind, finding.Detail = FindingOrphanUnadoptable, err.Error()
		return finding
	}
	if overlapping > 0 {
		finding.Kind = FindingOrphanOverlapping
		finding.Detail += fmt.Sprintf("; overlaps %d cataloged segment(s)", overlapping)
		return finding
	}
	if opts.Apply {
		if err := storage.GetDb().Create(record).Error; err != nil {
			finding.Detail += "; " + err.Error()
			return finding
		}
		finding.Fixed = true
	}
	return finding
}

// describeOrphan reads an orphan's rows, time range and stats into the
// catalog row it would get. The download stays as the local cache copy.
func describeOrphan(
	ctx context.Context, store *ObjectStore, opts ReconcileOptions, orphan orphanSegment, timeColumn string,
) (*SegmentRecord, error) {
	path := filepath.Join(opts.DataDir, orphan.stream, orphan.projectID.String(), orphan.id.String()+".parquet")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := store.Download(ctx, orphan.key, path); err != nil {
		return nil, err
	}
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	var rows int64
	var minTime, maxTime sql.NullTime
	if err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*), min("%s"), max("%s") FROM read_parquet(?)`,
		timeColumn, timeColumn), path).Scan(&rows, &minTime, &maxTime); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("unreadable: %w", err)
	}
	if rows == 0 || !minTime.Valid {
		_ = os.Remove(path)
		return nil, errors.New("no rows")
	}
	stats, err := computeStats(db, orphan.stream, "read_parquet(?)", []any{path})
	if err != nil {
		return nil, err
	}
	uploaded := orphan.modTime.UTC()
	return &SegmentRecord{
		ID: orphan.id, ProjectID: orphan.projectID, Stream: orphan.stream, Path: path,
		RowCount: rows, SizeBytes: orphan.size, RawSizeBytes: orphan.size,
		MinTime: minTime.Time.UTC(), MaxTime: maxTime.Time.UTC(), CreatedAt: uploaded,
		UploadedAt: &uploaded, ObjectKey: orphan.key,
		// v2 and v3 only differ by columns v2 files read as NULL; an orphan
		// is recent, never v1.
		SchemaVersion: CurrentSchemaVersion,
		Level:         orphan.level, Stats: stats,
	}, nil
}

// markLost moves a row to lost_segments in one transaction.
func markLost(row *SegmentRecord, reason string) error {
	return storage.GetDb().Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO lost_segments
			(id, project_id, stream, object_key, row_count, size_bytes, min_time, max_time, reason)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (id) DO NOTHING`,
			row.ID, row.ProjectID, row.Stream, row.ObjectKey, row.RowCount, row.SizeBytes,
			row.MinTime, row.MaxTime, reason).Error; err != nil {
			return err
		}
		return tx.Where("stream = ? AND id = ?", row.Stream, row.ID).Delete(&SegmentRecord{}).Error
	})
}

func knownSegmentIDs(stream string) (map[uuid.UUID]bool, error) {
	var ids []uuid.UUID
	if err := storage.GetDb().Raw(`SELECT id FROM stream_segments WHERE stream = ?
		UNION ALL SELECT id FROM lost_segments WHERE stream = ?`, stream, stream).Scan(&ids).Error; err != nil {
		return nil, fmt.Errorf("failed to list %s segment ids: %w", stream, err)
	}
	known := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		known[id] = true
	}
	return known, nil
}

// parseObjectKey reads an upload key: <stream>/<project>/<yyyy>/<mm>/<dd>/
// <level>/<id>.parquet, or the older <stream>/<project>/<id>.parquet.
func parseObjectKey(key string) (orphanSegment, bool) {
	parts := strings.Split(key, "/")
	if (len(parts) != 7 && len(parts) != 3) || !strings.HasSuffix(key, ".parquet") {
		return orphanSegment{}, false
	}
	projectID, err := uuid.Parse(parts[1])
	if err != nil {
		return orphanSegment{}, false
	}
	id, err := uuid.Parse(strings.TrimSuffix(parts[len(parts)-1], ".parquet"))
	if err != nil {
		return orphanSegment{}, false
	}
	segment := orphanSegment{key: key, stream: parts[0], id: id, projectID: projectID}
	if len(parts) == 7 {
		if _, err := fmt.Sscanf(parts[5], "%d", &segment.level); err != nil {
			return orphanSegment{}, false
		}
	}
	return segment, true
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
