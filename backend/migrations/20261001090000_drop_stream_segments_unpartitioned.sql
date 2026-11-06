-- +goose Up
-- +goose StatementBegin

-- The pre-partitioning catalog, kept by 20260930150000 for one release as
-- the rollback boundary. Partitioning shipped in v0.33.0 and later releases
-- are partitioned, so rolling back one release never needs it.
DROP TABLE IF EXISTS stream_segments_unpartitioned;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Empty, in the old shape, so 20260930150000's down step can copy rows back.
CREATE TABLE stream_segments_unpartitioned (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id     UUID        NOT NULL,
    stream         TEXT        NOT NULL,
    path           TEXT        NOT NULL,
    row_count      BIGINT      NOT NULL,
    size_bytes     BIGINT      NOT NULL,
    min_time       TIMESTAMPTZ NOT NULL,
    max_time       TIMESTAMPTZ NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    uploaded_at    TIMESTAMPTZ,
    object_key     TEXT,
    raw_size_bytes BIGINT      NOT NULL DEFAULT 0,
    schema_version INTEGER     NOT NULL DEFAULT 2,
    writer_id      TEXT,
    stats          JSONB
);

CREATE INDEX idx_stream_segments_project_stream_time
    ON stream_segments_unpartitioned (project_id, stream, min_time, max_time);

CREATE INDEX idx_stream_segments_pending_upload
    ON stream_segments_unpartitioned (stream, created_at)
    WHERE uploaded_at IS NULL;

CREATE INDEX idx_stream_segments_pending_upload_by_writer
    ON stream_segments_unpartitioned (stream, writer_id, created_at)
    WHERE uploaded_at IS NULL;

CREATE INDEX idx_stream_segments_schema_version
    ON stream_segments_unpartitioned (schema_version)
    WHERE schema_version < 2;

-- +goose StatementEnd
