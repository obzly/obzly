-- +goose Up
-- +goose StatementBegin

-- Which process's local disk holds the segment file until it is uploaded
-- (Phase 14-A interim uploader). Nullable and additive: rows written before
-- this column existed stay NULL and are uploaded by background-worker
-- replicas, as before.
ALTER TABLE stream_segments
    ADD COLUMN writer_id TEXT;

CREATE INDEX idx_stream_segments_pending_upload_by_writer
    ON stream_segments (stream, writer_id, created_at)
    WHERE uploaded_at IS NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_stream_segments_pending_upload_by_writer;

ALTER TABLE stream_segments DROP COLUMN writer_id;

-- +goose StatementEnd
