package ingest

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"obzly/internal/broker"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type countingWriter struct {
	mu      sync.Mutex
	flushes int
	// release, when set, holds every Upload until it is closed.
	release chan struct{}
	uploads int
}

// A buffer only an extra lane carries, idle past laneIdleFlush, is flushed at
// once instead of waiting for max-age: its lane was dropped.
func Test_SinkTick_ExtraLaneIdle_FlushesBeforeMaxAge(t *testing.T) {
	writer := &countingWriter{}
	sink := newTestSink(writer)
	projectID := uuid.New()
	extraLane := broker.LaneShard(projectID, 1)
	require.NoError(t, sink.Handle(extraLane, projectID, []byte("rows"), broker.Ack{}))

	sink.Tick(context.Background(), extraLane)
	require.Zero(t, writer.count(), "still getting batches")

	sink.projects[projectID].newest = time.Now().UTC().Add(-laneIdleFlush)
	sink.Tick(context.Background(), extraLane)

	require.Equal(t, 1, writer.count())
}

// The project's own shard (lane 0) waits for max-age however quiet it is.
func Test_SinkTick_ProjectsOwnShardIdle_WaitsForMaxAge(t *testing.T) {
	writer := &countingWriter{}
	sink := newTestSink(writer)
	projectID := uuid.New()
	shard := broker.ShardFor(projectID)
	require.NoError(t, sink.Handle(shard, projectID, []byte("rows"), broker.Ack{}))

	sink.projects[projectID].newest = time.Now().UTC().Add(-time.Hour / 2)
	sink.Tick(context.Background(), shard)

	require.Zero(t, writer.count())
}

// Held batches get their ack wait restarted every ackProgressEvery, well
// within AckWait.
func Test_SinkTick_BatchesHeldPastProgressInterval_RestartsTheirAckWait(t *testing.T) {
	require.Less(t, 2*ackProgressEvery, AckWait+time.Second, "one lost InProgress is survivable")
	sink := newTestSink(&countingWriter{})
	projectID := uuid.New()
	shard := broker.ShardFor(projectID)
	require.NoError(t, sink.Handle(shard, projectID, []byte("rows"), broker.Ack{}))
	held := time.Now().UTC().Add(-ackProgressEvery)
	sink.projects[projectID].progressed = held

	sink.Tick(context.Background(), shard)

	require.True(t, sink.projects[projectID].progressed.After(held))
}

// A slow upload doesn't hold the shard: batches keep being taken and
// flushed while up to queuedFlushes wait for theirs, the next flush waits,
// and every batch is acked once its segment is uploaded.
func Test_SinkHandle_UploadsSlow_KeepsTakingBatchesUntilTooManyWait(t *testing.T) {
	writer := &countingWriter{release: make(chan struct{})}
	sink := NewSink("logs", func(uuid.UUID, []byte) error { return nil },
		map[string]Writer{"logs": writer}, 1, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	projectID := uuid.New()
	shard := broker.ShardFor(projectID)

	for range queuedFlushes {
		require.NoError(t, sink.Handle(shard, projectID, []byte("rows"), broker.Ack{}))
	}
	require.Equal(t, queuedFlushes, writer.count(), "flushed while the first upload hangs")

	handled := make(chan struct{})
	go func() {
		_ = sink.Handle(shard, projectID, []byte("rows"), broker.Ack{})
		close(handled)
	}()
	select {
	case <-handled:
		t.Fatal("a flush beyond queuedFlushes waits for uploads")
	case <-time.After(200 * time.Millisecond):
	}

	close(writer.release)
	<-handled
	require.Eventually(t, func() bool {
		sink.projects[projectID].mu.Lock()
		defer sink.projects[projectID].mu.Unlock()
		return len(sink.projects[projectID].flushed) == 0
	}, 5*time.Second, 10*time.Millisecond, "every batch uploaded and acked")
	require.Equal(t, queuedFlushes+1, writer.uploadCount())
}

// Releasing a shard waits for its background uploads, so nothing is left
// unacked when another worker takes it.
func Test_SinkRelease_WaitsForBackgroundUploads(t *testing.T) {
	writer := &countingWriter{release: make(chan struct{})}
	sink := NewSink("logs", func(uuid.UUID, []byte) error { return nil },
		map[string]Writer{"logs": writer}, 1, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	projectID := uuid.New()
	shard := broker.ShardFor(projectID)
	require.NoError(t, sink.Handle(shard, projectID, []byte("rows"), broker.Ack{}))

	released := make(chan struct{})
	go func() {
		sink.Release(context.Background(), shard)
		close(released)
	}()
	select {
	case <-released:
		t.Fatal("released before the upload finished")
	case <-time.After(200 * time.Millisecond):
	}
	close(writer.release)
	<-released

	require.Equal(t, 1, writer.uploadCount())
	require.Empty(t, sink.buffers())
}

func newTestSink(writer *countingWriter) *Sink {
	return NewSink("logs", func(uuid.UUID, []byte) error { return nil },
		map[string]Writer{"logs": writer}, 1<<30, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func (w *countingWriter) Flush(projectID uuid.UUID) (*segments.SegmentRecord, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flushes++
	return &segments.SegmentRecord{ID: uuid.New(), ProjectID: projectID, Stream: "logs"}, nil
}

func (w *countingWriter) Upload(context.Context, *segments.SegmentRecord) error {
	if w.release != nil {
		<-w.release
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.uploads++
	return nil
}

func (w *countingWriter) uploadCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.uploads
}

func (w *countingWriter) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushes
}
