package segments

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"obzly/internal/telemetry"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"gocloud.dev/blob"
	"gocloud.dev/gcerrors"
	"golang.org/x/sync/errgroup"

	// fileblob (file:// URLs) needs no cloud SDK, so it is always available.
	// Cloud providers are blank-imported at the application entry point.
	_ "gocloud.dev/blob/fileblob"
)

const (
	opUpload   = "upload"
	opDownload = "download"
	opExists   = "exists"
	opDelete   = "delete"
	// opDeleteBatch is one S3 DeleteObjects request.
	opDeleteBatch = "delete_batch"
)

// S3 DeleteObjects takes at most 1,000 keys per request; other providers get
// this many parallel single deletes.
const (
	deleteBatchSize   = 1000
	deleteParallelism = 8
)

// ObjectStore wraps a gocloud.dev/blob.Bucket for the cold tier: segment
// uploads and on-demand downloads. The provider comes from NewObjectStore's
// URL ("s3://bucket", "gs://bucket", "azblob://container", "file:///path",
// "mem://").
type ObjectStore struct {
	bucket *blob.Bucket
	// s3Client and s3Bucket are set for s3:// buckets (AWS, MinIO), whose
	// DeleteBatch uses DeleteObjects - gocloud blob has no batch delete.
	s3Client *s3.Client
	s3Bucket string
}

func NewObjectStore(ctx context.Context, bucketURL string) (*ObjectStore, error) {
	bucket, err := blob.OpenBucket(ctx, bucketURL)
	if err != nil {
		return nil, fmt.Errorf("failed to open object storage bucket %q: %w", bucketURL, err)
	}
	store := &ObjectStore{bucket: bucket}
	if parsed, err := url.Parse(bucketURL); err == nil && parsed.Scheme == "s3" {
		var client *s3.Client
		if bucket.As(&client) {
			store.s3Client, store.s3Bucket = client, parsed.Host
		}
	}
	return store, nil
}

func (o *ObjectStore) Close() error {
	return o.bucket.Close()
}

// Upload streams localPath to the bucket under key; segments can be 128MB.
func (o *ObjectStore) Upload(ctx context.Context, key, localPath string) (err error) {
	defer observeObjectStoreOp(opUpload, time.Now(), &err)

	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("failed to open local file %q: %w", localPath, err)
	}
	defer func() { _ = file.Close() }()

	writer, err := o.bucket.NewWriter(ctx, key, writerOptions(key))
	if err != nil {
		return fmt.Errorf("failed to open object storage writer for %q: %w", key, err)
	}

	written, err := io.Copy(writer, file)
	if err != nil {
		_ = writer.Close()
		return fmt.Errorf("failed to upload %q: %w", key, err)
	}

	if err := writer.Close(); err != nil {
		return fmt.Errorf("failed to finalize upload of %q: %w", key, err)
	}

	telemetry.ObjectStoreBytes.WithLabelValues(opUpload).Add(float64(written))
	return nil
}

// Download streams key to localPath via a temp file renamed into place, so
// nothing reads a partial copy (compaction once read a segment mid-download).
// The .parquet.tmp name is swept by CleanupStaleTempFiles after a crash.
func (o *ObjectStore) Download(ctx context.Context, key, localPath string) (err error) {
	defer observeObjectStoreOp(opDownload, time.Now(), &err)

	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return fmt.Errorf("failed to create local directory for %q: %w", localPath, err)
	}

	reader, err := o.bucket.NewReader(ctx, key, nil)
	if err != nil {
		return fmt.Errorf("failed to open object storage reader for %q: %w", key, err)
	}
	defer func() { _ = reader.Close() }()

	file, err := os.CreateTemp(filepath.Dir(localPath), filepath.Base(localPath)+".*.parquet.tmp")
	if err != nil {
		return fmt.Errorf("failed to create local file for %q: %w", localPath, err)
	}
	tempPath := file.Name()
	defer func() {
		if err != nil {
			_ = file.Close()
			_ = os.Remove(tempPath)
		}
	}()

	read, err := io.Copy(file, reader)
	if err != nil {
		return fmt.Errorf("failed to download %q: %w", key, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("failed to write %q: %w", localPath, err)
	}
	if err := os.Rename(tempPath, localPath); err != nil {
		return fmt.Errorf("failed to place downloaded %q: %w", localPath, err)
	}

	telemetry.ObjectStoreBytes.WithLabelValues(opDownload).Add(float64(read))
	return nil
}

func (o *ObjectStore) Exists(ctx context.Context, key string) (bool, error) {
	start := time.Now()
	exists, err := o.bucket.Exists(ctx, key)
	observeObjectStoreOp(opExists, start, &err)
	if err != nil {
		return false, fmt.Errorf("failed to check existence of %q: %w", key, err)
	}
	return exists, nil
}

// ObjectInfo is one listed object.
type ObjectInfo struct {
	Key     string
	Size    int64
	ModTime time.Time
}

// ReadRange is length bytes of key from offset - a column chunk of a
// partially downloaded segment (partial.go).
func (o *ObjectStore) ReadRange(ctx context.Context, key string, offset, length int64) (data []byte, err error) {
	defer observeObjectStoreOp(opDownload, time.Now(), &err)
	reader, err := o.bucket.NewRangeReader(ctx, key, offset, length, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to open a range of %q: %w", key, err)
	}
	defer func() { _ = reader.Close() }()
	data, err = io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read a range of %q: %w", key, err)
	}
	telemetry.ObjectStoreBytes.WithLabelValues(opDownload).Add(float64(len(data)))
	return data, nil
}

