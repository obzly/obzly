package query

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

// UploadedSubject carries a notice for every segment uploaded to object
// storage, so the executor it belongs to (Owned) downloads it into its
// cache before anyone searches it - OpenObserve's download on ingest. Core
// NATS, best effort: a missed notice only means a query fetches the file.
const UploadedSubject = "obzly.segments.uploaded"

// prefetchQueue bounds notices waiting for download; more are dropped (a
// backlog this deep is older than what the warm-up already covers).
const prefetchQueue = 4096

type uploadNotice struct {
	ID        uuid.UUID `json:"id"`
	ProjectID uuid.UUID `json:"projectId"`
	Stream    string    `json:"stream"`
	ObjectKey string    `json:"objectKey"`
	SizeBytes int64     `json:"sizeBytes"`
	MinTime   time.Time `json:"minTime"`
	MaxTime   time.Time `json:"maxTime"`
	// TokenBloom: a token filter was uploaded with it, fetched too.
	TokenBloom bool `json:"tokenBloom,omitempty"`
}

// PublishUploaded announces record (uploaded under its ObjectKey) on conn.
func PublishUploaded(conn *nats.Conn, record *segments.SegmentRecord) {
	data, err := json.Marshal(uploadNotice{
		ID: record.ID, ProjectID: record.ProjectID, Stream: record.Stream, ObjectKey: record.ObjectKey,
		SizeBytes: record.SizeBytes, MinTime: record.MinTime, MaxTime: record.MaxTime,
		TokenBloom: record.Stats != nil && record.Stats.TokenBloom,
	})
	if err == nil {
		_ = conn.Publish(UploadedSubject, data)
	}
}

// ServePrefetch hands fetch every uploaded segment this executor (affinity)
// owns among registry's executors, one at a time, until ctx ends.
func ServePrefetch(
	ctx context.Context,
	conn *nats.Conn,
	registry *Registry,
	affinity string,
	fetch func(*segments.SegmentRecord),
	log *slog.Logger,
) error {
	queue := make(chan *segments.SegmentRecord, prefetchQueue)
	subscription, err := conn.Subscribe(UploadedSubject, func(message *nats.Msg) {
		var notice uploadNotice
		if json.Unmarshal(message.Data, &notice) != nil || notice.ObjectKey == "" {
			return
		}
		record := &segments.SegmentRecord{
			ID: notice.ID, ProjectID: notice.ProjectID, Stream: notice.Stream, ObjectKey: notice.ObjectKey,
			SizeBytes: notice.SizeBytes, MinTime: notice.MinTime, MaxTime: notice.MaxTime,
		}
		if notice.TokenBloom {
			record.Stats = &segments.SegmentStats{TokenBloom: true}
		}
		select {
		case queue <- record:
		default:
		}
	})
	if err != nil {
		return err
	}
	defer func() { _ = subscription.Unsubscribe() }()
	log.Info("Pre-fetching uploaded segments into the query cache")

	for {
		select {
		case <-ctx.Done():
			return nil
		case record := <-queue:
			if len(Owned([]*segments.SegmentRecord{record}, registry.Executors(), affinity)) == 1 {
				fetch(record)
			}
		}
	}
}
