// Package correlation_api is the read-only API over incident sessions; only
// correlation_worker creates them.
package correlation_api

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	correlation_core "obzly/internal/features/correlation/core"
	logs_core "obzly/internal/features/logs/core"
	projects_services "obzly/internal/features/projects/services"
	spans_core "obzly/internal/features/spans/core"
	users_models "obzly/internal/features/users/models"

	"github.com/google/uuid"
)

// maxSessionListLookback keeps a request without a range from reading every
// session ever created; generous, since sessions are low-volume rows.
const maxSessionListLookback = 30 * 24 * time.Hour

const (
	// maxImpactSignalRefs bounds the id lists the impact queries bind; a
	// session's services show up within its first signals anyway.
	maxImpactSignalRefs = 1000
	maxImpactServices   = 50
	// impactWindowPadding widens the window past Postgres's microsecond
	// rounding of occurred_at, so the signals at its edges still count.
	impactWindowPadding    = time.Millisecond
	impactAggregationLimit = 10_000
)

type CorrelationService struct {
	projectService *projects_services.ProjectService
}

func (s *CorrelationService) ListSessions(
	projectID uuid.UUID,
	from, to *time.Time,
	limit int,
	user *users_models.User,
) (*correlation_core.ListSessionsResponseDTO, error) {
	if err := s.requireAccess(projectID, user); err != nil {
		return nil, err
	}

	resolvedFrom, resolvedTo := clampTimeRange(from, to)

	response, err := correlation_core.GetIncidentSessionRepository().
		ListSessions(projectID, &correlation_core.ListSessionsRequestDTO{
			TimeRange: &correlation_core.TimeRangeDTO{From: &resolvedFrom, To: &resolvedTo},
			Limit:     limit,
		})
	if err != nil {
		return nil, fmt.Errorf("failed to list incident sessions: %w", err)
	}
	return response, nil
}

func (s *CorrelationService) GetSessionDetail(
	projectID, sessionID uuid.UUID,
	user *users_models.User,
) (*correlation_core.SessionDetailDTO, error) {
	if err := s.requireAccess(projectID, user); err != nil {
		return nil, err
	}

	detail, err := correlation_core.GetIncidentSessionRepository().
		GetSessionDetail(projectID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to load incident session detail: %w", err)
	}
	return detail, nil
}

// GetSessionImpact ranks the session's services by error logs plus error
// spans within its window. The services are the session's own service plus
// the owners of its signals. Three bounded queries over the window: log
// signal owners, error spans per service, error logs per service (logs_1m).
func (s *CorrelationService) GetSessionImpact(
	projectID, sessionID uuid.UUID,
	user *users_models.User,
) (*correlation_core.SessionImpactDTO, error) {
	if err := s.requireAccess(projectID, user); err != nil {
		return nil, err
	}
	return ComputeSessionImpact(projectID, sessionID)
}

// ComputeSessionImpact is GetSessionImpact without the access check, for
// incident notifications.
func ComputeSessionImpact(projectID, sessionID uuid.UUID) (*correlation_core.SessionImpactDTO, error) {
	session, signals, err := correlation_core.GetIncidentSessionRepository().GetSessionSignals(projectID, sessionID)
	if err != nil {
		return nil, err
	}

	from := session.WindowStart.Add(-impactWindowPadding)
	to := session.WindowEnd.Add(impactWindowPadding)
	spanIDs, logIDs := signalIDs(signals)

	services := map[string]bool{session.ServiceName: true}
	logOwners, err := logSignalServices(projectID, logIDs, from, to)
	if err != nil {
		return nil, err
	}
	for _, service := range logOwners {
		services[service] = true
	}

	errorSpans, err := spans_core.GetSpanCoreRepository().ServiceErrorSpanCounts(
		projectID, &spans_core.TimeRangeDTO{From: &from, To: &to}, sortedNames(services), spanIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to count error spans: %w", err)
	}
	for service := range errorSpans {
		services[service] = true
	}

	errorLogs, err := errorLogCounts(projectID, sortedNames(services), from, to)
	if err != nil {
		return nil, err
	}

	rows := make([]correlation_core.ServiceImpactDTO, 0, len(services))
	for service := range services {
		row := correlation_core.ServiceImpactDTO{
			ServiceName: service,
			ErrorLogs:   errorLogs[service],
			ErrorSpans:  errorSpans[service],
		}
		row.Total = row.ErrorLogs + row.ErrorSpans
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Total != rows[j].Total {
			return rows[i].Total > rows[j].Total
		}
		return rows[i].ServiceName < rows[j].ServiceName
	})
	if len(rows) > maxImpactServices {
		rows = rows[:maxImpactServices]
	}

	return &correlation_core.SessionImpactDTO{
		SessionID:   session.ID.String(),
		WindowStart: session.WindowStart,
		WindowEnd:   session.WindowEnd,
		Services:    rows,
	}, nil
}

