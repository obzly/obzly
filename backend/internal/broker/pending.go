package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

// pendingBatch is how many messages one direct-get request returns.
const pendingBatch = 256

// PendingMessage is one stored ingest batch no worker has acked yet: its
// rows are in no queryable segment.
type PendingMessage struct {
	Seq  uint64
	Data []byte
}

type directGetRequest struct {
	Seq     uint64 `json:"seq"`
	NextFor string `json:"next_by_subj"`
	Batch   int    `json:"batch"`
	UpToSeq uint64 `json:"up_to_seq,omitempty"`
	// MaxBytes caps the batch by size as well (readMaxBytes).
	MaxBytes int `json:"max_bytes,omitempty"`
}

// ErrPendingTooLarge: the project has more pending bytes than the caller
// asked for; the result is the oldest maxBytes of them.
var ErrPendingTooLarge = errors.New("pending ingest exceeds the byte limit")

// PendingIngest returns a project's batches still in signal's stream, oldest
// first, up to maxBytes, via batched direct get (a consumer can't overlap the
// shard consumers on a work-queue stream).
func (c *Client) PendingIngest(
	ctx context.Context,
	signal string,
	projectID uuid.UUID,
	maxBytes int,
) ([]PendingMessage, error) {
	return c.PendingIngestRange(ctx, signal, projectID, 1, 0, maxBytes)
}

// FirstPendingSeq is the sequence of the project's oldest pending batch;
// ok is false when none is pending.
func (c *Client) FirstPendingSeq(ctx context.Context, signal string, projectID uuid.UUID) (uint64, bool, error) {
	stream, err := streamFor(signal)
	if err != nil {
		return 0, false, err
	}
	batch, _, err := c.directGetBatch(ctx, stream, IngestSubject(signal, projectID), 1, 0, 1)
	if err != nil || len(batch) == 0 {
		return 0, false, err
	}
	return batch[0].Seq, true, nil
}

// PendingIngestRange is PendingIngest for sequences fromSeq..upToSeq
// (0: no upper bound).
func (c *Client) PendingIngestRange(
	ctx context.Context,
	signal string,
	projectID uuid.UUID,
	fromSeq, upToSeq uint64,
	maxBytes int,
) ([]PendingMessage, error) {
	stream, err := streamFor(signal)
	if err != nil {
		return nil, err
	}

	subject := IngestSubject(signal, projectID)
	var messages []PendingMessage
	total := 0
	next := max(fromSeq, 1)
	for {
		batch, more, err := c.directGetBatch(ctx, stream, subject, next, upToSeq, pendingBatch)
		if err != nil {
			return nil, err
		}
		for _, msg := range batch {
			if total+len(msg.Data) > maxBytes {
				return messages, ErrPendingTooLarge
			}
			total += len(msg.Data)
			messages = append(messages, msg)
			next = msg.Seq + 1
		}
		if !more || len(batch) == 0 {
			return messages, nil
		}
	}
}

// directGetBatch fetches up to pendingBatch messages on subject from seq on.
// more reports whether the server says further ones match.
func (c *Client) directGetBatch(
	ctx context.Context,
	stream, subject string,
	seq, upToSeq uint64,
	batchSize int,
) ([]PendingMessage, bool, error) {
	request, err := json.Marshal(directGetRequest{
		Seq: seq, NextFor: subject, Batch: batchSize, UpToSeq: upToSeq, MaxBytes: readMaxBytes,
	})
	if err != nil {
		return nil, false, err
	}
	inbox := c.Conn.NewRespInbox()
	sub, err := c.Conn.SubscribeSync(inbox)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = sub.Unsubscribe() }()
	if err := c.Conn.PublishRequest("$JS.API.DIRECT.GET."+stream, inbox, request); err != nil {
		return nil, false, err
	}

	var batch []PendingMessage
	for {
		msg, err := sub.NextMsgWithContext(ctx)
		if err != nil {
			return nil, false, fmt.Errorf("direct get on %s: %w", stream, err)
		}
		switch msg.Header.Get("Status") {
		case "":
		case "404", "408":
			return batch, false, nil
		case "204": // end of batch
			pending, _ := strconv.ParseUint(msg.Header.Get("Nats-Num-Pending"), 10, 64)
			return batch, pending > 0, nil
		default:
			return nil, false, fmt.Errorf("direct get on %s: status %s %s",
				stream, msg.Header.Get("Status"), msg.Header.Get("Description"))
		}
		msgSeq, err := strconv.ParseUint(msg.Header.Get(nats.JSSequence), 10, 64)
		if err != nil {
			return nil, false, fmt.Errorf("direct get on %s: bad sequence header: %w", stream, err)
		}
		batch = append(batch, PendingMessage{Seq: msgSeq, Data: msg.Data})
	}
}

func streamFor(signal string) (string, error) {
	for _, s := range IngestStreams {
		if s.Signal == signal {
			return s.Name, nil
		}
	}
	return "", fmt.Errorf("no ingest stream for signal %q", signal)
}
