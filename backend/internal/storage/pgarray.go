package storage

import (
	"database/sql/driver"
	"strings"
	"time"
)

// TextArray is a Postgres array literal argument: GORM expands a plain Go
// slice into a comma-separated list, a driver.Valuer it passes as is.
type TextArray []string

func (a TextArray) Value() (driver.Value, error) {
	quoted := make([]string, len(a))
	for i, value := range a {
		quoted[i] = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
	}
	return "{" + strings.Join(quoted, ",") + "}", nil
}

// TimeArray is TextArray for timestamps (cast to timestamptz[] in SQL).
func TimeArray(times []time.Time) TextArray {
	formatted := make(TextArray, len(times))
	for i, t := range times {
		formatted[i] = t.UTC().Format(time.RFC3339Nano)
	}
	return formatted
}
