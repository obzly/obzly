-- +goose Up
-- +goose StatementBegin
ALTER TABLE stream_segments
    ADD COLUMN compaction_attempts SMALLINT NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE stream_segments DROP COLUMN compaction_attempts;
-- +goose StatementEnd
