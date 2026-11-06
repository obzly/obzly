package metrics_querying

import (
	"testing"

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
