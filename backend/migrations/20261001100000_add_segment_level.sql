-- +goose Up
-- +goose StatementBegin
-- Compaction level (Phase 14 item 1): 0 = a writer's flush (L0), 1 = merged
-- by compaction. Rows from before this column read as 0. Part of the object
-- key of new uploads (<stream>/<project>/<yyyy>/<mm>/<dd>/<level>/<id>).
ALTER TABLE stream_segments
    ADD COLUMN level SMALLINT NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE stream_segments DROP COLUMN level;
-- +goose StatementEnd
