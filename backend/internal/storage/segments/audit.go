package segments

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"obzly/internal/storage"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
)

// AuditOptions: how much of the uploaded catalog to sample.
type AuditOptions struct {
	// Percent of uploaded segments, at least one, at most MaxSamples.
	SamplePercent float64
	MaxSamples    int
	// Only these projects (all when empty).
	Projects []uuid.UUID
	// Where sampled objects are downloaded (removed afterwards).
	TempDir     string
	Concurrency int
}

// AuditFailure is a sampled segment object storage doesn't match.
type AuditFailure struct {
	SegmentID string `json:"segmentId"`
	Stream    string `json:"stream"`
	ObjectKey string `json:"objectKey"`
	// missing, size, row-count or unreadable.
	Problem string `json:"problem"`
	Detail  string `json:"detail,omitempty"`
}

type AuditResult struct {
	Sampled  int            `json:"sampled"`
	Failures []AuditFailure `json:"failures"`
}

// Audit checks a random sample of uploaded segments against object
// storage: the object exists, its size is the catalog's, and its row count
// is the catalog's. Read-only.
func Audit(ctx context.Context, store *ObjectStore, opts AuditOptions) (*AuditResult, error) {
	if opts.Concurrency <= 0 {
		opts.Concurrency = 4
	}
	base := storage.GetDb().Model(&SegmentRecord{}).Where("uploaded_at IS NOT NULL AND object_key <> ''")
	if len(opts.Projects) > 0 {
		base = base.Where("project_id IN ?", opts.Projects)
	}
	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, err
	}
	result := &AuditResult{Failures: []AuditFailure{}}
	if total == 0 {
		return result, nil
	}
	limit := int(math.Ceil(float64(total) * opts.SamplePercent / 100))
	limit = max(1, min(limit, opts.MaxSamples))
	var sample []SegmentRecord
	if err := base.Order("random()").Limit(limit).Find(&sample).Error; err != nil {
		return nil, err
	}
	result.Sampled = len(sample)

	failures := make([]*AuditFailure, len(sample))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(opts.Concurrency)
	for i := range sample {
		record := &sample[i]
		group.Go(func() error {
			failures[i] = auditSegment(groupCtx, store, opts.TempDir, record)
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	for _, failure := range failures {
		if failure != nil {
			result.Failures = append(result.Failures, *failure)
		}
	}
	return result, nil
}

func auditSegment(ctx context.Context, store *ObjectStore, tempDir string, record *SegmentRecord) *AuditFailure {
	fail := func(problem, detail string) *AuditFailure {
		return &AuditFailure{SegmentID: record.ID.String(), Stream: record.Stream, ObjectKey: record.ObjectKey,
			Problem: problem, Detail: detail}
	}
	size, found, err := store.Size(ctx, record.ObjectKey)
	switch {
	case err != nil:
		return fail("unreadable", err.Error())
	case !found:
		return fail("missing", "")
	case size != record.SizeBytes:
		return fail("size", fmt.Sprintf("object %d bytes, catalog %d", size, record.SizeBytes))
	}
	path := filepath.Join(tempDir, "audit-"+record.ID.String()+".parquet")
	defer func() { _ = os.Remove(path) }()
	if err := store.Download(ctx, record.ObjectKey, path); err != nil {
		return fail("unreadable", err.Error())
	}
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return fail("unreadable", err.Error())
	}
	defer func() { _ = db.Close() }()
	var rows int64
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM read_parquet(?)", path).Scan(&rows); err != nil {
		return fail("unreadable", err.Error())
	}
	if rows != record.RowCount {
		return fail("row-count", fmt.Sprintf("file %d rows, catalog %d", rows, record.RowCount))
	}
	return nil
}
