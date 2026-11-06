-- +goose Up
-- +goose StatementBegin
CREATE TABLE segment_writers (
    id           TEXT PRIMARY KEY,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE segment_writers;
-- +goose StatementEnd
