package otlp_logs

import (
	"errors"
	"time"

	api_keys "obzly/internal/features/api_keys"
	logs_core "obzly/internal/features/logs/core"
	logs_receiving "obzly/internal/features/logs/receiving"

	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
)

// ExportErrorCode is transport-agnostic, so HTTP and gRPC each map it to their
// own status.
type ExportErrorCode string

const (
	ExportErrorUnauthenticated ExportErrorCode = "UNAUTHENTICATED"
	ExportErrorInvalidArgument ExportErrorCode = "INVALID_ARGUMENT"
	// The retryable ones: the project's rate limit or the ingest buffer is
	// full, or the buffer or a lookup is unreachable.
	ExportErrorResourceExhausted ExportErrorCode = "RESOURCE_EXHAUSTED"
	ExportErrorUnavailable       ExportErrorCode = "UNAVAILABLE"
)

type ExportError struct {
	Code    ExportErrorCode
	Message string
	// RetryAfter: when a throttled batch may be sent again; 0 is the default.
	RetryAfter time.Duration
}

func (e *ExportError) Error() string {
	return e.Message
}

type OtlpLogsService struct {
	apiKeyService       *api_keys.ApiKeyService
	logReceivingService *logs_receiving.LogReceivingService
}

// Export resolves the project from apiKey and submits the converted records.
// The key is always required: OTLP has no other place to name a project.
func (s *OtlpLogsService) Export(
	apiKey, clientIP string,
	resourceLogs []*logsv1.ResourceLogs,
) (accepted, rejected int, err error) {
	if apiKey == "" {
		return 0, 0, &ExportError{
			Code:    ExportErrorUnauthenticated,
			Message: "API key required for OTLP ingestion",
		}
	}

	resolved, err := s.apiKeyService.ResolveProjectFromApiKey(apiKey)
	if err != nil {
		return 0, 0, &ExportError{Code: ExportErrorUnavailable, Message: api_keys.LookupUnavailableMessage}
	}
	if !resolved.IsValid {
		return 0, 0, &ExportError{Code: ExportErrorUnauthenticated, Message: "invalid API key"}
	}

	items := ConvertResourceLogs(resourceLogs)
	if len(items) == 0 {
		// An empty export (a collector flushing an empty batch) is valid.
		return 0, 0, nil
	}

	request := &logs_receiving.SubmitLogsRequestDTO{Logs: items}

	response, err := s.logReceivingService.SubmitLogs(resolved.ProjectID, request, clientIP, apiKey, "")
	if err != nil {
		return 0, 0, mapSubmitError(err)
	}

	return response.Accepted, response.Rejected, nil
}

// mapSubmitError turns logs_core.ValidationError into an ExportError, so
// transports don't depend on logs_core.
func mapSubmitError(err error) error {
	var validationErr *logs_core.ValidationError
	if !errors.As(err, &validationErr) {
		return err
	}

	switch validationErr.Code {
	case logs_core.ErrorAPIKeyRequired, logs_core.ErrorAPIKeyInvalid:
		return &ExportError{Code: ExportErrorUnauthenticated, Message: validationErr.Message}
	case logs_core.ErrorRateLimitExceeded:
		return &ExportError{
			Code:       ExportErrorResourceExhausted,
			Message:    validationErr.Message,
			RetryAfter: validationErr.RetryAfter,
		}
	case logs_core.ErrorIngestBufferFull:
		return &ExportError{Code: ExportErrorResourceExhausted, Message: validationErr.Message}
	case logs_core.ErrorIngestUnavailable:
		return &ExportError{Code: ExportErrorUnavailable, Message: validationErr.Message}
	default:
		return &ExportError{Code: ExportErrorInvalidArgument, Message: validationErr.Message}
	}
}
