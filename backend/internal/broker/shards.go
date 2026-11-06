package broker

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"obzly/internal/telemetry"
	"obzly/internal/util/leaderlock"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/valkey-io/valkey-go"
)

// Shard ownership: a lease per shard, renewed every tick. A worker that
// stops renewing loses its shards after leaseTicks ticks.
const (
	defaultShardTick = 5 * time.Second
	leaseTicks       = 6
	fetchMaxWait     = time.Second
	// readMaxBytes caps one fetch or direct get, above maxServerPayload so the
	// largest batch still fits, and well under the 64 MB a NATS client may have
	// pending: past it the broker cuts the connection mid-fetch and acks are lost.
	readMaxBytes = 12 << 20
	// maxFetchBatch / firstFetchBatch bound a fetch's message count, which
	// nats.go allocates channels for (FetchBytes asks for a million, 32 MB/call).
	maxFetchBatch   = 256
	firstFetchBatch = 16
	// minAssumedMessage: fetchBatchCap never sizes for smaller messages, so a
	// shard switching to MaxIngestPayload messages pulls at most 48 MB next fetch.
	minAssumedMessage = MaxIngestPayload / 4
	membersKeyBase    = "obzly:ingest:workers:"
	// poisonDeliveries / poisonAge: a batch delivered this many times and
	// published this long ago is terminated instead of redelivered forever (one
	// failing batch wedged its project). Both conditions, so neither a backlog
	// nor an outage under a day drops data. MaxDeliver stays unlimited: it counts
	// deliveries only and would give up within hours of an outage.
	poisonDeliveries = 50
	poisonAge        = 24 * time.Hour
)

// ShardHandler writes one signal's messages. A project lives in one shard
// and a shard runs on one goroutine, so calls for a project never overlap.
type ShardHandler interface {
	// Handle buffers one message's rows and acks it once they are durable.
	// An error naks it for redelivery.
	Handle(shard int, projectID uuid.UUID, data []byte, ack Ack) error
	// Tick flushes and uploads whatever is due in shard.
	Tick(ctx context.Context, shard int)
	// Release makes everything buffered for shard durable and acks it,
	// before another worker takes the shard.
	Release(ctx context.Context, shard int)
}

// Ack acknowledges one message later without holding its payload.
type Ack struct {
	conn  *nats.Conn
	reply string
}

// ShardRunner consumes one signal's ingest stream on this worker: it keeps
// a fair share of the shards (IngestShards / live workers), each with its
// own durable consumer.
type ShardRunner struct {
	client   *Client
	valkey   valkey.Client
	signal   string
	stream   string
	ackWait  time.Duration
	handler  ShardHandler
	log      *slog.Logger
	memberID string
	scope    string
	tick     time.Duration

	mu    sync.Mutex
	owned map[int]*ownedShard
}

// runners are this process's shard runners by signal (RegisterRunner).
var (
	runnersMu sync.RWMutex
	runners   = map[string]*ShardRunner{}
)

// ShardRunnerOption adjusts a ShardRunner.
type ShardRunnerOption func(*ShardRunner)

type ownedShard struct {
	lock   *leaderlock.Lock
	cancel context.CancelFunc
	done   chan struct{}
}

// WithLeaseScope namespaces the leases and membership (default: the
// signal), so tests sharing a Valkey don't contend.
func WithLeaseScope(scope string) ShardRunnerOption {
	return func(r *ShardRunner) { r.scope = scope }
}

// WithTick sets how often shards are rebalanced and handlers ticked.
func WithTick(tick time.Duration) ShardRunnerOption {
	return func(r *ShardRunner) { r.tick = tick }
}

// NewAck builds an Ack for a message's reply subject.
func NewAck(conn *nats.Conn, reply string) Ack {
	return Ack{conn: conn, reply: reply}
}

// Ack removes the message from the work-queue stream.
func (a Ack) Ack() error {
	if a.conn == nil {
		return nil
	}
	return a.conn.Publish(a.reply, []byte("+ACK"))
}

