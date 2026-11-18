package logs_querier

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"time"

	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/storage/segments"
	"obzly/internal/util/attrnames"

	"github.com/google/uuid"
)

// The aggregation options beyond a line count: a measure over a field, a top
// N of groups for a time series, and log-scale bands of a numeric field.

// quantiles maps a percentile measure to its fraction.
var quantiles = map[logs_core.AggregationMeasure]string{
	logs_core.AggregationMeasureP50: "0.5",
	logs_core.AggregationMeasureP95: "0.95",
	logs_core.AggregationMeasureP99: "0.99",
}

// numericAggregates maps the measures that read a number to their DuckDB aggregate.
var numericAggregates = map[logs_core.AggregationMeasure]string{
	logs_core.AggregationMeasureSum: "sum",
	logs_core.AggregationMeasureAvg: "avg",
	logs_core.AggregationMeasureMin: "min",
	logs_core.AggregationMeasureMax: "max",
}

// validateValueFields refuses a number read from a system column: none holds
// a number, and casting a timestamp fails the whole query.
func validateValueFields(request *logs_core.AggregationRequestDTO) error {
	var numeric []string
	if readsNumber(request.Measure) {
		numeric = append(numeric, request.MeasureField)
	}
	if request.GroupBands != "" {
		numeric = append(numeric, request.GroupField)
	}
	for _, field := range numeric {
		if _, isSystem := systemFieldColumns[attrnames.Normalize(field)]; isSystem {
			return fmt.Errorf("%w: %s is not a number field", logs_core.ErrInvalidAggregation, field)
		}
	}
	return nil
}

// restrictToTopGroups turns TopGroups into a filter on the N groups with the
// largest value over the whole range (an unbucketed top N first); nil when
// no group matched.
func (q *LogQuerier) restrictToTopGroups(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.AggregationRequestDTO,
) (*logs_core.AggregationRequestDTO, error) {
	restricted := *request
	restricted.TopGroups = 0
	if request.BucketMs <= 0 {
		if restricted.Limit <= 0 || restricted.Limit > request.TopGroups {
			restricted.Limit = request.TopGroups
		}
		return &restricted, nil
	}

	top := *request
	top.BucketMs, top.TopGroups, top.Limit = 0, 0, request.TopGroups
	response, err := q.executeAggregation(ctx, projectID, &top, nil)
	if err != nil {
		return nil, err
	}
	if len(response.Buckets) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(response.Buckets))
	absent := false
	for _, bucket := range response.Buckets {
		if bucket.GroupKey == "" {
			absent = true
			continue
		}
		keys = append(keys, bucket.GroupKey)
	}
	var groups []logs_core.QueryNode
	if len(keys) > 0 {
		groups = append(groups, conditionNode(request.GroupField, logs_core.ConditionOperatorIn, keys))
	}
	if absent {
		groups = append(groups, conditionNode(request.GroupField, logs_core.ConditionOperatorNotExists, nil))
	}
	filter := groups[0]
	if len(groups) > 1 {
		filter = logs_core.QueryNode{
			Type:  logs_core.QueryNodeTypeLogical,
			Logic: &logs_core.LogicalNode{Operator: logs_core.LogicalOperatorOr, Children: groups},
		}
	}
	if request.Query == nil {
		restricted.Query = &filter
	} else {
		restricted.Query = &logs_core.QueryNode{
			Type: logs_core.QueryNodeTypeLogical,
			Logic: &logs_core.LogicalNode{
				Operator: logs_core.LogicalOperatorAnd,
				Children: []logs_core.QueryNode{*request.Query, filter},
			},
		}
	}
	return &restricted, nil
}

// hourlyTopNRequest is an unbucketed grouped count as hourly buckets, which
// per-minute counts and the partition cache can answer; nil for any other.
func hourlyTopNRequest(request *logs_core.AggregationRequestDTO) *logs_core.AggregationRequestDTO {
	if request.BucketMs != 0 || request.GroupField == "" || !request.UsesCountsOnly() {
		return nil
	}
	hourly := *request
	hourly.BucketMs = time.Hour.Milliseconds()
	hourly.Limit = 0
	return &hourly
}

// aggregateTopNByPartition answers an unbucketed top N from hourly counts
// read by partition (logs_1m where it can), summed per group.
func (q *LogQuerier) aggregateTopNByPartition(
	ctx context.Context,
	projectID uuid.UUID,
	hourly *logs_core.AggregationRequestDTO,
	from, to time.Time,
	tail []segments.ResolvedFile,
	ordered []*segments.SegmentRecord,
	whereClause string,
	whereArgs []any,
	plan aggregationPlan,
	limit int,
) (*logs_core.AggregationResponseDTO, error) {
	response, err := q.aggregateByPartition(ctx, projectID, hourly, from, to, tail, ordered, whereClause, whereArgs,
		plan, math.MaxInt32, nil)
	if err != nil {
		return nil, err
	}
	totals := map[string]int64{}
	for _, bucket := range response.Buckets {
		totals[bucket.GroupKey] += bucket.Count
	}
	buckets := make([]logs_core.AggregationBucketDTO, 0, len(totals))
	for key, count := range totals {
		buckets = append(buckets, logs_core.AggregationBucketDTO{GroupKey: key, Count: count})
	}
	// The unbucketed plan's order: highest count first, ties by key.
	sort.Slice(buckets, func(i, j int) bool {
		if buckets[i].Count != buckets[j].Count {
			return buckets[i].Count > buckets[j].Count
		}
		return buckets[i].GroupKey < buckets[j].GroupKey
	})
	return &logs_core.AggregationResponseDTO{Buckets: buckets[:min(len(buckets), limit)]}, nil
}

