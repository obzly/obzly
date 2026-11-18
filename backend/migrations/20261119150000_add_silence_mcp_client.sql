-- +goose Up
-- +goose StatementBegin
ALTER TABLE silences
    ADD COLUMN mcp_client TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE silences DROP COLUMN mcp_client;
-- +goose StatementEnd
