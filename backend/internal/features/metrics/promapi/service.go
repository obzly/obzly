package metrics_promapi

import (
	"context"
	"errors"
	"fmt"
	"time"

	api_keys "obzly/internal/features/api_keys"
	metrics_promql "obzly/internal/features/metrics/promql"
	"obzly/internal/util/attrnames"

	"github.com/google/uuid"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
	"github.com/prometheus/prometheus/util/annotations"
)

var errUnauthenticated = errors.New("invalid or missing API key")

// queryTimeout bounds every call: metadata lookups bypass the engine's
// Timeout, which also only fires between evaluation steps, so a hung
// storage read would outlive it.
const queryTimeout = 2 * time.Minute

// MetricsQueryService authenticates every call by the project's API key:
// Prometheus clients can't send a user Bearer token.
type MetricsQueryService struct {
	apiKeyService *api_keys.ApiKeyService
}

func (s *MetricsQueryService) authenticate(projectID uuid.UUID, apiKey string) error {
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

func (s *MetricsQueryService) InstantQuery(
	projectID uuid.UUID, apiKey, query string, ts time.Time,
) (parser.Value, annotations.Annotations, error) {
	if err := s.authenticate(projectID, apiKey); err != nil {
		return nil, nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	q, err := metrics_promql.NewInstantQuery(ctx, projectID, metrics_promql.NormalizePromQL(query), ts)
	if err != nil {
		return nil, nil, err
	}
	defer q.Close()

	result := q.Exec(ctx)
	if result.Err != nil {
		return nil, nil, result.Err
	}
	return result.Value, result.Warnings, nil
}

func (s *MetricsQueryService) RangeQuery(
	projectID uuid.UUID, apiKey, query string, start, end time.Time, step time.Duration,
) (parser.Value, annotations.Annotations, error) {
	if err := s.authenticate(projectID, apiKey); err != nil {
		return nil, nil, err
	}

	if err := metrics_promql.ValidateRangeQueryResolution(start, end, step); err != nil {
		return nil, nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	q, err := metrics_promql.NewRangeQuery(ctx, projectID, metrics_promql.NormalizePromQL(query), start, end, step)
	if err != nil {
		return nil, nil, err
	}
	defer q.Close()

	result := q.Exec(ctx)
	if result.Err != nil {
		return nil, nil, result.Err
	}
	return result.Value, result.Warnings, nil
}

// Series answers /api/v1/series with match[] semantics: a union across
// matcherSets, an intersection within one.
func (s *MetricsQueryService) Series(
	projectID uuid.UUID, apiKey string, matcherSets [][]*labels.Matcher, start, end time.Time,
) ([]map[string]string, error) {
	if err := s.authenticate(projectID, apiKey); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	querier, err := metrics_promql.NewQueryable(projectID).Querier(start.UnixMilli(), end.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer func() { _ = querier.Close() }()

	seen := map[string]map[string]string{}
	var order []string

	for _, matchers := range matcherSets {
		seriesSet := querier.Select(ctx, false, nil, metrics_promql.NormalizeMatchers(matchers)...)
		for seriesSet.Next() {
			lbls := seriesSet.At().Labels()
			key := lbls.String()
			if _, ok := seen[key]; !ok {
				seen[key] = lbls.Map()
				order = append(order, key)
			}
		}
		if err := seriesSet.Err(); err != nil {
			return nil, err
		}
	}

	result := make([]map[string]string, len(order))
	for i, key := range order {
		result[i] = seen[key]
	}
	return result, nil
}

func (s *MetricsQueryService) LabelNames(
	projectID uuid.UUID, apiKey string, matchers []*labels.Matcher, start, end time.Time,
) ([]string, error) {
	if err := s.authenticate(projectID, apiKey); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	querier, err := metrics_promql.NewQueryable(projectID).Querier(start.UnixMilli(), end.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer func() { _ = querier.Close() }()

	names, _, err := querier.LabelNames(ctx, nil, metrics_promql.NormalizeMatchers(matchers)...)
	return names, err
}

func (s *MetricsQueryService) LabelValues(
	projectID uuid.UUID, apiKey, labelName string, matchers []*labels.Matcher, start, end time.Time,
) ([]string, error) {
	if err := s.authenticate(projectID, apiKey); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	querier, err := metrics_promql.NewQueryable(projectID).Querier(start.UnixMilli(), end.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer func() { _ = querier.Close() }()

	values, _, err := querier.LabelValues(
		ctx, attrnames.Normalize(labelName), nil, metrics_promql.NormalizeMatchers(matchers)...,
	)
	return values, err
}
