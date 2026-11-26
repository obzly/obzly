package logs_querier

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/query"
	"obzly/internal/storage/segments"
	"obzly/internal/telemetry"

	"github.com/google/uuid"
	duckdb "github.com/marcboeker/go-duckdb/v2"
)

// userSQLErrorTypes: DuckDB errors that come from the user's statement, not
// from us.
var userSQLErrorTypes = map[duckdb.ErrorType]bool{
	duckdb.ErrorTypeBinder: true, duckdb.ErrorTypeParser: true, duckdb.ErrorTypeSyntax: true,
	duckdb.ErrorTypeCatalog: true, duckdb.ErrorTypeConversion: true, duckdb.ErrorTypeInvalidInput: true,
	duckdb.ErrorTypeOutOfRange: true, duckdb.ErrorTypeMismatchType: true, duckdb.ErrorTypeDivideByZero: true,
	duckdb.ErrorTypeInvalidType: true, duckdb.ErrorTypeNotImplemented: true, duckdb.ErrorTypePermission: true,
	duckdb.ErrorTypeParameterNotResolved: true, duckdb.ErrorTypeParameterNotAllowed: true,
}

// maxSQLResultRows caps rows read and returned by ExecuteSQLQuery: a guard
// against a query without its own LIMIT. SQL mode is not for bulk export.
const maxSQLResultRows = 10_000

// sqlQueryTimeout bounds one user-submitted (untrusted) SQL statement.
const sqlQueryTimeout = 15 * time.Second

// sqlPrepareTimeout bounds materializing the "logs" table, which
// sqlQueryTimeout doesn't cover. Under the control plane's 180 s proxy timeout.
const sqlPrepareTimeout = 120 * time.Second

// ExecuteSQLQuery answers the logs page's SQL mode: it materializes the
// project's segments for the time range into a TEMP "logs" table on a fresh,
// single-use DuckDB handle (never q.db) and runs the user's SQL against it.
// Security layers:
//  1. segments.ValidateSingleStatementSelect (single SELECT, keyword
//     blocklist), confirmed by DuckDB's parser (segments.ParsesAsSingleSelect).
//  2. The SQL sees only the TEMP table, never file paths.
//  3. Timeouts (sqlQueryTimeout, sqlPrepareTimeout), row cap, scan budget,
//     memory limit and spill cap (segments.ConfigureMemory).
//  4. SET enable_external_access=false (blocks read_parquet/read_csv, ATTACH,
//     COPY TO), then lock_configuration=true so limits can't be changed.
func (q *LogQuerier) ExecuteSQLQuery(
	projectID uuid.UUID,
	request *logs_core.ExecuteSQLQueryRequestDTO,
) (*logs_core.SQLQueryResponseDTO, error) {
	return q.ExecuteSQLQueryContext(context.Background(), projectID, request)
}

// ExecuteSQLQueryContext is ExecuteSQLQuery stopping when ctx ends (the
// client left), on top of its own timeouts.
func (q *LogQuerier) ExecuteSQLQueryContext(
	parent context.Context,
	projectID uuid.UUID,
	request *logs_core.ExecuteSQLQueryRequestDTO,
) (*logs_core.SQLQueryResponseDTO, error) {
	startTime := time.Now()

	statement, err := segments.ValidateSingleStatementSelect(request.SQL)
	if err != nil {
		return nil, fmt.Errorf("invalid query: %w", err)
	}
	if err := segments.ParsesAsSingleSelect(q.db, statement); err != nil {
		return nil, fmt.Errorf("invalid query: %w", err)
	}

	from, to := resolveTimeRange(request.TimeRange)
	prepareCtx, cancelPrepare := context.WithTimeout(parent, sqlPrepareTimeout)
	defer cancelPrepare()
	sandbox, err := q.openLogsSandbox(prepareCtx, projectID, from, to, tableShape{}, request.ScanBudget)
	if err != nil {
		return nil, outOfMemoryError(err)
	}
	defer sandbox.close()

	ctx, cancel := context.WithTimeout(parent, sqlQueryTimeout)
	defer cancel()

	rows, err := sandbox.db.QueryContext(ctx, statement)
	if err != nil {
		return nil, userSQLError(err)
	}
	defer func() { _ = rows.Close() }()

	columns, resultRows, truncated, err := scanSQLRows(rows, maxSQLResultRows)
	if err != nil {
		return nil, userSQLError(err)
	}

	return &logs_core.SQLQueryResponseDTO{
		Columns:      columns,
		Rows:         resultRows,
		RowCount:     len(resultRows),
		Truncated:    truncated,
		ExecutedInMs: time.Since(startTime).String(),
	}, nil
}

