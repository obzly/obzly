package servicemap

import (
	"testing"
	"time"

	metrics_core "obzly/internal/features/metrics/core"

	"github.com/stretchr/testify/assert"
)

func samplesAt(base time.Time, points ...[2]float64) []metrics_core.SampleDTO {
	out := make([]metrics_core.SampleDTO, 0, len(points))
	for _, p := range points {
		out = append(out, metrics_core.SampleDTO{
			Timestamp: base.Add(time.Duration(p[0]) * time.Second),
			Value:     p[1],
		})
	}
	return out
}

func Test_counterIncrease_SpanLongerThanWindow_ScaledToWindow(t *testing.T) {
	base := time.Now()
	// 6000 bytes over 120s = 50 B/s; a 60s window is 3000.
	got := counterIncrease(samplesAt(base, [2]float64{0, 1000}, [2]float64{120, 7000}), 60)
	assert.Equal(t, int64(3000), got)
}

func Test_counterIncrease_SpanShorterThanWindow_NotExtrapolated(t *testing.T) {
	base := time.Now()
	got := counterIncrease(samplesAt(base, [2]float64{0, 1024}, [2]float64{1, 3072}), 86400)
	assert.Equal(t, int64(2048), got)
}

func Test_counterIncrease_SingleSample_ReturnsZero(t *testing.T) {
	got := counterIncrease(samplesAt(time.Now(), [2]float64{0, 5000}), 60)
	assert.Equal(t, int64(0), got)
}

func Test_counterIncrease_CounterReset_ReturnsZero(t *testing.T) {
	base := time.Now()
	got := counterIncrease(samplesAt(base, [2]float64{0, 9000}, [2]float64{60, 100}), 60)
	assert.Equal(t, int64(0), got)
}
