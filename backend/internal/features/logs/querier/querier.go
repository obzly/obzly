// Package logs_querier is the logs stream's read path off the Parquet
// storage engine (see ARCHITECTURE.md). where_builder.go translates the
// storage-agnostic logs/core QueryNode AST to SQL.
package logs_querier

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/query"
	"obzly/internal/query/partcache"
	"obzly/internal/storage/segments"
	"obzly/internal/telemetry"
	"obzly/internal/util/logger"

	"github.com/google/uuid"
	_ "github.com/marcboeker/go-duckdb/v2"
)

const defaultLimit = 100

// maxLimit/maxOffset bound a request: limit sizes an allocation and offset
// an OFFSET scan, so unbounded values let any viewer force a DoS.
const (
	maxLimit  = 1000
	maxOffset = 1_000_000
)

type LogQuerier struct {
	db          *sql.DB
	segmentRepo *segments.SegmentRepository

	// objectStore/cacheDir: a segment missing from local disk (uploaded, then
	// reclaimed) is downloaded into cacheDir on demand. Unset: local disk only.
	objectStore *segments.ObjectStore
	cacheDir    string
	stream      string
	// coordinator runs searches as fragments over executors: remote querier
	// replicas when present, else in-process on db.
	coordinator *query.Coordinator
	queryDir    string
	memoryLimit string
	spillDir    string
	// tail adds rows still pending in JetStream (nil without the broker).
	tail segments.TailSource
	// partitionCache keeps time-bucketed aggregations per partition (nil: off).
	partitionCache *partcache.Cache
}

type QuerierOption func(*LogQuerier)

// WithStream reads another stream with the logs schema (e.g. "ai_logs").
func WithStream(stream string) QuerierOption {
	return func(q *LogQuerier) {
		q.stream = stream
	}
}

// WithObjectStore enables downloading segments from object storage into
// cacheDir when they're missing from local disk.
func WithObjectStore(store *segments.ObjectStore, cacheDir string) QuerierOption {
	return func(q *LogQuerier) {
		q.objectStore = store
		q.cacheDir = cacheDir
	}
}

// WithTail makes every query include rows still pending in JetStream.
func WithTail(tail segments.TailSource) QuerierOption {
	return func(q *LogQuerier) {
		q.tail = tail
	}
}

// WithMemoryLimit caps the querier's DuckDB and its SQL sandboxes (e.g.
// "1GB", or segments.HandleMemoryLimit), spilling to queryDir: an oversized
// query then spills or fails instead of the process being OOM-killed.
func WithMemoryLimit(limit string) QuerierOption {
	return func(q *LogQuerier) {
		q.memoryLimit = limit
	}
}

// WithQueryDir keeps query fragment results in dir.
func WithQueryDir(dir string) QuerierOption {
	return func(q *LogQuerier) {
		q.queryDir = dir
	}
}

