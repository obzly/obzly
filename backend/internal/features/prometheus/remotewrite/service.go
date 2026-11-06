package prometheus_remotewrite

import (
	"errors"
	"fmt"

	"obzly/internal/broker"
	api_keys "obzly/internal/features/api_keys"
	metrics_core "obzly/internal/features/metrics/core"
	"obzly/internal/features/otlp"
	"obzly/internal/ingest"
	"obzly/internal/telemetry"

	"github.com/google/uuid"

	"github.com/prometheus/prometheus/prompb"
)

// maxSamplesPerWrite is far above real agents' batches (low thousands).
const maxSamplesPerWrite = 200_000

type WriteErrorCode string

const (
	WriteErrorUnauthenticated WriteErrorCode = "UNAUTHENTICATED"
	WriteErrorInvalidArgument WriteErrorCode = "INVALID_ARGUMENT"
	// The two retryable ones: the ingest buffer is full or unreachable.
	WriteErrorResourceExhausted WriteErrorCode = "RESOURCE_EXHAUSTED"
	WriteErrorUnavailable       WriteErrorCode = "UNAVAILABLE"
)

type WriteError struct {
	Code    WriteErrorCode
	Message string
}

func (e *WriteError) Error() string {
	return e.Message
}

type RemoteWriteService struct {
	apiKeyService *api_keys.ApiKeyService
}

// Write requires an API key: it is the only way to resolve the project.
func (s *RemoteWriteService) Write(apiKey string, request *prompb.WriteRequest) (accepted, rejected int, err error) {
	if apiKey == "" {
		return 0, 0, &WriteError{
			Code:    WriteErrorUnauthenticated,
			Message: "API key required for remote-write ingestion",
		}
	}

	resolved, err := s.apiKeyService.ResolveProjectFromApiKey(apiKey)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to resolve API key: %w", err)
	}
	if !resolved.IsValid {
		return 0, 0, &WriteError{Code: WriteErrorUnauthenticated, Message: "invalid API key"}
	}

	converted := ConvertTimeSeries(resolved.ProjectID, request.Timeseries, MetadataTypes(request.Metadata))
	if len(converted) == 0 {
		return 0, 0, nil
	}
	if len(converted) > maxSamplesPerWrite {
		return 0, 0, &WriteError{
			Code: WriteErrorInvalidArgument,
			Message: fmt.Sprintf(
				"write request contains %d samples, exceeding the %d limit per request",
				len(converted),
				maxSamplesPerWrite,
			),
		}
	}

	entries := map[uuid.UUID][]*metrics_core.MetricSample{resolved.ProjectID: converted}
	if client := broker.Default(); client != nil {
		if err := ingest.Publish(client, broker.SignalMetrics, resolved.ProjectID, converted); err != nil {
			return 0, 0, publishError(err)
		}
	} else if err := metrics_core.GetMetricCoreRepository().StoreSamplesBatch(entries); err != nil {
		return 0, 0, fmt.Errorf("failed to store metric samples: %w", err)
	}

	telemetry.IngestAcceptedRows.WithLabelValues(telemetry.SignalMetrics).Add(float64(len(converted)))
	return len(converted), 0, nil
}

// publishError maps a JetStream publish failure to a retryable WriteError.
func publishError(err error) error {
	if errors.Is(err, broker.ErrIngestFull) {
		return &WriteError{Code: WriteErrorResourceExhausted, Message: otlp.IngestBufferFullMessage}
	}
	return &WriteError{Code: WriteErrorUnavailable, Message: otlp.IngestBufferUnavailableMessage}
}
