package traces_querying

import (
	"testing"
	"time"

	spans_core "obzly/internal/features/spans/core"

	"github.com/stretchr/testify/require"
)

// The pickers' range: the last 31 days by default, never longer, counted
// back from to (from now without one, the range then staying open-ended).
func Test_LookupRange_DefaultsToAndClampsAt31Days(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	at := func(daysAgo int) *time.Time {
		moment := now.AddDate(0, 0, -daysAgo)
		return &moment
	}
	cases := []struct {
		name  string
		given *spans_core.TimeRangeDTO
		from  *time.Time
		to    *time.Time
	}{
		{"no range", nil, at(31), nil},
		{"empty range", &spans_core.TimeRangeDTO{}, at(31), nil},
		{"only from, older than the limit", &spans_core.TimeRangeDTO{From: at(400)}, at(31), nil},
		{"only from, inside the limit", &spans_core.TimeRangeDTO{From: at(2)}, at(2), nil},
		{"only to", &spans_core.TimeRangeDTO{To: at(10)}, at(41), at(10)},
		{"both, too long", &spans_core.TimeRangeDTO{From: at(100), To: at(10)}, at(41), at(10)},
		{"both, short", &spans_core.TimeRangeDTO{From: at(12), To: at(10)}, at(12), at(10)},
	}
	for _, c := range cases {
		got := lookupRange(c.given, now)

		require.True(t, c.from.Equal(*got.From), "%s: from %s", c.name, got.From)
		if c.to == nil {
			require.Nil(t, got.To, c.name)
			continue
		}
		require.True(t, c.to.Equal(*got.To), "%s: to %s", c.name, got.To)
	}
}
