-- v0.56.0: Settings → Identity & access → "Require MFA for everyone".
-- Admins always need MFA; this extends it to every user.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE users_settings
    ADD COLUMN is_mfa_required_for_everyone BOOLEAN NOT NULL DEFAULT FALSE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE users_settings DROP COLUMN IF EXISTS is_mfa_required_for_everyone;
-- +goose StatementEnd