func (s *CorrelationService) requireAccess(projectID uuid.UUID, user *users_models.User) error {
	canAccess, _, err := s.projectService.CanUserAccessProject(projectID, user)
	if err != nil {
		return fmt.Errorf("failed to verify project access: %w", err)
	}
	if !canAccess {
		return errors.New("insufficient permissions to query project correlation sessions")
	}
	return nil
}

// clampTimeRange is servicemap.clampTimeRange with maxSessionListLookback.
func clampTimeRange(from, to *time.Time) (time.Time, time.Time) {
	resolvedTo := time.Now().UTC()
	if to != nil {
		resolvedTo = *to
	}

	earliestAllowedFrom := resolvedTo.Add(-maxSessionListLookback)
	resolvedFrom := earliestAllowedFrom
	if from != nil && from.After(earliestAllowedFrom) {
		resolvedFrom = *from
	}

	return resolvedFrom, resolvedTo
}

func signalIDs(signals []*correlation_core.IncidentSessionSignal) (spanIDs, logIDs []string) {
	for _, signal := range signals {
		switch signal.SignalType {
		case correlation_core.SignalTypeSpan:
			if id, _ := signal.SignalRef["spanId"].(string); id != "" && len(spanIDs) < maxImpactSignalRefs {
				spanIDs = append(spanIDs, id)
			}
		case correlation_core.SignalTypeLog:
			if id, _ := signal.SignalRef["logId"].(string); id != "" && len(logIDs) < maxImpactSignalRefs {
				logIDs = append(logIDs, id)
			}
		}
	}
	return spanIDs, logIDs
}

// logSignalServices is the services owning the session's log signals: a
// log-only service merged in by a shared trace has no span signal to name it.
func logSignalServices(projectID uuid.UUID, logIDs []string, from, to time.Time) ([]string, error) {
	if len(logIDs) == 0 {
		return nil, nil
	}
	response, err := logs_core.GetLogCoreRepository().ExecuteAggregation(
		context.Background(), projectID, &logs_core.AggregationRequestDTO{
			Query: &logs_core.QueryNode{
				Type: logs_core.QueryNodeTypeCondition,
				Condition: &logs_core.ConditionNode{
					Field: "id", Operator: logs_core.ConditionOperatorIn, Value: logIDs,
				},
			},
			TimeRange:  &logs_core.TimeRangeDTO{From: &from, To: &to},
			GroupField: "service_name",
			Limit:      maxImpactServices,
		})
	if err != nil {
		return nil, fmt.Errorf("failed to resolve log signal services: %w", err)
	}
	services := make([]string, 0, len(response.Buckets))
	for _, bucket := range response.Buckets {
		if bucket.GroupKey != "" {
			services = append(services, bucket.GroupKey)
		}
	}
	return services, nil
}

// errorLogCounts counts ERROR logs (the level sessions are built from) per
// service. One bucket wider than the window, in whole minutes, lets logs_1m
// answer it; a service spans at most two buckets, summed here.
func errorLogCounts(projectID uuid.UUID, services []string, from, to time.Time) (map[string]int64, error) {
	bucket := to.Sub(from).Truncate(time.Minute) + 2*time.Minute
	response, err := logs_core.GetLogCoreRepository().ExecuteAggregation(
		context.Background(), projectID, &logs_core.AggregationRequestDTO{
			Query: &logs_core.QueryNode{
				Type: logs_core.QueryNodeTypeLogical,
				Logic: &logs_core.LogicalNode{
					Operator: logs_core.LogicalOperatorAnd,
					Children: []logs_core.QueryNode{
						{
							Type: logs_core.QueryNodeTypeCondition,
							Condition: &logs_core.ConditionNode{
								Field: "level", Operator: logs_core.ConditionOperatorEquals,
								Value: string(logs_core.LogLevelError),
							},
						},
						{
							Type: logs_core.QueryNodeTypeCondition,
							Condition: &logs_core.ConditionNode{
								Field: "service_name", Operator: logs_core.ConditionOperatorIn, Value: services,
							},
						},
					},
				},
			},
			TimeRange:  &logs_core.TimeRangeDTO{From: &from, To: &to},
			GroupField: "service_name",
			BucketMs:   bucket.Milliseconds(),
			Limit:      impactAggregationLimit,
		})
	if err != nil {
		return nil, fmt.Errorf("failed to count error logs: %w", err)
	}
	counts := make(map[string]int64, len(services))
	for _, row := range response.Buckets {
		counts[row.GroupKey] += row.Count
	}
	return counts, nil
}

func sortedNames(set map[string]bool) []string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
