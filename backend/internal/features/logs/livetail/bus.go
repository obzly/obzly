package logs_livetail

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"obzly/internal/broker"
	logs_core "obzly/internal/features/logs/core"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/valkey-io/valkey-go"
)

// Interest on core NATS: an open tail announces its project every
// interestRefresh; a replica publishes a project's logs only while an
// announcement is younger than interestTTL.
const (
	interestRefresh = 5 * time.Second
	interestTTL     = 3 * interestRefresh
	interestSubject = "tail.interest."
	logsSubject     = "tail.logs."
)

// bus carries ingested logs to open tails: core NATS when the broker is in
// use, Valkey Pub/Sub otherwise.
type bus interface {
	publish(ctx context.Context, projectID uuid.UUID, items []*logs_core.LogItem) error
	// subscribe calls onItems for every published batch until ctx ends;
	// onReady fires once nothing published from then on can be missed.
	subscribe(ctx context.Context, projectID uuid.UUID, onReady func(), onItems func([]*logs_core.LogItem)) error
}

type valkeyBus struct {
	client valkey.Client
	logger *slog.Logger
}

type natsBus struct {
	conn     *nats.Conn
	interest *interestTracker
}

// interestTracker is this process's view of which projects have an open
// tail on any replica.
type interestTracker struct {
	mu    sync.Mutex
	until map[uuid.UUID]time.Time
	// conn is the connection the announcement subscription lives on.
	conn *nats.Conn
}

var tracker = &interestTracker{until: map[uuid.UUID]time.Time{}}

// currentBus picks the transport per call: the broker connection is set up
// after this package's singletons are built.
func currentBus(client valkey.Client, logger *slog.Logger) bus {
	if b := broker.Default(); b != nil {
		return &natsBus{conn: b.Conn, interest: tracker}
	}
	return &valkeyBus{client: client, logger: logger}
}

// channelForProject is the single source of a project's Pub/Sub channel, so
// publish and subscribe always agree.
func channelForProject(projectID uuid.UUID) string {
	return "obzly:logs:live:" + projectID.String()
}

func (b *valkeyBus) publish(ctx context.Context, projectID uuid.UUID, items []*logs_core.LogItem) error {
	channel := channelForProject(projectID)
	for _, item := range items {
		encoded, err := json.Marshal(item)
		if err != nil {
			b.logger.Warn("live tail: failed to marshal log for publish", slog.String("error", err.Error()))
			continue
		}
		cmd := b.client.B().Publish().Channel(channel).Message(string(encoded)).Build()
		if err := b.client.Do(ctx, cmd).Error(); err != nil {
			// A connection-level error fails every later publish too.
			return err
		}
	}
	return nil
}

// subscribe fires onReady only once Valkey has acknowledged the SUBSCRIBE:
// Pub/Sub never replays, so a "ready" sent earlier could hide a dropped log.
func (b *valkeyBus) subscribe(
	ctx context.Context,
	projectID uuid.UUID,
	onReady func(),
	onItems func([]*logs_core.LogItem),
) error {
	var readyOnce sync.Once
	subscribedCtx := valkey.WithOnSubscriptionHook(ctx, func(valkey.PubSubSubscription) {
		readyOnce.Do(onReady)
	})
	cmd := b.client.B().Subscribe().Channel(channelForProject(projectID)).Build()
	return b.client.Receive(subscribedCtx, cmd, func(msg valkey.PubSubMessage) {
		var item logs_core.LogItem
		if err := json.Unmarshal([]byte(msg.Message), &item); err != nil {
			b.logger.Warn("live tail: failed to unmarshal published log", slog.String("error", err.Error()))
			return
		}
		onItems([]*logs_core.LogItem{&item})
	})
}

// publish sends one message per batch, and nothing when no tail is open.
func (b *natsBus) publish(_ context.Context, projectID uuid.UUID, items []*logs_core.LogItem) error {
	if err := b.interest.ensureStarted(b.conn); err != nil {
		return err
	}
	if !b.interest.has(projectID) {
		return nil
	}
	encoded := make([][]byte, 0, len(items))
	for _, item := range items {
		data, err := json.Marshal(item)
		if err != nil {
			return fmt.Errorf("failed to encode log for live tail: %w", err)
		}
		encoded = append(encoded, data)
	}
	for _, payload := range broker.SplitPayloads(encoded) {
		if err := b.conn.Publish(logsSubject+projectID.String(), payload); err != nil {
			return err
		}
	}
	return nil
}

func (b *natsBus) subscribe(
	ctx context.Context,
	projectID uuid.UUID,
	onReady func(),
	onItems func([]*logs_core.LogItem),
) error {
	if err := b.interest.ensureStarted(b.conn); err != nil {
		return err
	}
	sub, err := b.conn.Subscribe(logsSubject+projectID.String(), func(msg *nats.Msg) {
		var items []*logs_core.LogItem
		if err := json.Unmarshal(msg.Data, &items); err == nil {
			onItems(items)
		}
	})
	if err != nil {
		return err
	}
	defer func() { _ = sub.Unsubscribe() }()

	// This replica publishes for the project at once; others once the
	// announcement reaches them, which Flush makes the broker deliver first.
	b.interest.mark(projectID)
	if err := b.announce(projectID); err != nil {
		return err
	}
	onReady()

	ticker := time.NewTicker(interestRefresh)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			b.interest.mark(projectID)
			_ = b.announce(projectID)
		}
	}
}

// announce tells every replica a tail is open; the Flush round trip means
// the subscription and the announcement are both on the broker.
func (b *natsBus) announce(projectID uuid.UUID) error {
	if err := b.conn.Publish(interestSubject+projectID.String(), nil); err != nil {
		return err
	}
	return b.conn.Flush()
}

// ensureStarted subscribes to announcements once per broker connection.
func (t *interestTracker) ensureStarted(conn *nats.Conn) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.conn == conn {
		return nil
	}
	_, err := conn.Subscribe(interestSubject+"*", func(msg *nats.Msg) {
		if projectID, err := uuid.Parse(msg.Subject[len(interestSubject):]); err == nil {
			t.mark(projectID)
		}
	})
	if err != nil {
		return err
	}
	// Registered on the broker before any announcement this replica must see.
	if err := conn.Flush(); err != nil {
		return err
	}
	t.conn = conn
	return nil
}

func (t *interestTracker) mark(projectID uuid.UUID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.until[projectID] = time.Now().Add(interestTTL)
}

func (t *interestTracker) has(projectID uuid.UUID) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	until, ok := t.until[projectID]
	if ok && time.Now().After(until) {
		delete(t.until, projectID)
		return false
	}
	return ok
}
