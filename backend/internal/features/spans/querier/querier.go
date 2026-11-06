// Package spans_querier is the spans stream's read path off the Parquet
// storage engine: trace search, whole traces, search-form dropdowns and
// dashboard aggregations.
package spans_querier

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	spans_core "obzly/internal/features/spans/core"
	"obzly/internal/query"
	"obzly/internal/storage/segments"
	"obzly/internal/telemetry"
	"obzly/internal/util/logger"

	"github.com/google/uuid"
	_ "github.com/marcboeker/go-duckdb/v2"
)

const (
	defaultTraceSearchLimit = 20
	// maxTraceSearchLimit bounds request.Limit: it sizes a slice
	// preallocation and an SQL LIMIT.
	maxTraceSearchLimit = 1000
	maxSpansPerTrace    = 10_000
)

type SpanQuerier struct {
	db          *sql.DB
	segmentRepo *segments.SegmentRepository

	// objectStore/cacheDir: the cold-tier read path.
	objectStore *segments.ObjectStore
	cacheDir    string
	// tail adds rows still pending in JetStream (nil without the broker).
	tail segments.TailSource
	// coordinator runs queries as fragments over executors.
	coordinator *query.Coordinator
	queryDir    string
	memoryLimit string
	spillDir    string
}

type QuerierOption func(*SpanQuerier)

// WithTail makes every query include rows still pending in JetStream.
func WithTail(tail segments.TailSource) QuerierOption {
	return func(q *SpanQuerier) {
		q.tail = tail
	}
}

func WithObjectStore(store *segments.ObjectStore, cacheDir string) QuerierOption {
	return func(q *SpanQuerier) {
		q.objectStore = store
		q.cacheDir = cacheDir
	}
}

// WithMemoryLimit caps the querier's DuckDB memory.
func WithMemoryLimit(limit string) QuerierOption {
	return func(q *SpanQuerier) {
		q.memoryLimit = limit
	}
}

// WithQueryDir keeps query fragment results in dir.
func WithQueryDir(dir string) QuerierOption {
	return func(q *SpanQuerier) {
		q.queryDir = dir
	}
}

func NewSpanQuerier(opts ...QuerierOption) (*SpanQuerier, error) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, fmt.Errorf("failed to open duckdb handle: %w", err)
	}
	if err := segments.RegisterNormalizationFunctions(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	querier := &SpanQuerier{
		db: db, segmentRepo: &segments.SegmentRepository{},
		queryDir: filepath.Join(os.TempDir(), "obzly-query-spans"),
	}
	for _, opt := range opts {
		opt(querier)
	}
	querier.spillDir, err = segments.ConfigureMemory(db, querier.memoryLimit, filepath.Join(querier.queryDir, "spill"))
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	local := query.NewLocalExecutor("local", db, querier.objectStore, querier.cacheDir)
	querier.coordinator, err = query.NewCoordinator(
		querier.queryDir,
		db,
		local,
		query.RemoteExecutors,
		logger.GetLogger(),
	)
	if err != nil {
		_ = db.Close()
		_ = os.RemoveAll(querier.spillDir)
		return nil, err
	}
	telemetry.RegisterDuckDB("querier_spans", db)

	return querier, nil
}

func (q *SpanQuerier) Close() error {
	telemetry.UnregisterDuckDB(q.db)
	err := q.db.Close()
	_ = os.RemoveAll(q.spillDir)
	return err
}

