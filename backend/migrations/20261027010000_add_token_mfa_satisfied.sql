-- +goose Up
-- +goose StatementBegin
-- Whether the session that created the token had passed MFA (or a
-- trusted provider's MFA). Only such tokens let MCP calls skip the
-- local MFA requirement; existing tokens start as false.
ALTER TABLE access_tokens
    ADD COLUMN mfa_satisfied BOOLEAN NOT NULL DEFAULT FALSE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE oauth_refresh_tokens
    ADD COLUMN mfa_satisfied BOOLEAN NOT NULL DEFAULT FALSE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE oauth_refresh_tokens DROP COLUMN mfa_satisfied;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE access_tokens DROP COLUMN mfa_satisfied;
-- +goose StatementEnd
