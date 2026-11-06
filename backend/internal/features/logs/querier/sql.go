package logs_querier

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/query"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	_ "github.com/marcboeker/go-duckdb/v2"
)

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
	startTime := time.Now()

	statement, err := segments.ValidateSingleStatementSelect(request.SQL)
	if err != nil {
		return nil, fmt.Errorf("invalid query: %w", err)
	}
	if err := segments.ParsesAsSingleSelect(q.db, statement); err != nil {
		return nil, fmt.Errorf("invalid query: %w", err)
	}

	from, to := resolveTimeRange(request.TimeRange)
	prepareCtx, cancelPrepare := context.WithTimeout(context.Background(), sqlPrepareTimeout)
	defer cancelPrepare()
	sandbox, err := q.openLogsSandbox(prepareCtx, projectID, from, to, "", request.ScanBudget)
	if err != nil {
		return nil, err
	}
	defer sandbox.close()

	ctx, cancel := context.WithTimeout(context.Background(), sqlQueryTimeout)
	defer cancel()

	rows, err := sandbox.db.QueryContext(ctx, statement)
	if err != nil {
		return nil, fmt.Errorf("query execution failed: %w", err)
	}
	defer func() { _ = rows.Close() }()

	columns, resultRows, truncated, err := scanSQLRows(rows)
	if err != nil {
		return nil, err
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
	prepareCtx, cancelPrepare := context.WithTimeout(context.Background(), sqlPrepareTimeout)
	defer cancelPrepare()
	sandbox, err := q.openLogsSandbox(prepareCtx, projectID, from, to, request.MessagePrefix, request.ScanBudget)
	if err != nil {
		return nil, err
	}
	defer sandbox.close()

	ctx, cancel := context.WithTimeout(context.Background(), sqlQueryTimeout)
	defer cancel()

	results := make([]*logs_core.SQLQueryResponseDTO, 0, len(request.Statements))
	for _, statement := range request.Statements {
		startTime := time.Now()
		rows, err := sandbox.db.QueryContext(ctx, statement.SQL, statement.Args...)
		if err != nil {
			return nil, fmt.Errorf("query execution failed: %w", err)
		}
		columns, resultRows, truncated, err := scanSQLRows(rows)
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

// logsSandbox is a single-use DuckDB handle holding the "logs" table.
type logsSandbox struct {
	db       *sql.DB
	spillDir string
}

func (s *logsSandbox) close() {
	_ = s.db.Close()
	_ = os.RemoveAll(s.spillDir)
}

// openLogsSandbox materializes the project's segments for [from, to] into a
// TEMP "logs" table on a fresh DuckDB handle with external access disabled and
// configuration locked. The table exists even when empty. scanBudget (0: none)
// refuses a larger range.
func (q *LogQuerier) openLogsSandbox(
	ctx context.Context, projectID uuid.UUID, from, to time.Time, messagePrefix string, scanBudget int64,
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

	sandboxDB, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, fmt.Errorf("failed to open sandboxed duckdb handle: %w", err)
	}
	// Before prepareLogsTable turns off external access (temp_directory
	// can't be set after that). Each sandbox spills to its own directory.
	spillDir, err := segments.ConfigureMemory(sandboxDB, q.memoryLimit, filepath.Join(q.queryDir, "spill"))
	if err != nil {
		_ = sandboxDB.Close()
		return nil, err
	}
	sandbox := &logsSandbox{db: sandboxDB, spillDir: spillDir}
	if err := q.prepareLogsTable(ctx, sandboxDB, records, projectID, from, to, messagePrefix); err != nil {
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
	messagePrefix string,
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
	if len(paths) > 0 {
		fromExpr, fileArgs := segments.SourceExpr(q.stream, paths)

		createTableSQL := "CREATE TEMP TABLE logs AS SELECT timestamp, level, message, client_ip, " +
			"trace_id, span_id, fields FROM " + fromExpr +
			" WHERE project_id = ? AND timestamp >= ? AND timestamp <= ?"
		createArgs := append(append([]any{}, fileArgs...), projectID.String(), from, to)
		if messagePrefix != "" {
			createTableSQL += " AND starts_with(message, ?)"
			createArgs = append(createArgs, messagePrefix)
		}

		if _, err := sandboxDB.ExecContext(ctx, createTableSQL, createArgs...); err != nil {
			return fmt.Errorf("failed to prepare logs table: %w", err)
		}
	} else {
		emptyTableSQL := "CREATE TEMP TABLE logs (" +
			"timestamp TIMESTAMP_NS, level VARCHAR, message VARCHAR, client_ip VARCHAR, " +
			"trace_id VARCHAR, span_id VARCHAR, fields VARCHAR)"
		if _, err := sandboxDB.ExecContext(ctx, emptyTableSQL); err != nil {
			return fmt.Errorf("failed to prepare empty logs table: %w", err)
		}
	}

	// lock_configuration last: it also blocks enable_external_access.
	for _, setting := range []string{"SET enable_external_access=false", "SET lock_configuration=true"} {
		if _, err := sandboxDB.ExecContext(ctx, setting); err != nil {
			return fmt.Errorf("failed to sandbox query connection: %w", err)
		}
	}

	return nil
}

func scanSQLRows(rows *sql.Rows) ([]string, [][]any, bool, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, nil, false, fmt.Errorf("failed to read result columns: %w", err)
	}

	resultRows := make([][]any, 0)
	truncated := false
	for rows.Next() {
		if len(resultRows) >= maxSQLResultRows {
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