// numericExpression is a custom field's value as a DOUBLE (NULL when not a
// number): a promoted number field's column, else the text cast.
func (b *whereBuilder) numericExpression(field string) (string, []any) {
	field = attrnames.Normalize(field)
	if number, args, ok := b.fieldNumber(field); ok {
		return number, args
	}
	text, args := b.fieldText(field)
	return "TRY_CAST(" + text + " AS DOUBLE)", args
}

// measureExpression is the aggregate a measure computes, as a DOUBLE.
func (b *whereBuilder) measureExpression(measure logs_core.AggregationMeasure, field string) (string, []any) {
	if measure == logs_core.AggregationMeasureDistinct {
		text, args := b.groupFieldExpression(field)
		return "CAST(count(DISTINCT " + text + ") AS DOUBLE)", args
	}
	value, args := b.numericExpression(field)
	if fraction, ok := quantiles[measure]; ok {
		return "CAST(quantile_cont(" + value + ", " + fraction + ") AS DOUBLE)", args
	}
	return "CAST(" + numericAggregates[measure] + "(" + value + ") AS DOUBLE)", args
}

// logBandExpression is a field's 1-2-5 log band as text: the band's lower
// bound (500 for 500 to 999), "0" for zero or less. The epsilon keeps an exact
// power of ten (log10 can land just under it) in its own band.
func (b *whereBuilder) logBandExpression(field string) (string, []any) {
	value, valueArgs := b.numericExpression(field)
	var args []any
	v := func() string {
		args = append(args, valueArgs...)
		return value
	}
	decade := func() string { return "pow(10, floor(log10(" + v() + ") + 1e-9))" }
	expr := "CASE WHEN " + v() + " <= 0 THEN '0' ELSE CAST(" + decade() + " * CASE WHEN " + v() + " >= 5 * " +
		decade() + " THEN 5 WHEN " + v() + " >= 2 * " + decade() + " THEN 2 ELSE 1 END AS VARCHAR) END"
	return expr, args
}

// requiredValue is the condition that a line has the value a measure or band
// reads ("" when the request reads none).
func (b *whereBuilder) requiredValue(request *logs_core.AggregationRequestDTO) (string, []any) {
	var parts []string
	var args []any
	if request.GroupBands != "" {
		value, valueArgs := b.numericExpression(request.GroupField)
		parts = append(parts, value+" IS NOT NULL")
		args = append(args, valueArgs...)
	}
	switch {
	case request.Measure == logs_core.AggregationMeasureDistinct:
		text, textArgs := b.groupFieldExpression(request.MeasureField)
		parts = append(parts, text+" IS NOT NULL")
		args = append(args, textArgs...)
	case readsNumber(request.Measure):
		value, valueArgs := b.numericExpression(request.MeasureField)
		parts = append(parts, value+" IS NOT NULL")
		args = append(args, valueArgs...)
	}
	if len(parts) == 0 {
		return "", nil
	}
	sqlExpr := parts[0]
	if len(parts) > 1 {
		sqlExpr = "(" + parts[0] + " AND " + parts[1] + ")"
	}
	return sqlExpr, args
}

func readsNumber(measure logs_core.AggregationMeasure) bool {
	_, numeric := numericAggregates[measure]
	_, quantile := quantiles[measure]
	return numeric || quantile
}

func conditionNode(field string, operator logs_core.ConditionOperator, value any) logs_core.QueryNode {
	return logs_core.QueryNode{
		Type:      logs_core.QueryNodeTypeCondition,
		Condition: &logs_core.ConditionNode{Field: field, Operator: operator, Value: value},
	}
}

// measureScannerFor reads a measure's value (cnt, a DOUBLE) into Value and
// its rounding into Count.
func measureScannerFor(bucketed, grouped bool) func(rowScanner) (logs_core.AggregationBucketDTO, error) {
	return func(row rowScanner) (logs_core.AggregationBucketDTO, error) {
		var ts time.Time
		var groupKey string
		var value sql.NullFloat64
		dest := make([]any, 0, 3)
		if bucketed {
			dest = append(dest, &ts)
		}
		if grouped {
			dest = append(dest, &groupKey)
		}
		dest = append(dest, &value)
		if err := row.Scan(dest...); err != nil {
			return logs_core.AggregationBucketDTO{}, err
		}
		bucket := logs_core.AggregationBucketDTO{GroupKey: groupKey}
		if bucketed {
			bucket.Timestamp = &ts
		}
		if value.Valid {
			v := value.Float64
			bucket.Value = &v
			bucket.Count = int64(math.Round(v))
		}
		return bucket, nil
	}
}
