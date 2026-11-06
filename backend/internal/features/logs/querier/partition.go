package logs_querier

import (
	"time"

	"obzly/internal/storage/segments"
)

// A search reads its range in windows from the newest end (oldest when
// ascending) and stops once the page is full, so most of a 7-day range is
// never read. Windows grow (5 min, 20 min, 80 min, ... 14 d) so first rows
// come fast and sparse matches take few steps.
const (
	firstSearchWindow  = 5 * time.Minute
	searchWindowGrowth = 4
	// searchEdgeSlack: rows can be stamped a little ahead of the newest
	// segment or the clock (skew, late flushes).
	searchEdgeSlack = time.Hour
)

// searchWindow is [From, To), or [From, To] when IncludeTo (the window at
// the range's upper end).
type searchWindow struct {
	From, To  time.Time
	IncludeTo bool
}

// searchWindows splits [from, to] into windows, newest or oldest first, sized
// from where records have data (no walking empty years), always covering all
// of [from, to].
func searchWindows(
	from, to time.Time,
	records []*segments.SegmentRecord,
	now time.Time,
	newestFirst bool,
) []searchWindow {
	if !from.Before(to) {
		return []searchWindow{{From: from, To: to, IncludeTo: true}}
	}
	if len(records) == 0 {
		return []searchWindow{{From: from, To: to, IncludeTo: true}}
	}
	lower, upper := dataEdges(records, now)
	lower, upper = maxTime(lower, from), minTime(upper, to)
	if !lower.Before(upper) {
		return []searchWindow{{From: from, To: to, IncludeTo: true}}
	}

	var windows []searchWindow
	size := firstSearchWindow
	if newestFirst {
		for end := upper; ; size *= searchWindowGrowth {
			start := end.Add(-size)
			if !start.After(lower) {
				windows = append(windows, searchWindow{From: lower, To: end})
				break
			}
			windows = append(windows, searchWindow{From: start, To: end})
			end = start
		}
		windows[0].To, windows[0].IncludeTo = to, true
		windows[len(windows)-1].From = from
		return windows
	}
	for start := lower; ; size *= searchWindowGrowth {
		end := start.Add(size)
		if !end.Before(upper) {
			windows = append(windows, searchWindow{From: start, To: upper})
			break
		}
		windows = append(windows, searchWindow{From: start, To: end})
		start = end
	}
	windows[0].From = from
	windows[len(windows)-1].To, windows[len(windows)-1].IncludeTo = to, true
	return windows
}

// overlapping is the records with rows that can fall in window, in order.
func overlapping(records []*segments.SegmentRecord, window searchWindow) []*segments.SegmentRecord {
	var result []*segments.SegmentRecord
	for _, record := range records {
		if record.MaxTime.Before(window.From) {
			continue
		}
		if record.MinTime.After(window.To) || (!window.IncludeTo && record.MinTime.Equal(window.To)) {
			continue
		}
		result = append(result, record)
	}
	return result
}

func dataEdges(records []*segments.SegmentRecord, now time.Time) (time.Time, time.Time) {
	lower, upper := records[0].MinTime, now
	for _, record := range records {
		lower = minTime(lower, record.MinTime)
		upper = maxTime(upper, record.MaxTime)
	}
	return lower, upper.Add(searchEdgeSlack)
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
