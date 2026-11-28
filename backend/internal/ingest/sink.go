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

// uploadTimeout bounds one segment upload.
const uploadTimeout = 2 * time.Minute

const (
	// AckWait is the shard consumers' ack wait. A batch held longer (up to
	// INGEST_L0_MAX_AGE for a quiet project) is kept alive with InProgress
	// every ackProgressEvery. It used to be the max-age plus 10 minutes: a
	// delivery lost while a broker restarted then stayed unseen for 70
	// minutes.
	AckWait          = 5 * time.Minute
	ackProgressEvery = 2 * time.Minute
	// laneIdleFlush: a buffer only extra lanes carry, with no batch for this
	// long, lost its lane (the lane count came down) and is flushed rather
	// than held until max-age.
	laneIdleFlush = time.Minute
	// queuedFlushes: flushed batches of a project waiting for their upload
	// before its next flush waits. Uploads run beside the shard's fetch
	// loop; in the homelab a metrics shard spent a fifth of its time
	// blocked on them (4-5 s per 3.6 MB segment).
	queuedFlushes = 2
	// concurrentUploads: segment uploads one sink runs at once.
	concurrentUploads = 8
)

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

	// unacked: batches written whose ack JetStream hasn't confirmed,
	// retried every tick until it does or, after AckWait, has redelivered
	// them anyway.
	ackMu      sync.Mutex
	unacked    []unconfirmedAck
	ackFailing bool

	uploads chan struct{}
}

type unconfirmedAck struct {
	ack   broker.Ack
	since time.Time
}

// projectBuffer is one project's batches on this worker since its last
// flush. With lanes (docs/INGEST_LANES.md) several of this worker's shards
// can carry the project, and its rows share one writer buffer, so one
// project buffer serves them all: mu serializes them, shards lists them.
// A batch's rows are appended and its ack added under mu, as a flush takes
// the acks and flushes the writer, so a flush covers exactly the batches
// whose rows it writes.
type projectBuffer struct {
	mu     sync.Mutex
	shards map[int]bool
	bytes  int64
	oldest time.Time
	// newest: when the last batch arrived; progressed: when the held
	// batches' ack wait was last restarted.
	newest     time.Time
	progressed time.Time
	acks       []broker.Ack
	flushed    []flushedBatch
	// uploading: a background uploader owns flushed[0] and removes batches
	// as they are acked; nothing else uploads meanwhile. uploaded is
	// signalled (on mu) whenever it removes one or stops.
	uploading bool
	uploaded  *sync.Cond
	// gone: removed from the sink; a goroutine that finds it gone takes a
	// new one.
	gone bool
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
		uploads:     make(chan struct{}, concurrentUploads),
	}
}

// Handle buffers one message. Its rows are appended under the project
// buffer's lock: with lanes another shard's flush could otherwise write them
// before their ack is added, and a failed flush would drop them while the
// ack survives.
func (s *Sink) Handle(shard int, projectID uuid.UUID, data []byte, ack broker.Ack) error {
	buf := s.lockedBuffer(shard, projectID)
	defer buf.mu.Unlock()
	started := time.Now()
	err := s.appendRows(projectID, data)
	s.observe("append", started)
	if err != nil {
		if errors.Is(err, ErrUndecodable) {
			s.log.Error("Dropping undecodable batch", "project", projectID, "error", err)
			return ack.Term()
		}
		return err
	}

	now := time.Now().UTC()
	if len(buf.acks) == 0 {
		buf.oldest = now
		if len(buf.flushed) == 0 {
			buf.progressed = now
		}
	}
	buf.newest = now
	buf.bytes += int64(len(data))
	buf.acks = append(buf.acks, ack)
	s.held().Inc()

	if buf.bytes >= s.targetBytes {
		s.flush(projectID, buf)
		s.awaitUploads(projectID, buf)
	}
	return nil
}

// Tick flushes shard's projects past maxAge (or left by their lane),
// retries failed uploads, and keeps held batches from being redelivered.
func (s *Sink) Tick(_ context.Context, shard int) {
	s.retryAcks()
	now := time.Now().UTC()
	cutoff := now.Add(-s.maxAge)
	for projectID, buf := range s.buffers() {
		buf.mu.Lock()
		if buf.gone || !buf.shards[shard] {
			buf.mu.Unlock()
			continue
		}
		if len(buf.acks) > 0 && (buf.oldest.Before(cutoff) || laneLeft(projectID, buf, now)) {
			s.flush(projectID, buf)
		} else {
			s.startUpload(projectID, buf)
		}
		if len(buf.acks) == 0 && len(buf.flushed) == 0 && !buf.uploading {
			s.forget(projectID, buf)
		} else if now.Sub(buf.progressed) >= ackProgressEvery {
			for _, ack := range buf.acks {
				_ = ack.InProgress()
			}
			for _, batch := range buf.flushed {
				for _, ack := range batch.acks {
					_ = ack.InProgress()
				}
			}
			buf.progressed = now
		}
		buf.mu.Unlock()
	}
}