func NewLogQuerier(opts ...QuerierOption) (*LogQuerier, error) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, fmt.Errorf("failed to open duckdb handle: %w", err)
	}
	if err := segments.RegisterNormalizationFunctions(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	querier := &LogQuerier{
		db: db, segmentRepo: &segments.SegmentRepository{}, stream: logs_core.StreamLogs,
		queryDir: filepath.Join(os.TempDir(), "obzly-query"),
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
	telemetry.RegisterDuckDB("querier_"+querier.stream, db)

	return querier, nil
}

func (q *LogQuerier) Close() error {
	telemetry.UnregisterDuckDB(q.db)
	err := q.db.Close()
	_ = os.RemoveAll(q.spillDir)
	return err
}

// ExecuteQuery mirrors LogCoreRepository.ExecuteQueryForProject: same
// QueryNode in, same LogItemDTO (client_ip also in Fields) and total out.
func (q *LogQuerier) ExecuteQuery(
	projectID uuid.UUID,
	request *logs_core.LogQueryRequestDTO,
) (*logs_core.LogQueryResponseDTO, error) {
	response, err := q.executeQuery(context.Background(), projectID, request, nil)
	if segmentVanished(err) {
		return q.executeQuery(context.Background(), projectID, request, nil)
	}
	return response, err
}

// ExecuteQueryStream is ExecuteQuery handing emit each search window's rows
// as they are read (streaming search). The returned response holds the
// whole page, as ExecuteQuery's does. No retry when a segment vanishes
// mid-search: rows already emitted would come again.
func (q *LogQuerier) ExecuteQueryStream(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.LogQueryRequestDTO,
	emit logs_core.SearchEmitter,
) (*logs_core.LogQueryResponseDTO, error) {
	return q.executeQuery(ctx, projectID, request, emit)
}

func (q *LogQuerier) executeQuery(
	ctx context.Context,
	projectID uuid.UUID,
	request *logs_core.LogQueryRequestDTO,
	emit logs_core.SearchEmitter,
) (*logs_core.LogQueryResponseDTO, error) {
	startTime := time.Now()

	limit := request.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	offset := request.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > maxOffset {
		offset = maxOffset
	}

	from, to := resolveTimeRange(request.TimeRange)
	records, err := q.segmentRepo.PlanSegments(projectID, q.stream, from, to, hintsFor(request.Query))
	if err != nil {
		return nil, fmt.Errorf("failed to list segments for project: %w", err)
	}

	empty := &logs_core.LogQueryResponseDTO{
		Logs:   []logs_core.LogItemDTO{},
		Limit:  limit,
		Offset: offset,
	}
	tail, ordered := q.searchInputs(context.Background(), projectID, records, from, to,
		!strings.EqualFold(request.SortOrder, "asc"))
	if len(tail) == 0 && len(ordered) == 0 {
		empty.ExecutedInMs = time.Since(startTime).String()
		return empty, nil
	}
	// Segment pruning is by file; a segment's time span can exceed the query
	// window, so rows are still filtered by timestamp.
	querySQL, queryArgs := newWhereBuilder(projectID).Build(request.Query)
	whereClause := "project_id = ? AND timestamp >= ? AND timestamp <= ?"
	whereArgs := append([]any{projectID.String(), from, to}, queryArgs...)
	if querySQL != "" {
		whereClause += " AND " + querySQL
	}

	ascending := strings.EqualFold(request.SortOrder, "asc")
	total := int64(logs_core.TotalNotCounted)
	countSkipped := false
	if request.Cursor == "" && (request.TrackTotal == nil || *request.TrackTotal) {
		total, err = q.countMatching(
			tail,
			ordered,
			from,
			to,
			querySQL == "",
			whereClause,
			whereArgs,
			request.ScanBudget,
			requiredTokens(request.Query),
		)
		if errors.Is(err, query.ErrNoData) {
			empty.ExecutedInMs = time.Since(startTime).String()
			return empty, nil
		}
		// Too much to count: the page is still worth returning.
		if errors.Is(err, query.ErrScanBudget) {
			total, countSkipped = logs_core.TotalNotCounted, true
		} else if err != nil {
			return nil, err
		}
	}

	pageClause, pageArgs := whereClause, whereArgs
	if request.Cursor != "" {
		cursor, err := logs_core.DecodeCursor(request.Cursor)
		if err != nil {
			return nil, err
		}
		pageClause, pageArgs = afterCursor(whereClause, whereArgs, cursor, ascending)
		offset = 0
	}

	pageFrom, pageTo := from, to
	if cursor := request.Cursor; cursor != "" {
		decoded, _ := logs_core.DecodeCursor(cursor)
		pageFrom, pageTo = cursorPageRange(pageFrom, pageTo, decoded, ascending)
	}
	page, err := q.selectPartitioned(ctx, pageRequest{
		tail: tail, ordered: ordered, whereClause: pageClause, whereArgs: pageArgs,
		ascending: ascending, limit: limit, offset: offset, timestampsOnly: request.TimestampsOnly,
		from: pageFrom, to: pageTo, tokens: requiredTokens(request.Query),
	}, emit)
	if err != nil {
		return nil, err
	}
	logItems := page.logs

	response := &logs_core.LogQueryResponseDTO{
		Logs:         logItems,
		Total:        total,
		Limit:        limit,
		Offset:       offset,
		ExecutedInMs: time.Since(startTime).String(),
		CountSkipped: countSkipped,
		Partial:      page.partial,
	}
	if page.partial {
		response.SearchedThrough = &page.searched
	}
	if len(logItems) == limit {
		last := logItems[len(logItems)-1]
		response.NextCursor = logs_core.Cursor{Timestamp: last.Timestamp, ID: last.ID}.Encode()
	}
	return response, nil
}

// afterCursor narrows clause to the rows past cursor in page order: older
// for a descending page, newer for an ascending one, with id breaking
// timestamp ties so a page never repeats or skips a row.
func afterCursor(clause string, args []any, cursor logs_core.Cursor, ascending bool) (string, []any) {
	operator := "<"
	if ascending {
		operator = ">"
	}
	at := nanoTimestamp(cursor.Timestamp)
	clause += " AND (timestamp " + operator + " " + nanoParam + " OR (timestamp = " + nanoParam + " AND id " +
		operator + " ?))"
	return clause, append(append([]any{}, args...), at, at, cursor.ID)
}

// nanoParam / nanoTimestamp bind a timestamp at nanosecond precision. A
// time.Time binds as a microsecond TIMESTAMP, which never equals a stored
// TIMESTAMP_NS with a sub-microsecond part, so cursors skipped or repeated rows.
const nanoParam = "CAST(? AS TIMESTAMP_NS)"

func nanoTimestamp(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05.999999999")
}

// cursorPageRange narrows [from, to] to the cursor's side, rounded out to
// whole microseconds (window bounds and catalog times are microsecond values).
// afterCursor is the exact filter.
func cursorPageRange(from, to time.Time, cursor logs_core.Cursor, ascending bool) (time.Time, time.Time) {
	floor := cursor.Timestamp.Truncate(time.Microsecond)
	if ascending {
		return maxTime(from, floor), to
	}
	ceil := floor
	if !ceil.Equal(cursor.Timestamp) {
		ceil = ceil.Add(time.Microsecond)
	}
	return from, minTime(to, ceil)
}

// Field discovery reads only the project's newest segments, capped by rows
// and files: reading the whole history made the Logs sidebar never finish on
// busy projects. Recent segments hold every attribute currently sent.
const (
	fieldDiscoveryMaxSegments = 20
	fieldDiscoveryRowBudget   = 500_000
)

// DiscoverFields returns unique custom field names in the project's newest
// logs, mirroring LogCoreRepository.DiscoverFields.
func (q *LogQuerier) DiscoverFields(projectID uuid.UUID) ([]string, error) {
	newest, err := q.segmentRepo.ListNewest(projectID, q.stream, fieldDiscoveryMaxSegments)
	if err != nil {
		return nil, fmt.Errorf("failed to list segments for project: %w", err)
	}
	var records []*segments.SegmentRecord
	var rows int64
	for _, record := range newest {
		records = append(records, record)
		rows += record.RowCount
		if rows >= fieldDiscoveryRowBudget {
			break
		}
	}
	tail, ordered := q.searchInputs(context.Background(), projectID, records, time.Time{}, maxRepresentableTime, false)
	if len(tail) == 0 && len(ordered) == 0 {
		return []string{}, nil
	}
	names := "SELECT DISTINCT unnest(json_keys(fields)) AS name FROM " + query.SourcePlaceholder +
		" WHERE project_id = ? AND fields <> ''"
	discovered := []string{}
	err = q.coordinator.Query(context.Background(), query.Request{
		Stream:       q.stream,
		Segments:     ordered,
		LocalFiles:   tail,
		FragmentSQL:  names,
		FragmentArgs: []any{projectID.String()},
		FinalSQL:     "SELECT DISTINCT name FROM " + query.ResultsPlaceholder + " ORDER BY name",
		SingleSQL:    names + " ORDER BY name",
		SingleArgs:   []any{projectID.String()},
	}, func(rows *sql.Rows) error {
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return fmt.Errorf("failed to scan field name: %w", err)
			}
			discovered = append(discovered, name)
		}
		return nil
	})
	if errors.Is(err, query.ErrNoData) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to run field discovery query: %w", err)
	}
	return discovered, nil
}

