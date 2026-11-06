package system_metrics

import (
	"fmt"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
)

// sampleWindow is how long cpu.Percent blocks: short enough to feel instant, long
// enough not to be a noisy single tick.
const sampleWindow = 200 * time.Millisecond

// HostMetrics is resource usage of the machine this process runs on. In a container
// gopsutil reads /proc, so this usually reflects the whole node.
type HostMetrics struct {
	CPUPercent       float64
	MemoryUsedBytes  int64
	MemoryTotalBytes int64
}

// GetHostMetrics samples current CPU/memory usage. CPU is measured over a short window,
// not "since last call", so each call is self-contained regardless of other callers.
func GetHostMetrics() (*HostMetrics, error) {
	cpuPercents, err := cpu.Percent(sampleWindow, false)
	if err != nil {
		return nil, fmt.Errorf("failed to sample CPU usage: %w", err)
	}
	var cpuPercent float64
	if len(cpuPercents) > 0 {
		cpuPercent = cpuPercents[0]
	}

	virtualMemory, err := mem.VirtualMemory()
	if err != nil {
		return nil, fmt.Errorf("failed to sample memory usage: %w", err)
	}

	return &HostMetrics{
		CPUPercent:       cpuPercent,
		MemoryUsedBytes:  int64(virtualMemory.Used),
		MemoryTotalBytes: int64(virtualMemory.Total),
	}, nil
}
