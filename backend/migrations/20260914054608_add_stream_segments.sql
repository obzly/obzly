-- +goose Up
-- +goose StatementBegin

-- Metadata/file-list index for the Parquet segment store (ARCHITECTURE.md,
-- Phase 2c). One row per segment file written by internal/storage/segments.Writer.
-- "stream" is the signal type ("logs" today; "spans"/"metrics_*" later) so this
-- one table serves every signal, not just logs.
CREATE TABLE stream_segments (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id   UUID NOT NULL,
    stream       TEXT NOT NULL,
    path         TEXT NOT NULL,
    row_count    BIGINT NOT NULL,
    size_bytes   BIGINT NOT NULL,
    min_time     TIMESTAMPTZ NOT NULL,
    max_time     TIMESTAMPTZ NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- NULL until Phase 2f's uploader pushes this segment to object storage.
    uploaded_at  TIMESTAMPTZ,
    object_key   TEXT
);

-- Primary access pattern for the future querier (Phase 2d): find every
-- segment for a project/stream whose time range overlaps a query window.
CREATE INDEX idx_stream_segments_project_stream_time
    ON stream_segments (project_id, stream, min_time, max_time);

-- Primary access pattern for the future uploader (Phase 2f): find segments
-- not yet pushed to object storage.
CREATE INDEX idx_stream_segments_pending_upload
    ON stream_segments (stream, created_at)
    WHERE uploaded_at IS NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS stream_segments;

-- +goose StatementEnd
