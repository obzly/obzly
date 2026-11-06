// Package metrics_querying is the session-authenticated metrics API used by
// the frontend. metrics_promapi is the Prometheus-compatible API for
// external tools, authenticated by project API key.
package metrics_querying

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	projects_services "obzly/internal/features/projects/services"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/util/attrnames"
	"obzly/internal/util/querylimit"

	"github.com/google/uuid"
	"github.com/prometheus/common/model"
)

// metadataQueryTimeout bounds metadata lookups, which have no row LIMIT; the
// request context alone could be held open indefinitely by the client.
const metadataQueryTimeout = 30 * time.Second

type MetricQueryService struct {
	projectService *projects_services.ProjectService
	promQLLimiter  *querylimit.Limiter
}

func (s *MetricQueryService) ListMetricNames(
	ctx context.Context,
	projectID uuid.UUID,
	user *users_models.User,
) (*metrics_core.ListMetricNamesResponseDTO, error) {
	if err := s.requireAccess(projectID, user); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, metadataQueryTimeout)
	defer cancel()
	return metrics_core.GetMetricCoreRepository().ListMetricNames(ctx, projectID, nil)
}

// ListLabelValues backs the dashboard filter bar (Namespace/Pod/Node/etc.).
func (s *MetricQueryService) ListLabelValues(
	ctx context.Context,
	projectID uuid.UUID,
	request *metrics_core.ListLabelValuesRequestDTO,
	user *users_models.User,
) (*metrics_core.ListLabelValuesResponseDTO, error) {
	if err := s.requireAccess(projectID, user); err != nil {
		return nil, err
	}
	if request.LabelName == "" {
		return nil, errors.New("labelName is required")
	}
	ctx, cancel := context.WithTimeout(ctx, metadataQueryTimeout)
	defer cancel()
	normalized := *request
	normalized.LabelName = attrnames.Normalize(request.LabelName)
	normalized.Matchers = normalizeMatchers(request.Matchers)
	return metrics_core.GetMetricCoreRepository().ListLabelValues(ctx, projectID, &normalized)
}

// normalizeMatchers accepts dotted OTel names, as PromQL queries do.
func normalizeMatchers(matchers []metrics_core.MatcherDTO) []metrics_core.MatcherDTO {
	out := make([]metrics_core.MatcherDTO, len(matchers))
	for i, matcher := range matchers {
		switch {
		case matcher.Name != model.MetricNameLabel:
			matcher.Name = attrnames.Normalize(matcher.Name)
		case matcher.Type == metrics_core.MatchEqual || matcher.Type == metrics_core.MatchNotEqual:
			matcher.Value = attrnames.NormalizeMetricName(matcher.Value)
		default: // regex: an escaped dot becomes an underscore
			matcher.Value = strings.ReplaceAll(matcher.Value, `\.`, "_")
		}
		out[i] = matcher
	}
	return out
}

// GetProjectStats returns the project's metrics consumption stats.
func (s *MetricQueryService) GetProjectStats(
	projectID uuid.UUID,
	user *users_models.User,
) (*metrics_core.MetricsStatsDTO, error) {
	if err := s.requireAccess(projectID, user); err != nil {
		return nil, err
	}
	return metrics_core.GetMetricCoreRepository().GetProjectMetricStats(projectID)
}

func (s *MetricQueryService) requireAccess(projectID uuid.UUID, user *users_models.User) error {
	canAccess, _, err := s.projectService.CanUserAccessProject(projectID, user)
	if err != nil {
		return fmt.Errorf("failed to check project access: %w", err)
	}
	if !canAccess {
		return errors.New("insufficient permissions to access project metrics")
	}
	return nil
}
