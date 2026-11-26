package dashboards_services

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// A supplied bucketMs is floored too: a 1 ms step over a wide range is
// millions of evaluation points per render.
func Test_MetricPanelStep_TinyPositiveBucketMs_FlooredToOneSecond(t *testing.T) {
	start := time.Now().Add(-30 * 24 * time.Hour)
	end := time.Now()

	step := metricPanelStep(1, start, end)

	assert.Equal(t, time.Second, step)
}

func Test_MetricPanelStep_ZeroBucketMs_AutoFlooredToOneSecond(t *testing.T) {
	start := time.Now().Add(-time.Minute)
	end := time.Now()

	step := metricPanelStep(0, start, end)

	assert.Equal(t, time.Second, step)
}

func Test_MetricPanelStep_ReasonableBucketMs_UsedAsIs(t *testing.T) {
	start := time.Now().Add(-time.Hour)
	end := time.Now()

	step := metricPanelStep(30_000, start, end)

	assert.Equal(t, 30*time.Second, step)
}

func Test_MetricPanelStep_ZeroBucketMs_TargetsRoughly120Points(t *testing.T) {
	// One clock reading: with two, the range was a little over 2 h whenever
	// the clock ticked between them, and its step rounded up to 2 m.
	end := time.Now().UTC()
	start := end.Add(-2 * time.Hour)

	step := metricPanelStep(0, start, end)

	assert.Equal(t, end.Sub(start)/120, step)
}

// A 24 h panel's 12 minutes round up to 15: a multiple of 5 m, which
// storage answers from 5-minute partials.
func Test_MetricPanelStep_ZeroBucketMs_RoundsUpToAStepOfAMultipleOf5Minutes(t *testing.T) {
	end := time.Now()

	assert.Equal(t, 15*time.Minute, metricPanelStep(0, end.Add(-24*time.Hour), end))
	assert.Equal(t, 5*time.Minute, metricPanelStep(0, end.Add(-6*time.Hour), end))
	assert.Equal(t, 2*time.Hour, metricPanelStep(0, end.Add(-7*24*time.Hour), end))
	assert.Equal(t, 48*time.Hour, metricPanelStep(0, end.Add(-200*24*time.Hour), end))
}

func Test_AlignToStep_StartBackEndOnToMultiplesOfTheStep(t *testing.T) {
	start := time.Date(2026, 11, 24, 10, 7, 31, 0, time.UTC)
	end := time.Date(2026, 11, 25, 10, 7, 31, 0, time.UTC)

	alignedStart, alignedEnd := alignToStep(start, end, 15*time.Minute)

	assert.Equal(t, time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC), alignedStart)
	assert.Equal(t, time.Date(2026, 11, 25, 10, 15, 0, 0, time.UTC), alignedEnd)
}
