-- +goose Up
-- +goose StatementBegin

-- OAuth 2.1 for the MCP endpoint. Access tokens issued through OAuth share
-- access_tokens with personal ones (kind), bound to one resource (audience).
ALTER TABLE access_tokens
    ADD COLUMN kind      TEXT NOT NULL DEFAULT 'pat',
    ADD COLUMN client_id TEXT,
    ADD COLUMN audience  TEXT,
    ADD COLUMN family_id UUID;

CREATE INDEX idx_access_tokens_family_id ON access_tokens (family_id);

-- Clients registered through RFC 7591 dynamic registration. Clients using a
-- Client ID Metadata Document are not stored; their metadata is fetched.
CREATE TABLE oauth_clients (
    client_id     TEXT PRIMARY KEY,
    client_name   TEXT NOT NULL,
    client_uri    TEXT,
    redirect_uris TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Rotating refresh tokens. A family is one consent; reusing a rotated token
-- revokes the whole family.
CREATE TABLE oauth_refresh_tokens (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    family_id         UUID NOT NULL,
    user_id           UUID NOT NULL,
    client_id         TEXT NOT NULL,
    client_name       TEXT NOT NULL,
    token_hash        TEXT NOT NULL,
    scopes            TEXT NOT NULL,
    audience          TEXT NOT NULL,
    family_expires_at TIMESTAMPTZ NOT NULL,
    expires_at        TIMESTAMPTZ NOT NULL,
    used_at           TIMESTAMPTZ,
    revoked_at        TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE oauth_refresh_tokens
    ADD CONSTRAINT fk_oauth_refresh_tokens_user_id
    FOREIGN KEY (user_id)
    REFERENCES users (id) ON DELETE CASCADE;

CREATE UNIQUE INDEX idx_oauth_refresh_tokens_token_hash ON oauth_refresh_tokens (token_hash);

CREATE INDEX idx_oauth_refresh_tokens_family_id ON oauth_refresh_tokens (family_id);

CREATE INDEX idx_oauth_refresh_tokens_user_id ON oauth_refresh_tokens (user_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE oauth_refresh_tokens;
DROP TABLE oauth_clients;
DROP INDEX idx_access_tokens_family_id;
ALTER TABLE access_tokens
    DROP COLUMN family_id,
    DROP COLUMN audience,
    DROP COLUMN client_id,
    DROP COLUMN kind;

-- +goose StatementEnd
