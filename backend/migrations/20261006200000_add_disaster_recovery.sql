-- +goose Up
-- Each release records itself after migrating, so an older binary can
-- tell "one release ahead" (mid-rollout, a supported rollback) from more.
-- +goose StatementBegin
CREATE TABLE schema_releases (
    release       TEXT PRIMARY KEY,
    goose_version BIGINT      NOT NULL,
    recorded_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- Rewrite failures, so every replica reports them (they were per-process).
-- +goose StatementBegin
ALTER TABLE stream_segments
    ADD COLUMN rewrite_failed_at TIMESTAMPTZ,
    ADD COLUMN rewrite_error     TEXT;
-- +goose StatementEnd

-- Segments whose data is gone from both disk and object storage, moved out
-- of the catalog by `obzly reconcile` so queries stop reading them.
-- +goose StatementBegin
CREATE TABLE lost_segments (
    id         UUID PRIMARY KEY,
    project_id UUID        NOT NULL,
    stream     TEXT        NOT NULL,
    object_key TEXT,
    row_count  BIGINT      NOT NULL,
    size_bytes BIGINT      NOT NULL,
    min_time   TIMESTAMPTZ NOT NULL,
    max_time   TIMESTAMPTZ NOT NULL,
    reason     TEXT        NOT NULL,
    lost_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_lost_segments_project ON lost_segments (project_id, stream);
-- +goose StatementEnd

-- The weekly sample of uploaded segments checked against object storage.
-- +goose StatementBegin
CREATE TABLE integrity_audits (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    started_at  TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ NOT NULL,
    sampled     INT         NOT NULL,
    failed      INT         NOT NULL,
    failures    JSONB       NOT NULL DEFAULT '[]'
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE integrity_audits;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE lost_segments;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE stream_segments DROP COLUMN rewrite_failed_at, DROP COLUMN rewrite_error;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE schema_releases;
-- +goose StatementEnd
