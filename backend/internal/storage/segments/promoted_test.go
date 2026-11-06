package segments_test

import (
	"testing"

	"obzly/internal/storage/segments"

	"github.com/stretchr/testify/require"
)

// A promoted field reads from its column alone only when every file of the
// fragment has it on every row; otherwise per row, by promoted_mask.
func Test_ResolvePromotedFields_ColumnOnlyWhenEveryFileHasIt(t *testing.T) {
	token := segments.PromotedToken("s2", 2, `$."it's"`)
	sqlText := "SELECT count(*) WHERE " + token + " = ?"
	full := func(masks ...int64) []*segments.SegmentRecord {
		records := make([]*segments.SegmentRecord, len(masks))
		for i, mask := range masks {
			records[i] = &segments.SegmentRecord{Stats: &segments.SegmentStats{PromotedFull: mask}}
		}
		return records
	}
	caseForm := "SELECT count(*) WHERE (CASE WHEN (promoted_mask & 2) <> 0 THEN promoted_s2 " +
		"ELSE json_extract_string(fields, '$.\"it''s\"') END) = ?"

	require.Equal(t, "SELECT count(*) WHERE promoted_s2 = ?", segments.ResolvePromotedFields(sqlText, full(3, 2), 0))
	require.Equal(t, caseForm, segments.ResolvePromotedFields(sqlText, full(3, 1), 0), "one file without it")
	require.Equal(t, caseForm, segments.ResolvePromotedFields(sqlText, full(3), 1), "tail files")
	require.Equal(t, caseForm, segments.ResolvePromotedFields(sqlText, []*segments.SegmentRecord{{}}, 0), "no stats")

	number := segments.ResolvePromotedFields(segments.PromotedToken("n1", 256, `$."ms"`), full(0), 0)
	require.Equal(
		t,
		`(CASE WHEN (promoted_mask & 256) <> 0 THEN promoted_n1 ELSE TRY_CAST(json_extract_string(fields, '$."ms"') AS DOUBLE) END)`,
		number,
	)
}