// Size is key's size; found is false when it doesn't exist.
func (o *ObjectStore) Size(ctx context.Context, key string) (size int64, found bool, err error) {
	start := time.Now()
	attrs, err := o.bucket.Attributes(ctx, key)
	if err != nil && gcerrors.Code(err) == gcerrors.NotFound {
		return 0, false, nil
	}
	observeObjectStoreOp(opExists, start, &err)
	if err != nil {
		return 0, false, fmt.Errorf("failed to read attributes of %q: %w", key, err)
	}
	return attrs.Size, true, nil
}

// List calls fn for every object under prefix.
func (o *ObjectStore) List(ctx context.Context, prefix string, fn func(ObjectInfo) error) error {
	iter := o.bucket.List(&blob.ListOptions{Prefix: prefix})
	for {
		object, err := iter.Next(ctx)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("failed to list %q: %w", prefix, err)
		}
		if object.IsDir {
			continue
		}
		if err := fn(ObjectInfo{Key: object.Key, Size: object.Size, ModTime: object.ModTime}); err != nil {
			return err
		}
	}
}

// Delete removes key. An already-missing key (a retried delete, an upload
// that never finished) is not an error.
func (o *ObjectStore) Delete(ctx context.Context, key string) error {
	start := time.Now()
	err := o.bucket.Delete(ctx, key)
	if err != nil && gcerrors.Code(err) == gcerrors.NotFound {
		err = nil
	}
	observeObjectStoreOp(opDelete, start, &err)
	if err != nil {
		return fmt.Errorf("failed to delete %q: %w", key, err)
	}
	return nil
}

// DeleteBatch removes keys; already-missing ones are not an error, as with
// Delete. One DeleteObjects request per 1,000 keys on S3, parallel single
// deletes elsewhere.
func (o *ObjectStore) DeleteBatch(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	if o.s3Client != nil {
		for start := 0; start < len(keys); start += deleteBatchSize {
			if err := o.deleteObjects(ctx, keys[start:min(start+deleteBatchSize, len(keys))]); err != nil {
				return err
			}
		}
		return nil
	}

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(deleteParallelism)
	for _, key := range keys {
		group.Go(func() error { return o.Delete(groupCtx, key) })
	}
	return group.Wait()
}

// ObjectKeyFor builds a new upload's key:
// <stream>/<project_id>/<yyyy>/<mm>/<dd>/<level>/<segment_id>.parquet, dated by
// the segment's newest row (UTC), so a lifecycle rule on a date prefix only
// matches data at least that old. Older uploads keep the key their catalog row
// records.
func ObjectKeyFor(record *SegmentRecord) string {
	day := record.MaxTime.UTC()
	return fmt.Sprintf("%s/%s/%04d/%02d/%02d/%d/%s.parquet",
		record.Stream, record.ProjectID, day.Year(), int(day.Month()), day.Day(), record.Level, record.ID)
}

// observeObjectStoreOp takes err by pointer so a deferred call sees the
// function's final return value.
func observeObjectStoreOp(op string, start time.Time, err *error) {
	result := telemetry.ResultOK
	if *err != nil {
		result = telemetry.ResultError
	}
	telemetry.ObjectStoreRequests.WithLabelValues(op, result).Inc()
	telemetry.ObjectStoreDuration.WithLabelValues(op).Observe(time.Since(start).Seconds())
}

func (o *ObjectStore) deleteObjects(ctx context.Context, keys []string) (err error) {
	defer observeObjectStoreOp(opDeleteBatch, time.Now(), &err)

	objects := make([]s3types.ObjectIdentifier, len(keys))
	for i, key := range keys {
		objects[i] = s3types.ObjectIdentifier{Key: aws.String(key)}
	}
	out, err := o.s3Client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
		Bucket: aws.String(o.s3Bucket),
		Delete: &s3types.Delete{Objects: objects, Quiet: aws.Bool(true)},
	})
	if err != nil {
		return fmt.Errorf("failed to delete %d objects: %w", len(keys), err)
	}
	var failures []error
	for _, failure := range out.Errors {
		if aws.ToString(failure.Code) == "NoSuchKey" {
			continue
		}
		failures = append(failures, fmt.Errorf("%s: %s", aws.ToString(failure.Key), aws.ToString(failure.Message)))
	}
	if len(failures) > 0 {
		return fmt.Errorf("failed to delete %d of %d objects: %w", len(failures), len(keys), errors.Join(failures...))
	}
	return nil
}

// ObjectTags are a segment object's tags for S3 lifecycle rules, read from its
// key. A bucket allows 1,000 rules, so per-project schedules filter on tags,
// not prefixes. None for a key in another layout.
func ObjectTags(key string) map[string]string {
	parts := strings.Split(key, "/")
	if len(parts) != 7 || !strings.HasSuffix(parts[6], ".parquet") {
		return nil
	}
	return map[string]string{
		"obzly-stream":  parts[0],
		"obzly-project": parts[1],
		"obzly-level":   parts[5],
	}
}

// writerOptions tags an S3 upload with ObjectTags; other stores ignore it.
func writerOptions(key string) *blob.WriterOptions {
	tags := ObjectTags(key)
	if len(tags) == 0 {
		return nil
	}
	values := url.Values{}
	for name, value := range tags {
		values.Set(name, value)
	}
	tagging := values.Encode()
	return &blob.WriterOptions{BeforeWrite: func(as func(any) bool) error {
		var input *transfermanager.UploadObjectInput
		if as(&input) {
			input.Tagging = aws.String(tagging)
		}
		return nil
	}}
}
