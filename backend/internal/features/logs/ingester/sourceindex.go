package logs_ingester

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/storage"

	"github.com/google/uuid"
)

const (
	// sourceIndexBatch bounds one upsert statement's rows.
	sourceIndexBatch = 5000
	// sourceIndexTimeout: a slow Postgres must not hold up the segment flush
	// (and ingest behind it). A source still logging is written again by its
	// next flush; one that stopped keeps its earlier last sighting.
	sourceIndexTimeout = 5 * time.Second
)

// sourceFields is logs_core.SourceIndexFields as an array, so observe's
// per-row values need no allocation.
var sourceFields = [12]string(logs_core.SourceIndexFields)

// sourceIndex collects the log sources (logs_core.SourceIndexFields values)
// of each project's buffered rows until its segment is recorded, then writes
// them to log_source_index.
type sourceIndex struct {
	mu        sync.Mutex
	byProject map[uuid.UUID]map[string]*logSource
}

type logSource struct {
	attributes  map[string]string
	first, last time.Time
}

func newSourceIndex() *sourceIndex {
	return &sourceIndex{byProject: map[uuid.UUID]map[string]*logSource{}}
}

// observe records one log's source; fields are normalized. It runs per
// ingested row: a source already seen costs a key build on the stack and a
// map lookup, with no allocation.
func (x *sourceIndex) observe(projectID uuid.UUID, fields map[string]any, at time.Time) {
	var values [len(sourceFields)]string
	var buffer [512]byte
	key := buffer[:0]
	for i, field := range sourceFields {
		raw, ok := fields[field]
		if !ok {
			continue
		}
		value, isString := raw.(string)
		if !isString {
			value = logs_core.StringifyFieldValue(raw)
		}
		if value == "" {
			continue
		}
		values[i] = value
		key = append(key, field...)
		key = append(key, '=')
		key = append(key, value...)
		key = append(key, '\n')
	}
	if len(key) == 0 {
		return
	}
	// A clock-skewed sender's future timestamp would otherwise stay the
	// source's last sighting until that time passes.
	at = at.UTC()
	if now := time.Now().UTC(); at.After(now) {
		at = now
	}

	x.mu.Lock()
	defer x.mu.Unlock()
	sources, ok := x.byProject[projectID]
	if !ok {
		sources = map[string]*logSource{}
		x.byProject[projectID] = sources
	}
	if source, ok := sources[string(key)]; ok {
		if at.Before(source.first) {
			source.first = at
		}
		if at.After(source.last) {
			source.last = at
		}
		return
	}
	attributes := map[string]string{}
	for i, value := range values {
		if value != "" {
			// Postgres text can't hold NUL.
			attributes[sourceFields[i]] = strings.ReplaceAll(value, "\x00", "")
		}
	}
	sources[string(key)] = &logSource{attributes: attributes, first: at, last: at}
}

// take removes and returns a project's collected sources.
func (x *sourceIndex) take(projectID uuid.UUID) map[string]*logSource {
	x.mu.Lock()
	defer x.mu.Unlock()
	sources := x.byProject[projectID]
	delete(x.byProject, projectID)
	return sources
}

// flush writes a project's collected sources, keeping each one's earliest
// first and latest last sighting.
func (x *sourceIndex) flush(projectID uuid.UUID) error {
	sources := x.take(projectID)
	ctx, cancel := context.WithTimeout(context.Background(), sourceIndexTimeout)
	defer cancel()
	var keys, attributes []string
	var firsts, lasts []time.Time
	write := func() error {
		if len(keys) == 0 {
			return nil
		}
		err := storage.GetDb().WithContext(ctx).Exec(`
			INSERT INTO log_source_index (project_id, source_key, attributes, first_seen, last_seen)
			SELECT ?, key, attrs::jsonb, first, last
			FROM unnest(?::text[], ?::text[], ?::timestamptz[], ?::timestamptz[]) AS s(key, attrs, first, last)
			ON CONFLICT (project_id, source_key) DO UPDATE SET
				first_seen = LEAST(log_source_index.first_seen, EXCLUDED.first_seen),
				last_seen = GREATEST(log_source_index.last_seen, EXCLUDED.last_seen)`,
			projectID, storage.TextArray(keys), storage.TextArray(attributes),
			storage.TimeArray(firsts), storage.TimeArray(lasts),
		).Error
		keys, attributes, firsts, lasts = nil, nil, nil, nil
		if err != nil {
			return fmt.Errorf("failed to update log source index: %w", err)
		}
		return nil
	}
	for key, source := range sources {
		encoded, err := json.Marshal(source.attributes)
		if err != nil {
			continue
		}
		sum := sha256.Sum256([]byte(key))
		keys = append(keys, hex.EncodeToString(sum[:]))
		attributes = append(attributes, string(encoded))
		firsts = append(firsts, source.first)
		lasts = append(lasts, source.last)
		if len(keys) == sourceIndexBatch {
			if err := write(); err != nil {
				return err
			}
		}
	}
	return write()
}
