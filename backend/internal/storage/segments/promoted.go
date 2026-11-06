package segments

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// A promoted field in fragment SQL is a token each fragment resolves against
// its own files: the bare column where every row has it promoted
// (PromotedFull), so DuckDB skips the fields JSON and can push filters down;
// else a CASE on promoted_mask. Tokens carry no bind arguments.
var promotedToken = regexp.MustCompile(`\{\{promoted:(s|n)(\d+):(\d+):([A-Za-z0-9_=-]*)\}\}`)

// PromotedToken is the token for a promoted field's value: slot ("s1",
// "n2"), its mask bit, and the field's JSONPath in the fields column.
func PromotedToken(slot string, bit int64, jsonPath string) string {
	return fmt.Sprintf("{{promoted:%s:%d:%s}}", slot, bit, base64.URLEncoding.EncodeToString([]byte(jsonPath)))
}

// ResolvePromotedFields replaces sqlText's promoted-field tokens for a
// fragment reading records and localFiles more files (tail files, whose
// rows aren't known to all have a promotion).
func ResolvePromotedFields(sqlText string, records []*SegmentRecord, localFiles int) string {
	if !strings.Contains(sqlText, "{{promoted:") {
		return sqlText
	}
	full := int64(0)
	if localFiles == 0 && len(records) > 0 {
		full = -1
		for _, record := range records {
			if record.Stats == nil {
				full = 0
				break
			}
			full &= record.Stats.PromotedFull
		}
	}
	return promotedToken.ReplaceAllStringFunc(sqlText, func(token string) string {
		parts := promotedToken.FindStringSubmatch(token)
		column := "promoted_" + parts[1] + parts[2]
		bit, _ := strconv.ParseInt(parts[3], 10, 64)
		path, err := base64.URLEncoding.DecodeString(parts[4])
		if err != nil {
			return "NULL"
		}
		if full&bit == bit {
			return column
		}
		fromJSON := "json_extract_string(fields, " + sqlString(string(path)) + ")"
		if parts[1] == "n" {
			fromJSON = "TRY_CAST(" + fromJSON + " AS DOUBLE)"
		}
		return fmt.Sprintf("(CASE WHEN (promoted_mask & %d) <> 0 THEN %s ELSE %s END)", bit, column, fromJSON)
	})
}

func sqlString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