// ExecuteScopedSQL runs trusted, server-built statements (never user SQL)
// against one "logs" table, so several aggregates share one segment scan.
// MessagePrefix narrows what gets materialized to the events they read.
func (q *LogQuerier) ExecuteScopedSQL(
	projectID uuid.UUID,
	request *logs_core.ScopedSQLRequestDTO,
) ([]*logs_core.SQLQueryResponseDTO, error) {
	from := clampToRepresentableRange(request.From)
	to := clampToRepresentableRange(request.To)
	parent := context.Background()
	if !request.Deadline.IsZero() {
		var cancelDeadline context.CancelFunc
		parent, cancelDeadline = context.WithDeadline(parent, request.Deadline)
		defer cancelDeadline()
	}
	prepareCtx, cancelPrepare := context.WithTimeout(parent, sqlPrepareTimeout)
	defer cancelPrepare()
	shape := tableShape{messagePrefix: request.MessagePrefix, table: request.Table, tableArgs: request.TableArgs,
		tableFilter: request.TableFilter, rawColumns: request.RawColumns, rawFilter: request.RawFilter,
		memoryLimit: request.MemoryLimit, threads: request.Threads}
	if err := shape.validate(); err != nil {
		return nil, err
	}
	sandbox, err := q.openLogsSandbox(prepareCtx, projectID, from, to, shape, request.ScanBudget)
	if err != nil {
		return nil, err
	}
	defer sandbox.close()

	timeout := sqlQueryTimeout
	if request.Timeout > 0 {
		timeout = request.Timeout
	}
	maxRows := maxSQLResultRows
	if request.MaxRows > 0 {
		maxRows = request.MaxRows
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	results := make([]*logs_core.SQLQueryResponseDTO, 0, len(request.Statements))
	for _, statement := range request.Statements {
		startTime := time.Now()
		rows, err := sandbox.db.QueryContext(ctx, statement.SQL, statement.Args...)
		if err != nil {
			return nil, fmt.Errorf("query execution failed: %w", err)
		}
		columns, resultRows, truncated, err := scanSQLRows(rows, maxRows)
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
		results = append(results, &logs_core.SQLQueryResponseDTO{
			Columns:      columns,
			Rows:         resultRows,
			RowCount:     len(resultRows),
			Truncated:    truncated,
			ExecutedInMs: time.Since(startTime).String(),
		})
	}

	return results, nil
}

// tableShape narrows the materialized "logs" table: rows whose message
// starts with messagePrefix, or table, a trusted SELECT over "raw" (only
// rawColumns, rows rawFilter holds for), then rows tableFilter holds for.
type tableShape struct {
	messagePrefix string
	table         string
	tableArgs     []any
	tableFilter   string
	rawColumns    []string
	rawFilter     string
	// memoryLimit (bytes) and threads replace the querier's caps when set.
	memoryLimit int64
	threads     int
}

// rawColumn is a column of "raw" (the segment scan) and its type.
type rawColumn struct{ name, sqlType string }

// rawColumnTypes are raw's columns, in order.
var rawColumnTypes = []rawColumn{
	{"timestamp", "TIMESTAMP_NS"},
	{"level", "VARCHAR"},
	{"message", "VARCHAR"},
	{"client_ip", "VARCHAR"},
	{"trace_id", "VARCHAR"},
	{"span_id", "VARCHAR"},
	{"fields", "VARCHAR"},
}

func (s tableShape) validate() error {
	if s.table == "" && (s.tableFilter != "" || s.rawFilter != "" || len(s.rawColumns) > 0) {
		return errors.New("a table filter, raw columns or raw filter needs a table")
	}
	for _, column := range s.rawColumns {
		if !slices.ContainsFunc(rawColumnTypes, func(c rawColumn) bool { return c.name == column }) {
			return fmt.Errorf("unknown raw column %q", column)
		}
	}
	return nil
}

// columns are the raw columns the scan reads, with their types.
func (s tableShape) columns() []rawColumn {
	if len(s.rawColumns) == 0 {
		return rawColumnTypes
	}
	var columns []rawColumn
	for _, column := range rawColumnTypes {
		if slices.Contains(s.rawColumns, column.name) {
			columns = append(columns, column)
		}
	}
	return columns
}

// logsSandbox is a single-use DuckDB handle holding the "logs" table.
type logsSandbox struct {
	db       *sql.DB
	spillDir string
	// release returns the sandbox's memory to the gate.
	release func()
}

func (s *logsSandbox) close() {
	telemetry.UnregisterDuckDB(s.db)
	_ = s.db.Close()
	_ = os.RemoveAll(s.spillDir)
	s.release()
}

// openLogsSandbox materializes the project's segments for [from, to] into a
// TEMP "logs" table on a fresh DuckDB handle with external access disabled and
// configuration locked. The table exists even when empty. scanBudget (0: none)
// refuses a larger range.
func (q *LogQuerier) openLogsSandbox(
	ctx context.Context, projectID uuid.UUID, from, to time.Time, shape tableShape, scanBudget int64,
) (*logsSandbox, error) {
	records, err := q.segmentRepo.ListByTimeRange(projectID, q.stream, from, to)
	if err != nil {
		return nil, fmt.Errorf("failed to list segments for project: %w", err)
	}
	var planned int64
	for _, record := range records {
		planned += record.SizeBytes
	}
	if scanBudget > 0 && planned > scanBudget {
		return nil, &query.ScanBudgetError{Planned: planned, Budget: scanBudget}
	}

	limit, threads := q.memoryLimit, q.threads
	if shape.memoryLimit > 0 {
		limit = fmt.Sprintf("%dMB", shape.memoryLimit/1_000_000)
	}
	if shape.threads > 0 {
		threads = shape.threads
	}
	// Each sandbox is a new handle with its own cap: the gate gives it what
	// is free (it spills past that), so a few at once can't OOM the replica.
	want := segments.MemoryLimitBytes(limit)
	admitted, release, err := segments.QueryMemoryGate().Admit(ctx, want)
	if err != nil {
		return nil, err
	}
	if admitted < want {
		limit = fmt.Sprintf("%dMB", admitted/1_000_000)
	}

	sandboxDB, err := sql.Open("duckdb", "")
	if err != nil {
		release()
		return nil, fmt.Errorf("failed to open sandboxed duckdb handle: %w", err)
	}
	// TEMP tables and SET are per connection: one keeps every step on it.
	sandboxDB.SetMaxOpenConns(1)
	// Before prepareLogsTable turns off external access (temp_directory
	// can't be set after that). Each sandbox spills to its own directory.
	spillDir, err := segments.ConfigureMemory(sandboxDB, limit, filepath.Join(q.queryDir, "spill"))
	if err == nil {
		err = segments.SetHandleLimits(sandboxDB, "", threads)
	}
	if err != nil {
		_ = sandboxDB.Close()
		if spillDir != "" {
			_ = os.RemoveAll(spillDir)
		}
		release()
		return nil, err
	}
	telemetry.RegisterDuckDB(segments.SandboxHandleKind, sandboxDB, segments.MemoryLimitBytes(limit))
	sandbox := &logsSandbox{db: sandboxDB, spillDir: spillDir, release: release}
	if err := q.prepareLogsTable(ctx, sandboxDB, records, projectID, from, to, shape); err != nil {
		sandbox.close()
		return nil, err
	}

	return sandbox, nil
}

func (q *LogQuerier) prepareLogsTable(
	ctx context.Context,
	sandboxDB *sql.DB,
	records []*segments.SegmentRecord,
	projectID uuid.UUID,
	from, to time.Time,
	shape tableShape,
) error {
	if err := segments.RegisterNormalizationFunctions(sandboxDB); err != nil {
		return fmt.Errorf("failed to prepare sandboxed duckdb handle: %w", err)
	}

	paths := q.queryFiles(ctx, projectID, records, from, to)
	// ResolveFiles skips a segment it couldn't fetch: past the deadline
	// that would silently be most of the range.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("failed to prepare logs table: %w", err)
	}
	var empty, names []string
	for _, column := range shape.columns() {
		empty = append(empty, "CAST(NULL AS "+column.sqlType+") AS "+column.name)
		names = append(names, column.name)
	}
	rawSQL := "SELECT " + strings.Join(empty, ", ") + " WHERE false"
	var rawArgs []any
	if len(paths) > 0 {
		fromExpr, fileArgs := segments.SourceExpr(q.stream, paths)
		rawSQL = "SELECT " + strings.Join(names, ", ") + " FROM " + fromExpr +
			" WHERE project_id = ? AND timestamp >= ? AND timestamp <= ?"
		rawArgs = append(append([]any{}, fileArgs...), projectID.String(), from, to)
		if shape.messagePrefix != "" {
			rawSQL += " AND starts_with(message, ?)"
			rawArgs = append(rawArgs, shape.messagePrefix)
		}
		if shape.rawFilter != "" {
			rawSQL += " AND (" + shape.rawFilter + ")"
		}
	}
	if shape.table == "" {
		if _, err := sandboxDB.ExecContext(ctx, "CREATE TEMP TABLE logs AS "+rawSQL, rawArgs...); err != nil {
			return fmt.Errorf("failed to prepare logs table: %w", err)
		}
	} else if err := createShapedTable(ctx, sandboxDB, rawSQL, rawArgs, shape); err != nil {
		return fmt.Errorf("failed to prepare logs table: %w", err)
	}

	// lock_configuration last: it also blocks enable_external_access.
	for _, setting := range []string{"SET enable_external_access=false", "SET lock_configuration=true"} {
		if _, err := sandboxDB.ExecContext(ctx, setting); err != nil {
			return fmt.Errorf("failed to sandbox query connection: %w", err)
		}
	}

	return nil
}

