package telemetry

import (
	"cmp"
	"context"
	"database/sql"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// duckDBScrapeTimeout bounds one handle's memory query during a scrape.
const duckDBScrapeTimeout = 2 * time.Second

// DuckDBHandleKind is the registered handles of one kind with one memory
// limit, for the startup memory budget.
type DuckDBHandleKind struct {
	Handle string
	Count  int
	// LimitBytes is each handle's memory_limit; 0 for DuckDB's default.
	LimitBytes int64
}

// duckDBCollector reports DuckDB's own memory accounting for every
// long-lived handle, beside each handle's memory_limit. Their sum is what
// shows how close the process is to OOM.
type duckDBCollector struct {
	mu      sync.Mutex
	handles map[*sql.DB]*duckDBHandle

	memory    *prometheus.Desc
	temporary *prometheus.Desc
	limit     *prometheus.Desc
}

// duckDBHandle keeps a handle's last reading: a handle busy with a big query
// can miss the scrape timeout exactly when it uses the most, and dropping its
// series then hid the spikes.
type duckDBHandle struct {
	name  string
	limit int64

	mu      sync.Mutex
	read    bool
	used    float64
	spilled float64
}

var duckDB = &duckDBCollector{
	handles: map[*sql.DB]*duckDBHandle{},
	memory: prometheus.NewDesc(
		namespace+"_duckdb_memory_bytes",
		"Memory DuckDB reports in use (duckdb_memory()), summed over the handles of one kind. "+
			"A handle that misses a scrape reports its last reading.",
		[]string{"handle"}, nil,
	),
	temporary: prometheus.NewDesc(
		namespace+"_duckdb_temporary_storage_bytes",
		"Bytes DuckDB has spilled to temporary storage, summed over the handles of one kind.",
		[]string{"handle"}, nil,
	),
	limit: prometheus.NewDesc(
		namespace+"_duckdb_memory_limit_bytes",
		"memory_limit of the handles of one kind, summed; 0 for handles left at DuckDB's default (80% of RAM).",
		[]string{"handle"}, nil,
	),
}

// RegisterDuckDB adds a long-lived DuckDB handle to the memory metrics under
// handle, a bounded name such as "writer_logs" or "compactor", with its
// memory_limit in bytes (0: DuckDB's default). Call UnregisterDuckDB before
// closing it.
func RegisterDuckDB(handle string, db *sql.DB, limitBytes int64) {
	duckDB.mu.Lock()
	defer duckDB.mu.Unlock()
	duckDB.handles[db] = &duckDBHandle{name: handle, limit: limitBytes}
}

// UnregisterDuckDB removes a handle registered with RegisterDuckDB.
func UnregisterDuckDB(db *sql.DB) {
	duckDB.mu.Lock()
	defer duckDB.mu.Unlock()
	delete(duckDB.handles, db)
}

// DuckDBHandles is every registered handle grouped by kind and limit.
func DuckDBHandles() []DuckDBHandleKind {
	duckDB.mu.Lock()
	defer duckDB.mu.Unlock()
	counts := map[DuckDBHandleKind]int{}
	for _, handle := range duckDB.handles {
		counts[DuckDBHandleKind{Handle: handle.name, LimitBytes: handle.limit}]++
	}
	kinds := make([]DuckDBHandleKind, 0, len(counts))
	for kind, count := range counts {
		kind.Count = count
		kinds = append(kinds, kind)
	}
	slices.SortFunc(kinds, func(a, b DuckDBHandleKind) int {
		return cmp.Or(cmp.Compare(a.Handle, b.Handle), cmp.Compare(a.LimitBytes, b.LimitBytes))
	})
	return kinds
}

func (c *duckDBCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.memory
	ch <- c.temporary
	ch <- c.limit
}

func (c *duckDBCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	handles := maps.Clone(c.handles)
	c.mu.Unlock()

	memory := map[string]float64{}
	temporary := map[string]float64{}
	limit := map[string]float64{}
	for db, handle := range handles {
		limit[handle.name] += float64(handle.limit)
		used, spilled, ok := handle.reading(db)
		if !ok {
			continue // not read yet; reported once it answers
		}
		memory[handle.name] += used
		temporary[handle.name] += spilled
	}

	for name, value := range limit {
		ch <- prometheus.MustNewConstMetric(c.limit, prometheus.GaugeValue, value, name)
	}
	for name, value := range memory {
		ch <- prometheus.MustNewConstMetric(c.memory, prometheus.GaugeValue, value, name)
		ch <- prometheus.MustNewConstMetric(c.temporary, prometheus.GaugeValue, temporary[name], name)
	}
}

// reading queries the handle now, or falls back to its last reading when it
// is busy or closing; ok is false before its first reading.
func (h *duckDBHandle) reading(db *sql.DB) (used, spilled float64, ok bool) {
	used, spilled, err := duckDBMemory(db)
	h.mu.Lock()
	defer h.mu.Unlock()
	if err == nil {
		h.read, h.used, h.spilled = true, used, spilled
	}
	return h.used, h.spilled, h.read
}

func duckDBMemory(db *sql.DB) (used, spilled float64, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), duckDBScrapeTimeout)
	defer cancel()
	err = db.QueryRowContext(ctx,
		`SELECT coalesce(sum(memory_usage_bytes), 0), coalesce(sum(temporary_storage_bytes), 0) FROM duckdb_memory()`,
	).Scan(&used, &spilled)
	return used, spilled, err
}

func init() {
	Registry.MustRegister(duckDB)
}
