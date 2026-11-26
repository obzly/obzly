package broker

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
)

// IngestShards is the fixed shard count per signal: the <shard> subject
// token, one durable consumer and one worker lease each. More shards than
// workers, so adding a worker moves only some of them. Changing it remaps
// every project, so it is not configurable.
const IngestShards = 64

// MaxIngestPayload bounds one published message; a batch is split into
// messages of at most this size. Under the server's MaxPayload.
const MaxIngestPayload = 1 << 20

// maxServerPayload is the embedded server's max_payload (NATS default 1 MB):
// one log line may be larger than MaxIngestPayload.
const maxServerPayload = 8 << 20

// maxClientPending is how much the broker queues for one client connection.
const maxClientPending = 256 << 20

// jsErrCodeStoreFailed is JSStreamStoreFailedF, which a DiscardNew stream
// returns when it is full ("maximum bytes exceeded").
const jsErrCodeStoreFailed jetstream.ErrorCode = 10077

var (
	// ErrIngestFull: the stream is at max_bytes - workers are behind. The
	// receivers answer 429 / RESOURCE_EXHAUSTED so clients retry later.
	ErrIngestFull = errors.New("ingest buffer is full")
	// ErrIngestUnavailable: no JetStream ack (broker down, no quorum,
	// timeout). The receivers answer 503 / UNAVAILABLE.
	ErrIngestUnavailable = errors.New("ingest buffer is unavailable")
)

// ShardFor maps a project to its shard.
func ShardFor(projectID uuid.UUID) int {
	h := fnv.New32a()
	_, _ = h.Write(projectID[:])
	return int(h.Sum32() % IngestShards)
}

// IngestSubject is ingest.<signal>.<shard>.<project>.
func IngestSubject(signal string, projectID uuid.UUID) string {
	return fmt.Sprintf("ingest.%s.%d.%s", signal, ShardFor(projectID), projectID)
}

// ProjectFromSubject returns the project token of an ingest subject.
func ProjectFromSubject(subject string) (uuid.UUID, error) {
	tokens := strings.Split(subject, ".")
	if len(tokens) != 4 || tokens[0] != "ingest" {
		return uuid.Nil, fmt.Errorf("not an ingest subject: %q", subject)
	}
	if _, err := strconv.Atoi(tokens[2]); err != nil {
		return uuid.Nil, fmt.Errorf("invalid shard in subject %q", subject)
	}
	return uuid.Parse(tokens[3])
}

// PublishIngest stores payloads for one project under random message ids;
// see PublishIngestWithIDs.
func (c *Client) PublishIngest(ctx context.Context, signal string, projectID uuid.UUID, payloads [][]byte) error {
	msgIDs := make([]string, len(payloads))
	for i := range payloads {
		msgIDs[i] = uuid.NewString()
	}
	return c.PublishIngestWithIDs(ctx, signal, projectID, payloads, msgIDs)
}

// PublishIngestWithIDs stores payloads for one project and returns once
// JetStream has acked every one, i.e. they are on a quorum (R3) or the local
// store (R1). On error some payloads may be stored. msgIDs[i] is payloads[i]'s
// Nats-Msg-Id: a payload whose id the stream saw inside duplicateWindow is
// acked without being stored again, so a client resending a stored batch
// (receivers derive ids from its rows, ingest.Publish) doesn't duplicate it.
// The window is per stream, so a resend on another lane is caught too. A
// project with lanes spreads its payloads over them (laneSubject).
func (c *Client) PublishIngestWithIDs(
	ctx context.Context,
	signal string,
	projectID uuid.UUID,
	payloads [][]byte,
	msgIDs []string,
) error {
	futures := make([]jetstream.PubAckFuture, 0, len(payloads))
	for i, payload := range payloads {
		future, err := c.JetStream.PublishAsync(laneSubject(signal, projectID), payload,
			jetstream.WithMsgID(msgIDs[i]))
		if err != nil {
			return classifyPublishError(err)
		}
		futures = append(futures, future)
	}

	for _, future := range futures {
		select {
		case <-future.Ok():
		case err := <-future.Err():
			return classifyPublishError(err)
		case <-ctx.Done():
			return fmt.Errorf("%w: %w", ErrIngestUnavailable, ctx.Err())
		}
	}
	return nil
}

// SplitPayloads packs pre-encoded items into JSON arrays of at most
// MaxIngestPayload bytes each; an item larger than that gets its own.
func SplitPayloads(items [][]byte) [][]byte {
	payloads, _ := SplitPayloadRanges(items)
	return payloads
}

// SplitPayloadRanges is SplitPayloads that also returns each payload's
// first item index; the last payload runs to the end.
func SplitPayloadRanges(items [][]byte) (payloads [][]byte, starts []int) {
	current := []byte{'['}
	start := 0
	for i, item := range items {
		if len(current) > 1 && len(current)+len(item)+2 > MaxIngestPayload {
			payloads = append(payloads, append(current, ']'))
			starts = append(starts, start)
			current = []byte{'['}
			start = i
		}
		if len(current) > 1 {
			current = append(current, ',')
		}
		current = append(current, item...)
	}
	if len(current) > 1 {
		payloads = append(payloads, append(current, ']'))
		starts = append(starts, start)
	}
	return payloads, starts
}

func classifyPublishError(err error) error {
	var jsErr jetstream.JetStreamError
	if errors.As(err, &jsErr) {
		if apiErr := jsErr.APIError(); apiErr != nil && apiErr.ErrorCode == jsErrCodeStoreFailed &&
			strings.Contains(apiErr.Description, "exceeded") {
			return fmt.Errorf("%w: %w", ErrIngestFull, err)
		}
	}
	return fmt.Errorf("%w: %w", ErrIngestUnavailable, err)
}
