package metrics_ingester

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"obzly/internal/storage"

	"github.com/google/uuid"
)

// labelIndexBatch bounds one upsert statement's rows.
const labelIndexBatch = 5000

// nulByte: Postgres text can't hold it.
const nulByte = "\x00"

type labelKey struct {
	metricName, labelName, labelValue string
}

type seenSpan struct {
	first, last time.Time
}

type nameEntry struct {
	metricType string
	seenSpan
}

// updateLabelIndex records the label values and metric names of one
// project's flushed series in metric_label_index / metric_name_index,
// keeping each one's earliest first and latest last sighting.
func updateLabelIndex(projectID uuid.UUID, series map[uint64]*seriesEntry) error {
	labels := map[labelKey]seenSpan{}
	names := map[string]nameEntry{}
	for _, entry := range series {
		name, ok := names[entry.metricName]
		if !ok || entry.lastSeen.After(name.last) {
			name.metricType = entry.metricType
		}
		names[entry.metricName] = nameEntry{name.metricType, widen(name.seenSpan, ok, entry)}

		var decoded map[string]string
		if entry.labels != "" && entry.labels != "{}" {
			if err := json.Unmarshal([]byte(entry.labels), &decoded); err != nil {
				continue
			}
		}
		for labelName, labelValue := range decoded {
			// Series buffered before NormalizeStringMap dropped NULs.
			key := labelKey{entry.metricName, labelName, strings.ReplaceAll(labelValue, nulByte, "")}
			span, ok := labels[key]
			labels[key] = widen(span, ok, entry)
		}
	}

	if err := upsertLabels(projectID, labels); err != nil {
		return err
	}
	return upsertNames(projectID, names)
}

func widen(span seenSpan, ok bool, entry *seriesEntry) seenSpan {
	if !ok || entry.firstSeen.Before(span.first) {
		span.first = entry.firstSeen
	}
	if !ok || entry.lastSeen.After(span.last) {
		span.last = entry.lastSeen
	}
	return span
}

func upsertLabels(projectID uuid.UUID, labels map[labelKey]seenSpan) error {
	var metricNames, labelNames, labelValues []string
	var firsts, lasts []time.Time
	flush := func() error {
		if len(metricNames) == 0 {
			return nil
		}
		err := storage.GetDb().Exec(`
			INSERT INTO metric_label_index (project_id, metric_name, label_name, label_value, first_seen, last_seen)
			SELECT ?, * FROM unnest(?::text[], ?::text[], ?::text[], ?::timestamptz[], ?::timestamptz[])
			ON CONFLICT (project_id, metric_name, label_name, label_value) DO UPDATE SET
				first_seen = LEAST(metric_label_index.first_seen, EXCLUDED.first_seen),
				last_seen = GREATEST(metric_label_index.last_seen, EXCLUDED.last_seen)`,
			projectID, storage.TextArray(metricNames), storage.TextArray(labelNames), storage.TextArray(labelValues),
			storage.TimeArray(firsts), storage.TimeArray(lasts),
		).Error
		metricNames, labelNames, labelValues, firsts, lasts = nil, nil, nil, nil, nil
		if err != nil {
			return fmt.Errorf("failed to update metric label index: %w", err)
		}
		return nil
	}
	for key, span := range labels {
		metricNames = append(metricNames, key.metricName)
		labelNames = append(labelNames, key.labelName)
		labelValues = append(labelValues, key.labelValue)
		firsts = append(firsts, span.first)
		lasts = append(lasts, span.last)
		if len(metricNames) == labelIndexBatch {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

func upsertNames(projectID uuid.UUID, names map[string]nameEntry) error {
	if len(names) == 0 {
		return nil
	}
	var metricNames, metricTypes []string
	var firsts, lasts []time.Time
	for name, entry := range names {
		metricNames = append(metricNames, name)
		metricTypes = append(metricTypes, entry.metricType)
		firsts = append(firsts, entry.first)
		lasts = append(lasts, entry.last)
	}
	err := storage.GetDb().Exec(`
		INSERT INTO metric_name_index (project_id, metric_name, metric_type, first_seen, last_seen)
		SELECT ?, * FROM unnest(?::text[], ?::text[], ?::timestamptz[], ?::timestamptz[])
		ON CONFLICT (project_id, metric_name) DO UPDATE SET
			metric_type = CASE WHEN EXCLUDED.last_seen >= metric_name_index.last_seen
				THEN EXCLUDED.metric_type ELSE metric_name_index.metric_type END,
			first_seen = LEAST(metric_name_index.first_seen, EXCLUDED.first_seen),
			last_seen = GREATEST(metric_name_index.last_seen, EXCLUDED.last_seen)`,
		projectID, storage.TextArray(metricNames), storage.TextArray(metricTypes), storage.TimeArray(firsts), storage.TimeArray(lasts),
	).
		Error
	if err != nil {
		return fmt.Errorf("failed to update metric name index: %w", err)
	}
	return nil
}