// ListTraces returns one summary row per matching trace. As in Jaeger,
// service/operation filters match if any span matches and duration/error
// filters apply to the whole trace; filters are HAVING over the grouped
// trace, so a summary covers all its spans, not just the matching ones.
func (q *SpanQuerier) ListTraces(
	ctx context.Context,
	projectID uuid.UUID,
	request *spans_core.ListTracesRequestDTO,
) (*spans_core.ListTracesResponseDTO, error) {
	empty := &spans_core.ListTracesResponseDTO{Traces: []spans_core.TraceSummaryDTO{}}
	from, to := resolveTimeRange(request.TimeRange)
	tail, records, err := q.inputs(projectID, from, to, segments.PruningHints{})
	if err != nil {
		return nil, err
	}

	whereClause := "project_id = ? AND start_time >= ? AND start_time <= ?"
	whereArgs := []any{projectID.String(), from, to}

	limit := request.Limit
	if limit <= 0 {
		limit = defaultTraceSearchLimit
	}
	if limit > maxTraceSearchLimit {
		limit = maxTraceSearchLimit
	}

	// A trace's spans can be in several fragments: each returns per-trace
	// partials (and whether any of its spans matched the search), and the
	// filters apply to the merged trace.
	search := buildTraceSearch(request)
	partials := "SELECT trace_id, MIN(start_time) AS trace_start, MAX(end_time) AS trace_end, " +
		"COUNT(*) AS span_count, COUNT(*) FILTER (WHERE status_code = 'ERROR') AS error_count, " +
		"min_by(name, start_time) AS root_name, min_by(service_name, start_time) AS root_service, " +
		"list(DISTINCT service_name) AS services" + search.partialColumns +
		" FROM " + query.SourcePlaceholder + " WHERE " + whereClause + " GROUP BY trace_id"
	order := " ORDER BY trace_start DESC, trace_id DESC LIMIT ?"
	single := "SELECT trace_id, MIN(start_time) AS trace_start, MAX(end_time) AS trace_end, " +
		"COUNT(*) AS span_count, COUNT(*) FILTER (WHERE status_code = 'ERROR') AS error_count, " +
		"min_by(name, start_time) AS root_name, min_by(service_name, start_time) AS root_service, " +
		"array_agg(DISTINCT service_name) AS services " +
		"FROM " + query.SourcePlaceholder + " WHERE " + whereClause + " GROUP BY trace_id" + search.having + order

	traces := make([]spans_core.TraceSummaryDTO, 0, limit)
	found, err := q.runContext(ctx, query.Request{
		Segments:     records,
		LocalFiles:   tail,
		FragmentSQL:  partials,
		FragmentArgs: append(append([]any{}, search.partialArgs...), whereArgs...),
		FinalSQL: "SELECT trace_id, MIN(trace_start) AS trace_start, MAX(trace_end) AS trace_end, " +
			"SUM(span_count)::BIGINT AS span_count, SUM(error_count)::BIGINT AS error_count, " +
			"min_by(root_name, trace_start) AS root_name, min_by(root_service, trace_start) AS root_service, " +
			"list_distinct(flatten(list(services))) AS services FROM " + query.ResultsPlaceholder +
			" GROUP BY trace_id" + search.mergedHaving + order,
		FinalArgs:  append(append([]any{}, search.mergedArgs...), limit),
		SingleSQL:  single,
		SingleArgs: append(append(append([]any{}, whereArgs...), search.havingArgs...), limit),
		ScanBudget: request.ScanBudget,
	}, func(rows *sql.Rows) error {
		for rows.Next() {
			summary, err := scanTraceSummary(rows)
			if err != nil {
				return fmt.Errorf("failed to scan trace summary row: %w", err)
			}
			traces = append(traces, summary)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to execute trace search query: %w", err)
	}
	if !found {
		return empty, nil
	}
	return &spans_core.ListTracesResponseDTO{Traces: traces}, nil
}

// traceSearch is a trace search's filters three ways: as HAVING over raw
// spans (one query), as per-fragment partial columns, and as HAVING over
// the merged partials.
type traceSearch struct {
	having         string
	havingArgs     []any
	partialColumns string
	partialArgs    []any
	mergedHaving   string
	mergedArgs     []any
}

func buildTraceSearch(request *spans_core.ListTracesRequestDTO) traceSearch {
	var search traceSearch
	var having, merged []string

	if request.ServiceName != "" {
		having = append(having, "bool_or(service_name = ?)")
		search.havingArgs = append(search.havingArgs, request.ServiceName)
		search.partialColumns += ", bool_or(service_name = ?) AS service_match"
		search.partialArgs = append(search.partialArgs, request.ServiceName)
		merged = append(merged, "bool_or(service_match)")
	}
	if request.OperationName != "" {
		having = append(having, "bool_or(name = ?)")
		search.havingArgs = append(search.havingArgs, request.OperationName)
		search.partialColumns += ", bool_or(name = ?) AS operation_match"
		search.partialArgs = append(search.partialArgs, request.OperationName)
		merged = append(merged, "bool_or(operation_match)")
	}
	if request.ErrorsOnly {
		having = append(having, "bool_or(status_code = 'ERROR')")
		merged = append(merged, "SUM(error_count) > 0")
	}
	if request.MinDurationMs != nil {
		having = append(having, "(epoch_ns(MAX(end_time)) - epoch_ns(MIN(start_time))) / 1000000.0 >= ?")
		search.havingArgs = append(search.havingArgs, *request.MinDurationMs)
		merged = append(merged, "(epoch_ns(MAX(trace_end)) - epoch_ns(MIN(trace_start))) / 1000000.0 >= ?")
		search.mergedArgs = append(search.mergedArgs, *request.MinDurationMs)
	}
	if request.MaxDurationMs != nil {
		having = append(having, "(epoch_ns(MAX(end_time)) - epoch_ns(MIN(start_time))) / 1000000.0 <= ?")
		search.havingArgs = append(search.havingArgs, *request.MaxDurationMs)
		merged = append(merged, "(epoch_ns(MAX(trace_end)) - epoch_ns(MIN(trace_start))) / 1000000.0 <= ?")
		search.mergedArgs = append(search.mergedArgs, *request.MaxDurationMs)
	}

	if len(having) > 0 {
		search.having = " HAVING " + strings.Join(having, " AND ")
		search.mergedHaving = " HAVING " + strings.Join(merged, " AND ")
	}
	return search
}

func scanTraceSummary(rows *sql.Rows) (spans_core.TraceSummaryDTO, error) {
	var traceID, rootName, rootService string
	var traceStart, traceEnd time.Time
	var spanCount, errorCount int64
	var rawServices []any

	if err := rows.Scan(
		&traceID, &traceStart, &traceEnd, &spanCount, &errorCount, &rootName, &rootService, &rawServices,
	); err != nil {
		return spans_core.TraceSummaryDTO{}, err
	}

	services := make([]string, 0, len(rawServices))
	for _, service := range rawServices {
		if s, ok := service.(string); ok {
			services = append(services, s)
		}
	}

	return spans_core.TraceSummaryDTO{
		TraceID:         traceID,
		RootName:        rootName,
		RootServiceName: rootService,
		StartTime:       traceStart,
		DurationMs:      traceEnd.Sub(traceStart).Seconds() * 1000,
		SpanCount:       spanCount,
		ErrorCount:      errorCount,
		Services:        services,
	}, nil
}

// GetTrace returns every span of one trace. timeRange narrows which
// segments are opened; nil searches the whole retained history.
func (q *SpanQuerier) GetTrace(
	projectID uuid.UUID,
	traceID string,
	timeRange *spans_core.TimeRangeDTO,
) (*spans_core.GetTraceResponseDTO, error) {
	from, to := resolveTimeRange(timeRange)
	tail, records, err := q.inputs(projectID, from, to, segments.PruningHints{})
	if err != nil {
		return nil, err
	}

	spansOf := "SELECT id, trace_id, span_id, parent_span_id, name, service_name, kind, " +
		"start_time, end_time, status_code, status_message, attributes, resource_attributes, events " +
		"FROM " + query.SourcePlaceholder +
		" WHERE project_id = ? AND trace_id = ? AND start_time >= ? AND start_time <= ? " +
		"ORDER BY start_time ASC, span_id ASC LIMIT ?"
	args := []any{projectID.String(), traceID, from, to, maxSpansPerTrace}
	spanList := make([]spans_core.SpanDTO, 0)
	_, err = q.run(query.Request{
		Segments:     records,
		LocalFiles:   tail,
		FragmentSQL:  spansOf,
		FragmentArgs: args,
		FinalSQL:     "SELECT * FROM " + query.ResultsPlaceholder + " ORDER BY start_time ASC, span_id ASC LIMIT ?",
		FinalArgs:    []any{maxSpansPerTrace},
		SingleSQL:    spansOf,
		SingleArgs:   args,
	}, func(rows *sql.Rows) error {
		for rows.Next() {
			span, err := scanSpan(rows)
			if err != nil {
				return fmt.Errorf("failed to scan span row: %w", err)
			}
			spanList = append(spanList, span)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to execute get-trace query: %w", err)
	}
	return &spans_core.GetTraceResponseDTO{TraceID: traceID, Spans: spanList}, nil
}

// GetTraces returns the spans of traceIDs within timeRange in one scan
// (Jaeger search returns whole traces; a GetTrace per result is a scan
// each), at most maxSpansPerTrace per trace.
func (q *SpanQuerier) GetTraces(
	ctx context.Context,
	projectID uuid.UUID,
	traceIDs []string,
	timeRange *spans_core.TimeRangeDTO,
	scanBudget int64,
) (map[string][]spans_core.SpanDTO, error) {
	result := map[string][]spans_core.SpanDTO{}
	if len(traceIDs) == 0 {
		return result, nil
	}
	from, to := resolveTimeRange(timeRange)
	tail, records, err := q.inputs(projectID, from, to, segments.PruningHints{})
	if err != nil {
		return nil, err
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(traceIDs)), ", ")
	perTrace := " QUALIFY row_number() OVER (PARTITION BY trace_id ORDER BY start_time ASC, span_id ASC) <= ?"
	spansOf := "SELECT id, trace_id, span_id, parent_span_id, name, service_name, kind, " +
		"start_time, end_time, status_code, status_message, attributes, resource_attributes, events " +
		"FROM " + query.SourcePlaceholder +
		" WHERE project_id = ? AND trace_id IN (" + placeholders + ") AND start_time >= ? AND start_time <= ?" +
		perTrace
	args := []any{projectID.String()}
	for _, traceID := range traceIDs {
		args = append(args, traceID)
	}
	args = append(args, from, to, maxSpansPerTrace)
	_, err = q.runContext(ctx, query.Request{
		Segments:     records,
		LocalFiles:   tail,
		FragmentSQL:  spansOf,
		FragmentArgs: args,
		FinalSQL: "SELECT * FROM " + query.ResultsPlaceholder + perTrace +
			" ORDER BY trace_id, start_time ASC, span_id ASC",
		FinalArgs:  []any{maxSpansPerTrace},
		SingleSQL:  spansOf + " ORDER BY trace_id, start_time ASC, span_id ASC",
		SingleArgs: args,
		ScanBudget: scanBudget,
	}, func(rows *sql.Rows) error {
		for rows.Next() {
			span, err := scanSpan(rows)
			if err != nil {
				return fmt.Errorf("failed to scan span row: %w", err)
			}
			result[span.TraceID] = append(result[span.TraceID], span)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to execute get-traces query: %w", err)
	}
	return result, nil
}

func scanSpan(rows *sql.Rows) (spans_core.SpanDTO, error) {
	var id, traceID, spanID, parentSpanID, name, serviceName, kind string
	var startTime, endTime time.Time
	var statusCode, statusMessage, attributesJSON, resourceAttributesJSON, eventsJSON string

	if err := rows.Scan(
		&id, &traceID, &spanID, &parentSpanID, &name, &serviceName, &kind,
		&startTime, &endTime, &statusCode, &statusMessage,
		&attributesJSON, &resourceAttributesJSON, &eventsJSON,
	); err != nil {
		return spans_core.SpanDTO{}, err
	}

	span := spans_core.SpanDTO{
		ID:            id,
		TraceID:       traceID,
		SpanID:        spanID,
		ParentSpanID:  parentSpanID,
		Name:          name,
		ServiceName:   serviceName,
		Kind:          kind,
		StartTime:     startTime,
		EndTime:       endTime,
		DurationMs:    endTime.Sub(startTime).Seconds() * 1000,
		StatusCode:    statusCode,
		StatusMessage: statusMessage,
	}

	span.Attributes = decodeJSONMap(attributesJSON)
	span.ResourceAttributes = decodeJSONMap(resourceAttributesJSON)

	if eventsJSON != "" && eventsJSON != "[]" {
		var events []spans_core.SpanEventDTO
		if err := json.Unmarshal([]byte(eventsJSON), &events); err == nil {
			span.Events = events
		}
	}

	return span, nil
}

func decodeJSONMap(raw string) map[string]any {
	if raw == "" || raw == "{}" {
		return nil
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil
	}
	return decoded
}

// ListServices returns every distinct service_name seen in the time range.
func (q *SpanQuerier) ListServices(
	projectID uuid.UUID,
	timeRange *spans_core.TimeRangeDTO,
) (*spans_core.ListServicesResponseDTO, error) {
	from, to := resolveTimeRange(timeRange)
	tail, records, err := q.inputs(projectID, from, to, segments.PruningHints{})
	if err != nil {
		return nil, err
	}
	values, err := q.distinctStrings(tail, records, "service_name",
		"project_id = ? AND start_time >= ? AND start_time <= ?", []any{projectID.String(), from, to})
	if err != nil {
		return nil, fmt.Errorf("failed to list services: %w", err)
	}
	return &spans_core.ListServicesResponseDTO{Services: values}, nil
}

// ListOperations returns every distinct span name for one service within
// the time range.
func (q *SpanQuerier) ListOperations(
	projectID uuid.UUID,
	serviceName string,
	timeRange *spans_core.TimeRangeDTO,
) (*spans_core.ListOperationsResponseDTO, error) {
	from, to := resolveTimeRange(timeRange)
	hints := segments.PruningHints{Services: []string{serviceName}}
	tail, records, err := q.inputs(projectID, from, to, hints)
	if err != nil {
		return nil, err
	}
	values, err := q.distinctStrings(tail, records, "name",
		"project_id = ? AND service_name = ? AND start_time >= ? AND start_time <= ?",
		[]any{projectID.String(), serviceName, from, to})
	if err != nil {
		return nil, fmt.Errorf("failed to list operations: %w", err)
	}
	return &spans_core.ListOperationsResponseDTO{Operations: values}, nil
}

// distinctStrings is column's distinct values where whereClause holds,
// sorted.
func (q *SpanQuerier) distinctStrings(
	tail []segments.ResolvedFile,
	records []*segments.SegmentRecord,
	column, whereClause string,
	whereArgs []any,
) ([]string, error) {
	distinct := "SELECT DISTINCT " + column + " AS value FROM " + query.SourcePlaceholder + " WHERE " + whereClause
	values := make([]string, 0)
	_, err := q.run(query.Request{
		Segments:     records,
		LocalFiles:   tail,
		FragmentSQL:  distinct,
		FragmentArgs: whereArgs,
		FinalSQL:     "SELECT DISTINCT value FROM " + query.ResultsPlaceholder + " ORDER BY value",
		SingleSQL:    distinct + " ORDER BY value",
		SingleArgs:   whereArgs,
	}, func(rows *sql.Rows) error {
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				return err
			}
			values = append(values, value)
		}
		return nil
	})
	return values, err
}

// inputs is a query's pending tail files and planned segments.
func (q *SpanQuerier) inputs(
	projectID uuid.UUID,
	from, to time.Time,
	hints segments.PruningHints,
) ([]segments.ResolvedFile, []*segments.SegmentRecord, error) {
	records, err := q.segmentRepo.PlanSegments(projectID, "spans", from, to, hints)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list segments for project: %w", err)
	}
	tail := segments.AppendTail(context.Background(), q.tail, projectID, "spans", records, from, to, nil)
	return tail, records, nil
}

// run runs request over the spans stream; false when there was nothing to
// read (the caller returns its empty response).
func (q *SpanQuerier) run(request query.Request, scan func(*sql.Rows) error) (bool, error) {
	return q.runContext(context.Background(), request, scan)
}

// runContext is run, ended early when ctx is (a caller gone away).
func (q *SpanQuerier) runContext(ctx context.Context, request query.Request, scan func(*sql.Rows) error) (bool, error) {
	if len(request.Segments) == 0 && len(request.LocalFiles) == 0 {
		return false, nil
	}
	request.Stream = "spans"
	err := q.coordinator.Query(ctx, request, scan)
	if errors.Is(err, query.ErrNoData) {
		return false, nil
	}
	return err == nil, err
}

// noUpperBound / resolveTimeRange duplicate logs_querier's helpers.
var noUpperBound = time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)

// minRepresentableTime/maxRepresentableTime bound TIMESTAMP_NS; times
// outside are clamped so they give a result, not a 500 (see logs_querier).
var (
	minRepresentableTime = time.Date(1678, 1, 1, 0, 0, 0, 0, time.UTC)
	maxRepresentableTime = time.Date(2261, 1, 1, 0, 0, 0, 0, time.UTC)
)

func resolveTimeRange(timeRange *spans_core.TimeRangeDTO) (time.Time, time.Time) {
	from := time.Unix(0, 0).UTC()
	to := noUpperBound

	if timeRange != nil {
		if timeRange.From != nil {
			from = *timeRange.From
		}
		if timeRange.To != nil {
			to = *timeRange.To
		}
	}

	return clampToRepresentableRange(from), clampToRepresentableRange(to)
}

func clampToRepresentableRange(t time.Time) time.Time {
	if t.Before(minRepresentableTime) {
		return minRepresentableTime
	}
	if t.After(maxRepresentableTime) {
		return maxRepresentableTime
	}
	return t
}
