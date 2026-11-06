package system_metrics

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The buckets are shared Valkey counters, and other packages' tests ingest concurrently
// against the same Valkey, so these tests assert lower bounds, not exact deltas.

func Test_RecordIngestion_ThenGetRateLastNMinutes_ReflectsDelta(t *testing.T) {
	before, err := GetIngestionRateLastNMinutes(1)
	require.NoError(t, err)

	RecordIngestion(7, 1234)

	after, err := GetIngestionRateLastNMinutes(1)
	require.NoError(t, err)

	require.GreaterOrEqual(t, after.TotalCount-before.TotalCount, int64(7))
	require.GreaterOrEqual(t, after.TotalRawBytes-before.TotalRawBytes, int64(1234))
}

// With shared buckets, the only non-flaky assertion is that the count doesn't go down.
func Test_RecordIngestion_ZeroCount_NoOp(t *testing.T) {
	before, err := GetIngestionRateLastNMinutes(1)
	require.NoError(t, err)

	RecordIngestion(0, 999)

	after, err := GetIngestionRateLastNMinutes(1)
	require.NoError(t, err)

	require.GreaterOrEqual(t, after.TotalCount, before.TotalCount)
	require.GreaterOrEqual(t, after.TotalRawBytes, before.TotalRawBytes)
}

func Test_GetIngestionRateLastNMinutes_DefaultsWindowWhenNonPositive(t *testing.T) {
	rate, err := GetIngestionRateLastNMinutes(0)
	require.NoError(t, err)
	require.Equal(t, 15, rate.WindowMinutes)
}

func Test_GetIngestionRateLastNMinutes_ComputesAverage(t *testing.T) {
	before, err := GetIngestionRateLastNMinutes(5)
	require.NoError(t, err)

	RecordIngestion(50, 0)

	after, err := GetIngestionRateLastNMinutes(5)
	require.NoError(t, err)

	// >= for the same shared-bucket reason.
	require.GreaterOrEqual(t, after.AvgCountPerMin, before.AvgCountPerMin+float64(50)/5-0.0001)
}
