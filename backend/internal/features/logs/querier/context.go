package logs_querier

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/query"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

// Log context: the logs just before and after an anchor log from the same
// source. Each side is a cursor page from the anchor, so it reads only the
// nearest windows, and (timestamp, id) cursors place ties exactly.
const (
	defaultContextRows    = 50
	maxContextRows        = 1000
	defaultContextMinutes = 60
	maxContextMinutes     = 24 * 60
)

// ExecuteContext answers a LogContextRequestDTO. A nil request.Scope means
// the anchor's workload (logs_core.AutoContextScope).
func (q *LogQuerier) ExecuteContext(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.LogContextRequestDTO,
) (*logs_core.LogContextResponseDTO, error) {
	response, err := q.executeContext(ctx, projectID, request)
	if segmentVanished(err) {
		return q.executeContext(ctx, projectID, request)
	}
	return response, err
}

func (q *LogQuerier) executeContext(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.LogContextRequestDTO,
) (*logs_core.LogContextResponseDTO, error) {
	minutes := request.WindowMinutes
	if minutes <= 0 {
		minutes = defaultContextMinutes
	}
	minutes = min(minutes, maxContextMinutes)
	response := &logs_core.LogContextResponseDTO{
		Before: []logs_core.LogItemDTO{}, After: []logs_core.LogItemDTO{},
		Scope: []logs_core.LogContextScopeDTO{}, WindowMinutes: minutes,
	}

	at := request.Anchor.Timestamp.UTC()
	window := time.Duration(minutes) * time.Minute
	from, to := at.Add(-window), at.Add(window)
	records, err := q.segmentRepo.PlanSegments(projectID, q.stream, from, to, hintsFor(request.Query))
	if err != nil {
		return nil, fmt.Errorf("failed to list segments for project: %w", err)
	}
	tail, ordered := q.searchInputs(ctx, projectID, records, from, to, true)
	if len(tail) == 0 && len(ordered) == 0 {
		return response, nil
	}

	anchor, err := q.contextAnchor(ctx, projectID, tail, ordered, request.Anchor)
	if err != nil || anchor == nil {
		return response, err
	}
	response.Anchor = anchor

	scope := request.Scope
	if scope == nil {
		scope = logs_core.AutoContextScope(anchor, request.DefaultExtraFields)
	}
	filter := request.Query
	for _, field := range scope {
		value, ok := logs_core.ContextValue(anchor, field)
		if !ok || slices.ContainsFunc(response.Scope, func(s logs_core.LogContextScopeDTO) bool {
			return s.Field == field
		}) {
			continue
		}
		response.Scope = append(response.Scope, logs_core.LogContextScopeDTO{Field: field, Value: value})
		filter = andNode(filter, &logs_core.QueryNode{
			Type: logs_core.QueryNodeTypeCondition,
			Condition: &logs_core.ConditionNode{
				Field: field, Operator: logs_core.ConditionOperatorEquals, Value: value,
			},
		})
	}

	querySQL, queryArgs := newWhereBuilder(projectID).Build(filter)
	whereClause := "project_id = ? AND timestamp >= ? AND timestamp <= ?"
	whereArgs := append([]any{projectID.String(), from, to}, queryArgs...)
	if querySQL != "" {
		whereClause += " AND " + querySQL
	}
	tokens := requiredTokens(filter)

	start := logs_core.Cursor{Timestamp: anchor.Timestamp, ID: anchor.ID}
	sides := []struct {
		direction string
		limit     int
		ascending bool
	}{
		{logs_core.ContextBefore, contextRows(request.Before), false},
		{logs_core.ContextAfter, contextRows(request.After), true},
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	for _, side := range sides {
		if request.Direction != "" && request.Direction != side.direction {
			continue
		}
		cursor := start
		if request.Direction != "" && request.Cursor != "" {
			if cursor, err = logs_core.DecodeCursor(request.Cursor); err != nil {
				return nil, err
			}
		}
		wg.Go(func() {
			pageFrom, pageTo := cursorPageRange(from, to, cursor, side.ascending)
			clause, args := afterCursor(whereClause, whereArgs, cursor, side.ascending)
			page, err := q.selectPartitioned(ctx, pageRequest{
				tail: tail, ordered: ordered, whereClause: clause, whereArgs: args,
				ascending: side.ascending, limit: side.limit, from: pageFrom, to: pageTo, tokens: tokens,
			}, nil)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			response.Partial = response.Partial || page.partial
			rows := page.logs
			next := ""
			if len(rows) == side.limit && !page.partial {
				last := rows[len(rows)-1]
				next = logs_core.Cursor{Timestamp: last.Timestamp, ID: last.ID}.Encode()
			}
			if side.ascending {
				response.After, response.AfterCursor = rows, next
				return
			}
			slices.Reverse(rows)
			response.Before, response.BeforeCursor = rows, next
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("failed to read log context: %w", err)
	}
	return response, nil
}

// contextAnchor reads the anchor row itself: its fields give the scope's
// values. nil when it's gone.
func (q *LogQuerier) contextAnchor(
	ctx context.Context,
	projectID uuid.UUID,
	tail []segments.ResolvedFile,
	ordered []*segments.SegmentRecord,
	anchor logs_core.LogContextAnchorDTO,
) (*logs_core.LogItemDTO, error) {
	at := anchor.Timestamp.UTC()
	// The catalog's segment times are microsecond values (Postgres).
	window := searchWindow{From: at.Add(-time.Microsecond), To: at.Add(time.Microsecond), IncludeTo: true}
	found, err := q.selectMatching(ctx, tail, overlapping(ordered, window),
		"project_id = ? AND timestamp = "+nanoParam+" AND id = ?",
		[]any{projectID.String(), nanoTimestamp(at), anchor.ID},
		false, 1, 0, false, nil)
	if errors.Is(err, query.ErrNoData) || (err == nil && len(found) == 0) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the anchor log: %w", err)
	}
	return &found[0], nil
}

func contextRows(requested int) int {
	if requested <= 0 {
		return defaultContextRows
	}
	return min(requested, maxContextRows)
}

// andNode is a AND b, either of which may be nil.
func andNode(a, b *logs_core.QueryNode) *logs_core.QueryNode {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return &logs_core.QueryNode{
		Type:  logs_core.QueryNodeTypeLogical,
		Logic: &logs_core.LogicalNode{Operator: logs_core.LogicalOperatorAnd, Children: []logs_core.QueryNode{*a, *b}},
	}
}
