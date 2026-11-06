package logs_querier

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/query"
	"obzly/internal/util/attrnames"

	"github.com/google/uuid"
)

// defaultAggregationLimit caps rows when the caller sets no limit: the N of a
// top-N, or a safety cap for a time series.
const defaultAggregationLimit = 1000

// maxAggregationLimit bounds request.Limit, which sizes an allocation and an
// SQL LIMIT (same DoS risk as querier.go's maxLimit).
const maxAggregationLimit = 10_000

// minBucketMs floors a time bucket: a 1 ms bucket over a day is 86M
// buckets to group and return.
const minBucketMs = 1000

// ExecuteAggregation answers dashboard panel data: counts grouped by an
// optional time bucket and/or field, covering every panel shape in one query.
func (q *LogQuerier) ExecuteAggregation(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.AggregationRequestDTO,
) (*logs_core.AggregationResponseDTO, error) {
	response, err := q.executeAggregation(ctx, projectID, request, nil)
	if segmentVanished(err) {
		return q.executeAggregation(ctx, projectID, request, nil)
	}
	return response, err
}

// StreamAggregation is ExecuteAggregation emitting counts as they are read: a
// bucketed aggregation by partition group, newest first (cached partitions at
// once), any other in one call at the end. The response is the whole result.
func (q *LogQuerier) StreamAggregation(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.AggregationRequestDTO,
	emit logs_core.AggregationEmitter,
) (*logs_core.AggregationResponseDTO, error) {
	emitted := false
	tracked := func(buckets []logs_core.AggregationBucketDTO) error {
		emitted = true
		return emit(buckets)
	}
	response, err := q.executeAggregation(ctx, projectID, request, tracked)
	// A retry would emit the partitions already sent a second time.
	if segmentVanished(err) && !emitted {
		return q.executeAggregation(ctx, projectID, request, tracked)
	}
	return response, err
}

func (q *LogQuerier) executeAggregation(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.AggregationRequestDTO,
	emit logs_core.AggregationEmitter,
) (*logs_core.AggregationResponseDTO, error) {
	empty := &logs_core.AggregationResponseDTO{Buckets: []logs_core.AggregationBucketDTO{}}
	if request.BucketMs > 0 && request.BucketMs < minBucketMs {
		floored := *request
		floored.BucketMs = minBucketMs
		request = &floored
	}
	from, to := resolveTimeRange(request.TimeRange)
	records, err := q.segmentRepo.PlanSegments(projectID, q.stream, from, to, hintsFor(request.Query))
	if err != nil {
		return nil, fmt.Errorf("failed to list segments for project: %w", err)
	}
	tail, ordered := q.searchInputs(ctx, projectID, records, from, to, false)
	if len(tail) == 0 && len(ordered) == 0 {
		return empty, nil
	}

	builder := newWhereBuilder(projectID)
	querySQL, queryArgs := builder.Build(request.Query)
	whereClause := "project_id = ? AND timestamp >= ? AND timestamp <= ?"
	whereArgs := append([]any{projectID.String(), from, to}, queryArgs...)
	if querySQL != "" {
		whereClause += " AND " + querySQL
	}

	plan := buildAggregationPlan(request, builder)

	limit := request.Limit
	if limit <= 0 {
		limit = defaultAggregationLimit
	}
	if limit > maxAggregationLimit {
		limit = maxAggregationLimit
	}
	if request.BucketMs > 0 && (q.partitionCache != nil || q.rollupsApply(request) || emit != nil) &&
		partitionedAggregationFits(request, from, to, tail, ordered) {
		return q.aggregateByPartition(ctx, projectID, request, from, to, tail, ordered,
			whereClause, whereArgs, plan, limit, emit)
	}

	// Fragments count per bucket/group without a limit (a top-N must see
	// every group's full count); the merge sums them, then orders and limits.
	fromWhere := " FROM " + query.SourcePlaceholder + " WHERE " + whereClause + plan.groupClause
	selectArgs := append(append([]any{}, plan.selectArgs...), whereArgs...)
	buckets := make([]logs_core.AggregationBucketDTO, 0, limit)
	err = q.coordinator.Query(ctx, query.Request{
		Stream:       q.stream,
		Segments:     ordered,
		LocalFiles:   tail,
		FragmentSQL:  "SELECT " + plan.selectClause + fromWhere,
		FragmentArgs: selectArgs,
		FinalSQL: "SELECT " + plan.mergeClause + " FROM " + query.ResultsPlaceholder +
			plan.groupClause + plan.orderClause + " LIMIT ?",
		FinalArgs:  []any{limit},
		SingleSQL:  "SELECT " + plan.selectClause + fromWhere + plan.orderClause + " LIMIT ?",
		SingleArgs: append(selectArgs, limit),
		ScanBudget: request.ScanBudget,
		Tokens:     requiredTokens(request.Query),
	}, func(rows *sql.Rows) error {
		for rows.Next() {
			bucket, err := plan.scan(rows)
			if err != nil {
				return fmt.Errorf("failed to scan aggregation row: %w", err)
			}
			buckets = append(buckets, bucket)
		}
		return nil
	})
	if errors.Is(err, query.ErrNoData) {
		return empty, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to execute aggregation query: %w", err)
	}
	if emit != nil && len(buckets) > 0 {
		if err := emit(buckets); err != nil {
			return nil, err
		}
	}

	return &logs_core.AggregationResponseDTO{Buckets: buckets}, nil
}

