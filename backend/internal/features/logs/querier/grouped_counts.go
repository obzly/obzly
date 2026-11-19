package logs_querier

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/query"
	"obzly/internal/storage/segments"
	"obzly/internal/util/attrnames"

	"github.com/google/uuid"
)

// Grouped counts over several time windows, merged in SQL: fragments count
// each group key per window, the merge sums them and keeps only the keys each
// selection asks for. Go then holds the selections' rows, never every group
// (a top N read back whole, every group of every hour, OOM-killed API pods on
// high-cardinality patterns and pods). Whole minutes come from logs_1m where
// segments have it; there is no partition cache: per-partition group lists
// are the unbounded part, and per-minute counts make a rerun cheap.
const (
	// maxCountWindows: Volume & noise counts one window per day (31 at most).
	maxCountWindows = 40
	// maxGroupKeys bounds GroupWindowsRequestDTO.Keys (an IN list).
	maxGroupKeys  = 5_000
	maxSelections = 10
)

var errInvalidGroupWindows = errors.New("invalid grouped count request")

// ExecuteGroupWindows counts the logs matching request.Query per group key
// in each window and returns the keys each selection picks.
func (q *LogQuerier) ExecuteGroupWindows(
	ctx context.Context, projectID uuid.UUID, request *logs_core.GroupWindowsRequestDTO,
) (*logs_core.GroupWindowsResponseDTO, error) {
	response, err := q.groupWindows(ctx, projectID, request)
	if segmentVanished(err) && ctx.Err() == nil {
		return q.groupWindows(ctx, projectID, request)
	}
	return response, err
}

// windowSource is one input of a grouped count: its stream, files and the
// ranges each window counts in it.
type windowSource struct {
	stream  string
	records []*segments.SegmentRecord
	tail    []segments.ResolvedFile
	rollup  bool
	ranges  [][]timeRange
}

func (q *LogQuerier) groupWindows(
	ctx context.Context, projectID uuid.UUID, request *logs_core.GroupWindowsRequestDTO,
) (*logs_core.GroupWindowsResponseDTO, error) {
	if err := validateGroupWindows(request); err != nil {
		return nil, err
	}
	response := emptyGroupWindows(request)
	from, to := request.Windows[0].From, request.Windows[0].To
	windows := make([]timeRange, len(request.Windows))
	for i, window := range request.Windows {
		from, to = minTime(from, window.From), maxTime(to, window.To)
		windows[i] = timeRange{start: window.From, end: window.To, includeEnd: true}
	}
	records, err := q.segmentRepo.PlanSegments(projectID, q.stream, from, to, hintsFor(request.Query))
	if err != nil {
		return nil, fmt.Errorf("failed to list segments for project: %w", err)
	}
	tail, planned := q.searchInputs(ctx, projectID, records, from, to, false)
	var ordered []*segments.SegmentRecord
	for _, record := range planned {
		for _, window := range windows {
			if len(overlapping([]*segments.SegmentRecord{record},
				searchWindow{From: window.start, To: window.end, IncludeTo: true})) > 0 {
				ordered = append(ordered, record)
				break
			}
		}
	}
	if len(tail) == 0 && len(ordered) == 0 {
		return response, nil
	}

	sources, err := q.windowSources(projectID, request, from, to, windows, tail, ordered)
	if err != nil {
		return nil, err
	}
	built := make([]query.Source, 0, len(sources))
	for _, source := range sources {
		sqlText, args := q.windowFragment(projectID, request, source)
		built = append(built, query.Source{
			Stream: source.stream, Segments: source.records, LocalFiles: source.tail,
			FragmentSQL: sqlText, FragmentArgs: args, Tokens: ternaryTokens(!source.rollup, request.Query),
		})
	}
	finalSQL, finalArgs := windowMergeSQL(request)
	main := built[0]
	err = q.coordinator.Query(ctx, query.Request{
		Stream: main.Stream, Segments: main.Segments, LocalFiles: main.LocalFiles,
		FragmentSQL: main.FragmentSQL, FragmentArgs: main.FragmentArgs, Tokens: main.Tokens,
		Sources:  built[1:],
		FinalSQL: finalSQL, FinalArgs: finalArgs, ScanBudget: request.ScanBudget,
	}, func(rows *sql.Rows) error {
		return q.scanWindowRows(rows, len(request.Windows), response)
	})
	if errors.Is(err, query.ErrNoData) {
		return emptyGroupWindows(request), nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to count groups: %w", err)
	}
	return response, nil
}

