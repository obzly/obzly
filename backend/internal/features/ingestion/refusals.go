package ingestion

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"time"

	"obzly/internal/broker"
	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/storage"

	"github.com/google/uuid"
)

const (
	// refusalRetention: how long refusal counts are kept.
	refusalRetention = 7 * 24 * time.Hour
	// refusedWindow: what the status page sums.
	refusedWindow = 24 * time.Hour
	// maxTrackedRefusals bounds one flush window's memory.
	maxTrackedRefusals = 10_000
	// RefusedOther stands for a reason that is not an error code.
	RefusedOther = "OTHER"
)

// retriedReasons: refusals a sender retries (429 or 503), so they delay the
// data rather than lose it. Everything else is dropped by the sender.
var retriedReasons = map[string]bool{
	logs_core.ErrorRateLimitExceeded: true,
	logs_core.ErrorIngestBufferFull:  true,
	logs_core.ErrorIngestUnavailable: true,
}

// reasonPattern keeps reasons to error codes, a bounded set; a free-text
// message becomes RefusedOther.
var reasonPattern = regexp.MustCompile(`^[A-Z][A-Z_]{0,39}$`)

type refusalKey struct {
	projectID uuid.UUID
	hour      int64
	signal    string
	reason    string
}

type refusalEntry struct {
	count int64
	last  time.Time
}

type refusalRow struct {
	Signal string
	Reason string
	Count  int64
	LastAt time.Time
}

// Refused counts count records of signal that a receiver refused for
// reason (a logs_core error code), shown on the project's ingestion status.
func (t *Tracker) Refused(projectID uuid.UUID, signal, reason string, count int) {
	if count <= 0 {
		return
	}
	if !reasonPattern.MatchString(reason) {
		reason = RefusedOther
	}
	now := t.now()
	key := refusalKey{projectID, now.Truncate(time.Hour).Unix(), signal, reason}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.refusals == nil {
		t.refusals = map[refusalKey]*refusalEntry{}
	}
	entry, ok := t.refusals[key]
	if !ok {
		if len(t.refusals) >= maxTrackedRefusals {
			return
		}
		entry = &refusalEntry{}
		t.refusals[key] = entry
	}
	entry.count += int64(count)
	entry.last = now
}

// PendingRefused is how many refused records of projectID wait for the next
// flush, all signals and reasons together.
func (t *Tracker) PendingRefused(projectID uuid.UUID) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	var total int64
	for key, entry := range t.refusals {
		if key.projectID == projectID {
			total += entry.count
		}
	}
	return total
}

// PublishRefusalReason is the refusal reason for a failed ingest publish.
func PublishRefusalReason(err error) string {
	if errors.Is(err, broker.ErrIngestFull) {
		return logs_core.ErrorIngestBufferFull
	}
	return logs_core.ErrorIngestUnavailable
}

// TooLargeReason is the refusal reason for a request over a receiver's limit.
const TooLargeReason = logs_core.ErrorBatchTooLarge

// writeRefusals adds a flush window's refusal counts; a project deleted
// meanwhile writes nothing.
func (r *repository) writeRefusals(ctx context.Context, refusals map[refusalKey]*refusalEntry) error {
	keys := make([]refusalKey, 0, len(refusals))
	for key := range refusals {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.projectID != b.projectID {
			return a.projectID.String() < b.projectID.String()
		}
		if a.hour != b.hour {
			return a.hour < b.hour
		}
		if a.signal != b.signal {
			return a.signal < b.signal
		}
		return a.reason < b.reason
	})

	var projects, signals, reasons, counts []string
	var hours, lasts []time.Time
	for _, key := range keys {
		entry := refusals[key]
		projects = append(projects, key.projectID.String())
		hours = append(hours, time.Unix(key.hour, 0).UTC())
		signals = append(signals, key.signal)
		reasons = append(reasons, key.reason)
		counts = append(counts, strconv.FormatInt(entry.count, 10))
		lasts = append(lasts, entry.last)
	}
	if len(keys) == 0 {
		return nil
	}

	err := storage.GetDb().WithContext(ctx).Exec(`
		INSERT INTO ingestion_refusals (project_id, hour, signal, reason, count, last_at)
		SELECT s.p, s.h, s.sig, s.r, s.n, s.l
		FROM unnest(?::uuid[], ?::timestamptz[], ?::text[], ?::text[], ?::bigint[], ?::timestamptz[])
			AS s(p, h, sig, r, n, l)
		WHERE EXISTS (SELECT 1 FROM projects WHERE id = s.p)
		ORDER BY s.p, s.h, s.sig, s.r
		ON CONFLICT (project_id, hour, signal, reason) DO UPDATE SET
			count = ingestion_refusals.count + EXCLUDED.count,
			last_at = GREATEST(ingestion_refusals.last_at, EXCLUDED.last_at)`,
		storage.TextArray(projects), storage.TimeArray(hours), storage.TextArray(signals),
		storage.TextArray(reasons), storage.TextArray(counts), storage.TimeArray(lasts),
	).Error
	if err != nil {
		return fmt.Errorf("failed to write ingestion refusals: %w", err)
	}
	return nil
}

// sumRefusals totals a project's refusals since since by signal and reason.
func (r *repository) sumRefusals(ctx context.Context, projectID uuid.UUID, since time.Time) ([]refusalRow, error) {
	var rows []refusalRow
	err := storage.GetDb().WithContext(ctx).Raw(`
		SELECT signal, reason, sum(count)::bigint AS count, max(last_at) AS last_at
		FROM ingestion_refusals
		WHERE project_id = ? AND hour >= ?
		GROUP BY signal, reason
		ORDER BY count DESC`, projectID, since.Truncate(time.Hour)).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to read ingestion refusals: %w", err)
	}
	return rows, nil
}

func refusedDTOs(rows []refusalRow) []RefusedDTO {
	refused := make([]RefusedDTO, 0, len(rows))
	for _, row := range rows {
		refused = append(refused, RefusedDTO{
			Signal:  row.Signal,
			Reason:  row.Reason,
			Count:   row.Count,
			LastAt:  row.LastAt.UTC(),
			Retried: retriedReasons[row.Reason],
		})
	}
	return refused
}
