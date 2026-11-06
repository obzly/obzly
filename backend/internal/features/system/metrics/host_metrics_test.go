package system_metrics

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_GetHostMetrics_ReturnsSaneValues(t *testing.T) {
	metrics, err := GetHostMetrics()
	require.NoError(t, err)

	require.GreaterOrEqual(t, metrics.CPUPercent, 0.0)
	require.Positive(t, metrics.MemoryTotalBytes)
	require.GreaterOrEqual(t, metrics.MemoryUsedBytes, int64(0))
	require.LessOrEqual(t, metrics.MemoryUsedBytes, metrics.MemoryTotalBytes)
}
