package telemetry

import (
	"context"
	"database/sql"
	"maps"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// duckDBScrapeTimeout bounds one handle's memory query during a scrape.
const duckDBScrapeTimeout = 2 * time.Second

// duckDBCollector reports DuckDB's own memory accounting for every
// long-lived handle. Each handle defaults to a memory_limit of 80% of RAM
// and the process opens several (a writer, querier and compactor per
// stream), so their sum is what shows how close the process is to OOM.
type duckDBCollector struct {
	mu      sync.Mutex
	handles map[*sql.DB]string

	memory    *prometheus.Desc
	temporary *prometheus.Desc
}

var duckDB = &duckDBCollector{
	handles: map[*sql.DB]string{},
	memory: prometheus.NewDesc(
		namespace+"_duckdb_memory_bytes",
		"Memory DuckDB reports in use (duckdb_memory()), summed over the handles of one kind.",
		[]string{"handle"}, nil,
	),
	temporary: prometheus.NewDesc(
		namespace+"_duckdb_temporary_storage_bytes",
		"Bytes DuckDB has spilled to temporary storage, summed over the handles of one kind.",
		[]string{"handle"}, nil,
	),
}

// RegisterDuckDB adds a long-lived DuckDB handle to the memory metrics under
// handle, a bounded name such as "writer_logs" or "compactor". Call
// UnregisterDuckDB before closing it.
func RegisterDuckDB(handle string, db *sql.DB) {
	duckDB.mu.Lock()
	defer duckDB.mu.Unlock()
	duckDB.handles[db] = handle
}

// UnregisterDuckDB removes a handle registered with RegisterDuckDB.
func UnregisterDuckDB(db *sql.DB) {
	duckDB.mu.Lock()
	defer duckDB.mu.Unlock()
	delete(duckDB.handles, db)
}

func (c *duckDBCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.memory
	ch <- c.temporary
}

func (c *duckDBCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	handles := maps.Clone(c.handles)
	c.mu.Unlock()

	memory := map[string]float64{}
	temporary := map[string]float64{}
	for db, name := range handles {
		used, spilled, err := duckDBMemory(db)
		if err != nil {
			continue // closed or busy; reported on the next scrape
		}
		memory[name] += used
		temporary[name] += spilled
	}

	for name, value := range memory {
		ch <- prometheus.MustNewConstMetric(c.memory, prometheus.GaugeValue, value, name)
		ch <- prometheus.MustNewConstMetric(c.temporary, prometheus.GaugeValue, temporary[name], name)
	}
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
