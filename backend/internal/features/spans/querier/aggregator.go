package spans_querier

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	spans_core "obzly/internal/features/spans/core"
	"obzly/internal/query"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

const defaultAggregationLimit = 1000

// maxAggregationLimit bounds request.Limit.
const maxAggregationLimit = 10_000

type spanAggregationPlan struct {
	// partialSelect / partialGroup: each fragment's columns over span stats
	// (selectArgs bind in partialSelect); finalSQL merges them, ordered and
	// limited (one argument: the limit).
	partialSelect string
	selectArgs    []any
	partialGroup  string
	finalSQL      string
	// grain: the time buckets' width, 0 without.
	grain time.Duration
	scan  func(rowScanner) (spans_core.AggregationBucketDTO, error)
}

type rowScanner interface {
	Scan(dest ...any) error
}

// ExecuteAggregation answers span-backed dashboard panels (and alert
// rules) from span stats. BucketMs and GroupField are independently
// optional (time series, stacked time series or top-N); Metric picks what
// is measured per group. Percentiles come from the duration histogram.
func (q *SpanQuerier) ExecuteAggregation(
	projectID uuid.UUID,
	request *spans_core.AggregationRequestDTO,
) (*spans_core.AggregationResponseDTO, error) {
	// GroupField is interpolated into the SQL as a column name (identifiers
	// can't be bound), so it must be one of the closed enum's values.
	if request.GroupField != "" && !request.GroupField.IsValid() {
		return nil, fmt.Errorf("invalid group field %q", request.GroupField)
	}

	from, to := resolveTimeRange(request.TimeRange)
	var hints segments.PruningHints
	if request.ServiceName != "" {
		hints.Services = []string{request.ServiceName}
	}

	var where []string
	var whereArgs []any
	if request.ServiceName != "" {
		where = append(where, "service_name = ?")
		whereArgs = append(whereArgs, request.ServiceName)
	}
	if request.OperationName != "" {
		where = append(where, "name = ?")
		whereArgs = append(whereArgs, request.OperationName)
	}
	if request.RootSpansOnly {
		where = append(where, "root")
	}

	limit := request.Limit
	if limit <= 0 {
		limit = defaultAggregationLimit
	}
	if limit > maxAggregationLimit {
		limit = maxAggregationLimit
	}

	plan := buildSpanAggregationPlan(request)
	buckets := make([]spans_core.AggregationBucketDTO, 0, defaultAggregationLimit)
	_, err := q.runStats(context.Background(), projectID, statsQuery{
		schema:     spanStats,
		ranges:     []interval{{start: from, end: to, includeEnd: true}},
		hints:      hints,
		grain:      plan.grain,
		selectSQL:  plan.partialSelect,
		selectArgs: plan.selectArgs,
		whereSQL:   strings.Join(where, " AND "),
		whereArgs:  whereArgs,
		groupSQL:   plan.partialGroup,
		finalSQL:   plan.finalSQL,
		finalArgs:  []any{limit},
		scanBudget: request.ScanBudget,
	}, func(rows *sql.Rows) error {
		for rows.Next() {
			bucket, err := plan.scan(rows)
			if err != nil {
				return fmt.Errorf("failed to scan span aggregation row: %w", err)
			}
			buckets = append(buckets, bucket)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to execute span aggregation query: %w", err)
	}

	return &spans_core.AggregationResponseDTO{Buckets: buckets}, nil
}

func buildSpanAggregationPlan(request *spans_core.AggregationRequestDTO) spanAggregationPlan {
	bucketed := request.BucketMs > 0
	grouped := request.GroupField != ""

	var keyColumns, keys, orderParts []string
	var selectArgs []any
	var grain time.Duration
	if bucketed {
		// Floored at 1 s: a 1 ms bucket over a day is 86M buckets.
		bucketSeconds := max(float64(request.BucketMs)/1000.0, 1)
		keyColumns = append(keyColumns, "to_timestamp(floor(epoch(timestamp) / ?) * ?) AS bucket_ts")
		selectArgs = append(selectArgs, bucketSeconds, bucketSeconds)
		keys = append(keys, "bucket_ts")
		orderParts = append(orderParts, "bucket_ts ASC")
		grain = time.Duration(bucketSeconds * float64(time.Second))
	}
	if grouped {
		// COALESCE to '': group_key must never scan as NULL.
		keyColumns = append(keyColumns, "COALESCE("+string(request.GroupField)+", '') AS group_key")
		keys = append(keys, "group_key")
		if !bucketed {
			orderParts = append(orderParts, "metric_value DESC", "group_key ASC")
		} else {
			orderParts = append(orderParts, "group_key ASC")
		}
	}
	if !bucketed && !grouped {
		orderParts = append(orderParts, "metric_value DESC")
	}
	order := " ORDER BY " + strings.Join(orderParts, ", ") + " LIMIT ?"
	selected := ""
	if len(keys) > 0 {
		selected = strings.Join(keys, ", ") + ", "
	}

	partials, groupKeys := metricPartial(request.Metric)
	groupKeys = append(append([]string{}, keys...), groupKeys...)
	partialGroup := ""
	if len(groupKeys) > 0 {
		partialGroup = " GROUP BY " + strings.Join(groupKeys, ", ")
	}
	// No spans leave an average or a percentile NULL: no row, as before.
	var merged string
	if quantile, ok := metricQuantile(request.Metric); ok {
		histogram := histogramSQL("r", "cnt", keys, nil, []float64{quantile})
		merged = "WITH r AS (SELECT * FROM " + query.ResultsPlaceholder + ") SELECT " + selected +
			fmt.Sprintf("p%d", int(quantile*100+0.5)) + " AS metric_value FROM (" + histogram + ")"
	} else {
		merged = "SELECT " + selected + metricMerge(request.Metric) + " FROM " + query.ResultsPlaceholder
		if len(keys) > 0 {
			merged += " GROUP BY " + strings.Join(keys, ", ")
		}
	}
	finalSQL := "SELECT * FROM (" + merged + ") WHERE metric_value IS NOT NULL" + order

	return spanAggregationPlan{
		partialSelect: strings.Join(append(keyColumns, partials), ", "),
		selectArgs:    selectArgs,
		partialGroup:  partialGroup,
		finalSQL:      finalSQL,
		grain:         grain,
		scan:          scannerFor(bucketed, grouped),
	}
}

// metricPartial is a metric's per-fragment columns over span stats and the
// extra columns they group by (a percentile's histogram bucket).
func metricPartial(metric spans_core.AggregationMetric) (string, []string) {
	switch metric {
	case spans_core.AggregationMetricErrorCount:
		return "coalesce(sum(cnt) FILTER (WHERE status_code = 'ERROR'), 0)::BIGINT AS metric_value", nil
	case spans_core.AggregationMetricAvgDuration:
		return "sum(duration_sum) AS duration_sum, sum(cnt)::BIGINT AS duration_count", nil
	case spans_core.AggregationMetricP50Duration, spans_core.AggregationMetricP95Duration,
		spans_core.AggregationMetricP99Duration:
		return "duration_bucket, sum(cnt)::BIGINT AS cnt", []string{"duration_bucket"}
	default:
		return "sum(cnt)::BIGINT AS metric_value", nil
	}
}

// metricMerge merges a sum-based metric's partials into metric_value.
func metricMerge(metric spans_core.AggregationMetric) string {
	if metric == spans_core.AggregationMetricAvgDuration {
		return "sum(duration_sum) / sum(duration_count) AS metric_value"
	}
	return "coalesce(sum(metric_value), 0)::DOUBLE AS metric_value"
}

func metricQuantile(metric spans_core.AggregationMetric) (float64, bool) {
	switch metric {
	case spans_core.AggregationMetricP50Duration:
		return 0.5, true
	case spans_core.AggregationMetricP95Duration:
		return 0.95, true
	case spans_core.AggregationMetricP99Duration:
		return 0.99, true
	}
	return 0, false
}

func scannerFor(bucketed, grouped bool) func(rowScanner) (spans_core.AggregationBucketDTO, error) {
	switch {
	case bucketed && grouped:
		return func(row rowScanner) (spans_core.AggregationBucketDTO, error) {
			var ts time.Time
			var groupKey string
			var value float64
			if err := row.Scan(&ts, &groupKey, &value); err != nil {
				return spans_core.AggregationBucketDTO{}, err
			}
			return spans_core.AggregationBucketDTO{Timestamp: &ts, GroupKey: groupKey, Value: value}, nil
		}
	case bucketed:
		return func(row rowScanner) (spans_core.AggregationBucketDTO, error) {
			var ts time.Time
			var value float64
			if err := row.Scan(&ts, &value); err != nil {
				return spans_core.AggregationBucketDTO{}, err
			}
			return spans_core.AggregationBucketDTO{Timestamp: &ts, Value: value}, nil
		}
	case grouped:
		return func(row rowScanner) (spans_core.AggregationBucketDTO, error) {
			var groupKey string
			var value float64
			if err := row.Scan(&groupKey, &value); err != nil {
				return spans_core.AggregationBucketDTO{}, err
			}
			return spans_core.AggregationBucketDTO{GroupKey: groupKey, Value: value}, nil
		}
	default:
		return func(row rowScanner) (spans_core.AggregationBucketDTO, error) {
			var value float64
			if err := row.Scan(&value); err != nil {
				return spans_core.AggregationBucketDTO{}, err
			}
			return spans_core.AggregationBucketDTO{Value: value}, nil
		}
	}
}