// countMatching counts the rows whereClause matches. Without a search filter,
// a segment wholly inside [from, to] counts from its catalog row count; only
// boundary segments and the tail are read. Catalog counts include only
// readable segments, since a query skips missing files.
func (q *LogQuerier) countMatching(
	tail []segments.ResolvedFile,
	ordered []*segments.SegmentRecord,
	from, to time.Time,
	unfiltered bool,
	whereClause string,
	whereArgs []any,
	scanBudget int64,
	tokens []string,
) (int64, error) {
	var catalogCount int64
	toRead := ordered
	if unfiltered {
		toRead = nil
		for _, record := range ordered {
			if record.RowCount > 0 && !record.MinTime.Before(from) && !record.MaxTime.After(to) && q.readable(record) {
				catalogCount += record.RowCount
			} else {
				toRead = append(toRead, record)
			}
		}
		if len(toRead) == 0 && len(tail) == 0 {
			return catalogCount, nil
		}
	}

	var total int64
	err := q.coordinator.Query(context.Background(), query.Request{
		Stream:       q.stream,
		Segments:     toRead,
		LocalFiles:   tail,
		FragmentSQL:  "SELECT count(*) AS n FROM " + query.SourcePlaceholder + " WHERE " + whereClause,
		FragmentArgs: whereArgs,
		FinalSQL:     "SELECT coalesce(sum(n), 0)::BIGINT FROM " + query.ResultsPlaceholder,
		Tokens:       tokens,
		ScanBudget:   scanBudget,
	}, func(rows *sql.Rows) error {
		if !rows.Next() {
			return errors.New("no count row")
		}
		return rows.Scan(&total)
	})
	if errors.Is(err, query.ErrNoData) && catalogCount > 0 {
		return catalogCount, nil
	}
	if err != nil {
		return 0, fmt.Errorf("failed to count matching logs: %w", err)
	}
	return catalogCount + total, nil
}

