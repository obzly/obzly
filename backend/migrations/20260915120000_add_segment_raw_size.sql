-- +goose Up
-- +goose StatementBegin

-- raw_size_bytes tracks the approximate *uncompressed* size of a segment's
-- rows as received (the same approxBytes accounting segments.Writer already
-- computes to decide when to flush) - size_bytes is the Parquet file's
-- on-disk (compressed) size. Together they let the system dashboard show
-- "log ingested size" vs "log size on disk (compressed)". Defaults to 0 so
-- segments written before this migration just don't contribute to the raw
-- total, rather than skewing it.
ALTER TABLE stream_segments ADD COLUMN raw_size_bytes BIGINT NOT NULL DEFAULT 0;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE stream_segments DROP COLUMN raw_size_bytes;

-- +goose StatementEnd