// laneLeft: buf is carried only by extra lanes of the project and has had
// no batch for laneIdleFlush. A busy project's lanes get batches round-robin
// all the time; an idle one was dropped, and its last batches would wait
// for max-age (an hour by default) under target bytes.
func laneLeft(projectID uuid.UUID, buf *projectBuffer, now time.Time) bool {
	return !buf.shards[broker.ShardFor(projectID)] && now.Sub(buf.newest) >= laneIdleFlush
}

// Release flushes and uploads everything in shard. Batches whose upload
// still fails stay unacked and are redelivered after AckWait.
//
// A project buffer another of this worker's shards still carries (lanes) is
// flushed too, acking that shard's batches as well, and kept for it.
func (s *Sink) Release(_ context.Context, shard int) {
	s.retryAcks()
	for projectID, buf := range s.buffers() {
		buf.mu.Lock()
		if buf.gone || !buf.shards[shard] {
			buf.mu.Unlock()
			continue
		}
		if len(buf.acks) > 0 {
			s.flush(projectID, buf)
		}
		for buf.uploading {
			buf.uploaded.Wait()
		}
		if len(buf.flushed) > 0 {
			s.uploadFlushed(projectID, buf)
		}
		delete(buf.shards, shard)
		if len(buf.shards) > 0 {
			buf.mu.Unlock()
			continue
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
		s.forget(projectID, buf)
		buf.mu.Unlock()
	}
}

// flush writes the project's rows in every stream to segments and starts
// their upload; batches are acked once it is done.
func (s *Sink) flush(projectID uuid.UUID, buf *projectBuffer) {
	acks := buf.acks
	buf.acks, buf.bytes, buf.oldest = nil, 0, time.Time{}
	started := time.Now()
	defer s.observe("flush", started)

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
	s.startUpload(projectID, buf)
}

// awaitUploads holds the caller (a shard's fetch loop) while more than
// queuedFlushes batches wait for their upload. With uploads failing nothing
// runs in the background, and it retries them itself as before.
func (s *Sink) awaitUploads(projectID uuid.UUID, buf *projectBuffer) {
	started := time.Now()
	waited := false
	for len(buf.flushed) > queuedFlushes {
		if !buf.uploading {
			s.uploadFlushed(projectID, buf)
			break
		}
		waited = true
		buf.uploaded.Wait()
	}
	if waited {
		s.observe("wait", started)
	}
}

// startUpload uploads buf's flushed batches in the background, unless that
// already runs. Call with buf.mu held.
func (s *Sink) startUpload(projectID uuid.UUID, buf *projectBuffer) {
	if buf.uploading || len(buf.flushed) == 0 {
		return
	}
	buf.uploading = true
	go s.uploadInBackground(projectID, buf)
}

// uploadInBackground uploads and acks buf's flushed batches in order, with
// buf.mu released while it waits on object storage, and stops when none are
// left or an upload fails (the next tick starts it again).
func (s *Sink) uploadInBackground(projectID uuid.UUID, buf *projectBuffer) {
	buf.mu.Lock()
	defer func() {
		buf.uploading = false
		buf.uploaded.Broadcast()
		buf.mu.Unlock()
	}()
	for len(buf.flushed) > 0 {
		for len(buf.flushed[0].records) > 0 {
			record := buf.flushed[0].records[0]
			buf.mu.Unlock()
			err := s.upload(projectID, record)
			buf.mu.Lock()
			if err != nil {
				for _, ack := range buf.flushed[0].acks {
					_ = ack.InProgress()
				}
				return
			}
			buf.flushed[0].records = buf.flushed[0].records[1:]
		}
		acks := buf.flushed[0].acks
		buf.mu.Unlock()
		s.ackAll(acks)
		buf.mu.Lock()
		s.held().Sub(float64(len(acks)))
		buf.flushed = buf.flushed[1:]
		buf.uploaded.Broadcast()
	}
}

// uploadFlushed uploads pending segments in order, holding buf.mu, and acks
// each batch once all of its segments are durable. Only while no background
// upload runs.
func (s *Sink) uploadFlushed(projectID uuid.UUID, buf *projectBuffer) {
	for len(buf.flushed) > 0 {
		batch := &buf.flushed[0]
		for len(batch.records) > 0 {
			if err := s.upload(projectID, batch.records[0]); err != nil {
				for _, ack := range batch.acks {
					_ = ack.InProgress()
				}
				return
			}
			batch.records = batch.records[1:]
		}

		s.ackAll(batch.acks)
		s.held().Sub(float64(len(batch.acks)))
		buf.flushed = buf.flushed[1:]
	}
}

// upload makes one segment durable, at most concurrentUploads at once.
func (s *Sink) upload(projectID uuid.UUID, record *segments.SegmentRecord) error {
	s.uploads <- struct{}{}
	defer func() { <-s.uploads }()
	ctx, cancel := context.WithTimeout(context.Background(), uploadTimeout)
	defer cancel()
	started := time.Now()
	err := s.writers[record.Stream].Upload(ctx, record)
	s.observe("upload", started)
	if err != nil {
		s.log.Warn("Failed to upload L0 segment; retrying next tick",
			"project", projectID, "segment", record.ID, "error", err)
	}
	return err
}

// ackAll acks batches whose rows are durable, keeping those JetStream didn't
// confirm for the next tick.
func (s *Sink) ackAll(acks []broker.Ack) {
	now := time.Now().UTC()
	pending := make([]unconfirmedAck, len(acks))
	for i, ack := range acks {
		pending[i] = unconfirmedAck{ack: ack, since: now}
	}
	s.confirm(pending)
}

// retryAcks acks again the batches whose ack wasn't confirmed.
func (s *Sink) retryAcks() {
	s.ackMu.Lock()
	pending := s.unacked
	s.unacked = nil
	s.ackMu.Unlock()
	if len(pending) > 0 {
		s.confirm(pending)
	}
}

func (s *Sink) confirm(pending []unconfirmedAck) {
	acks := make([]broker.Ack, len(pending))
	since := make(map[broker.Ack]time.Time, len(pending))
	for i, item := range pending {
		acks[i] = item.ack
		since[item.ack] = item.since
	}
	started := time.Now()
	unconfirmed := broker.AckAll(acks)
	s.observe("ack", started)
	cutoff := time.Now().UTC().Add(-AckWait)
	var kept []unconfirmedAck
	expired := 0
	for _, ack := range unconfirmed {
		if since[ack].Before(cutoff) {
			expired++
			continue
		}
		kept = append(kept, unconfirmedAck{ack: ack, since: since[ack]})
	}

	s.ackMu.Lock()
	defer s.ackMu.Unlock()
	switch {
	case len(kept) > 0 && !s.ackFailing:
		s.log.Warn("JetStream didn't confirm acks of written batches; retrying them", "messages", len(kept))
		s.ackFailing = true
	case len(unconfirmed) == 0 && s.ackFailing && len(s.unacked) == 0:
		s.log.Info("JetStream confirms acks again")
		s.ackFailing = false
	}
	if expired > 0 {
		s.log.Warn("Gave up acking written batches after AckWait; they are redelivered and written twice",
			"messages", expired)
	}
	s.unacked = append(s.unacked, kept...)
}

func (s *Sink) held() prometheus.Gauge {
	return telemetry.IngestHeldMessages.WithLabelValues(s.signal)
}

func (s *Sink) observe(step string, started time.Time) {
	telemetry.IngestStepDuration.WithLabelValues(s.signal, step).Observe(time.Since(started).Seconds())
}

// lockedBuffer is projectID's buffer, locked and carrying shard. s.mu is
// never held while taking a buffer's lock.
func (s *Sink) lockedBuffer(shard int, projectID uuid.UUID) *projectBuffer {
	for {
		s.mu.Lock()
		buf, ok := s.projects[projectID]
		if !ok {
			buf = &projectBuffer{shards: map[int]bool{}}
			buf.uploaded = sync.NewCond(&buf.mu)
			s.projects[projectID] = buf
		}
		s.mu.Unlock()
		buf.mu.Lock()
		if buf.gone {
			buf.mu.Unlock()
			continue
		}
		buf.shards[shard] = true
		return buf
	}
}

// buffers is every project buffer; callers check under each buffer's lock
// which shards it carries.
func (s *Sink) buffers() map[uuid.UUID]*projectBuffer {
	s.mu.Lock()
	defer s.mu.Unlock()
	buffers := map[uuid.UUID]*projectBuffer{}
	for projectID, buf := range s.projects {
		buffers[projectID] = buf
	}
	return buffers
}

// forget removes buf from the sink. Call with buf.mu held.
func (s *Sink) forget(projectID uuid.UUID, buf *projectBuffer) {
	buf.gone = true
	s.mu.Lock()
	if s.projects[projectID] == buf {
		delete(s.projects, projectID)
	}
	s.mu.Unlock()
}
