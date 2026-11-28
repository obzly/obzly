package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"obzly/internal/broker"
	"obzly/internal/telemetry"

	"github.com/google/uuid"
)

// publishTimeout bounds waiting for the JetStream ack, inside a collector's
// default 5s export timeout.
const publishTimeout = 4 * time.Second

// RetryAfterSeconds is the Retry-After a receiver sends with ErrIngestFull
// or ErrIngestUnavailable.
const RetryAfterSeconds = 5

// Identified is an ingest row with an id (logs, spans, metric samples).
type Identified interface {
	RowID() uuid.UUID
}

// EncodePayloads JSON-encodes items into message payloads (JSON arrays of
// at most broker.MaxIngestPayload bytes).
func EncodePayloads[T any](items []T) ([][]byte, error) {
	encoded := make([][]byte, 0, len(items))
	for _, item := range items {
		data, err := json.Marshal(item)
		if err != nil {
			return nil, fmt.Errorf("failed to encode ingest item: %w", err)
		}
		encoded = append(encoded, data)
	}
	return broker.SplitPayloads(encoded), nil
}

// DecodePayload decodes one message; a failure wraps ErrUndecodable.
func DecodePayload[T any](data []byte) ([]T, error) {
	var items []T
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUndecodable, err)
	}
	return items, nil
}

// Publish stores one project's items in signal's stream and returns once
// JetStream holds them. Errors wrap broker.ErrIngestFull or
// broker.ErrIngestUnavailable. A payload's message id is a hash of its
// rows' ids (encoded rows for items without one): receivers derive row ids
// from the request (RowIDs), so a resent batch is dropped by the stream
// even when a field such as the client IP differs.
func Publish[T any](client *broker.Client, signal string, projectID uuid.UUID, items []T) error {
	payloads, msgIDs, err := encodeForPublish(items)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	err = client.PublishIngestWithIDs(ctx, signal, projectID, payloads, msgIDs)
	switch {
	case err == nil:
		telemetry.IngestPublishes.WithLabelValues(signal, telemetry.ResultOK).Inc()
	case errors.Is(err, broker.ErrIngestFull):
		telemetry.IngestPublishes.WithLabelValues(signal, telemetry.ResultFull).Inc()
	default:
		telemetry.IngestPublishes.WithLabelValues(signal, telemetry.ResultUnavailable).Inc()
	}
	return err
}

func encodeForPublish[T any](items []T) (payloads [][]byte, msgIDs []string, err error) {
	encoded := make([][]byte, 0, len(items))
	for _, item := range items {
		data, err := json.Marshal(item)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to encode ingest item: %w", err)
		}
		encoded = append(encoded, data)
	}
	payloads, starts := broker.SplitPayloadRanges(encoded)
	msgIDs = make([]string, len(payloads))
	for p, start := range starts {
		end := len(items)
		if p+1 < len(starts) {
			end = starts[p+1]
		}
		h := sha256.New()
		for i := start; i < end; i++ {
			if row, ok := any(items[i]).(Identified); ok {
				id := row.RowID()
				h.Write(id[:])
			} else {
				h.Write(encoded[i])
			}
		}
		msgIDs[p] = hex.EncodeToString(h.Sum(nil))
	}
	return payloads, msgIDs, nil
}
