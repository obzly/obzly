-- +goose Up
-- +goose StatementBegin

-- Phase C's correlation engine data model - deliberately relational
-- (Postgres), not a segment-stream (see correlation_core's package doc
-- comment): low volume, needs joins, and signal_ref is a pointer back to
-- the owning stream's live data, not a duplicated copy of the row.
CREATE TABLE incident_sessions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    service_name    TEXT NOT NULL,
    window_start    TIMESTAMPTZ NOT NULL,
    window_end      TIMESTAMPTZ NOT NULL,
    trigger_type    TEXT NOT NULL, -- error_log | span_error_status | metric_threshold | trace_id_link
    trigger_summary TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_incident_sessions_project_window ON incident_sessions (project_id, window_start);

CREATE TABLE incident_session_signals (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id  UUID NOT NULL REFERENCES incident_sessions (id) ON DELETE CASCADE,
    signal_type TEXT NOT NULL, -- log | span | metric | flow
    signal_ref  JSONB NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_incident_session_signals_session_id ON incident_session_signals (session_id);

-- Per-project incremental-scan bookkeeping for the correlation worker
-- (bounded, incremental window per run, not a full-history rescan) - its
-- own tiny table rather than a column on an existing per-project settings
-- table, kept simplest since it's the only thing keyed by project_id
-- alone here.
CREATE TABLE correlation_watermarks (
    project_id      UUID PRIMARY KEY REFERENCES projects (id) ON DELETE CASCADE,
    last_scanned_at TIMESTAMPTZ NOT NULL
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS correlation_watermarks;
DROP TABLE IF EXISTS incident_session_signals;
DROP TABLE IF EXISTS incident_sessions;

-- +goose StatementEnd
