-- +goose Up
-- +goose StatementBegin
ALTER TABLE users ADD COLUMN name TEXT NOT NULL DEFAULT 'User';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE users DROP COLUMN name;
-- +goose StatementEnd
