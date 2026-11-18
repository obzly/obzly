package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"obzly/internal/storage"

	"github.com/google/uuid"
)

const (
	// sourceRetention: how long a source that stopped stays listed.
	sourceRetention = 7 * 24 * time.Hour
	// minuteRetention: per-minute counts are only read for the rate window.
	minuteRetention = time.Hour
	// maxSourceRows bounds one status read (sources x signals).
	maxSourceRows = 1000
)

type repository struct{}

type sourceRow struct {
	SourceKey      string
	Signal         string
	IdentifiedBy   string
	DataSource     string
	DataSourceRank int
	Attributes     []byte
	FirstSeen      time.Time
	LastSeen       time.Time
}

type minuteRow struct {
	SourceKey string
	Signal    string
	Count     int64
}

// write upserts one project's window: rows in key order, so replicas
// writing the same sources lock them in the same order. A project deleted
// meanwhile writes nothing.
func (r *repository) write(ctx context.Context, projectID uuid.UUID, entries map[entryKey]*trackedEntry) error {
	keys := make([]entryKey, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].sourceKey != keys[j].sourceKey {
			return keys[i].sourceKey < keys[j].sourceKey
		}
		return keys[i].signal < keys[j].signal
	})

	var sourceKeys, signals, identifiedBy, kinds, ranks, attributes []string
	var firsts, lasts []time.Time
	var minuteKeys, minuteSignals, counts []string
	var minutes []time.Time
	for _, key := range keys {
		entry := entries[key]
		encoded, err := json.Marshal(entry.attributes)
		if err != nil {
			continue
		}
		sourceKeys = append(sourceKeys, key.sourceKey)
		signals = append(signals, key.signal)
		identifiedBy = append(identifiedBy, entry.identifiedBy)
		kinds = append(kinds, entry.kind.kind)
		ranks = append(ranks, strconv.Itoa(entry.kind.rank))
		attributes = append(attributes, string(encoded))
		firsts = append(firsts, entry.first)
		lasts = append(lasts, entry.last)
		for _, minute := range sortedMinutes(entry.minutes) {
			minuteKeys = append(minuteKeys, key.sourceKey)
			minuteSignals = append(minuteSignals, key.signal)
			minutes = append(minutes, time.Unix(minute, 0).UTC())
			counts = append(counts, strconv.FormatInt(entry.minutes[minute], 10))
		}
	}
	if len(sourceKeys) == 0 {
		return nil
	}

	db := storage.GetDb().WithContext(ctx)
	if err := db.Exec(`
		INSERT INTO ingestion_sources (project_id, source_key, signal, identified_by, data_source,
			data_source_rank, attributes, first_seen, last_seen)
		SELECT ?, s.k, s.sig, s.ident, s.kind, s.rnk, s.attrs::jsonb, s.fst, s.lst
		FROM unnest(?::text[], ?::text[], ?::text[], ?::text[], ?::smallint[], ?::text[],
			?::timestamptz[], ?::timestamptz[]) AS s(k, sig, ident, kind, rnk, attrs, fst, lst)
		WHERE EXISTS (SELECT 1 FROM projects WHERE id = ?)
		ORDER BY s.k, s.sig
		ON CONFLICT (project_id, source_key, signal) DO UPDATE SET
			identified_by = EXCLUDED.identified_by,
			attributes = EXCLUDED.attributes,
			data_source = CASE WHEN EXCLUDED.data_source_rank >= ingestion_sources.data_source_rank
				THEN EXCLUDED.data_source ELSE ingestion_sources.data_source END,
			data_source_rank = GREATEST(ingestion_sources.data_source_rank, EXCLUDED.data_source_rank),
			first_seen = LEAST(ingestion_sources.first_seen, EXCLUDED.first_seen),
			last_seen = GREATEST(ingestion_sources.last_seen, EXCLUDED.last_seen)`,
		projectID, storage.TextArray(sourceKeys), storage.TextArray(signals), storage.TextArray(identifiedBy),
		storage.TextArray(kinds), storage.TextArray(ranks), storage.TextArray(attributes),
		storage.TimeArray(firsts), storage.TimeArray(lasts), projectID,
	).Error; err != nil {
		return fmt.Errorf("failed to write ingestion sources: %w", err)
	}
	if err := db.Exec(`
		INSERT INTO ingestion_source_minutes (project_id, minute, source_key, signal, count)
		SELECT ?, s.m, s.k, s.sig, s.n
		FROM unnest(?::timestamptz[], ?::text[], ?::text[], ?::bigint[]) AS s(m, k, sig, n)
		WHERE EXISTS (SELECT 1 FROM projects WHERE id = ?)
		ORDER BY s.m, s.k, s.sig
		ON CONFLICT (project_id, minute, source_key, signal) DO UPDATE SET
			count = ingestion_source_minutes.count + EXCLUDED.count`,
		projectID, storage.TimeArray(minutes), storage.TextArray(minuteKeys), storage.TextArray(minuteSignals),
		storage.TextArray(counts), projectID,
	).Error; err != nil {
		return fmt.Errorf("failed to write ingestion counts: %w", err)
	}
	return nil
}

// prune drops counts past minuteRetention and sources unseen for
// sourceRetention.
func (r *repository) prune(ctx context.Context, now time.Time) error {
	db := storage.GetDb().WithContext(ctx)
	if err := db.Exec(`DELETE FROM ingestion_source_minutes WHERE minute < ?`, now.Add(-minuteRetention)).
		Error; err != nil {
		return fmt.Errorf("failed to prune ingestion counts: %w", err)
	}
	if err := db.Exec(`DELETE FROM ingestion_sources WHERE last_seen < ?`, now.Add(-sourceRetention)).
		Error; err != nil {
		return fmt.Errorf("failed to prune ingestion sources: %w", err)
	}
	return nil
}

// listSources reads a project's sources seen since since, newest first, via
// idx_ingestion_sources_last_seen.
func (r *repository) listSources(ctx context.Context, projectID uuid.UUID, since time.Time) ([]sourceRow, error) {
	var rows []sourceRow
	err := storage.GetDb().WithContext(ctx).Raw(`
		SELECT source_key, signal, identified_by, data_source, data_source_rank, attributes, first_seen, last_seen
		FROM ingestion_sources
		WHERE project_id = ? AND last_seen >= ?
		ORDER BY last_seen DESC
		LIMIT ?`, projectID, since, maxSourceRows).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to read ingestion sources: %w", err)
	}
	return rows, nil
}

// sumCounts totals each source and signal's counts since since: a primary
// key range scan over about one row per active source, signal and minute.
func (r *repository) sumCounts(ctx context.Context, projectID uuid.UUID, since time.Time) ([]minuteRow, error) {
	var rows []minuteRow
	err := storage.GetDb().WithContext(ctx).Raw(`
		SELECT source_key, signal, sum(count)::bigint AS count
		FROM ingestion_source_minutes
		WHERE project_id = ? AND minute >= ?
		GROUP BY source_key, signal`, projectID, since).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to read ingestion counts: %w", err)
	}
	return rows, nil
}

func sortedMinutes(minutes map[int64]int64) []int64 {
	sorted := make([]int64, 0, len(minutes))
	for minute := range minutes {
		sorted = append(sorted, minute)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted
}
