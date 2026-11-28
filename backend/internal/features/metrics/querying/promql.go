package metrics_querying

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"sort"
	"time"

	metrics_promql "obzly/internal/features/metrics/promql"
	users_models "obzly/internal/features/users/models"
	obzly_query "obzly/internal/query"
	"obzly/internal/util/logger"

	"github.com/google/uuid"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
)

const (
	promQLQueryTimeout = 30 * time.Second
	maxPromQLSeries    = 100
	// hardMaxPromQLSeries bounds what a caller can ask for with MaxSeries.
	hardMaxPromQLSeries = 10000
	// defaultPromQLPoints sets the step when none is given.
	defaultPromQLPoints = 60
)

type PromQLQueryRequestDTO struct {
	Query string     `json:"query"           binding:"required"`
	Start *time.Time `json:"start,omitempty"`
	End   *time.Time `json:"end,omitempty"`
	// StepSeconds for range queries; 0 picks ~60 points.
	StepSeconds int `json:"stepSeconds,omitempty"`
	// Instant evaluates once at End (default now).
	Instant bool `json:"instant,omitempty"`
	// MaxSeries raises the series limit for bulk callers (up to 10000; default 100).
	MaxSeries int `json:"maxSeries,omitempty"`
}

type PromQLPointDTO struct {
	Timestamp time.Time `json:"timestamp"`
	Value     float64   `json:"value"`
}

type PromQLSeriesDTO struct {
	Labels map[string]string `json:"labels"`
	Points []PromQLPointDTO  `json:"points"`
}

type PromQLQueryResponseDTO struct {
	ResultType  string            `json:"resultType"`
	Series      []PromQLSeriesDTO `json:"series"`
	Scalar      *float64          `json:"scalar,omitempty"`
	StepSeconds int               `json:"stepSeconds,omitempty"`
	// Truncated: more series matched than the limit.
	Truncated bool `json:"truncated,omitempty"`
}

// ErrInvalidPromQL wraps errors that are safe and useful to show the caller
// (syntax, resolution, evaluation).
var ErrInvalidPromQL = errors.New("invalid PromQL")

// ErrPromQLTimeout: a valid query that ran past promQLQueryTimeout. It was
// "invalid PromQL" and a 400, as if the query were wrong.
var ErrPromQLTimeout = errors.New("the PromQL query timed out")

// ErrPromQLFailed: storage failed under a valid query. The cause is logged,
// never returned: it used to reach the caller as "invalid PromQL: query
// fragment failed: ...", with executor ids and segment paths.
var ErrPromQLFailed = errors.New("failed to execute the PromQL query")

func (s *MetricQueryService) PromQLQuery(
	ctx context.Context,
	projectID uuid.UUID,
	request *PromQLQueryRequestDTO,
	user *users_models.User,
) (*PromQLQueryResponseDTO, error) {
	if err := s.requireAccess(projectID, user); err != nil {
		return nil, err
	}
	release, err := s.promQLLimiter.Acquire(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	defer release()

	end := time.Now().UTC()
	if request.End != nil {
		end = request.End.UTC()
	}
	start := end.Add(-time.Hour)
	if request.Start != nil {
		start = request.Start.UTC()
	}
	if !start.Before(end) && !request.Instant {
		return nil, fmt.Errorf("%w: start must be before end", ErrInvalidPromQL)
	}

	ctx, cancel := context.WithTimeout(ctx, promQLQueryTimeout)
	defer cancel()

	// OTel's dotted names still work: system.cpu.time -> system_cpu_time.
	expression := metrics_promql.NormalizePromQL(request.Query)

	var query promql.Query
	step := time.Duration(0)
	if request.Instant {
		query, err = metrics_promql.NewInstantQuery(ctx, projectID, expression, end)
	} else {
		step = time.Duration(request.StepSeconds) * time.Second
		if step <= 0 {
			step = max(end.Sub(start)/defaultPromQLPoints, time.Second).Truncate(time.Second)
		}
		if err := metrics_promql.ValidateRangeQueryResolution(start, end, step); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidPromQL, err)
		}
		query, err = metrics_promql.NewRangeQuery(ctx, projectID, expression, start, end, step)
	}
	if err != nil {
		// Creating the query already runs the pushed-down fragments in storage.
		return nil, promQLError(ctx, projectID, err)
	}
	defer query.Close()

	result := query.Exec(ctx)
	if result.Err != nil {
		return nil, promQLError(ctx, projectID, result.Err)
	}

	response := toPromQLResponse(result.Value, seriesLimit(request.MaxSeries))
	response.StepSeconds = int(step / time.Second)
	return response, nil
}

