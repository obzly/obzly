package query

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"obzly/internal/storage/segments"
	"obzly/internal/telemetry"
)

// LocalExecutor runs fragments in this process. It reads a segment from
// its catalog path, or downloads it from store into cacheDir (store may be
// nil on a local-disk-only install).
type LocalExecutor struct {
	id       string
	db       *sql.DB
	store    *segments.ObjectStore
	cacheDir string
}

// NewLocalExecutor runs fragments on db, which must have the segments
// normalization functions registered.
func NewLocalExecutor(id string, db *sql.DB, store *segments.ObjectStore, cacheDir string) *LocalExecutor {
	return &LocalExecutor{id: id, db: db, store: store, cacheDir: cacheDir}
}

func (e *LocalExecutor) ID() string {
	return e.id
}

// Execute writes the fragment's rows to resultPath, or returns ErrNoFiles
// when none of its files can be read (ResolveFiles skips a missing one).
func (e *LocalExecutor) Execute(ctx context.Context, fragment Fragment, resultPath string) error {
	files := append([]segments.ResolvedFile{}, fragment.LocalFiles...)
	candidates := segments.SkipByTokens(ctx, e.store, e.cacheDir, fragment.Segments, fragment.Tokens)
	telemetry.TokenFilterSkips.Add(float64(len(fragment.Segments) - len(candidates)))
	fragment.Segments = candidates
	// A large cold segment is fetched only for the columns the SQL reads.
	files = append(files, segments.ResolveFilesReading(ctx, e.store, e.cacheDir, fragment.Segments,
		segments.ReferencedColumns(fragment.Stream, fragment.SQL))...)
	// ResolveFiles skips a segment it couldn't fetch: past the deadline that
	// would silently be most of the range.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("fragment %s: %w", fragment.ID, err)
	}
	if len(files) == 0 {
		return ErrNoFiles
	}
	sqlText, args, err := Render(fragment, files)
	if err != nil {
		return err
	}
	copySQL := fmt.Sprintf("COPY (%s) TO '%s' (FORMAT PARQUET)", sqlText, strings.ReplaceAll(resultPath, "'", "''"))
	if _, err := e.db.ExecContext(ctx, copySQL, args...); err != nil {
		return fmt.Errorf("fragment %s: %w", fragment.ID, err)
	}
	return nil
}

// Render replaces the fragment's source placeholder with the read
// expression over files and returns the arguments in binding order: the
// fragment's arguments for the parameters before the source (e.g. a
// bucket width in the SELECT list), the source's, then the rest.
func Render(fragment Fragment, files []segments.ResolvedFile) (string, []any, error) {
	fragment.SQL = segments.ResolvePromotedFields(fragment.SQL, fragment.Segments, len(fragment.LocalFiles))
	at := strings.Index(fragment.SQL, SourcePlaceholder)
	if at < 0 {
		return "", nil, errors.New("fragment SQL has no source placeholder")
	}
	before := countParameters(fragment.SQL[:at])
	if before > len(fragment.Args) {
		return "", nil, errors.New("fragment SQL has more parameters than arguments")
	}
	sourceExpr, sourceArgs := segments.SourceExpr(fragment.Stream, files)
	sqlText := strings.Replace(fragment.SQL, SourcePlaceholder, sourceExpr, 1)
	args := append(append([]any{}, fragment.Args[:before]...), sourceArgs...)
	return sqlText, append(args, fragment.Args[before:]...), nil
}

// countParameters counts the ? placeholders in sqlText outside quoted
// strings and identifiers (a pattern regex literal holds ?s).
func countParameters(sqlText string) int {
	count := 0
	var quote rune
	for _, r := range sqlText {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
		case r == '?':
			count++
		}
	}
	return count
}
