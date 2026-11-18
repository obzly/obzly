package segments

import (
	"fmt"
	"strings"
)

// BudgetItem is Count DuckDB handles of one kind, each capped at LimitBytes
// (0: DuckDB's default memory_limit).
type BudgetItem struct {
	Kind       string
	Count      int
	LimitBytes int64
}

// MemoryBudget is what a process may use if every DuckDB handle fills its
// cap at once, plus an allowance for Go. Caps are ceilings, not
// reservations, so a budget over the container limit is a risk, not an
// outcome: the handles that can fill together are what OOM-kill a replica.
type MemoryBudget struct {
	Items          []BudgetItem
	DuckDBBytes    int64
	GoBytes        int64
	TotalBytes     int64
	ContainerBytes int64
	// Uncapped counts handles left at DuckDB's default, each counted as 80%
	// of the container (nothing without a container limit).
	Uncapped int
}

const (
	// goMemoryShare: Go's heap, goroutines, gRPC and caches on top of
	// DuckDB; production workers ran about 0.9 GB of Go beside 2.5-3 GB of
	// DuckDB.
	goMemoryShare = 0.25
	// duckDBDefaultMemoryShare: DuckDB's memory_limit when none is set.
	duckDBDefaultMemoryShare = 0.8
)

// NewMemoryBudget sums items against containerBytes (0: no limit known).
func NewMemoryBudget(items []BudgetItem, containerBytes int64) MemoryBudget {
	budget := MemoryBudget{Items: items, ContainerBytes: containerBytes}
	for _, item := range items {
		limit := item.LimitBytes
		if limit <= 0 {
			budget.Uncapped += item.Count
			limit = int64(float64(containerBytes) * duckDBDefaultMemoryShare)
		}
		budget.DuckDBBytes += int64(item.Count) * limit
	}
	budget.GoBytes = int64(float64(budget.DuckDBBytes) * goMemoryShare)
	budget.TotalBytes = budget.DuckDBBytes + budget.GoBytes
	return budget
}

// OnDemandBudget is the handles a replica opens per task rather than at
// startup, so the registered handles don't include them yet.
func OnDemandBudget() []BudgetItem {
	metadata := MemoryLimitBytes(metadataMemoryLimit)
	items := []BudgetItem{
		{Kind: "integrity_audit", Count: defaultAuditConcurrency, LimitBytes: metadata},
		{Kind: "schema_anchor", Count: 1, LimitBytes: metadata},
	}
	if !footerOpened.Load() {
		items = append(items, BudgetItem{Kind: "parquet_footer", Count: 1, LimitBytes: metadata})
	}
	return items
}

// Exceeds reports whether the budget is over a known container limit.
func (b MemoryBudget) Exceeds() bool {
	return b.ContainerBytes > 0 && b.TotalBytes > b.ContainerBytes
}

// Summary lists the items as "compactor 4 x 1.0 GB, writer_logs 3 x 256 MB".
func (b MemoryBudget) Summary() string {
	parts := make([]string, 0, len(b.Items))
	for _, item := range b.Items {
		limit := "default"
		if item.LimitBytes > 0 {
			limit = FormatBytes(item.LimitBytes)
		}
		parts = append(parts, fmt.Sprintf("%s %d x %s", item.Kind, item.Count, limit))
	}
	return strings.Join(parts, ", ")
}

// FormatBytes writes n in DuckDB's decimal units ("1.6 GB", "256 MB").
func FormatBytes(n int64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%d MB", n/1e6)
	default:
		return fmt.Sprintf("%d B", n)
	}
}