// createShapedTable materializes shape.table straight from the segment
// scan in one statement, so neither the raw rows nor columns the shape
// doesn't read are ever held: memory and spill follow the shaped rows.
// shape.rawFilter narrows the scan itself. shape.table has no WHERE (in one
// statement DuckDB pushed the shape's final WHERE down through its
// projections, inlining every column it reads: Web analytics' filter became
// 432 copies of an 87-path json_extract_string per row, past 4 GB for one
// thread), so shape.tableFilter is applied by deleting the rows it rejects.
func createShapedTable(ctx context.Context, sandboxDB *sql.DB, rawSQL string, rawArgs []any, shape tableShape) error {
	create := "CREATE TEMP TABLE logs AS WITH raw AS NOT MATERIALIZED (" + rawSQL + ") " + shape.table
	args := append(append([]any{}, rawArgs...), shape.tableArgs...)
	if _, err := sandboxDB.ExecContext(ctx, create, args...); err != nil {
		return err
	}
	if shape.tableFilter == "" {
		return nil
	}
	_, err := sandboxDB.ExecContext(ctx, "DELETE FROM logs WHERE NOT coalesce(("+shape.tableFilter+"), false)")
	return err
}

func scanSQLRows(rows *sql.Rows, maxRows int) ([]string, [][]any, bool, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, nil, false, fmt.Errorf("failed to read result columns: %w", err)
	}

	resultRows := make([][]any, 0)
	truncated := false
	for rows.Next() {
		if len(resultRows) >= maxRows {
			truncated = true
			break
		}

		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, nil, false, fmt.Errorf("failed to scan result row: %w", err)
		}

		for i, v := range values {
			values[i] = jsonSafeValue(v)
		}
		resultRows = append(resultRows, values)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, false, fmt.Errorf("row iteration error: %w", err)
	}

	return columns, resultRows, truncated, nil
}