// windowSources splits the planned inputs: with per-minute counts, segments
// without them (and the tail) over the whole windows, the rest's partial edge
// minutes raw and their whole minutes from logs_1m; else everything raw.
func (q *LogQuerier) windowSources(
	projectID uuid.UUID,
	request *logs_core.GroupWindowsRequestDTO,
	from, to time.Time,
	windows []timeRange,
	tail []segments.ResolvedFile,
	ordered []*segments.SegmentRecord,
) ([]windowSource, error) {
	whole := make([][]timeRange, len(windows))
	for i, window := range windows {
		whole[i] = []timeRange{window}
	}
	raw := windowSource{stream: q.stream, records: ordered, tail: tail, ranges: whole}
	if !q.windowRollupsApply(request) {
		return []windowSource{raw}, nil
	}
	rollups, err := q.segmentRepo.PlanSegments(projectID, logs_core.StreamLogsRollup, from, to, segments.PruningHints{})
	if err != nil {
		return nil, fmt.Errorf("failed to list log rollups: %w", err)
	}
	bySource := q.readableRollups(rollups)
	var covered, uncovered, rollupRecords []*segments.SegmentRecord
	for _, record := range ordered {
		if rollup := bySource[record.ID.String()]; rollup != nil {
			covered = append(covered, record)
			rollupRecords = append(rollupRecords, rollup)
		} else {
			uncovered = append(uncovered, record)
		}
	}
	if len(covered) == 0 {
		return []windowSource{raw}, nil
	}
	inner := make([][]timeRange, len(windows))
	edges := make([][]timeRange, len(windows))
	anyEdge := false
	for i, window := range windows {
		inner[i], edges[i] = splitWholeMinutes([]timeRange{window})
		anyEdge = anyEdge || len(edges[i]) > 0
	}
	raw.records = uncovered
	sources := []windowSource{raw}
	if anyEdge {
		sources = append(sources, windowSource{stream: q.stream, records: covered, ranges: edges})
	}
	return append(sources, windowSource{
		stream: logs_core.StreamLogsRollup, records: rollupRecords, rollup: true, ranges: inner,
	}), nil
}

// windowRollupsApply reports whether logs_1m can answer request's counts:
// the logs stream, grouped and filtered on its dimensions only.
func (q *LogQuerier) windowRollupsApply(request *logs_core.GroupWindowsRequestDTO) bool {
	if q.stream != logs_core.StreamLogs {
		return false
	}
	for _, field := range groupWindowFields(request) {
		field = attrnames.Normalize(field)
		if field == logs_core.WorkloadField || field == logs_core.ReplicaSetField {
			field = "k8s_pod_name"
		}
		if field != "" && !rollupFields[field] {
			return false
		}
	}
	return onRollupFields(request.Query)
}

