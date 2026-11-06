-- +goose Up
-- +goose StatementBegin
ALTER TABLE sites
    ADD COLUMN display_name TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE sites
    ADD COLUMN environment TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE sites
    ADD CONSTRAINT chk_sites_environment
    CHECK (environment IN ('', 'production', 'staging', 'development'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE sites DROP CONSTRAINT chk_sites_environment;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE sites DROP COLUMN environment;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE sites DROP COLUMN display_name;
-- +goose StatementEnd