// jsonSafeValue converts a DuckDB driver value for encoding/json: time.Time as
// RFC3339Nano, []byte as a string.
func jsonSafeValue(v any) any {
	switch typed := v.(type) {
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano)
	case []byte:
		return string(typed)
	default:
		return v
	}
}

// userSQLError tells a mistake in the user's SQL ("invalid query", 400) and a
// query that ran out of memory (422) from a failure of ours (500): a typo
// answered 500 "Failed to execute query".
func userSQLError(err error) error {
	var duckErr *duckdb.Error
	if errors.As(err, &duckErr) && userSQLErrorTypes[duckErr.Type] {
		return fmt.Errorf("invalid query: %s", duckErr.Msg)
	}
	if wrapped := outOfMemoryError(err); wrapped != err {
		return wrapped
	}
	return fmt.Errorf("query execution failed: %w", err)
}

// outOfMemoryError marks err as logs_core.ErrSQLOutOfMemory when DuckDB ran
// out of the sandbox's memory, spilling included.
func outOfMemoryError(err error) error {
	var duckErr *duckdb.Error
	if errors.As(err, &duckErr) && duckErr.Type == duckdb.ErrorTypeOutOfMemory {
		return fmt.Errorf("%w: %w", logs_core.ErrSQLOutOfMemory, err)
	}
	return err
}