// windowFragment counts each key per window over source's rows: the key
// columns, then c0, c1, ... (NULL or 0 where a window has no range here).
func (q *LogQuerier) windowFragment(
	projectID uuid.UUID, request *logs_core.GroupWindowsRequestDTO, source windowSource,
) (string, []any) {
	builder := newWhereBuilder(projectID)
	builder.rollup = source.rollup
	countExpr := "count(*)"
	if source.rollup {
		countExpr = "sum(cnt)"
	}
	var selectParts []string
	var args []any
	keySelect, keyArgs, groupBy := windowKeyColumns(builder, request)
	selectParts = append(selectParts, keySelect)
	args = append(args, keyArgs...)

	level, levelArgs := builder.singleGroupFieldExpression("level")
	var all []timeRange
	for i, window := range request.Windows {
		ranges := source.ranges[i]
		all = append(all, ranges...)
		predicate, predicateArgs := rangesPredicate(ranges)
		if len(window.Levels) > 0 && predicate != "false" {
			predicate += " AND lower(" + level + ") IN (" + placeholders(len(window.Levels)) + ")"
			predicateArgs = append(append(predicateArgs, levelArgs...), toAny(window.Levels)...)
		}
		selectParts = append(selectParts, countExpr+" FILTER (WHERE "+predicate+") AS c"+strconv.Itoa(i))
		args = append(args, predicateArgs...)
	}

	where, whereArgs := rangesPredicate(mergeRanges(all))
	where = "project_id = ? AND " + where
	whereArgs = append([]any{projectID.String()}, whereArgs...)
	if querySQL, queryArgs := builder.Build(request.Query); querySQL != "" {
		where += " AND " + querySQL
		whereArgs = append(whereArgs, queryArgs...)
	}
	if len(request.Keys) > 0 {
		keyExpr, keyExprArgs := windowKeyExpression(builder, request.GroupField)
		where += " AND " + keyExpr + " IN (" + placeholders(len(request.Keys)) + ")"
		whereArgs = append(append(whereArgs, keyExprArgs...), toAny(request.Keys)...)
	}
	sqlText := "SELECT " + strings.Join(selectParts, ", ") + " FROM " + query.SourcePlaceholder +
		" WHERE " + where + " GROUP BY " + groupBy
	return sqlText, append(args, whereArgs...)
}

// windowMergeSQL sums the fragments per key and returns, ordered by sel and
// ord: the totals (sel -1, m the keys counted, v0.. each window's total),
// each selection's match count (sel -2-i, m) and its rows (sel i, the key's
// counts in v0..).
func windowMergeSQL(request *logs_core.GroupWindowsRequestDTO) (string, []any) {
	n := len(request.Windows)
	columns := func(prefix string) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = prefix + strconv.Itoa(i)
		}
		return strings.Join(parts, ", ")
	}
	sums := make([]string, n)
	totals := make([]string, n)
	nulls := make([]string, n)
	for i := range n {
		index := strconv.Itoa(i)
		sums[i] = "COALESCE(sum(c" + index + "), 0)::BIGINT AS w" + index
		totals[i] = "COALESCE(sum(w" + index + "), 0)::BIGINT AS t" + index
		nulls[i] = "NULL"
	}

	key := "key"
	totalsWhere := ""
	if len(request.GroupSets) > 0 {
		cases := make([]string, len(request.GroupSets))
		for i := range request.GroupSets {
			k := "k" + strconv.Itoa(i)
			cases[i] = "WHEN " + k + " IS NOT NULL THEN '" + strconv.Itoa(i) + "' || chr(31) || " + k
		}
		key = "CASE " + strings.Join(cases, " ") + " END"
		// Every log has a key in each set: the first set's sum to each.
		totalsWhere = " WHERE starts_with(key, '0' || chr(31))"
	}

	var b strings.Builder
	var args []any
	b.WriteString("WITH g AS (SELECT " + key + " AS key, " + strings.Join(sums, ", ") + " FROM " +
		query.ResultsPlaceholder + " GROUP BY 1), t AS (SELECT " + strings.Join(totals, ", ") + " FROM g" +
		totalsWhere + "), s AS (SELECT * FROM g, t) ")
	b.WriteString("SELECT -1 AS sel, NULL::VARCHAR AS key, 0::BIGINT AS ord, (SELECT count(*) FROM g)::BIGINT AS m, " +
		columns("t") + " FROM t")
	for i, selection := range request.Selections {
		sel := strconv.Itoa(i)
		where := selection.Where
		if where == "" {
			where = "true"
		}
		order := selection.OrderBy
		if order == "" {
			order = "w0 DESC"
		}
		order += ", key ASC"
		b.WriteString(" UNION ALL SELECT " + strconv.Itoa(-2-i) + ", NULL, 0, count(*), " +
			strings.Join(nulls, ", ") + " FROM s WHERE " + where)
		args = append(args, selection.WhereArgs...)
		if selection.Limit <= 0 {
			continue
		}
		b.WriteString(" UNION ALL SELECT " + sel + ", key, row_number() OVER (ORDER BY " + order + "), NULL, " +
			columns("w") + " FROM (SELECT * FROM s WHERE " + where + " ORDER BY " + order + " LIMIT " +
			strconv.Itoa(selection.Limit) + ")")
		args = append(args, selection.OrderArgs...)
		args = append(args, selection.WhereArgs...)
		args = append(args, selection.OrderArgs...)
	}
	b.WriteString(" ORDER BY sel, ord")
	return b.String(), args
}

