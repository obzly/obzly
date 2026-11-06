package ingest

import (
	"context"
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
// broker.ErrIngestUnavailable.
func Publish[T any](client *broker.Client, signal string, projectID uuid.UUID, items []T) error {
	payloads, err := EncodePayloads(items)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	err = client.PublishIngest(ctx, signal, projectID, payloads)
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
