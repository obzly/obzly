package otlp_traces

import (
	"errors"
	"fmt"

	"obzly/internal/broker"
	api_keys "obzly/internal/features/api_keys"
	"obzly/internal/features/otlp"
	spans_core "obzly/internal/features/spans/core"
	"obzly/internal/ingest"
	"obzly/internal/telemetry"

	"github.com/google/uuid"

	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
)

// maxSpansPerExport is a sanity cap against a misbehaving or malicious client;
// real batches are far smaller.
const maxSpansPerExport = 50_000

// ExportErrorCode is transport-agnostic, so HTTP and gRPC each map it to their
// own status.
type ExportErrorCode string

const (
	ExportErrorUnauthenticated ExportErrorCode = "UNAUTHENTICATED"
	ExportErrorInvalidArgument ExportErrorCode = "INVALID_ARGUMENT"
	// The two retryable ones: the ingest buffer is full or unreachable.
	ExportErrorResourceExhausted ExportErrorCode = "RESOURCE_EXHAUSTED"
	ExportErrorUnavailable       ExportErrorCode = "UNAVAILABLE"
)

type ExportError struct {
	Code    ExportErrorCode
	Message string
}

func (e *ExportError) Error() string {
	return e.Message
}

type OtlpTracesService struct {
	apiKeyService *api_keys.ApiKeyService
}

// Export resolves the project from apiKey (always required: OTLP has no other
// place to name a project) and stores the converted spans.
func (s *OtlpTracesService) Export(
	apiKey string,
	resourceSpans []*tracev1.ResourceSpans,
) (accepted, rejected int, err error) {
	if apiKey == "" {
		return 0, 0, &ExportError{
			Code:    ExportErrorUnauthenticated,
			Message: "API key required for OTLP ingestion",
		}
	}

	resolved, err := s.apiKeyService.ResolveProjectFromApiKey(apiKey)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to resolve API key: %w", err)
	}
	if !resolved.IsValid {
		return 0, 0, &ExportError{Code: ExportErrorUnauthenticated, Message: "invalid API key"}
	}

	spanCount := 0
	for _, rs := range resourceSpans {
		for _, ss := range rs.GetScopeSpans() {
			spanCount += len(ss.GetSpans())
		}
	}
	if spanCount == 0 {
		return 0, 0, nil
	}
	if spanCount > maxSpansPerExport {
		return 0, 0, &ExportError{
			Code: ExportErrorInvalidArgument,
			Message: fmt.Sprintf(
				"export contains %d spans, exceeding the %d limit per request",
				spanCount,
				maxSpansPerExport,
			),
		}
	}

	converted := ConvertResourceSpans(resolved.ProjectID, resourceSpans)
	entries := map[uuid.UUID][]*spans_core.Span{resolved.ProjectID: converted}
	if client := broker.Default(); client != nil {
		if err := ingest.Publish(client, broker.SignalSpans, resolved.ProjectID, converted); err != nil {
			return 0, 0, publishError(err)
		}
	} else if err := spans_core.GetSpanCoreRepository().StoreSpansBatch(entries); err != nil {
		return 0, 0, fmt.Errorf("failed to store spans: %w", err)
	}

	telemetry.IngestAcceptedRows.WithLabelValues(telemetry.SignalSpans).Add(float64(len(converted)))
	return len(converted), 0, nil
}

// publishError maps a JetStream publish failure to a retryable ExportError.
func publishError(err error) error {
	if errors.Is(err, broker.ErrIngestFull) {
		return &ExportError{Code: ExportErrorResourceExhausted, Message: otlp.IngestBufferFullMessage}
	}
	return &ExportError{Code: ExportErrorUnavailable, Message: otlp.IngestBufferUnavailableMessage}
}