// Term removes a message that can never be written, without redelivery.
func (a Ack) Term() error {
	if a.conn == nil {
		return nil
	}
	return a.conn.Publish(a.reply, []byte("+TERM"))
}

// InProgress restarts the message's AckWait timer.
func (a Ack) InProgress() error {
	if a.conn == nil {
		return nil
	}
	return a.conn.Publish(a.reply, []byte("+WPI"))
}

// NewShardRunner consumes signal's stream. ackWait must exceed the longest
// time the handler holds a message (its max-age plus an upload).
func NewShardRunner(
	client *Client,
	valkeyClient valkey.Client,
	signal string,
	ackWait time.Duration,
	handler ShardHandler,
	log *slog.Logger,
	opts ...ShardRunnerOption,
) (*ShardRunner, error) {
	stream := ""
	for _, s := range IngestStreams {
		if s.Signal == signal {
			stream = s.Name
		}
	}
	if stream == "" {
		return nil, fmt.Errorf("no ingest stream for signal %q", signal)
	}
	runner := &ShardRunner{
		client:   client,
		valkey:   valkeyClient,
		signal:   signal,
		stream:   stream,
		ackWait:  ackWait,
		handler:  handler,
		log:      log.With("signal", signal),
		memberID: uuid.NewString(),
		scope:    signal,
		tick:     defaultShardTick,
		owned:    map[int]*ownedShard{},
	}
	for _, opt := range opts {
		opt(runner)
	}
	return runner, nil
}

// ConsumerName is the durable consumer of one shard.
func ConsumerName(signal string, shard int) string {
	return fmt.Sprintf("%s-shard-%02d", signal, shard)
}

// RegisterRunner makes runner answer OwnsProject for its signal: compaction
// follows the ingest shards (one shard key and one lease for both).
func RegisterRunner(runner *ShardRunner) {
	runnersMu.Lock()
	defer runnersMu.Unlock()
	runners[runner.signal] = runner
}

// OwnsProject reports whether this process holds projectID's shard for
// signal. sharded is false when no runner runs here (no JetStream): the
// caller falls back to its cluster-wide lease.
func OwnsProject(signal string, projectID uuid.UUID) (owns, sharded bool) {
	runnersMu.RLock()
	runner := runners[signal]
	runnersMu.RUnlock()
	if runner == nil {
		return false, false
	}
	return runner.isOwned(ShardFor(projectID)), true
}

// Run balances shards until ctx ends, then releases every owned shard
// (flush, upload, ack) and returns.
func (r *ShardRunner) Run(ctx context.Context) {
	ticker := time.NewTicker(r.tick)
	defer ticker.Stop()

	for {
		r.rebalance(ctx)
		select {
		case <-ctx.Done():
			r.releaseAll()
			return
		case <-ticker.C:
		}
	}
}

// OwnedShards is how many shards this worker holds.
func (r *ShardRunner) OwnedShards() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.owned)
}

func (r *ShardRunner) rebalance(ctx context.Context) {
	fair, err := r.fairShare(ctx)
	if err != nil {
		r.log.Warn("Failed to read ingest worker membership", "error", err)
		fair = 0 // keep what we hold, take nothing new
	}

	var lost []int
	for _, shard := range r.ownedShards() {
		r.mu.Lock()
		lock := r.owned[shard].lock
		r.mu.Unlock()
		held, err := lock.TryAcquire(ctx)
		if err != nil || !held {
			r.log.Warn("Lost ingest shard lease", "shard", shard, "error", err)
			lost = append(lost, shard)
		}
	}
	r.stopShards(lost, false)

	owned := r.ownedShards()
	if fair > 0 && len(owned) > fair {
		r.stopShards(owned[fair:], true)
		return
	}

	start := r.startOffset()
	for i := 0; i < IngestShards && len(r.ownedShards()) < fair; i++ {
		shard := (start + i) % IngestShards
		if r.isOwned(shard) {
			continue
		}
		lock := leaderlock.New(r.valkey, fmt.Sprintf("ingest-%s-shard-%d", r.scope, shard), r.leaseTTL())
		acquired, err := lock.TryAcquire(ctx)
		if err != nil {
			r.log.Warn("Failed to acquire ingest shard lease", "shard", shard, "error", err)
			return
		}
		if acquired {
			r.startShard(ctx, shard, lock)
		}
	}
}

