package otlp_metrics

import (
	"context"
	"errors"
	"fmt"
	"time"

	"obzly/internal/broker"
	api_keys "obzly/internal/features/api_keys"
	metrics_core "obzly/internal/features/metrics/core"
	"obzly/internal/features/otlp"
	"obzly/internal/ingest"
	"obzly/internal/telemetry"
	"obzly/internal/util/logger"

	"github.com/google/uuid"

	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// maxSamplesPerExport bounds one export request. It counts converted samples,
// since one histogram point can become a dozen or more.
const maxSamplesPerExport = 200_000

const (
	deltaTotalsTimeout            = 5 * time.Second
	deltaTotalsUnavailableMessage = "delta temporality totals are unavailable, retry later"
)

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

type OtlpMetricsService struct {
	apiKeyService *api_keys.ApiKeyService
	deltaTotals   DeltaTotals
}

// Export resolves the project from apiKey (always required) and stores the
// converted samples.
func (s *OtlpMetricsService) Export(
	apiKey string,
	resourceMetrics []*metricsv1.ResourceMetrics,
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

	conversion := ConvertResourceMetrics(resolved.ProjectID, resourceMetrics)
	converted, rejected := conversion.Samples, conversion.Rejected
	if len(converted) == 0 {
		telemetry.IngestRejectedRows.WithLabelValues(telemetry.SignalMetrics).Add(float64(rejected))
		return 0, rejected, nil
	}
	if len(converted) > maxSamplesPerExport {
		return 0, 0, &ExportError{
			Code: ExportErrorInvalidArgument,
			Message: fmt.Sprintf(
				"export converts to %d samples, exceeding the %d limit per request",
				len(converted),
				maxSamplesPerExport,
			),
		}
	}
	undo, err := s.makeCumulative(resolved.ProjectID, conversion.Delta)
	if err != nil {
		return 0, 0, err
	}

	entries := map[uuid.UUID][]*metrics_core.MetricSample{resolved.ProjectID: converted}
	if client := broker.Default(); client != nil {
		if err := ingest.Publish(client, broker.SignalMetrics, resolved.ProjectID, converted); err != nil {
			s.undoCumulative(undo)
			return 0, 0, publishError(err)
		}
	} else if err := metrics_core.GetMetricCoreRepository().StoreSamplesBatch(entries); err != nil {
		s.undoCumulative(undo)
		return 0, 0, fmt.Errorf("failed to store metric samples: %w", err)
	}

	telemetry.IngestAcceptedRows.WithLabelValues(telemetry.SignalMetrics).Add(float64(len(converted)))
	telemetry.IngestRejectedRows.WithLabelValues(telemetry.SignalMetrics).Add(float64(rejected))
	return len(converted), rejected, nil
}

// makeCumulative turns delta samples into running totals. Without Valkey the
// request is refused as retryable rather than stored with wrong numbers.
func (s *OtlpMetricsService) makeCumulative(
	projectID uuid.UUID,
	deltas []*metrics_core.MetricSample,
) ([]DeltaIncrement, error) {
	if len(deltas) == 0 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), deltaTotalsTimeout)
	defer cancel()
	undo, err := toCumulative(ctx, s.deltaTotals, projectID, deltas)
	if err != nil {
		telemetry.OTLPDeltaSamples.WithLabelValues(telemetry.ResultError).Add(float64(len(deltas)))
		logger.GetLogger().Warn("OTLP delta metrics refused: running totals unavailable",
			"project_id", projectID, "samples", len(deltas), "error", err)
		return nil, &ExportError{Code: ExportErrorUnavailable, Message: deltaTotalsUnavailableMessage}
	}
	telemetry.OTLPDeltaSamples.WithLabelValues(telemetry.ResultOK).Add(float64(len(deltas)))
	return undo, nil
}

// undoCumulative takes back a refused request's increments, so the sender's
// retry doesn't count them twice. Best effort: if this fails too, the
// totals stay high by this request's deltas.
func (s *OtlpMetricsService) undoCumulative(undo []DeltaIncrement) {
	if len(undo) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), deltaTotalsTimeout)
	defer cancel()
	if _, err := s.deltaTotals.Add(ctx, undo); err != nil {
		logger.GetLogger().Warn("failed to undo OTLP delta totals after a refused request", "error", err)
	}
}

// publishError maps a JetStream publish failure to a retryable ExportError.
func publishError(err error) error {
	if errors.Is(err, broker.ErrIngestFull) {
		return &ExportError{Code: ExportErrorResourceExhausted, Message: otlp.IngestBufferFullMessage}
	}
	return &ExportError{Code: ExportErrorUnavailable, Message: otlp.IngestBufferUnavailableMessage}
}
