package traces_jaeger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"obzly/internal/config"
	api_keys "obzly/internal/features/api_keys"
	spans_core "obzly/internal/features/spans/core"

	"github.com/google/uuid"
)

var (
	errUnauthenticated = errors.New("invalid or missing API key")
	errTraceNotFound   = errors.New("trace not found")
)

// JaegerService authenticates every call by API key against the project in
// the URL and reshapes span DTOs into Jaeger's JSON schema.
type JaegerService struct {
	apiKeyService *api_keys.ApiKeyService
}

func (s *JaegerService) authenticate(projectID uuid.UUID, apiKey string) error {
	if apiKey == "" {
		return errUnauthenticated
	}

	result, err := s.apiKeyService.ValidateApiKey(apiKey, projectID)
	if err != nil {
		return fmt.Errorf("failed to validate API key: %w", err)
	}
	if !result.IsValid {
		return errUnauthenticated
	}
	return nil
}

func (s *JaegerService) ListServices(projectID uuid.UUID, apiKey string) ([]string, error) {
	if err := s.authenticate(projectID, apiKey); err != nil {
		return nil, err
	}

	response, err := spans_core.GetSpanCoreRepository().ListServices(projectID, nil)
	if err != nil {
		return nil, err
	}
	return response.Services, nil
}

func (s *JaegerService) ListOperations(projectID uuid.UUID, apiKey, serviceName string) ([]string, error) {
	if err := s.authenticate(projectID, apiKey); err != nil {
		return nil, err
	}

	response, err := spans_core.GetSpanCoreRepository().ListOperations(projectID, serviceName, nil)
	if err != nil {
		return nil, err
	}
	return response.Operations, nil
}

// Jaeger search bounds: a search answers with whole traces, so each result
// costs its spans; Jaeger's own UI looks back an hour by default. The
// default range reaches searchClockSkew past now, for spans stamped by a
// clock a little ahead of this one.
const (
	maxSearchTraces     = 50
	defaultSearchWindow = time.Hour
	searchClockSkew     = 5 * time.Minute
)

// SearchTraces answers GET /api/traces: find matching traces, then fetch
// their spans in one scan (Jaeger search returns whole traces).
func (s *JaegerService) SearchTraces(
	ctx context.Context,
	projectID uuid.UUID,
	apiKey string,
	request *spans_core.ListTracesRequestDTO,
) ([]Trace, error) {
	if err := s.authenticate(projectID, apiKey); err != nil {
		return nil, err
	}

	if request.Limit <= 0 || request.Limit > maxSearchTraces {
		request.Limit = maxSearchTraces
	}
	if request.TimeRange == nil {
		now := time.Now().UTC()
		from, to := now.Add(-defaultSearchWindow), now.Add(searchClockSkew)
		request.TimeRange = &spans_core.TimeRangeDTO{From: &from, To: &to}
	}
	request.ScanBudget = config.GetEnv().QueryMaxScanBytes
	summaries, err := spans_core.GetSpanCoreRepository().ListTraces(ctx, projectID, request)
	if err != nil {
		return nil, err
	}

	traceIDs := make([]string, len(summaries.Traces))
	for i, summary := range summaries.Traces {
		traceIDs[i] = summary.TraceID
	}
	spansByTrace, err := spans_core.GetSpanCoreRepository().GetTraces(
		ctx, projectID, traceIDs, request.TimeRange, request.ScanBudget,
	)
	if err != nil {
		return nil, err
	}

	traces := make([]Trace, 0, len(traceIDs))
	for _, traceID := range traceIDs {
		traces = append(traces, ConvertTrace(&spans_core.GetTraceResponseDTO{
			TraceID: traceID, Spans: spansByTrace[traceID],
		}))
	}
	return traces, nil
}

func (s *JaegerService) GetTrace(projectID uuid.UUID, apiKey, traceID string) (*Trace, error) {
	if err := s.authenticate(projectID, apiKey); err != nil {
		return nil, err
	}

	full, err := spans_core.GetSpanCoreRepository().GetTrace(projectID, traceID, nil)
	if err != nil {
		return nil, err
	}
	if len(full.Spans) == 0 {
		return nil, errTraceNotFound
	}

	trace := ConvertTrace(full)
	return &trace, nil
}
