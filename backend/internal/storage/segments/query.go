package segments

import "strings"

// ParquetFilesExpr builds a parameterized read_parquet([...]) expression over
// segment paths, binding each path rather than interpolating it (paths are
// not trusted input). union_by_name=true: without it DuckDB errors when a
// column is missing from any one file; with it older segments read NULL.
func ParquetFilesExpr(paths []string) (string, []any) {
	placeholders := make([]string, len(paths))
	args := make([]any, len(paths))
	for i, path := range paths {
		placeholders[i] = "?"
		args[i] = path
	}
	return "read_parquet([" + strings.Join(placeholders, ", ") + "], union_by_name=true)", args
}
