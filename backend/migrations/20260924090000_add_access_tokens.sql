-- +goose Up
-- +goose StatementBegin

-- Personal access tokens: a user's credential for non-browser clients (the
-- MCP endpoint). Only the SHA-256 of the token is stored.
CREATE TABLE access_tokens (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL,
    name         TEXT NOT NULL,
    token_prefix TEXT NOT NULL,
    token_hash   TEXT NOT NULL,
    scopes       TEXT NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    last_used_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at   TIMESTAMPTZ
);

ALTER TABLE access_tokens
    ADD CONSTRAINT fk_access_tokens_user_id
    FOREIGN KEY (user_id)
    REFERENCES users (id) ON DELETE CASCADE;

CREATE UNIQUE INDEX idx_access_tokens_token_hash ON access_tokens (token_hash);

CREATE INDEX idx_access_tokens_user_id ON access_tokens (user_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE access_tokens;

-- +goose StatementEnd
