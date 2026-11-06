-- +goose Up
-- +goose StatementBegin
ALTER TABLE users
    ADD COLUMN last_login_at  TIMESTAMPTZ,
    ADD COLUMN last_active_at TIMESTAMPTZ;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE users
    DROP COLUMN last_active_at,
    DROP COLUMN last_login_at;
-- +goose StatementEnd
