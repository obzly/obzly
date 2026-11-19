package logs_core

import (
	"sync"
	"time"

	"obzly/internal/storage"
)

// patternVersionMargin: while replicas roll over to a new version, logs
// written by either may carry either version's hashes.
const patternVersionMargin = 30 * time.Minute

// patternVersionRetry: how long a failed or empty read of the start is
// trusted before reading it again.
const patternVersionRetry = 5 * time.Minute

var (
	patternVersionMu     sync.Mutex
	patternVersionStart  time.Time
	patternVersionLoaded bool
	patternVersionReadAt time.Time
)

// PatternVersionSince is when PatternVersion started on this Site (zero if
// unknown). It never changes once recorded, so it is read once.
func PatternVersionSince() time.Time {
	patternVersionMu.Lock()
	defer patternVersionMu.Unlock()
	if patternVersionLoaded || time.Since(patternVersionReadAt) < patternVersionRetry {
		return patternVersionStart
	}
	patternVersionReadAt = time.Now().UTC()
	var since []time.Time
	err := storage.GetDb().Raw("SELECT since FROM log_pattern_versions WHERE version = ?", PatternVersion).
		Scan(&since).Error
	if err == nil && len(since) > 0 {
		patternVersionStart, patternVersionLoaded = since[0].UTC(), true
	}
	return patternVersionStart
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

// SetPatternVersionSinceForTest pins PatternVersionSince (zero: none).
func SetPatternVersionSinceForTest(since time.Time) {
	patternVersionMu.Lock()
	defer patternVersionMu.Unlock()
	patternVersionStart, patternVersionLoaded = since, true
}
