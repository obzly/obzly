package metrics_querying

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	metrics_promql "obzly/internal/features/metrics/promql"

	"github.com/google/uuid"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
	"github.com/stretchr/testify/require"
)

// Only the series kept are converted: converting all of them first held up
// to 50M samples as DTOs for a response of maxSeries.
func Test_ToPromQLResponse_MoreSeriesThanTheLimit_KeepsTheLongestUnconverted(t *testing.T) {
	series := func(name string, points int) promql.Series {
		floats := make([]promql.FPoint, points)
		for i := range floats {
			floats[i] = promql.FPoint{T: int64(i) * 1000, F: float64(i)}
		}
		return promql.Series{Metric: labels.FromStrings("job", name), Floats: floats}
	}
	matrix := promql.Matrix{series("short", 1), series("long", 3), series("middle", 2)}

	response := toPromQLResponse(matrix, 2)

	require.True(t, response.Truncated)
	require.Len(t, response.Series, 2)
	require.Equal(t, "long", response.Series[0].Labels["job"])
	require.Equal(t, "middle", response.Series[1].Labels["job"])
	require.Equal(t, "short", matrix[0].Metric.Get("job"), "the engine's result is left as it was")

	vector := promql.Vector{
		{Metric: labels.FromStrings("job", "a")}, {Metric: labels.FromStrings("job", "b")},
	}
	require.Len(t, toPromQLResponse(vector, 1).Series, 1)
}

// A query out of time is a 408 saying so, not a 400 calling it invalid.
func Test_PromQLErrorStatus_TimeoutIs408AndAMistakeIs400(t *testing.T) {
	timeout := fmt.Errorf("%w after 30s: narrow the time range", ErrPromQLTimeout)
	require.Equal(t, http.StatusRequestTimeout, promQLErrorStatus(timeout))
	require.NotContains(t, timeout.Error(), "invalid")
	require.Equal(t, http.StatusBadRequest, promQLErrorStatus(fmt.Errorf("%w: parse error", ErrInvalidPromQL)))
	require.Zero(t, promQLErrorStatus(errors.New("database unavailable")))
}

// A query fragment out of time (the engine's own deadline can end it just
// before the request's) is still the timeout, not "invalid PromQL": the
// lab's 24 h p95 query answered 400 that way, first from evaluation and then
// from building the query, which runs pushed-down fragments.
func Test_PromQLQuery_FragmentDeadline_IsReportedAsTheTimeout(t *testing.T) {
	err := promQLError(context.Background(), uuid.New(),
		errors.New("query fragment failed: fragment f1: context deadline exceeded"))

	require.ErrorIs(t, err, ErrPromQLTimeout)
	require.Equal(t, http.StatusRequestTimeout, promQLErrorStatus(err))
	require.ErrorIs(t, promQLError(context.Background(), uuid.New(), errors.New("bad aggregation")), ErrInvalidPromQL)
}

// A storage failure is neither the caller's mistake nor theirs to read: it
// was "invalid PromQL: query fragment failed: fragment ...: executor ...:
// Cannot open file ...", a 400 naming an executor and a segment path.
func Test_PromQLError_StorageFailure_IsA500WithoutItsCause(t *testing.T) {
	storage := &metrics_promql.StorageError{Err: errors.New(
		`query fragment failed: executor querier-0: Cannot open file "/var/lib/obzly/segments/metrics/1.parquet"`)}

	err := promQLError(context.Background(), uuid.New(), fmt.Errorf("expanding series: %w", storage))

	require.ErrorIs(t, err, ErrPromQLFailed)
	require.Equal(t, http.StatusInternalServerError, promQLErrorStatus(err))
	require.Equal(t, ErrPromQLFailed.Error(), err.Error())
}
