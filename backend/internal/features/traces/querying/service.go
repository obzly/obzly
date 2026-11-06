// Package traces_querying is the trace query REST API the frontend uses;
// traces_jaeger serves the same data to external Jaeger clients.
package traces_querying

import (
	"context"
	"errors"
	"fmt"

	"obzly/internal/config"
	spans_core "obzly/internal/features/spans/core"
	"obzly/internal/util/querylimit"

	projects_services "obzly/internal/features/projects/services"
	users_models "obzly/internal/features/users/models"

	"github.com/google/uuid"
)

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
