package metrics_promql

import (
	"context"
	"math"
	"slices"
	"strconv"
	"sync"
	"time"

	metrics_core "obzly/internal/features/metrics/core"
	"obzly/internal/telemetry"

	"github.com/google/uuid"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/model/value"
	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/promql/parser"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunks"
)

// pushdownPrefix names the series that stand in for pushed-down results.
const pushdownPrefix = "__obzly_pushdown_"

// maxConcurrentPushdowns bounds the subtrees of one expression storage
// evaluates at once: each is a distributed query, and the user's PromQL
// limiter slot covers the whole expression.
const maxConcurrentPushdowns = 4

// pushdownOutcome is storage's answer to one request: ok false when it
// declined.
type pushdownOutcome struct {
	response *metrics_core.RangeFunctionResponseDTO
	ok       bool
}

var pushdownAggregations = map[parser.ItemType]string{
	parser.SUM: "sum", parser.AVG: "avg", parser.MIN: "min", parser.MAX: "max", parser.COUNT: "count",
}

// NewRangeQuery is the engine's range query over projectID's metrics, with
// range functions pushed down to storage where it can answer them exactly.
func NewRangeQuery(
	ctx context.Context, projectID uuid.UUID, expression string, start, end time.Time, step time.Duration,
) (promql.Query, error) {
	queryable, expression, err := pushDown(ctx, projectID, expression,
		start.UnixMilli(), end.UnixMilli(), step.Milliseconds())
	if err != nil {
		return nil, err
	}
	return engine.NewRangeQuery(ctx, queryable, nil, expression, start, end, step)
}

// NewInstantQuery is NewRangeQuery at one time.
func NewInstantQuery(ctx context.Context, projectID uuid.UUID, expression string, at time.Time) (promql.Query, error) {
	queryable, expression, err := pushDown(ctx, projectID, expression, at.UnixMilli(), at.UnixMilli(), 0)
	if err != nil {
		return nil, err
	}
	return engine.NewInstantQuery(ctx, queryable, nil, expression, at)
}

// pushDown evaluates each pushable subtree of expression in storage and
// replaces it with a series holding the result at every step, which the
// engine reads like any other (a stale marker where there is no value);
// "+ 0" drops that series' name, as the function would. Everything else -
// and any subtree storage declines - the engine evaluates from samples.
// Subtrees are independent, so storage evaluates them concurrently (a ratio
// of two sums no longer waits for one before starting the other).
func pushDown(
	ctx context.Context, projectID uuid.UUID, expression string, startMs, endMs, stepMs int64,
) (storage.Queryable, string, error) {
	base := NewQueryable(projectID)
	expr, err := queryParser.ParseExpr(expression)
	if err != nil {
		return base, expression, nil
	}
	var requests []*metrics_core.RangeFunctionRequestDTO
	walkPushable(expr, func(node parser.Expr, request *metrics_core.RangeFunctionRequestDTO) parser.Expr {
		request.StartMs, request.EndMs, request.StepMs = startMs, endMs, stepMs
		requests = append(requests, request)
		return node
	})
	if len(requests) == 0 {
		telemetry.PromQLPushdown.WithLabelValues("unsupported").Inc()
		return base, expression, nil
	}
	outcomes, err := evaluatePushdowns(ctx, projectID, requests)
	if err != nil {
		return nil, "", &StorageError{Err: err}
	}

	results := map[string]*metrics_core.RangeFunctionResponseDTO{}
	next := 0
	rewritten := walkPushable(expr, func(node parser.Expr, _ *metrics_core.RangeFunctionRequestDTO) parser.Expr {
		outcome := outcomes[next]
		next++
		if !outcome.ok {
			return node
		}
		name := pushdownPrefix + strconv.Itoa(len(results)) + "__"
		results[name] = outcome.response
		return &parser.ParenExpr{Expr: &parser.BinaryExpr{
			Op: parser.ADD,
			LHS: &parser.VectorSelector{
				Name: name,
				LabelMatchers: []*labels.Matcher{
					labels.MustNewMatcher(labels.MatchEqual, model.MetricNameLabel, name),
				},
			},
			RHS: &parser.NumberLiteral{Val: 0},
		}}
	})
	if len(results) == 0 {
		telemetry.PromQLPushdown.WithLabelValues("fallback").Inc()
		return base, expression, nil
	}
	telemetry.PromQLPushdown.WithLabelValues("pushed").Inc()
	return &pushdownQueryable{base: base, results: results, startMs: startMs, endMs: endMs, stepMs: stepMs},
		rewritten.String(), nil
}