// fairShare registers this worker and returns ceil(shards / live workers).
func (r *ShardRunner) fairShare(ctx context.Context) (int, error) {
	key := membersKeyBase + r.scope
	now := time.Now().UTC()
	cutoff := now.Add(-r.leaseTTL()).UnixMilli()
	results := r.valkey.DoMulti(ctx,
		r.valkey.B().Zadd().Key(key).ScoreMember().ScoreMember(float64(now.UnixMilli()), r.memberID).Build(),
		r.valkey.B().Zremrangebyscore().Key(key).Min("-inf").Max(strconv.FormatInt(cutoff, 10)).Build(),
		r.valkey.B().Zcard().Key(key).Build(),
	)
	for _, result := range results {
		if err := result.Error(); err != nil {
			return 0, err
		}
	}
	members, err := results[2].AsInt64()
	if err != nil {
		return 0, err
	}
	members = max(members, 1)
	return int((IngestShards + members - 1) / members), nil
}

// startOffset spreads workers' first picks so they don't all race for
// shard 0.
func (r *ShardRunner) startOffset() int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(r.memberID))
	return int(h.Sum32() % IngestShards)
}

func (r *ShardRunner) startShard(parent context.Context, shard int, lock *leaderlock.Lock) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	owned := &ownedShard{lock: lock, cancel: cancel, done: make(chan struct{})}
	r.mu.Lock()
	r.owned[shard] = owned
	telemetry.IngestOwnedShards.WithLabelValues(r.signal).Set(float64(len(r.owned)))
	r.mu.Unlock()

	go func() {
		defer close(owned.done)
		r.consumeShard(ctx, shard)
	}()
	r.log.Info("Took ingest shard", "shard", shard)
}

// stopShard ends consumption and waits for the handler's release; the
// lease is freed only when giving the shard away (not when it was lost).
func (r *ShardRunner) stopShard(shard int, releaseLease bool) {
	r.mu.Lock()
	owned := r.owned[shard]
	delete(r.owned, shard)
	telemetry.IngestOwnedShards.WithLabelValues(r.signal).Set(float64(len(r.owned)))
	r.mu.Unlock()
	if owned == nil {
		return
	}

	owned.cancel()
	<-owned.done
	if releaseLease {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = owned.lock.Release(ctx)
	}
	r.log.Info("Released ingest shard", "shard", shard)
}

func (r *ShardRunner) releaseAll() {
	r.stopShards(r.ownedShards(), true)
}

// stopShards stops shards in parallel: each waits for its in-flight fetch.
func (r *ShardRunner) stopShards(shards []int, releaseLease bool) {
	var wg sync.WaitGroup
	for _, shard := range shards {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.stopShard(shard, releaseLease)
		}()
	}
	wg.Wait()
}

