-- +goose Up
-- +goose StatementBegin
CREATE INDEX idx_users_name ON users(name);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX idx_users_name;
-- +goose StatementEnd