// aggregationPlan is the SQL shape for a (BucketMs, GroupField) combination
// plus its scanner, kept together so SELECT columns and scan targets match.
type aggregationPlan struct {
	selectClause string
	selectArgs   []any
	// mergeClause re-aggregates fragment results (same columns).
	mergeClause string
	groupClause string
	orderClause string
	scan        func(rowScanner) (logs_core.AggregationBucketDTO, error)
}

// rowScanner is the subset of *sql.Rows the scan functions need.
type rowScanner interface {
	Scan(dest ...any) error
}

func buildAggregationPlan(request *logs_core.AggregationRequestDTO, builder *whereBuilder) aggregationPlan {
	return buildAggregationPlanCounting(request, builder, "count(*)")
}

// buildAggregationPlanCounting is buildAggregationPlan counting a row as
// countExpr - sum(cnt) over logs_1m's per-minute counts.
func buildAggregationPlanCounting(
	request *logs_core.AggregationRequestDTO, builder *whereBuilder, countExpr string,
) aggregationPlan {
	bucketed := request.BucketMs > 0
	grouped := request.GroupField != ""

	groupFieldExpr, groupFieldArgs := builder.groupFieldExpression(request.GroupField)

	var selectParts []string
	var selectArgs []any
	var groupParts []string
	var orderParts []string

	if bucketed {
		bucketSeconds := float64(request.BucketMs) / 1000.0
		selectParts = append(selectParts, "to_timestamp(floor(epoch(timestamp) / ?) * ?) AS bucket_ts")
		selectArgs = append(selectArgs, bucketSeconds, bucketSeconds)
		groupParts = append(groupParts, "bucket_ts")
		orderParts = append(orderParts, "bucket_ts ASC")
	}

	if grouped {
		// COALESCE to '': nullable columns and absent custom fields would fail to
		// scan into a string; NULL/absent is its own group, "".
		selectParts = append(selectParts, "COALESCE("+groupFieldExpr+", '') AS group_key")
		selectArgs = append(selectArgs, groupFieldArgs...)
		groupParts = append(groupParts, "group_key")
		if !bucketed {
			// Top-N breakdown: highest count first, ties by key so the
			// result doesn't depend on how fragments were split.
			orderParts = append(orderParts, "cnt DESC", "group_key ASC")
		} else {
			orderParts = append(orderParts, "group_key ASC")
		}
	}
	if !bucketed && !grouped {
		orderParts = append(orderParts, "cnt DESC")
	}

	selectClause := countExpr + " AS cnt"
	mergeClause := "sum(cnt)::BIGINT AS cnt"
	if len(selectParts) > 0 {
		selectClause = strings.Join(selectParts, ", ") + ", " + countExpr + " AS cnt"
		mergeClause = strings.Join(groupParts, ", ") + ", sum(cnt)::BIGINT AS cnt"
	}

	groupClause := ""
	if len(groupParts) > 0 {
		groupClause = " GROUP BY " + strings.Join(groupParts, ", ")
	}

	orderClause := " ORDER BY " + strings.Join(orderParts, ", ")

	return aggregationPlan{
		selectClause: selectClause,
		selectArgs:   selectArgs,
		mergeClause:  mergeClause,
		groupClause:  groupClause,
		orderClause:  orderClause,
		scan:         scannerFor(bucketed, grouped),
	}
}

func scannerFor(bucketed, grouped bool) func(rowScanner) (logs_core.AggregationBucketDTO, error) {
	switch {
	case bucketed && grouped:
		return func(row rowScanner) (logs_core.AggregationBucketDTO, error) {
			var ts time.Time
			var groupKey string
			var count int64
			if err := row.Scan(&ts, &groupKey, &count); err != nil {
				return logs_core.AggregationBucketDTO{}, err
			}
			return logs_core.AggregationBucketDTO{Timestamp: &ts, GroupKey: groupKey, Count: count}, nil
		}
	case bucketed:
		return func(row rowScanner) (logs_core.AggregationBucketDTO, error) {
			var ts time.Time
			var count int64
			if err := row.Scan(&ts, &count); err != nil {
				return logs_core.AggregationBucketDTO{}, err
			}
			return logs_core.AggregationBucketDTO{Timestamp: &ts, Count: count}, nil
		}
	case grouped:
		return func(row rowScanner) (logs_core.AggregationBucketDTO, error) {
			var groupKey string
			var count int64
			if err := row.Scan(&groupKey, &count); err != nil {
				return logs_core.AggregationBucketDTO{}, err
			}
			return logs_core.AggregationBucketDTO{GroupKey: groupKey, Count: count}, nil
		}
	default:
		return func(row rowScanner) (logs_core.AggregationBucketDTO, error) {
			var count int64
			if err := row.Scan(&count); err != nil {
				return logs_core.AggregationBucketDTO{}, err
			}
			return logs_core.AggregationBucketDTO{Count: count}, nil
		}
	}
}

// groupFieldExpression resolves a GroupField like a condition's field: a
// system column directly, else a custom field from the fields JSON.
func (b *whereBuilder) groupFieldExpression(field string) (expr string, args []any) {
	field = attrnames.Normalize(field)
	if column, isSystem := b.systemColumn(field); isSystem {
		return column, nil
	}
	return b.fieldText(field)
}
