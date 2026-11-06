package aiusage

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"obzly/internal/features/audit_logs"
	logs_core "obzly/internal/features/logs/core"
	projects_services "obzly/internal/features/projects/services"
	users_models "obzly/internal/features/users/models"

	"github.com/google/uuid"
)

const (
	maxRange        = 400 * 24 * time.Hour
	maxBuckets      = 2000
	targetBuckets   = 60
	maxSessionsPage = 200
)

var (
	ErrForbidden      = errors.New("insufficient permissions to query project AI usage")
	ErrInvalidRequest = errors.New("invalid request")
)

type AIUsageService struct {
	projectService *projects_services.ProjectService
	audit          *audit_logs.AuditLogService
	spendCache     spendCache
	spendSource    spendSource
	// scanBudget is QUERY_MAX_SCAN_GB in bytes (0: none).
	scanBudget func() int64
}

// WithScanBudget is this service with a scan limit of bytes (tests).
func (s *AIUsageService) WithScanBudget(bytes int64) *AIUsageService {
	clone := *s
	clone.scanBudget = func() int64 { return bytes }
	return &clone
}

func (s *AIUsageService) GetOverview(
	projectID uuid.UUID,
	request *OverviewRequest,
	user *users_models.User,
) (*OverviewResponseDTO, error) {
	if err := s.authorize(projectID, user, &request.UsageFilter); err != nil {
		return nil, err
	}

	span := int64(request.To.Sub(request.From) / time.Second)
	if request.BucketSeconds <= 0 {
		request.BucketSeconds = niceBucketSeconds(span / targetBuckets)
	}
	if span/request.BucketSeconds > maxBuckets {
		return nil, fmt.Errorf("%w: bucketSeconds gives more than %d buckets", ErrInvalidRequest, maxBuckets)
	}

	results, err := logs_core.GetAIRepository().ExecuteScopedSQL(projectID, &logs_core.ScopedSQLRequestDTO{
		From:          request.From,
		To:            request.To,
		MessagePrefix: eventPrefix,
		Statements:    overviewStatements(request),
	})
	if err != nil {
		return nil, err
	}

	response := &OverviewResponseDTO{
		BucketSeconds: request.BucketSeconds,
		Timeseries:    []TimeseriesPointDTO{},
		Tools:         []ToolRowDTO{},
		Errors:        []ErrorRowDTO{},
		FilterOptions: FilterOptionsDTO{Users: []string{}, Models: []string{}, Terminals: []string{}},
	}

	if rows := results[0].Rows; len(rows) > 0 {
		r := rows[0]
		response.Summary = UsageSummaryDTO{
			Requests:            toInt(r[0]),
			CostUSD:             toFloat(r[1]),
			InputTokens:         toInt(r[2]),
			OutputTokens:        toInt(r[3]),
			CacheReadTokens:     toInt(r[4]),
			CacheCreationTokens: toInt(r[5]),
			Sessions:            toInt(r[6]),
			Users:               toInt(r[7]),
			Prompts:             toInt(r[8]),
			APIErrors:           toInt(r[9]),
			ToolCalls:           toInt(r[10]),
			AvgDurationMs:       toFloat(r[11]),
			P50TtftMs:           toFloat(r[12]),
		}
		response.LatestEventAt = toInt(r[13])
	}
	for _, r := range results[1].Rows {
		response.Timeseries = append(response.Timeseries, TimeseriesPointDTO{
			Bucket: toInt(
				r[0],
			),
			Key:      toString(r[1]),
			CostUSD:  toFloat(r[2]),
			Tokens:   toInt(r[3]),
			Requests: toInt(r[4]),
		})
	}
	response.Users = breakdownRows(results[2].Rows)
	response.Models = breakdownRows(results[3].Rows)
	for _, r := range results[4].Rows {
		response.Tools = append(response.Tools, ToolRowDTO{
			Tool: toString(r[0]), Calls: toInt(r[1]), Failures: toInt(r[2]), Rejections: toInt(r[3]),
			AvgDurationMs: toFloat(r[4]), P95DurationMs: toFloat(r[5]),
		})
	}
	for _, r := range results[5].Rows {
		response.Errors = append(response.Errors, ErrorRowDTO{
			StatusCode: toString(
				r[0],
			),
			Error:    toString(r[1]),
			Model:    toString(r[2]),
			Count:    toInt(r[3]),
			LastSeen: toInt(r[4]),
		})
	}
	if rows := results[6].Rows; len(rows) > 0 {
		response.FilterOptions = FilterOptionsDTO{
			Users: toStrings(rows[0][0]), Models: toStrings(rows[0][1]), Terminals: toStrings(rows[0][2]),
		}
	}

	return response, nil
}

func (s *AIUsageService) GetSessions(
	projectID uuid.UUID,
	request *SessionsRequest,
	user *users_models.User,
) (*SessionsResponseDTO, error) {
	if err := s.authorize(projectID, user, &request.UsageFilter); err != nil {
		return nil, err
	}
	if request.Limit <= 0 || request.Limit > maxSessionsPage {
		return nil, fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalidRequest, maxSessionsPage)
	}
	if request.Offset < 0 {
		return nil, fmt.Errorf("%w: offset must not be negative", ErrInvalidRequest)
	}

	results, err := logs_core.GetAIRepository().ExecuteScopedSQL(projectID, &logs_core.ScopedSQLRequestDTO{
		From:          request.From,
		To:            request.To,
		MessagePrefix: eventPrefix,
		Statements:    sessionsStatements(request),
	})
	if err != nil {
		return nil, err
	}

	response := &SessionsResponseDTO{Sessions: []SessionRowDTO{}}
	for _, r := range results[0].Rows {
		response.Sessions = append(response.Sessions, SessionRowDTO{
			SessionID: toString(r[0]), User: toString(r[1]), Terminal: toString(r[2]),
			FirstSeen: toInt(r[3]), LastSeen: toInt(r[4]), Requests: toInt(r[5]), CostUSD: toFloat(r[6]),
			Tokens: toInt(r[7]), Prompts: toInt(r[8]), ToolCalls: toInt(r[9]), Errors: toInt(r[10]),
			Models: toStrings(r[11]),
		})
	}
	if rows := results[1].Rows; len(rows) > 0 {
		response.Total = toInt(rows[0][0])
	}

	return response, nil
}

