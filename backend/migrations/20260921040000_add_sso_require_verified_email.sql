-- +goose Up
-- +goose StatementBegin

-- When TRUE (the default, including for existing connectors), an SSO sign-in
-- may only link to an existing account, or create one, by email if the
-- identity provider says that email is verified. Users already linked to
-- their IdP subject are unaffected. Turn off per connector only where the
-- IdP does not send email_verified but its directory is trusted.
ALTER TABLE oauth_connectors ADD COLUMN require_verified_email BOOLEAN NOT NULL DEFAULT TRUE;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE oauth_connectors DROP COLUMN IF EXISTS require_verified_email;

-- +goose StatementEnd