func (q *LogQuerier) scanWindowRows(rows *sql.Rows, windows int, response *logs_core.GroupWindowsResponseDTO) error {
	for rows.Next() {
		var sel int
		var key sql.NullString
		var ord int64
		var matched sql.NullInt64
		values := make([]sql.NullInt64, windows)
		dest := []any{&sel, &key, &ord, &matched}
		for i := range values {
			dest = append(dest, &values[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return fmt.Errorf("failed to scan grouped count: %w", err)
		}
		q.mergedRows.Add(1)
		counts := make([]int64, windows)
		for i, value := range values {
			counts[i] = value.Int64
		}
		switch {
		case sel == -1:
			response.Keys, response.Totals = matched.Int64, counts
		case sel <= -2 && -2-sel < len(response.Selections):
			response.Selections[-2-sel].Matched = matched.Int64
		case sel >= 0 && sel < len(response.Selections):
			selection := &response.Selections[sel]
			selection.Rows = append(selection.Rows, logs_core.GroupCountsDTO{Key: key.String, Counts: counts})
		}
	}
	return nil
}

// windowKeyColumns is the fragment's key column(s) and GROUP BY: key, or
// k0, k1, ... as grouping sets.
func windowKeyColumns(builder *whereBuilder, request *logs_core.GroupWindowsRequestDTO) (string, []any, string) {
	if len(request.GroupSets) == 0 {
		expr, args := windowKeyExpression(builder, request.GroupField)
		return expr + " AS key", args, "key"
	}
	var columns, sets []string
	var args []any
	for i, field := range request.GroupSets {
		expr, fieldArgs := windowKeyExpression(builder, field)
		k := "k" + strconv.Itoa(i)
		columns = append(columns, expr+" AS "+k)
		sets = append(sets, "("+k+")")
		args = append(args, fieldArgs...)
	}
	return strings.Join(columns, ", "), args, "GROUPING SETS (" + strings.Join(sets, ", ") + ")"
}

// windowKeyExpression is groupFieldExpression with the pod-derived fields,
// never NULL (” for a missing value; ” for no group field at all).
func windowKeyExpression(builder *whereBuilder, groupField string) (string, []any) {
	if groupField == "" {
		return "''", nil
	}
	parts := strings.Split(groupField, logs_core.GroupFieldsSeparator)
	var exprs []string
	var args []any
	for _, part := range parts {
		expr, partArgs := windowFieldExpression(builder, strings.TrimSpace(part))
		if len(parts) == 1 {
			return "COALESCE(" + expr + ", '')", partArgs
		}
		exprs = append(exprs, "COALESCE(CAST("+expr+" AS VARCHAR), '')")
		args = append(args, partArgs...)
	}
	return "concat_ws(chr(31), " + strings.Join(exprs, ", ") + ")", args
}

func windowFieldExpression(builder *whereBuilder, field string) (string, []any) {
	switch attrnames.Normalize(field) {
	case logs_core.WorkloadField:
		return workloadSQL("COALESCE(k8s_pod_name, '')"), nil
	case logs_core.ReplicaSetField:
		return replicaSetSQL("COALESCE(k8s_pod_name, '')"), nil
	}
	return builder.singleGroupFieldExpression(field)
}

// workloadSQL is querying.WorkloadOfPod in SQL: a pod's name without its
// generated parts (Deployment: two, DaemonSet or Job: one), or without a
// StatefulSet ordinal.
func workloadSQL(pod string) string {
	generated, ordinal := "'"+logs_core.GeneratedNamePartPattern+"'", "'"+logs_core.StatefulSetOrdinalPattern+"'"
	once := "regexp_replace(" + pod + ", " + generated + ", '')"
	return "CASE WHEN regexp_matches(" + pod + ", " + generated + ") THEN " +
		"CASE WHEN regexp_matches(" + once + ", " + generated + ") THEN regexp_replace(" + once + ", " +
		generated + ", '') ELSE " + once + " END " +
		"WHEN regexp_matches(" + pod + ", " + ordinal + ") THEN regexp_replace(" + pod + ", " + ordinal + ", '') " +
		"ELSE " + pod + " END"
}

// replicaSetSQL is querying.ReplicaSetOfPod in SQL: a Deployment pod's name
// without its suffix, ” for any other pod.
func replicaSetSQL(pod string) string {
	generated := "'" + logs_core.GeneratedNamePartPattern + "'"
	once := "regexp_replace(" + pod + ", " + generated + ", '')"
	return "CASE WHEN regexp_matches(" + pod + ", " + generated + ") AND regexp_matches(" + once + ", " +
		generated + ") THEN " + once + " ELSE '' END"
}

func validateGroupWindows(request *logs_core.GroupWindowsRequestDTO) error {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{errInvalidGroupWindows}, args...)...)
	}
	switch {
	case len(request.Windows) == 0 || len(request.Windows) > maxCountWindows:
		return invalid("between 1 and %d windows", maxCountWindows)
	case len(request.Selections) > maxSelections:
		return invalid("at most %d selections", maxSelections)
	case len(request.Keys) > maxGroupKeys:
		return invalid("at most %d keys", maxGroupKeys)
	case len(request.GroupSets) > 0 && (request.GroupField != "" || len(request.Keys) > 0):
		return invalid("group sets take no group field or keys")
	}
	for _, selection := range request.Selections {
		if selection.Limit > maxAggregationLimit {
			return invalid("a selection's limit is at most %d", maxAggregationLimit)
		}
		// Written in code, but a quote or a statement break would mean one
		// was built from input.
		for _, text := range []string{selection.Where, selection.OrderBy} {
			if strings.ContainsAny(text, `;'"`) || strings.Contains(text, "--") || strings.Contains(text, "/*") {
				return invalid("selection SQL can't hold quotes, comments or statement breaks")
			}
		}
	}
	return nil
}

