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
	start := time.Now().Add(-2 * time.Hour)
	end := time.Now()

	step := metricPanelStep(0, start, end)

	assert.Equal(t, end.Sub(start)/120, step)
}
