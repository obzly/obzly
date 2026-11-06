package spans_querier

import (
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

// maxAggregationLimit bounds request.Limit: it sizes a slice preallocation.
const maxAggregationLimit = 10_000

// ExecuteAggregation answers span-backed dashboard panels. BucketMs and
// GroupField are independently optional (time series, stacked time series
// or top-N); Metric picks what is measured per group.
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
	tail, records, err := q.inputs(projectID, from, to, hints)
	if err != nil {
		return nil, err
	}

	var whereParts []string
	whereArgs := []any{projectID.String(), from, to}
	whereParts = append(whereParts, "project_id = ?", "start_time >= ?", "start_time <= ?")

	if request.ServiceName != "" {
		whereParts = append(whereParts, "service_name = ?")
		whereArgs = append(whereArgs, request.ServiceName)
	}
	if request.OperationName != "" {
		whereParts = append(whereParts, "name = ?")
		whereArgs = append(whereArgs, request.OperationName)
	}
	if request.RootSpansOnly {
		whereParts = append(whereParts, "parent_span_id = ''")
	}
	whereClause := strings.Join(whereParts, " AND ")

	plan := buildSpanAggregationPlan(request)

	limit := request.Limit
	if limit <= 0 {
		limit = defaultAggregationLimit
	}
	if limit > maxAggregationLimit {
		limit = maxAggregationLimit
	}

	fromWhere := " FROM " + query.SourcePlaceholder + " WHERE " + whereClause + plan.groupClause
	selectArgs := append(append([]any{}, plan.selectArgs...), whereArgs...)
	buckets := make([]spans_core.AggregationBucketDTO, 0, limit)
	_, err = q.run(query.Request{
		Segments:     records,
		LocalFiles:   tail,
		FragmentSQL:  "SELECT " + plan.partialClause + fromWhere,
		FragmentArgs: selectArgs,
		FinalSQL: "SELECT " + plan.mergeClause + " FROM " + query.ResultsPlaceholder +
			plan.groupClause + plan.orderClause + " LIMIT ?",
		FinalArgs:  []any{limit},
		SingleSQL:  "SELECT " + plan.selectClause + fromWhere + plan.orderClause + " LIMIT ?",
		SingleArgs: append(selectArgs, limit),
		// A quantile has no partial to merge: it reads every span at once.
		SingleFragment: plan.partialClause == "",
		ScanBudget:     request.ScanBudget,
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

type spanAggregationPlan struct {
	selectClause string
	selectArgs   []any
	// partialClause is the per-fragment form of selectClause (same args),
	// "" when the metric can't be merged; mergeClause re-aggregates it.
	partialClause string
	mergeClause   string
	groupClause   string
	orderClause   string
	scan          func(rowScanner) (spans_core.AggregationBucketDTO, error)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func buildSpanAggregationPlan(request *spans_core.AggregationRequestDTO) spanAggregationPlan {
	bucketed := request.BucketMs > 0
	grouped := request.GroupField != ""

	var selectParts []string
	var selectArgs []any
	var groupParts []string
	var orderParts []string

	if bucketed {
		// Floored at 1 s: a 1 ms bucket over a day is 86M buckets.
		bucketSeconds := max(float64(request.BucketMs)/1000.0, 1)
		selectParts = append(selectParts, "to_timestamp(floor(epoch(start_time) / ?) * ?) AS bucket_ts")
		selectArgs = append(selectArgs, bucketSeconds, bucketSeconds)
		groupParts = append(groupParts, "bucket_ts")
		orderParts = append(orderParts, "bucket_ts ASC")
	}

	if grouped {
		// COALESCE to '': group_key must never scan as NULL.
		selectParts = append(selectParts, "COALESCE("+string(request.GroupField)+", '') AS group_key")
		groupParts = append(groupParts, "group_key")
		if !bucketed {
			orderParts = append(orderParts, "metric_value DESC", "group_key ASC")
		} else {
			orderParts = append(orderParts, "group_key ASC")
		}
	}
	if !bucketed && !grouped {
		orderParts = append(orderParts, "metric_value DESC")
	}

	selectClause := metricExpression(request.Metric) + " AS metric_value"
	partial, merge := metricPartial(request.Metric)
	partialClause, mergeClause := "", ""
	if partial != "" {
		partialClause, mergeClause = partial, merge
	}
	if len(selectParts) > 0 {
		selectClause = strings.Join(selectParts, ", ") + ", " + selectClause
		if partial != "" {
			partialClause = strings.Join(selectParts, ", ") + ", " + partial
			mergeClause = strings.Join(groupParts, ", ") + ", " + merge
		}
	}

	groupClause := ""
	if len(groupParts) > 0 {
		groupClause = " GROUP BY " + strings.Join(groupParts, ", ")
	}

	orderClause := " ORDER BY " + strings.Join(orderParts, ", ")

	return spanAggregationPlan{
		selectClause:  selectClause,
		selectArgs:    selectArgs,
		partialClause: partialClause,
		mergeClause:   mergeClause,
		groupClause:   groupClause,
		orderClause:   orderClause,
		scan:          scannerFor(bucketed, grouped),
	}
}

// metricExpression maps an AggregationMetric onto its DuckDB aggregate.
// Duration uses epoch_ns: TIMESTAMP subtraction yields an INTERVAL.
func metricExpression(metric spans_core.AggregationMetric) string {
	const durationMsExpr = "(epoch_ns(end_time) - epoch_ns(start_time)) / 1000000.0"

	switch metric {
	case spans_core.AggregationMetricErrorCount:
		return "count(*) FILTER (WHERE status_code = 'ERROR')"
	case spans_core.AggregationMetricAvgDuration:
		return "avg(" + durationMsExpr + ")"
	case spans_core.AggregationMetricP50Duration:
		return "approx_quantile(" + durationMsExpr + ", 0.5)"
	case spans_core.AggregationMetricP95Duration:
		return "approx_quantile(" + durationMsExpr + ", 0.95)"
	case spans_core.AggregationMetricP99Duration:
		return "approx_quantile(" + durationMsExpr + ", 0.99)"
	case spans_core.AggregationMetricCount:
		return "count(*)"
	default:
		return "count(*)"
	}
}

// metricPartial is a metric as per-fragment columns and their merge into
// metric_value; "" for a quantile, which can't be merged from partials.
func metricPartial(metric spans_core.AggregationMetric) (string, string) {
	const durationMsExpr = "(epoch_ns(end_time) - epoch_ns(start_time)) / 1000000.0"

	switch metric {
	case spans_core.AggregationMetricErrorCount:
		return "count(*) FILTER (WHERE status_code = 'ERROR') AS metric_value", "sum(metric_value)::DOUBLE AS metric_value"
	case spans_core.AggregationMetricAvgDuration:
		return "sum(" + durationMsExpr + ") AS duration_sum, count(*) AS duration_count",
			"sum(duration_sum) / sum(duration_count) AS metric_value"
	case spans_core.AggregationMetricP50Duration, spans_core.AggregationMetricP95Duration,
		spans_core.AggregationMetricP99Duration:
		return "", ""
	default:
		return "count(*) AS metric_value", "sum(metric_value)::DOUBLE AS metric_value"
	}
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