// pushableRequest is node as a storage request, or nil: a supported range
// function over one metric's selector with no offset or @, optionally in a
// sum/avg/min/max/count by (...).
func pushableRequest(node parser.Expr) *metrics_core.RangeFunctionRequestDTO {
	if aggregate, ok := node.(*parser.AggregateExpr); ok {
		op, supported := pushdownAggregations[aggregate.Op]
		if !supported || aggregate.Without || aggregate.Param != nil ||
			slices.Contains(aggregate.Grouping, model.MetricNameLabel) {
			return nil
		}
		inner := unwrapParens(aggregate.Expr)
		request := pushableCall(inner)
		if request == nil {
			request = pushableSelector(inner)
		}
		if request == nil {
			return nil
		}
		request.Aggregation = op
		request.By = slices.Clone(aggregate.Grouping)
		return request
	}
	return pushableCall(node)
}

func pushableCall(node parser.Expr) *metrics_core.RangeFunctionRequestDTO {
	call, ok := node.(*parser.Call)
	if !ok || call.Func == nil || len(call.Args) != 1 {
		return nil
	}
	switch call.Func.Name {
	case "rate", "increase", "sum_over_time", "avg_over_time", "count_over_time", "min_over_time", "max_over_time":
	default:
		return nil
	}
	matrix, ok := call.Args[0].(*parser.MatrixSelector)
	if !ok {
		return nil
	}
	selector, ok := matrix.VectorSelector.(*parser.VectorSelector)
	if !ok || !plainSelector(selector) || matrix.Range <= 0 {
		return nil
	}
	return &metrics_core.RangeFunctionRequestDTO{
		Matchers: toMatcherDTOs(selector.LabelMatchers),
		Function: call.Func.Name,
		RangeMs:  matrix.Range.Milliseconds(),
	}
}

// pushableSelector is an instant selector inside an aggregation as a
// storage request: each series' latest sample in the engine's lookback
// window, as the engine selects it. A bare selector keeps its name, which a
// pushed-down result can't, so only aggregated ones qualify.
func pushableSelector(node parser.Expr) *metrics_core.RangeFunctionRequestDTO {
	selector, ok := node.(*parser.VectorSelector)
	if !ok || !plainSelector(selector) {
		return nil
	}
	return &metrics_core.RangeFunctionRequestDTO{
		Matchers: toMatcherDTOs(selector.LabelMatchers),
		Function: metrics_core.FunctionInstant,
		RangeMs:  lookbackDelta.Milliseconds(),
	}
}

// plainSelector: one metric's selector, with no offset, @ or experimental
// modifier.
func plainSelector(selector *parser.VectorSelector) bool {
	if selector.OriginalOffset != 0 || selector.OriginalOffsetExpr != nil || selector.Offset != 0 ||
		selector.Timestamp != nil || selector.StartOrEnd != 0 || selector.Anchored || selector.Smoothed {
		return false
	}
	for _, matcher := range selector.LabelMatchers {
		if matcher.Name == model.MetricNameLabel && matcher.Type == labels.MatchEqual {
			return true
		}
	}
	return false
}

func unwrapParens(node parser.Expr) parser.Expr {
	for {
		paren, ok := node.(*parser.ParenExpr)
		if !ok {
			return node
		}
		node = paren.Expr
	}
}

