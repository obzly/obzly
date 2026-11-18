-- +goose Up
-- +goose StatementBegin
ALTER TABLE users
    ADD COLUMN ui_preferences JSONB;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE users_settings
    ADD COLUMN default_theme_mode           TEXT    NOT NULL DEFAULT 'system',
    ADD COLUMN default_light_theme          TEXT    NOT NULL DEFAULT 'emerald-light',
    ADD COLUMN default_dark_theme           TEXT    NOT NULL DEFAULT 'emerald-dark',
    ADD COLUMN is_user_theme_choice_allowed BOOLEAN NOT NULL DEFAULT TRUE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE users_settings
    DROP COLUMN is_user_theme_choice_allowed,
    DROP COLUMN default_dark_theme,
    DROP COLUMN default_light_theme,
    DROP COLUMN default_theme_mode;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE users DROP COLUMN ui_preferences;
-- +goose StatementEnd
