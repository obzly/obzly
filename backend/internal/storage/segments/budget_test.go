package segments_test

import (
	"testing"

	"obzly/internal/storage/segments"

	"github.com/stretchr/testify/require"
)

func Test_NewMemoryBudget_SumsCapsTimesCountsPlusAQuarterForGo(t *testing.T) {
	const gib = int64(1) << 30
	items := []segments.BudgetItem{
		{Kind: "compactor", Count: 4, LimitBytes: segments.MemoryLimitBytes("1GB")},
		{Kind: "querier_metrics", Count: 1, LimitBytes: segments.MemoryLimitBytes("512MB")},
		{Kind: "writer_logs", Count: 2, LimitBytes: segments.MemoryLimitBytes("256MB")},
	}

	budget := segments.NewMemoryBudget(items, 4*gib)

	require.Equal(t, int64(5_024_000_000), budget.DuckDBBytes)
	require.Equal(t, int64(1_256_000_000), budget.GoBytes)
	require.Equal(t, int64(6_280_000_000), budget.TotalBytes)
	require.True(t, budget.Exceeds(), "6.3 GB of caps on a 4 GiB pod")
	require.Zero(t, budget.Uncapped)
	require.Equal(t, "compactor 4 x 1.0 GB, querier_metrics 1 x 512 MB, writer_logs 2 x 256 MB", budget.Summary())

	require.False(t, segments.NewMemoryBudget(items, 8*gib).Exceeds())
	require.False(t, segments.NewMemoryBudget(items, 0).Exceeds(), "no container limit: nothing to exceed")
}

func Test_NewMemoryBudget_UncappedHandle_CountsAsDuckDBsDefault(t *testing.T) {
	const gib = int64(1) << 30
	budget := segments.NewMemoryBudget([]segments.BudgetItem{{Kind: "tail_merger_logs", Count: 2}}, 4*gib)

	require.Equal(t, 2, budget.Uncapped)
	require.Equal(t, int64(2*3_435_973_836), budget.DuckDBBytes, "80% of 4 GiB each")
	require.True(t, budget.Exceeds())
	require.Equal(t, "tail_merger_logs 2 x default", budget.Summary())
}
