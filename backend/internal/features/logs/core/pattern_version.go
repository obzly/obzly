package logs_core

import (
	"sync"
	"time"

	"obzly/internal/storage"
)

// patternVersionMargin: while replicas roll over to a new version, logs
// written by either may carry either version's hashes.
const patternVersionMargin = 30 * time.Minute

// patternVersionRetry: how long a failed or empty read of the starts is
// trusted before reading them again.
const patternVersionRetry = 5 * time.Minute

var (
	patternVersionMu     sync.Mutex
	patternVersionStarts map[int]time.Time
	patternVersionLoaded bool
	patternVersionReadAt time.Time
)

// PatternVersionSince is when PatternVersion started on this Site (zero if
// unknown: a Site that had no logs before it).
func PatternVersionSince() time.Time {
	return patternVersionStartsOnce()[PatternVersion]
}

// PatternBaselineRegrouped reports whether a baseline starting at from
// reaches back before PatternVersion started (with a margin for the
// rollover), and when it started.
func PatternBaselineRegrouped(from time.Time) (time.Time, bool) {
	since := PatternVersionSince()
	if since.IsZero() {
		return since, false
	}
	return since, from.Before(since.Add(patternVersionMargin))
}

// PatternVersionsReached is the earlier versions whose logs a baseline
// starting at from reaches, newest first: each version ended when the next
// one started on this Site (a version that never ran here has no start).
func PatternVersionsReached(from time.Time) []int {
	starts := patternVersionStartsOnce()
	var reached []int
	for version := PatternVersion - 1; version >= 1; version-- {
		ended, ok := starts[version+1]
		if !ok || !from.Before(ended.Add(patternVersionMargin)) {
			break
		}
		reached = append(reached, version)
	}
	return reached
}

// SetPatternVersionSinceForTest pins PatternVersion's start (zero: none) and
// forgets the earlier versions'.
func SetPatternVersionSinceForTest(since time.Time) {
	starts := map[int]time.Time{}
	if !since.IsZero() {
		starts[PatternVersion] = since
	}
	SetPatternVersionStartsForTest(starts)
}

// SetPatternVersionStartsForTest pins every version's start.
func SetPatternVersionStartsForTest(starts map[int]time.Time) {
	patternVersionMu.Lock()
	defer patternVersionMu.Unlock()
	patternVersionStarts, patternVersionLoaded = starts, true
}

// patternVersionStartsOnce is when each version started on this Site. The
// starts never change once recorded, so they are read once.
func patternVersionStartsOnce() map[int]time.Time {
	patternVersionMu.Lock()
	defer patternVersionMu.Unlock()
	if patternVersionLoaded || time.Since(patternVersionReadAt) < patternVersionRetry {
		return patternVersionStarts
	}
	patternVersionReadAt = time.Now().UTC()
	var rows []struct {
		Version int
		Since   time.Time
	}
	err := storage.GetDb().Raw("SELECT version, since FROM log_pattern_versions").Scan(&rows).Error
	if err != nil {
		return patternVersionStarts
	}
	starts := make(map[int]time.Time, len(rows))
	for _, row := range rows {
		starts[row.Version] = row.Since.UTC()
	}
	patternVersionStarts = starts
	// Read again later while the current version has no start: the
	// migration that records it may run on another replica first.
	patternVersionLoaded = !starts[PatternVersion].IsZero()
	return patternVersionStarts
}