func (s *AIUsageService) GetSessionEvents(
	projectID uuid.UUID,
	sessionID string,
	filter *UsageFilter,
	user *users_models.User,
) (*SessionEventsResponseDTO, error) {
	if err := s.authorize(projectID, user, filter); err != nil {
		return nil, err
	}
	if sessionID == "" {
		return nil, fmt.Errorf("%w: session ID is required", ErrInvalidRequest)
	}

	results, err := logs_core.GetAIRepository().ExecuteScopedSQL(projectID, &logs_core.ScopedSQLRequestDTO{
		From:          filter.From,
		To:            filter.To,
		MessagePrefix: eventPrefix,
		Statements:    []logs_core.ScopedSQLStatement{sessionEventsStatement(sessionID)},
	})
	if err != nil {
		return nil, err
	}

	response := &SessionEventsResponseDTO{Events: eventRows(results[0].Rows)}
	response.Truncated = len(response.Events) >= maxSessionEvents

	return response, nil
}

func (s *AIUsageService) GetEvents(
	projectID uuid.UUID,
	request *EventsRequest,
	user *users_models.User,
) (*EventsResponseDTO, error) {
	if err := s.authorize(projectID, user, &request.UsageFilter); err != nil {
		return nil, err
	}
	if request.Limit <= 0 || request.Limit > maxEventsPage {
		return nil, fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalidRequest, maxEventsPage)
	}
	if request.Offset < 0 {
		return nil, fmt.Errorf("%w: offset must not be negative", ErrInvalidRequest)
	}

	results, err := logs_core.GetAIRepository().ExecuteScopedSQL(projectID, &logs_core.ScopedSQLRequestDTO{
		From:          request.From,
		To:            request.To,
		MessagePrefix: eventPrefix,
		Statements:    eventsStatements(request),
	})
	if err != nil {
		return nil, err
	}

	response := &EventsResponseDTO{Events: eventRows(results[0].Rows), EventTypes: []string{}}
	if rows := results[1].Rows; len(rows) > 0 {
		response.Total = toInt(rows[0][0])
	}
	for _, r := range results[2].Rows {
		response.EventTypes = append(response.EventTypes, strings.TrimPrefix(toString(r[0]), eventPrefix))
	}

	return response, nil
}

func (s *AIUsageService) authorize(projectID uuid.UUID, user *users_models.User, filter *UsageFilter) error {
	canAccess, _, err := s.projectService.CanUserAccessProject(projectID, user)
	if err != nil {
		return fmt.Errorf("failed to verify project access: %w", err)
	}
	if !canAccess {
		return ErrForbidden
	}

	if filter.From.IsZero() || filter.To.IsZero() || !filter.From.Before(filter.To) {
		return fmt.Errorf("%w: from and to are required and from must be before to", ErrInvalidRequest)
	}
	if filter.To.Sub(filter.From) > maxRange {
		return fmt.Errorf("%w: time range is longer than %d days", ErrInvalidRequest, int(maxRange.Hours()/24))
	}

	return nil
}

// niceBucketSeconds rounds up to a clock-friendly size, so bars start on
// round times (a 24h range gets 30-minute bars, not 24-minute ones).
func niceBucketSeconds(seconds int64) int64 {
	for _, nice := range []int64{60, 300, 600, 900, 1800, 3600, 7200, 10800, 21600, 43200, 86400} {
		if seconds <= nice {
			return nice
		}
	}
	days := (seconds + 86399) / 86400
	return days * 86400
}

func eventRows(rows [][]any) []SessionEventDTO {
	events := make([]SessionEventDTO, 0, len(rows))
	for _, r := range rows {
		fields := map[string]string{}
		_ = json.Unmarshal([]byte(toString(r[2])), &fields)
		events = append(events, SessionEventDTO{
			Timestamp: toInt(r[0]),
			Event:     strings.TrimPrefix(toString(r[1]), eventPrefix),
			Fields:    fields,
		})
	}
	return events
}

func breakdownRows(rows [][]any) []BreakdownRowDTO {
	result := make([]BreakdownRowDTO, 0, len(rows))
	for _, r := range rows {
		result = append(result, BreakdownRowDTO{
			Key: toString(r[0]), Requests: toInt(r[1]), CostUSD: toFloat(r[2]), InputTokens: toInt(r[3]),
			OutputTokens: toInt(r[4]), CacheReadTokens: toInt(r[5]), CacheCreationTokens: toInt(r[6]),
			Sessions: toInt(r[7]), LastSeen: toInt(r[8]),
		})
	}
	return result
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int64:
		return float64(n)
	case int32:
		return float64(n)
	case uint64:
		return float64(n)
	default:
		return 0
	}
}

func toInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case uint64:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return 0
	}
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func toStrings(v any) []string {
	items, _ := v.([]any)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}