func emptyGroupWindows(request *logs_core.GroupWindowsRequestDTO) *logs_core.GroupWindowsResponseDTO {
	return &logs_core.GroupWindowsResponseDTO{
		Totals:     make([]int64, len(request.Windows)),
		Selections: make([]logs_core.GroupSelectionResultDTO, len(request.Selections)),
	}
}

func groupWindowFields(request *logs_core.GroupWindowsRequestDTO) []string {
	var fields []string
	for _, group := range append([]string{request.GroupField}, request.GroupSets...) {
		if group != "" {
			fields = append(fields, strings.Split(group, logs_core.GroupFieldsSeparator)...)
		}
	}
	return fields
}

// rangesPredicate is a timestamp condition matching any of ranges ("false"
// for none).
func rangesPredicate(ranges []timeRange) (string, []any) {
	if len(ranges) == 0 {
		return "false", nil
	}
	clauses := make([]string, len(ranges))
	args := make([]any, 0, 2*len(ranges))
	for i, r := range ranges {
		clauses[i] = "(timestamp >= ? AND timestamp " + ternary(r.includeEnd, "<=", "<") + " ?)"
		args = append(args, r.start, r.end)
	}
	return "(" + strings.Join(clauses, " OR ") + ")", args
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

func toAny(values []string) []any {
	result := make([]any, len(values))
	for i, value := range values {
		result[i] = value
	}
	return result
}

func ternaryTokens(raw bool, filter *logs_core.QueryNode) []string {
	if !raw {
		return nil
	}
	return requiredTokens(filter)
}
