package metrics_promql

import (
	"fmt"
	"time"

	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/promql/parser"
)

// queryParser is shared by the engine and metrics_promapi's match[]
// selectors so both accept exactly the same syntax.
var queryParser = parser.NewParser(parser.Options{})

// GetParser returns the shared PromQL parser.
func GetParser() parser.Parser {
	return queryParser
}

// engine is shared process-wide, as in Prometheus: an Engine holds no
// per-query state beyond its in-flight tracker.
var engine = promql.NewEngine(promql.EngineOpts{
	Parser:     queryParser,
	MaxSamples: 50_000_000,
	Timeout:    2 * time.Minute,
	// Prometheus's defaults: 5m lookback, 1m subquery step.
	LookbackDelta:            5 * time.Minute,
	NoStepSubqueryIntervalFn: func(int64) int64 { return (1 * time.Minute).Milliseconds() },
	EnableAtModifier:         true,
	EnableNegativeOffset:     true,
})

// GetEngine returns the shared PromQL engine; callers query it through a
// per-project storage.Queryable (see NewQueryable).
func GetEngine() *promql.Engine {
	return engine
}

// MaxRangeQueryPoints caps (end-start)/step, as Prometheus's HTTP API does.
// MaxSamples doesn't cover it: a tiny step over sparse data loads few samples
// but still makes the engine evaluate every step (CPU/memory DoS).
const MaxRangeQueryPoints = 11_000

// ValidateRangeQueryResolution rejects a range query over
// MaxRangeQueryPoints before the engine starts evaluating it.
func ValidateRangeQueryResolution(start, end time.Time, step time.Duration) error {
	if step <= 0 {
		return fmt.Errorf("query step must be positive, got %s", step)
	}

	points := end.Sub(start)/step + 1
	if points > MaxRangeQueryPoints {
		return fmt.Errorf(
			"query resolution of %d points exceeds the maximum of %d points per series "+
				"(try a longer step or a narrower time range)",
			points, MaxRangeQueryPoints,
		)
	}
	return nil
}
