-- +goose Up
-- +goose StatementBegin

-- Portal-configurable OIDC SSO connectors (AWS Cognito, Google Workspace,
-- Microsoft Entra ID, or any other standard OIDC provider) - an admin
-- configures these here instead of the pre-existing GitHub/Google OAuth,
-- which stays env-var-configured and cloud-only, untouched. One generic
-- implementation (real OIDC discovery + ID token verification) covers all
-- three named providers rather than three bespoke integrations, since all
-- three publish a standard .well-known/openid-configuration document.
CREATE TABLE oauth_connectors (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                    TEXT NOT NULL,
    provider_type           TEXT NOT NULL,
    issuer_url              TEXT NOT NULL,
    client_id               TEXT NOT NULL,
    -- AES-256-GCM ciphertext (base64), keyed off the same DB-generated
    -- secret_keys row every JWT is already signed with - see
    -- internal/features/users/sso/encryption.go. Never returned by the
    -- API after creation, matching the API-key "shown once" convention
    -- already established elsewhere in this codebase.
    client_secret_encrypted TEXT NOT NULL,
    scopes                  TEXT NOT NULL DEFAULT 'openid email profile',
    button_label            TEXT NOT NULL,
    enabled                 BOOLEAN NOT NULL DEFAULT TRUE,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Links an Obzly user to their identity at one connector - a generic
-- replacement for the fixed github_oauth_id/google_oauth_id columns
-- (those stay as they are; a portal-configured connector always uses this
-- table instead, since there can be any number of them).
CREATE TABLE user_sso_identities (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    connector_id UUID NOT NULL REFERENCES oauth_connectors(id) ON DELETE CASCADE,
    subject      TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (connector_id, subject)
);

CREATE INDEX idx_user_sso_identities_user_id ON user_sso_identities (user_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS user_sso_identities;
DROP TABLE IF EXISTS oauth_connectors;

-- +goose StatementEnd