// consumeShard fetches until ctx ends, then releases the shard's buffers.
func (r *ShardRunner) consumeShard(ctx context.Context, shard int) {
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		r.handler.Release(releaseCtx, shard)
	}()

	consumer, err := r.ensureConsumer(ctx, shard)
	for err != nil {
		r.log.Warn("Failed to set up ingest shard consumer", "shard", shard, "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.tick):
		}
		consumer, err = r.ensureConsumer(ctx, shard)
	}

	lastTick := time.Now()
	size := firstFetchBatch
	largest := 0
	for ctx.Err() == nil {
		batch, err := consumer.Fetch(size, jetstream.FetchMaxWait(fetchMaxWait))
		if err != nil {
			r.log.Warn("Failed to fetch from ingest shard", "shard", shard, "error", err)
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		} else {
			count, bytes, fetchLargest := 0, 0, 0
			for msg := range batch.Messages() {
				count++
				bytes += len(msg.Data())
				fetchLargest = max(fetchLargest, len(msg.Data()))
				r.handle(shard, msg)
			}
			// Halving lets the cap recover once large messages stop.
			largest = max(fetchLargest, largest/2)
			size = min(nextFetchBatch(size, count, bytes), fetchBatchCap(largest))
			if err := batch.Error(); err != nil && !errors.Is(err, nats.ErrTimeout) {
				r.log.Warn("Ingest shard fetch ended with an error", "shard", shard, "error", err)
			}
		}

		if time.Since(lastTick) >= r.tick {
			r.handler.Tick(ctx, shard)
			lastTick = time.Now()
		}
	}
}

// nextFetchBatch sizes the next fetch so it brings about readMaxBytes, from
// the average message the last one brought; an empty fetch keeps it.
func nextFetchBatch(current, count, bytes int) int {
	if count == 0 || bytes == 0 {
		return current
	}
	return min(maxFetchBatch, max(1, readMaxBytes/(bytes/count+1)))
}

// fetchBatchCap bounds a fetch by the largest recent message: sizing from the
// average alone let a shard pull 256 x 1 MB messages at once.
func fetchBatchCap(largest int) int {
	return max(1, readMaxBytes/max(largest, minAssumedMessage))
}

func (r *ShardRunner) handle(shard int, msg jetstream.Msg) {
	projectID, err := ProjectFromSubject(msg.Subject())
	if err != nil {
		// Nothing can ever write it, and it would sit in the stream forever.
		r.log.Error("Dropping message with an invalid ingest subject", "subject", msg.Subject())
		_ = msg.Term()
		return
	}
	if meta, err := msg.Metadata(); err == nil && poisoned(meta.NumDelivered, meta.Timestamp, time.Now().UTC()) {
		r.log.Error("Dropping an ingest batch that has failed to be written for too long",
			"shard", shard, "project", projectID, "deliveries", meta.NumDelivered, "published", meta.Timestamp)
		telemetry.IngestTerminatedMessages.WithLabelValues(r.signal).Inc()
		_ = msg.Term()
		return
	}
	if err := r.handler.Handle(shard, projectID, msg.Data(), NewAck(r.client.Conn, msg.Reply())); err != nil {
		r.log.Error("Failed to buffer ingest message", "shard", shard, "project", projectID, "error", err)
		_ = msg.NakWithDelay(r.tick)
	}
}

// poisoned reports whether a batch has been redelivered too often for too
// long to be anything but unwritable.
func poisoned(deliveries uint64, published, now time.Time) bool {
	return deliveries >= poisonDeliveries && now.Sub(published) >= poisonAge
}

func (r *ShardRunner) ensureConsumer(ctx context.Context, shard int) (jetstream.Consumer, error) {
	return r.client.JetStream.CreateOrUpdateConsumer(ctx, r.stream, jetstream.ConsumerConfig{
		Durable:       ConsumerName(r.signal, shard),
		FilterSubject: fmt.Sprintf("ingest.%s.%d.>", r.signal, shard),
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       r.ackWait,
		// Messages stay unacked until their L0 file is uploaded, up to the
		// max-age: far more than the default 1000 can be outstanding.
		MaxAckPending: -1,
		MaxDeliver:    -1,
	})
}

func (r *ShardRunner) leaseTTL() time.Duration {
	return leaseTicks * r.tick
}

func (r *ShardRunner) ownedShards() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	shards := make([]int, 0, len(r.owned))
	for shard := range r.owned {
		shards = append(shards, shard)
	}
	return shards
}

func (r *ShardRunner) isOwned(shard int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.owned[shard]
	return ok
}
