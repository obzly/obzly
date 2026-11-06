-- +goose Up
-- +goose StatementBegin

-- What a segment contains (distinct services, levels, metric names), so
-- queries can skip whole files (Phase 14 item 2). NULL for rows written
-- before this column existed: those are never skipped.
ALTER TABLE stream_segments
    ADD COLUMN stats JSONB;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE stream_segments DROP COLUMN stats;

-- +goose StatementEnd
