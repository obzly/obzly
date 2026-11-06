package segments

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"obzly/internal/util/logger"
)

// Schema anchors: an empty Parquet file with a stream's full current schema,
// read with every query. union_by_name only yields columns some file has, so
// a query naming a newer column would fail on a window of older files.

var (
	anchorSchemas sync.Map // stream -> Schema
	anchorPaths   sync.Map // stream -> *anchorFile
)

type anchorFile struct {
	once sync.Once
	path string
}

// RegisterSchemaAnchor makes SourceExpr read schema's anchor with every
// query of schema.Stream.
func RegisterSchemaAnchor(schema Schema) {
	anchorSchemas.Store(schema.Stream, schema)
}

// anchorFor is stream's anchor file, written once per process; "" when
// none is registered or it can't be written (queries then work as before).
func anchorFor(stream string) string {
	value, ok := anchorSchemas.Load(stream)
	if !ok {
		return ""
	}
	schema, _ := value.(Schema)
	entry, _ := anchorPaths.LoadOrStore(stream, &anchorFile{})
	anchor, _ := entry.(*anchorFile)
	anchor.once.Do(func() {
		path, err := writeAnchor(schema)
		if err != nil {
			logger.GetLogger().Error("failed to write schema anchor", "stream", stream, "error", err)
			return
		}
		anchor.path = path
	})
	return anchor.path
}

func writeAnchor(schema Schema) (string, error) {
	columns := make([]string, len(schema.Columns))
	for i, column := range schema.Columns {
		columns[i] = fmt.Sprintf("CAST(NULL AS %s) AS %q", column.Type, column.Name)
	}
	sum := sha256.Sum256([]byte(strings.Join(columns, ",")))
	path := filepath.Join(os.TempDir(),
		fmt.Sprintf("obzly-anchor-%s-%s.parquet", schema.Stream, hex.EncodeToString(sum[:6])))
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return "", err
	}
	defer func() { _ = db.Close() }()
	temp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	query := fmt.Sprintf("COPY (SELECT %s LIMIT 0) TO '%s' (FORMAT PARQUET)",
		strings.Join(columns, ", "), strings.ReplaceAll(temp, "'", "''"))
	if _, err := db.Exec(query); err != nil {
		return "", err
	}
	return path, os.Rename(temp, path)
}