// walkPushable passes each pushable subtree of node, in a fixed order, to
// visit and puts what it returns in its place. Subqueries are left alone:
// they evaluate at their own steps.
func walkPushable(
	node parser.Expr, visit func(parser.Expr, *metrics_core.RangeFunctionRequestDTO) parser.Expr,
) parser.Expr {
	if request := pushableRequest(node); request != nil {
		return visit(node, request)
	}
	switch n := node.(type) {
	case *parser.AggregateExpr:
		n.Expr = walkPushable(n.Expr, visit)
		if n.Param != nil {
			n.Param = walkPushable(n.Param, visit)
		}
	case *parser.BinaryExpr:
		n.LHS = walkPushable(n.LHS, visit)
		n.RHS = walkPushable(n.RHS, visit)
	case *parser.Call:
		for i, arg := range n.Args {
			n.Args[i] = walkPushable(arg, visit)
		}
	case *parser.ParenExpr:
		n.Expr = walkPushable(n.Expr, visit)
	case *parser.UnaryExpr:
		n.Expr = walkPushable(n.Expr, visit)
	}
	return node
}

// evaluatePushdowns asks storage for every request, at most
// maxConcurrentPushdowns at once; the first error cancels the rest.
func evaluatePushdowns(
	ctx context.Context, projectID uuid.UUID, requests []*metrics_core.RangeFunctionRequestDTO,
) ([]pushdownOutcome, error) {
	repository := metrics_core.GetMetricCoreRepository()
	outcomes := make([]pushdownOutcome, len(requests))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		once     sync.Once
		firstErr error
	)
	slots := make(chan struct{}, maxConcurrentPushdowns)
	for i, request := range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-slots }()
			if ctx.Err() != nil {
				return
			}
			response, ok, err := repository.EvaluateRangeFunction(ctx, projectID, request)
			if err != nil {
				once.Do(func() {
					firstErr = err
					cancel()
				})
				return
			}
			outcomes[i] = pushdownOutcome{response: response, ok: ok}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return outcomes, nil
}

// pushdownQueryable serves the pushed-down results by name and everything
// else from base.
type pushdownQueryable struct {
	base                   storage.Queryable
	results                map[string]*metrics_core.RangeFunctionResponseDTO
	startMs, endMs, stepMs int64
}

func (q *pushdownQueryable) Querier(mint, maxt int64) (storage.Querier, error) {
	base, err := q.base.Querier(mint, maxt)
	if err != nil {
		return nil, err
	}
	return &pushdownQuerier{Querier: base, results: q}, nil
}

type pushdownQuerier struct {
	storage.Querier
	results *pushdownQueryable
}

func (q *pushdownQuerier) Select(
	ctx context.Context, sortSeries bool, hints *storage.SelectHints, matchers ...*labels.Matcher,
) storage.SeriesSet {
	for _, matcher := range matchers {
		if matcher.Name != model.MetricNameLabel || matcher.Type != labels.MatchEqual {
			continue
		}
		if response, ok := q.results.results[matcher.Value]; ok {
			return q.results.seriesSet(matcher.Value, response)
		}
	}
	return q.Querier.Select(ctx, sortSeries, hints, matchers...)
}

// seriesSet is response as series with a point at every step: the value,
// or a stale marker so the engine's lookback doesn't carry the previous
// step's value into one storage left empty.
func (q *pushdownQueryable) seriesSet(name string, response *metrics_core.RangeFunctionResponseDTO) storage.SeriesSet {
	step := q.stepMs
	if step <= 0 {
		step = 1
	}
	stale := math.Float64frombits(value.StaleNaN)
	series := make([]storage.Series, 0, len(response.Series))
	for _, result := range response.Series {
		withName := make(map[string]string, len(result.Labels)+1)
		for key, label := range result.Labels {
			withName[key] = label
		}
		withName[model.MetricNameLabel] = name
		samples := make([]chunks.Sample, 0, (q.endMs-q.startMs)/step+1)
		next := 0
		for t := q.startMs; t <= q.endMs; t += step {
			f := stale
			for next < len(result.Points) && result.Points[next].TimestampMs < t {
				next++
			}
			if next < len(result.Points) && result.Points[next].TimestampMs == t {
				f = result.Points[next].Value
			}
			samples = append(samples, floatSample{t: t, f: f})
		}
		series = append(series, storage.NewListSeries(labels.FromMap(withName), samples))
	}
	sortByLabels(series)
	return &sliceSeriesSet{series: series, idx: -1}
}
