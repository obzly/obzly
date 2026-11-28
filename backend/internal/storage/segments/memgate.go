package segments

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"obzly/internal/telemetry"
	"obzly/internal/util/querylimit"
)

// SandboxHandleKind names SQL mode's per-query DuckDB handles in the memory
// metrics. The gate counts them by the limits it gave them, not by their use.
const SandboxHandleKind = "logs_sql_sandbox"

// ErrMemoryBusy: too little memory is free for another SQL-mode query right
// now. It matches querylimit.ErrTooManyQueries, so it answers 429.
var ErrMemoryBusy error = memoryBusyError{}

// MemoryGate admits SQL mode's per-query DuckDB handles. Each is a new handle
// with its own cap, so a few at once, beside the long-lived search handles and
// Go, OOM-killed an API replica. The long-lived handles count at what they
// use (refreshed with SetInUse), so ordinary searches never wait; a new
// sandbox gets what is free up to its cap and spills past it, and waits only
// when less than floor is free.
type MemoryGate struct {
	capacity int64
	floor    int64
	wait     time.Duration
	inUse    atomic.Int64

	mu       sync.Mutex
	admitted int64
	changed  chan struct{}
}

type memoryBusyError struct{}

var queryGate atomic.Pointer[MemoryGate]

// NewMemoryGate admits sandboxes within capacity bytes, none with less than
// floor, waiting at most wait for room.
func NewMemoryGate(capacity, floor int64, wait time.Duration) *MemoryGate {
	return &MemoryGate{capacity: capacity, floor: floor, wait: wait, changed: make(chan struct{})}
}

// SetQueryMemoryGate installs the process's gate (nil: none, every sandbox
// gets its full cap).
func SetQueryMemoryGate(gate *MemoryGate) {
	queryGate.Store(gate)
}

// QueryMemoryGate is the process's gate; nil without one, which admits all.
func QueryMemoryGate() *MemoryGate {
	return queryGate.Load()
}

// IsSandboxHandle reports whether a registered handle kind is a sandbox, which
// the gate already counts by its admitted limit.
func IsSandboxHandle(kind string) bool {
	return kind == SandboxHandleKind
}

// SetInUse records what the long-lived DuckDB handles use now.
func (g *MemoryGate) SetInUse(bytes int64) {
	g.inUse.Store(bytes)
	g.broadcast()
}

// InUse is the long-lived handles' last recorded use.
func (g *MemoryGate) InUse() int64 {
	return g.inUse.Load()
}

// Admitted is the memory limits of the sandboxes running now.
func (g *MemoryGate) Admitted() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.admitted
}

// Admit returns the memory limit a new sandbox may have: want, or what is free
// when less. With less than floor free it waits up to the gate's wait for
// room, then returns ErrMemoryBusy; a sandbox is always admitted when none
// runs. Call release once the sandbox is closed.
func (g *MemoryGate) Admit(ctx context.Context, want int64) (int64, func(), error) {
	if g == nil || want <= 0 {
		return want, func() {}, nil
	}
	smallest := min(want, g.floor)
	deadline := time.NewTimer(g.wait)
	defer deadline.Stop()
	poll := time.NewTicker(250 * time.Millisecond)
	defer poll.Stop()
	waited := false
	for {
		g.mu.Lock()
		limit := min(want, max(g.capacity-g.inUse.Load()-g.admitted, 0))
		if g.admitted == 0 {
			limit = max(limit, smallest)
		}
		if limit >= smallest {
			g.admitted += limit
			telemetry.QueryMemorySandboxBytes.Set(float64(g.admitted))
			g.mu.Unlock()
			telemetry.QueryMemoryAdmissions.WithLabelValues(admissionOutcome(waited, limit == want)).Inc()
			return limit, g.releaser(limit), nil
		}
		changed := g.changed
		g.mu.Unlock()
		waited = true
		select {
		case <-changed:
		case <-poll.C:
		case <-deadline.C:
			telemetry.QueryMemoryAdmissions.WithLabelValues("refused").Inc()
			return 0, nil, ErrMemoryBusy
		case <-ctx.Done():
			return 0, nil, ctx.Err()
		}
	}
}

func (memoryBusyError) Error() string {
	return "too little memory is free for another SQL query right now: try again in a moment, or narrow the time range"
}

func (memoryBusyError) Is(target error) bool {
	return target == querylimit.ErrTooManyQueries
}

func (g *MemoryGate) releaser(limit int64) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			g.admitted -= limit
			telemetry.QueryMemorySandboxBytes.Set(float64(g.admitted))
			g.mu.Unlock()
			g.broadcast()
		})
	}
}

func (g *MemoryGate) broadcast() {
	g.mu.Lock()
	defer g.mu.Unlock()
	close(g.changed)
	g.changed = make(chan struct{})
}

func admissionOutcome(waited, full bool) string {
	switch {
	case waited:
		return "waited"
	case full:
		return "full"
	default:
		return "reduced"
	}
}
