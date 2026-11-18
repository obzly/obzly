// Package traces_querying is the trace query REST API the frontend uses;
// traces_jaeger serves the same data to external Jaeger clients.
package traces_querying

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"obzly/internal/config"
	spans_core "obzly/internal/features/spans/core"
	"obzly/internal/util/querylimit"

	projects_services "obzly/internal/features/projects/services"
	users_models "obzly/internal/features/users/models"

	"github.com/google/uuid"
)

// maxSummarySlots bounds a trace summary's trend (the UI asks for about 30 points).
const maxSummarySlots = 1024

const (
	// summaryTrendPoints is about how many buckets a summary trend has.
	summaryTrendPoints = 30
	// matchingTracesCap stops the header count on big windows ("10,000+").
	matchingTracesCap = 10_000
	// maxServiceErrorsRange: the per-service errors scan reads twice the range
	// of raw spans (there is no spans rollup), so a longer range is clamped.
	maxServiceErrorsRange = 7 * 24 * time.Hour
	defaultServiceErrors  = 10
	maxServiceErrors      = 50
)

// ErrInvalidSummaryRange: the summary needs a range with both ends, from
// before to.
var ErrInvalidSummaryRange = errors.New("timeRange.from and timeRange.to are required, with from before to")

type TraceQueryService struct {
	projectService *projects_services.ProjectService
	queryLimiter   *querylimit.Limiter
}