// singleFragmentOffset: a deeper page runs as one fragment, since every
// fragment would otherwise return offset+limit rows to merge.
const singleFragmentOffset = 10_000

func (q *LogQuerier) readable(record *segments.SegmentRecord) bool {
	if q.objectStore != nil && record.ObjectKey != "" {
		return true
	}
	_, err := os.Stat(record.Path)
	return err == nil
}

// pageRequest is one page of a search: its inputs, filter and bounds.
type pageRequest struct {
	tail           []segments.ResolvedFile
	ordered        []*segments.SegmentRecord
	whereClause    string
	whereArgs      []any
	ascending      bool
	limit, offset  int
	timestampsOnly bool
	from, to       time.Time
	tokens         []string
}

// searchDeadline bounds a whole page search, all its windows: past it the
// search returns what it found and how far back it got (Partial) instead of
// a rare term walking a week window by window for many minutes.
var searchDeadline = 170 * time.Second

// pageResult is a page and how much of its range was searched.
type pageResult struct {
	logs []logs_core.LogItemDTO
	// partial: searchDeadline passed; only [searched, to] (or [from,
	// searched] ascending) was read.
	partial  bool
	searched time.Time
}

// selectPartitioned reads a page window by window and stops at offset+limit
// rows or searchDeadline. Tail files go into every window (late rows of any
// age). A deep offset reads the whole range at once to avoid holding skipped rows.
func (q *LogQuerier) selectPartitioned(
	ctx context.Context,
	page pageRequest,
	emit logs_core.SearchEmitter,
) (pageResult, error) {
	deadlineCtx, cancel := context.WithTimeout(ctx, searchDeadline)
	defer cancel()
	if page.offset > singleFragmentOffset {
		logItems, err := q.selectMatching(deadlineCtx, page.tail, page.ordered, page.whereClause, page.whereArgs,
			page.ascending, page.limit, page.offset, page.timestampsOnly, page.tokens)
		if errors.Is(err, query.ErrNoData) {
			logItems = []logs_core.LogItemDTO{}
		} else if err != nil {
			return pageResult{}, err
		}
		if emit != nil {
			if err := emit(logItems, logs_core.SearchWindowDTO{From: page.from, To: page.to}); err != nil {
				return pageResult{}, err
			}
		}
		return pageResult{logs: logItems}, nil
	}

	needed := page.offset + page.limit
	rows := make([]logs_core.LogItemDTO, 0, needed)
	result := pageResult{searched: page.to}
	if page.ascending {
		result.searched = page.from
	}
	timedOut := func() bool {
		return errors.Is(deadlineCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
	}
	for _, window := range searchWindows(page.from, page.to, page.ordered, time.Now().UTC(), !page.ascending) {
		if timedOut() {
			result.partial = true
			break
		}
		if err := ctx.Err(); err != nil {
			return pageResult{}, err
		}
		inWindow := overlapping(page.ordered, window)
		before := len(rows)
		if len(inWindow) > 0 || len(page.tail) > 0 {
			upper := " < ?"
			if window.IncludeTo {
				upper = " <= ?"
			}
			clause := page.whereClause + " AND timestamp >= ? AND timestamp" + upper
			args := append(append([]any{}, page.whereArgs...), window.From, window.To)
			found, err := q.selectMatching(deadlineCtx, page.tail, inWindow, clause, args,
				page.ascending, needed-len(rows), 0, page.timestampsOnly, page.tokens)
			if timedOut() {
				result.partial = true
				break
			}
			if err != nil && !errors.Is(err, query.ErrNoData) {
				return pageResult{}, err
			}
			rows = append(rows, found...)
		}
		result.searched = window.From
		if page.ascending {
			result.searched = window.To
		}
		telemetry.SearchWindows.WithLabelValues(q.stream).Inc()
		if emit != nil {
			added := rows[min(max(before, page.offset), len(rows)):]
			if err := emit(added, logs_core.SearchWindowDTO{From: window.From, To: window.To}); err != nil {
				return pageResult{}, err
			}
		}
		if len(rows) >= needed {
			break
		}
	}
	result.logs = []logs_core.LogItemDTO{}
	if page.offset < len(rows) {
		result.logs = rows[page.offset:min(needed, len(rows))]
	}
	return result, nil
}

// selectMatching reads one page in (timestamp, id) order: id breaks
// timestamp ties, so a cursor page never repeats or skips a row.
func (q *LogQuerier) selectMatching(
	ctx context.Context,
	tail []segments.ResolvedFile,
	ordered []*segments.SegmentRecord,
	whereClause string,
	whereArgs []any,
	ascending bool,
	limit, offset int,
	timestampsOnly bool,
	tokens []string,
) ([]logs_core.LogItemDTO, error) {
	direction := "DESC"
	if ascending {
		direction = "ASC"
	}
	orderBy := " ORDER BY timestamp " + direction + ", id " + direction

	logItems := make([]logs_core.LogItemDTO, 0, limit)
	selected := "id, project_id, timestamp, level, message, client_ip, trace_id, span_id, fields, pattern_hash"
	if timestampsOnly {
		selected = "id, project_id, timestamp, '' AS level, '' AS message, '' AS client_ip, " +
			"NULL::VARCHAR AS trace_id, NULL::VARCHAR AS span_id, '' AS fields, NULL::VARCHAR AS pattern_hash"
	}
	columns := "SELECT " + selected + " FROM " + query.SourcePlaceholder + " WHERE " + whereClause + orderBy
	err := q.coordinator.Query(ctx, query.Request{
		Stream:         q.stream,
		Segments:       ordered,
		LocalFiles:     tail,
		FragmentSQL:    columns + " LIMIT ?",
		FragmentArgs:   append(append([]any{}, whereArgs...), limit+offset),
		FinalSQL:       "SELECT * FROM " + query.ResultsPlaceholder + orderBy + " LIMIT ? OFFSET ?",
		FinalArgs:      []any{limit, offset},
		SingleSQL:      columns + " LIMIT ? OFFSET ?",
		SingleArgs:     append(append([]any{}, whereArgs...), limit, offset),
		SingleFragment: offset > singleFragmentOffset,
		Tokens:         tokens,
	}, func(rows *sql.Rows) error {
		for rows.Next() {
			item, err := scanLogItem(rows)
			if err != nil {
				return fmt.Errorf("failed to scan log row: %w", err)
			}
			logItems = append(logItems, item)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to execute query: %w", err)
	}
	return logItems, nil
}

func scanLogItem(rows *sql.Rows) (logs_core.LogItemDTO, error) {
	var id, projectID, level, message, clientIP, fieldsJSON string
	var timestamp time.Time
	// Nullable: older segments lack trace_id/span_id (union_by_name gives NULL).
	var traceID, spanID, patternHash sql.NullString

	if err := rows.Scan(
		&id, &projectID, &timestamp, &level, &message, &clientIP, &traceID, &spanID, &fieldsJSON, &patternHash,
	); err != nil {
		return logs_core.LogItemDTO{}, err
	}

	item := logs_core.LogItemDTO{
		ID:        id,
		Timestamp: timestamp,
		Level:     level,
		Message:   message,
		ClientIP:  clientIP,
		TraceID:   traceID.String,
		SpanID:    spanID.String,
		// Stored at ingest, else computed with Live Tail's function, so it always
		// matches logs_core.PatternHashSQL (Test_PatternTemplate_AgreesWithDuckDB).
		PatternHash: patternHash.String,
	}
	if item.PatternHash == "" {
		item.PatternHash = logs_core.PatternHash(message)
	}

	// client_ip is duplicated into Fields, matching ExecuteQueryForProject.
	fields := map[string]any{}
	if clientIP != "" {
		fields["client_ip"] = clientIP
	}
	if fieldsJSON != "" && fieldsJSON != "{}" {
		var custom map[string]any
		if err := json.Unmarshal([]byte(fieldsJSON), &custom); err == nil {
			for fieldName, value := range custom {
				fields[fieldName] = value
			}
		}
	}
	if len(fields) > 0 {
		item.Fields = fields
	}

	return item, nil
}

// noUpperBound stands in for an unset To: ingest accepts timestamps up to 5
// minutes in the future, so time.Now() would drop valid logs.
// Year 2200 because TIMESTAMP_NS (int64 ns) only spans ~1677-2262; year 9999
// fails to bind.
var noUpperBound = time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)

// minRepresentableTime/maxRepresentableTime clamp client timestamps into
// TIMESTAMP_NS range: out-of-range values failed in DuckDB as a 500. Clamped,
// not rejected, so a zero time.Time still means "unbounded"; kept a little
// inside the limits to leave room for bucket widening.
var (
	minRepresentableTime = time.Date(1678, 1, 1, 0, 0, 0, 0, time.UTC)
	maxRepresentableTime = time.Date(2261, 1, 1, 0, 0, 0, 0, time.UTC)
)

func resolveTimeRange(timeRange *logs_core.TimeRangeDTO) (time.Time, time.Time) {
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

// searchInputs returns a search's pending tail files and its segments in read
// order. Newest-first searches list segments by newest row: DuckDB scans files
// roughly in list order and skips row groups older than its top-N, so an old
// multi-day segment listed first forced a full scan.
func (q *LogQuerier) searchInputs(
	ctx context.Context,
	projectID uuid.UUID,
	records []*segments.SegmentRecord,
	from, to time.Time,
	newestFirst bool,
) ([]segments.ResolvedFile, []*segments.SegmentRecord) {
	tail := segments.AppendTail(ctx, q.tail, projectID, q.stream, records, from, to, nil)
	if !newestFirst {
		return tail, records
	}
	newest := slices.Clone(records)
	slices.SortStableFunc(newest, func(a, b *segments.SegmentRecord) int { return b.MaxTime.Compare(a.MaxTime) })
	return tail, newest
}

// queryFiles resolves records to readable files (segments.ResolveFiles)
// and adds the pending tail when it overlaps [from, to].
func (q *LogQuerier) queryFiles(
	ctx context.Context,
	projectID uuid.UUID,
	records []*segments.SegmentRecord,
	from, to time.Time,
) []segments.ResolvedFile {
	files := segments.ResolveFiles(ctx, q.objectStore, q.cacheDir, records)
	return segments.AppendTail(ctx, q.tail, projectID, q.stream, records, from, to, files)
}

// segmentVanished: compaction deleted a planned segment mid-query; replanning
// from the catalog reads the merged file.
func segmentVanished(err error) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	// Gone before DuckDB bound the query, or while it ran.
	return strings.Contains(text, "No files found that match the pattern") ||
		(strings.Contains(text, "Cannot open file") && strings.Contains(text, "No such file or directory"))
}
