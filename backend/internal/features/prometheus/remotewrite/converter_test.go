package prometheus_remotewrite

import (
	"testing"
	"time"

	metrics_core "obzly/internal/features/metrics/core"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/prometheus/prometheus/prompb"
)

func Test_ConvertTimeSeries_UsesNameLabelAsMetricNameAndDropsItFromLabels(t *testing.T) {
	projectID := uuid.New()
	now := time.Now().UTC()

	series := []prompb.TimeSeries{
		{
			Labels: []prompb.Label{
				{Name: "__name__", Value: "http_requests_total"},
				{Name: "service", Value: "api"},
			},
			Samples: []prompb.Sample{{Value: 12.5, Timestamp: now.UnixMilli()}},
		},
	}

	samples := ConvertTimeSeries(projectID, series, nil)
	require.Len(t, samples, 1)

	s := samples[0]
	assert.Equal(t, "http_requests_total", s.MetricName)
	assert.Equal(t, "api", s.Labels["service"])
	_, hasName := s.Labels["__name__"]
	assert.False(t, hasName)
	assert.InDelta(t, 12.5, s.Value, 0.0001)
	assert.True(t, s.Timestamp.Equal(now.Truncate(time.Millisecond)))
}

func Test_ConvertTimeSeries_MultipleSamplesPerSeries(t *testing.T) {
	projectID := uuid.New()
	now := time.Now().UTC()

	series := []prompb.TimeSeries{
		{
			Labels: []prompb.Label{{Name: "__name__", Value: "cpu_usage"}},
			Samples: []prompb.Sample{
				{Value: 1, Timestamp: now.UnixMilli()},
				{Value: 2, Timestamp: now.Add(time.Minute).UnixMilli()},
			},
		},
	}

	samples := ConvertTimeSeries(projectID, series, nil)
	require.Len(t, samples, 2)
}

func Test_ConvertTimeSeries_SeriesWithoutNameLabel_Skipped(t *testing.T) {
	projectID := uuid.New()
	series := []prompb.TimeSeries{
		{
			Labels:  []prompb.Label{{Name: "service", Value: "api"}},
			Samples: []prompb.Sample{{Value: 1, Timestamp: time.Now().UnixMilli()}},
		},
	}

	samples := ConvertTimeSeries(projectID, series, nil)
	assert.Empty(t, samples)
}

func Test_ConvertTimeSeries_TypeFromMetadata(t *testing.T) {
	projectID := uuid.New()
	metadata := MetadataTypes([]prompb.MetricMetadata{
		{Type: prompb.MetricMetadata_COUNTER, MetricFamilyName: "http_requests_total"},
		{Type: prompb.MetricMetadata_GAUGE, MetricFamilyName: "cpu_usage"},
	})

	series := []prompb.TimeSeries{
		{
			Labels:  []prompb.Label{{Name: "__name__", Value: "http_requests_total"}},
			Samples: []prompb.Sample{{Value: 1, Timestamp: time.Now().UnixMilli()}},
		},
		{
			Labels:  []prompb.Label{{Name: "__name__", Value: "cpu_usage"}},
			Samples: []prompb.Sample{{Value: 1, Timestamp: time.Now().UnixMilli()}},
		},
	}

	samples := ConvertTimeSeries(projectID, series, metadata)
	require.Len(t, samples, 2)

	byName := map[string]metrics_core.MetricType{}
	for _, s := range samples {
		byName[s.MetricName] = s.Type
	}
	assert.Equal(t, metrics_core.MetricTypeCounter, byName["http_requests_total"])
	assert.Equal(t, metrics_core.MetricTypeGauge, byName["cpu_usage"])
}

func Test_ConvertTimeSeries_TypeHeuristicWithoutMetadata(t *testing.T) {
	projectID := uuid.New()
	series := []prompb.TimeSeries{
		{
			Labels:  []prompb.Label{{Name: "__name__", Value: "requests_total"}},
			Samples: []prompb.Sample{{Value: 1, Timestamp: time.Now().UnixMilli()}},
		},
		{
			Labels:  []prompb.Label{{Name: "__name__", Value: "temperature_celsius"}},
			Samples: []prompb.Sample{{Value: 1, Timestamp: time.Now().UnixMilli()}},
		},
	}

	samples := ConvertTimeSeries(projectID, series, nil)
	byName := map[string]metrics_core.MetricType{}
	for _, s := range samples {
		byName[s.MetricName] = s.Type
	}
	assert.Equal(t, metrics_core.MetricTypeCounter, byName["requests_total"])
	assert.Equal(t, metrics_core.MetricTypeGauge, byName["temperature_celsius"])
}