func (s *TraceQueryService) ListTraces(
	ctx context.Context,
	projectID uuid.UUID,
	request *spans_core.ListTracesRequestDTO,
	user *users_models.User,
) (*spans_core.ListTracesResponseDTO, error) {
	if err := s.requireAccess(projectID, user); err != nil {
		return nil, err
	}
	release, err := s.queryLimiter.Acquire(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	defer release()
	request.ScanBudget = config.GetEnv().QueryMaxScanBytes
	return spans_core.GetSpanCoreRepository().ListTraces(ctx, projectID, request)
}

// GetTraceSummary computes the Traces summary tiles for the searched range
// and the same-length range before it, plus the matching-traces count, in at
// most two span scans.
func (s *TraceQueryService) GetTraceSummary(
	ctx context.Context,
	projectID uuid.UUID,
	request *TraceSummaryRequestDTO,
	user *users_models.User,
) (*TraceSummaryResponseDTO, error) {
	if err := s.requireAccess(projectID, user); err != nil {
		return nil, err
	}
	if request.TimeRange == nil || request.TimeRange.From == nil || request.TimeRange.To == nil ||
		!request.TimeRange.From.Before(*request.TimeRange.To) {
		return nil, ErrInvalidSummaryRange
	}
	release, err := s.queryLimiter.Acquire(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	defer release()

	from, to := request.TimeRange.From.UTC(), request.TimeRange.To.UTC()
	span := max(to.Sub(from), time.Second)
	stepMs := summaryStepMs(span)
	query := &spans_core.TraceSummaryQuery{
		From:          from,
		To:            to,
		PreviousFrom:  from.Add(-span),
		StepMs:        stepMs,
		ServiceName:   request.ServiceName,
		OperationName: request.OperationName,
		MinDurationMs: request.MinDurationMs,
		MaxDurationMs: request.MaxDurationMs,
		ErrorsOnly:    request.ErrorsOnly,
		RootSpansOnly: request.ServiceName == "",
		CountOnly:     request.CountOnly,
		MatchCap:      matchingTracesCap,
		ScanBudget:    config.GetEnv().QueryMaxScanBytes,
	}
	result, err := spans_core.GetSpanCoreRepository().TraceSummary(ctx, projectID, query)
	if err != nil {
		return nil, err
	}
	return buildTraceSummaryResponse(query, result), nil
}

// GetServiceErrors ranks services by failed entry spans in the range and
// gives the same counts for the same-length range before it, in one scan.
func (s *TraceQueryService) GetServiceErrors(
	ctx context.Context,
	projectID uuid.UUID,
	request *ServiceErrorsRequestDTO,
	user *users_models.User,
) (*ServiceErrorsResponseDTO, error) {
	if err := s.requireAccess(projectID, user); err != nil {
		return nil, err
	}
	if request.TimeRange == nil || request.TimeRange.From == nil || request.TimeRange.To == nil ||
		!request.TimeRange.From.Before(*request.TimeRange.To) {
		return nil, ErrInvalidSummaryRange
	}
	release, err := s.queryLimiter.Acquire(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	defer release()

	from, to := request.TimeRange.From.UTC(), request.TimeRange.To.UTC()
	clamped := false
	if to.Sub(from) > maxServiceErrorsRange {
		from = to.Add(-maxServiceErrorsRange)
		clamped = true
	}
	limit := request.Limit
	if limit <= 0 {
		limit = defaultServiceErrors
	}
	limit = min(limit, maxServiceErrors)
	previousFrom := from.Add(-to.Sub(from))

	rows, err := spans_core.GetSpanCoreRepository().
		ServiceErrorRates(ctx, projectID, &spans_core.ServiceErrorRatesQuery{
			From:         from,
			To:           to,
			PreviousFrom: previousFrom,
			Limit:        limit,
			ScanBudget:   config.GetEnv().QueryMaxScanBytes,
		})
	if err != nil {
		return nil, err
	}
	response := &ServiceErrorsResponseDTO{
		From: from, To: to, PreviousFrom: previousFrom, Clamped: clamped,
		Services: make([]ServiceErrorsDTO, 0, len(rows)),
	}
	for _, row := range rows {
		response.Services = append(response.Services, ServiceErrorsDTO{
			ServiceName:      row.ServiceName,
			Requests:         row.Requests,
			Errors:           row.Errors,
			PreviousRequests: row.PreviousRequests,
			PreviousErrors:   row.PreviousErrors,
		})
	}
	return response, nil
}

func (s *TraceQueryService) GetTrace(
	projectID uuid.UUID,
	traceID string,
	timeRange *spans_core.TimeRangeDTO,
	user *users_models.User,
) (*spans_core.GetTraceResponseDTO, error) {
	if err := s.requireAccess(projectID, user); err != nil {
		return nil, err
	}
	release, err := s.queryLimiter.Acquire(context.Background(), user.ID)
	if err != nil {
		return nil, err
	}
	defer release()
	return spans_core.GetSpanCoreRepository().GetTrace(projectID, traceID, timeRange)
}

func (s *TraceQueryService) ListServices(
	projectID uuid.UUID,
	timeRange *spans_core.TimeRangeDTO,
	user *users_models.User,
) (*spans_core.ListServicesResponseDTO, error) {
	if err := s.requireAccess(projectID, user); err != nil {
		return nil, err
	}
	return spans_core.GetSpanCoreRepository().ListServices(projectID, timeRange)
}

func (s *TraceQueryService) ListOperations(
	projectID uuid.UUID,
	serviceName string,
	timeRange *spans_core.TimeRangeDTO,
	user *users_models.User,
) (*spans_core.ListOperationsResponseDTO, error) {
	if err := s.requireAccess(projectID, user); err != nil {
		return nil, err
	}
	return spans_core.GetSpanCoreRepository().ListOperations(projectID, serviceName, timeRange)
}

// GetProjectStats returns a project's spans consumption stats.
func (s *TraceQueryService) GetProjectStats(
	projectID uuid.UUID,
	user *users_models.User,
) (*spans_core.SpansStatsDTO, error) {
	if err := s.requireAccess(projectID, user); err != nil {
		return nil, err
	}
	return spans_core.GetSpanCoreRepository().GetProjectSpanStats(projectID)
}

func (s *TraceQueryService) requireAccess(projectID uuid.UUID, user *users_models.User) error {
	canAccess, _, err := s.projectService.CanUserAccessProject(projectID, user)
	if err != nil {
		return fmt.Errorf("failed to verify project access: %w", err)
	}
	if !canAccess {
		return errors.New("insufficient permissions to query project traces")
	}
	return nil
}

// summaryStepMs is the trend bucket width: whole seconds, about
// summaryTrendPoints buckets over span.
func summaryStepMs(span time.Duration) int64 {
	seconds := (span.Milliseconds() + 999) / 1000
	return max(1, (seconds+summaryTrendPoints-1)/summaryTrendPoints) * 1000
}

func buildTraceSummaryResponse(
	query *spans_core.TraceSummaryQuery,
	result *spans_core.TraceSummaryResult,
) *TraceSummaryResponseDTO {
	response := &TraceSummaryResponseDTO{
		WindowMinutes:     max(query.To.Sub(query.From), time.Second).Minutes(),
		RootSpansOnly:     query.RootSpansOnly,
		StepMs:            query.StepMs,
		CountTrend:        []float64{},
		ErrorRateTrend:    []float64{},
		P50Trend:          []float64{},
		P95Trend:          []float64{},
		MatchingTraces:    result.MatchingTraces,
		MatchingTracesCap: int64(query.MatchCap),
	}
	if result.MatchingTraces > int64(query.MatchCap) {
		response.MatchingTraces = int64(query.MatchCap)
		response.MatchingTracesCapped = true
	}
	if query.CountOnly {
		return response
	}

	response.Current = totalsDTO(result.Current)
	response.Previous = totalsDTO(result.Previous)

	// Slots line up with floor(time / step), as the buckets' Index does.
	stepNs := query.StepMs * int64(time.Millisecond)
	firstIndex := query.From.UnixNano() / stepNs
	slots := query.To.UnixNano()/stepNs - firstIndex + 1
	// The step is chosen for ~30 points; cap anyway so a request can't size these slices.
	if slots < 1 || slots > maxSummarySlots {
		slots = min(max(slots, 1), maxSummarySlots)
	}
	counts := make([]float64, slots)
	errorCounts := make([]float64, slots)
	buckets := append([]spans_core.TraceSummaryBucket{}, result.Buckets...)
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].Index < buckets[j].Index })
	for _, bucket := range buckets {
		slot := bucket.Index - firstIndex
		if slot < 0 || slot >= slots {
			continue
		}
		counts[slot] += float64(bucket.Count)
		errorCounts[slot] += float64(bucket.Errors)
		if bucket.P50Ms != nil {
			response.P50Trend = append(response.P50Trend, *bucket.P50Ms)
		}
		if bucket.P95Ms != nil {
			response.P95Trend = append(response.P95Trend, *bucket.P95Ms)
		}
	}
	response.CountTrend = counts
	for i, count := range counts {
		if count > 0 {
			response.ErrorRateTrend = append(response.ErrorRateTrend, errorCounts[i]/count*100)
		}
	}
	return response
}

// totalsDTO: counts are known (0 with no spans); percentiles only with spans.
func totalsDTO(period spans_core.TraceSummaryPeriod) TraceSummaryTotalsDTO {
	count, errorCount := period.Count, period.Errors
	totals := TraceSummaryTotalsDTO{Count: &count, Errors: &errorCount}
	if count > 0 {
		totals.P50Ms = period.P50Ms
		totals.P95Ms = period.P95Ms
	}
	return totals
}
