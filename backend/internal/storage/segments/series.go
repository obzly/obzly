package segments

import (
	"database/sql/driver"

	"github.com/cespare/xxhash/v2"
	"github.com/marcboeker/go-duckdb/v2"
)

// SeriesID identifies a metric series: xxhash64 of the normalized name and
// the canonical (key-sorted) labels JSON. obzly_series_id computes the same in
// SQL for rows written before the column existed.
func SeriesID(metricName, labelsJSON string) uint64 {
	h := xxhash.New()
	_, _ = h.WriteString(metricName)
	_, _ = h.Write([]byte{0})
	_, _ = h.WriteString(labelsJSON)
	return h.Sum64()
}

// seriesIDFunction is obzly_series_id(metric_name, labels) -> UBIGINT.
type seriesIDFunction struct{}

func (seriesIDFunction) Config() duckdb.ScalarFuncConfig {
	varchar, _ := duckdb.NewTypeInfo(duckdb.TYPE_VARCHAR)
	ubigint, _ := duckdb.NewTypeInfo(duckdb.TYPE_UBIGINT)
	return duckdb.ScalarFuncConfig{
		InputTypeInfos: []duckdb.TypeInfo{varchar, varchar},
		ResultTypeInfo: ubigint,
	}
}

func (seriesIDFunction) Executor() duckdb.ScalarFuncExecutor {
	return duckdb.ScalarFuncExecutor{RowExecutor: func(values []driver.Value) (any, error) {
		name, _ := values[0].(string)
		labels, _ := values[1].(string)
		return SeriesID(name, labels), nil
	}}
}
