// Package ingest is the signal-agnostic half of the JetStream ingest path:
// receivers publish encoded batches, and a worker's Sink writes them to L0
// segments and acks a batch only once its rows are in an uploaded, catalogued
// segment.
package ingest

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"obzly/internal/broker"
	"obzly/internal/storage/segments"
	"obzly/internal/telemetry"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
)

// uploadTimeout bounds the uploads of one flush.
const uploadTimeout = 2 * time.Minute

// ErrUndecodable marks a payload that redelivery can never fix.
var ErrUndecodable = errors.New("undecodable ingest payload")

// Writer is one stream's durable writer: it buffers rows in RAM until
// Flush, and never flushes on its own.
type Writer interface {
	Flush(projectID uuid.UUID) (*segments.SegmentRecord, error)
	Upload(ctx context.Context, record *segments.SegmentRecord) error
}

// AppendFunc decodes one message and appends its rows to the writers.
type AppendFunc func(projectID uuid.UUID, data []byte) error

// Sink is a broker.ShardHandler. A project's rows flush to L0 segments (one
// per writer with rows) at targetBytes of buffered payload or after maxAge.
type Sink struct {
	signal      string
	appendRows  AppendFunc
	writers     map[string]Writer
	targetBytes int64
	maxAge      time.Duration
	log         *slog.Logger

	mu       sync.Mutex
	projects map[uuid.UUID]*projectBuffer
}

// projectBuffer is touched only by its shard's goroutine; mu guards the map.
type projectBuffer struct {
	shard   int
	bytes   int64
	oldest  time.Time
	acks    []broker.Ack
	flushed []flushedBatch
}

// flushedBatch: segments written but not all uploaded yet, and the
// messages whose rows they hold.
type flushedBatch struct {
	records []*segments.SegmentRecord
	acks    []broker.Ack
}

// NewSink writes signal's messages through appendRows into writers, keyed
// by the stream name their segments carry.
func NewSink(
	signal string,
	appendRows AppendFunc,
	writers map[string]Writer,
	targetBytes int64,
	maxAge time.Duration,
	log *slog.Logger,
) *Sink {
	return &Sink{
		signal:      signal,
		appendRows:  appendRows,
		writers:     writers,
		targetBytes: targetBytes,
		maxAge:      maxAge,
		log:         log.With("signal", signal),
		projects:    map[uuid.UUID]*projectBuffer{},
	}
}

// Handle buffers one message.
func (s *Sink) Handle(shard int, projectID uuid.UUID, data []byte, ack broker.Ack) error {
	if err := s.appendRows(projectID, data); err != nil {
		if errors.Is(err, ErrUndecodable) {
			s.log.Error("Dropping undecodable batch", "project", projectID, "error", err)
			return ack.Term()
		}
		return err
	}

	buf := s.buffer(shard, projectID)
	if len(buf.acks) == 0 {
		buf.oldest = time.Now().UTC()
	}
	buf.bytes += int64(len(data))
	buf.acks = append(buf.acks, ack)
	s.held().Inc()

	if buf.bytes >= s.targetBytes {
		s.flush(projectID, buf)
	}
	return nil
}

// Tick flushes shard's projects past maxAge and retries failed uploads.
func (s *Sink) Tick(_ context.Context, shard int) {
	cutoff := time.Now().UTC().Add(-s.maxAge)
	for projectID, buf := range s.shardBuffers(shard) {
		if len(buf.acks) > 0 && buf.oldest.Before(cutoff) {
			s.flush(projectID, buf)
		} else if len(buf.flushed) > 0 {
			s.uploadFlushed(projectID, buf)
		}
		s.forgetIfEmpty(projectID, buf)
	}
}

// Release flushes and uploads everything in shard. Batches whose upload
// still fails stay unacked and are redelivered after AckWait.
func (s *Sink) Release(_ context.Context, shard int) {
	for projectID, buf := range s.shardBuffers(shard) {
		if len(buf.acks) > 0 {
			s.flush(projectID, buf)
		} else if len(buf.flushed) > 0 {
			s.uploadFlushed(projectID, buf)
		}
		if len(buf.flushed) > 0 {
			s.log.Warn("Releasing shard with segments not yet uploaded; their batches will be redelivered",
				"shard", shard, "project", projectID)
			held := 0
			for _, batch := range buf.flushed {
				held += len(batch.acks)
			}
			s.held().Sub(float64(held))
		}
		s.mu.Lock()
		delete(s.projects, projectID)
		s.mu.Unlock()
	}
}

// flush writes the project's rows in every stream to segments, then
// uploads and acks.
func (s *Sink) flush(projectID uuid.UUID, buf *projectBuffer) {
	acks := buf.acks
	buf.acks, buf.bytes, buf.oldest = nil, 0, time.Time{}

	var records []*segments.SegmentRecord
	for _, writer := range s.writers {
		record, err := writer.Flush(projectID)
		if err != nil {
			// The writer dropped these rows; leaving the messages unacked
			// redelivers them after AckWait (rows of another stream that
			// did flush are then written twice).
			s.log.Error("Failed to flush L0 segment; its batches will be redelivered",
				"project", projectID, "messages", len(acks), "error", err)
			s.held().Sub(float64(len(acks)))
			return
		}
		if record != nil {
			records = append(records, record)
		}
	}

	buf.flushed = append(buf.flushed, flushedBatch{records: records, acks: acks})
	s.uploadFlushed(projectID, buf)
}

// uploadFlushed uploads pending segments in order and acks each batch once
// all of its segments are durable.
func (s *Sink) uploadFlushed(projectID uuid.UUID, buf *projectBuffer) {
	ctx, cancel := context.WithTimeout(context.Background(), uploadTimeout)
	defer cancel()

	for len(buf.flushed) > 0 {
		batch := &buf.flushed[0]
		for len(batch.records) > 0 {
			record := batch.records[0]
			if err := s.writers[record.Stream].Upload(ctx, record); err != nil {
				s.log.Warn("Failed to upload L0 segment; retrying next tick",
					"project", projectID, "segment", record.ID, "error", err)
				for _, ack := range batch.acks {
					_ = ack.InProgress()
				}
				return
			}
			batch.records = batch.records[1:]
		}

		for _, ack := range batch.acks {
			if err := ack.Ack(); err != nil {
				s.log.Warn("Failed to ack ingest batch; it will be redelivered", "project", projectID, "error", err)
			}
		}
		s.held().Sub(float64(len(batch.acks)))
		buf.flushed = buf.flushed[1:]
	}
}

func (s *Sink) held() prometheus.Gauge {
	return telemetry.IngestHeldMessages.WithLabelValues(s.signal)
}

func (s *Sink) buffer(shard int, projectID uuid.UUID) *projectBuffer {
	s.mu.Lock()
	defer s.mu.Unlock()
	buf, ok := s.projects[projectID]
	if !ok {
		buf = &projectBuffer{shard: shard}
		s.projects[projectID] = buf
	}
	return buf
}

func (s *Sink) shardBuffers(shard int) map[uuid.UUID]*projectBuffer {
	s.mu.Lock()
	defer s.mu.Unlock()
	buffers := map[uuid.UUID]*projectBuffer{}
	for projectID, buf := range s.projects {
		if buf.shard == shard {
			buffers[projectID] = buf
		}
	}
	return buffers
}

func (s *Sink) forgetIfEmpty(projectID uuid.UUID, buf *projectBuffer) {
	if len(buf.acks) > 0 || len(buf.flushed) > 0 {
		return
	}
	s.mu.Lock()
	delete(s.projects, projectID)
	s.mu.Unlock()
}