// toPromQLResponse converts at most limit series (the ones with the most
// points): converting every series first held up to 50M samples as DTOs.
func toPromQLResponse(value any, limit int) *PromQLQueryResponseDTO {
	response := &PromQLQueryResponseDTO{Series: []PromQLSeriesDTO{}}
	switch v := value.(type) {
	case promql.Matrix:
		response.ResultType = "matrix"
		if len(v) > limit {
			v = slices.Clone(v)
			sort.SliceStable(v, func(i, j int) bool { return len(v[i].Floats) > len(v[j].Floats) })
			v = v[:limit]
			response.Truncated = true
		}
		for _, series := range v {
			points := make([]PromQLPointDTO, 0, len(series.Floats))
			for _, p := range series.Floats {
				points = append(points, PromQLPointDTO{time.UnixMilli(p.T).UTC(), finite(p.F)})
			}
			response.Series = append(response.Series, PromQLSeriesDTO{labelMap(series.Metric), points})
		}
	case promql.Vector:
		response.ResultType = "vector"
		if len(v) > limit {
			v = v[:limit]
			response.Truncated = true
		}
		for _, sample := range v {
			response.Series = append(response.Series, PromQLSeriesDTO{
				labelMap(sample.Metric),
				[]PromQLPointDTO{{time.UnixMilli(sample.T).UTC(), finite(sample.F)}},
			})
		}
	case promql.Scalar:
		response.ResultType = "scalar"
		f := finite(v.V)
		response.Scalar = &f
	default:
		response.ResultType = "unsupported"
	}
	return response
}

func seriesLimit(requested int) int {
	if requested <= 0 {
		return maxPromQLSeries
	}
	return min(requested, hardMaxPromQLSeries)
}

func labelMap(metric labels.Labels) map[string]string {
	out := map[string]string{}
	metric.Range(func(l labels.Label) { out[l.Name] = l.Value })
	return out
}

// finite maps NaN/Inf to 0 so the result stays valid JSON.
func finite(f float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return f
}

// promQLError classifies a failure to build or evaluate a query: past the
// deadline here or in a query fragment (the engine's own timeout can end one
// just before ctx), a mistake in the query, or a storage failure, whose
// cause is logged here and left out of the error.
func promQLError(ctx context.Context, projectID uuid.UUID, err error) error {
	if ctx.Err() != nil || obzly_query.IsTimeout(err) {
		return fmt.Errorf("%w after %s: narrow the time range or the series it selects",
			ErrPromQLTimeout, promQLQueryTimeout)
	}
	message, ok := metrics_promql.ClientMessage(err)
	if !ok {
		logger.GetLogger().Error("PromQL query failed in storage",
			"projectId", projectID.String(), "error", err)
		return ErrPromQLFailed
	}
	return fmt.Errorf("%w: %s", ErrInvalidPromQL, message)
}

// promQLErrorStatus is the HTTP status for a PromQL query's error: 400 for
// a mistake in it, 408 when it ran out of time, 500 when storage failed, 0
// for anything else.
func promQLErrorStatus(err error) int {
	switch {
	case errors.Is(err, ErrPromQLTimeout):
		return http.StatusRequestTimeout
	case errors.Is(err, ErrInvalidPromQL):
		return http.StatusBadRequest
	case errors.Is(err, ErrPromQLFailed):
		return http.StatusInternalServerError
	}
	return 0
}
