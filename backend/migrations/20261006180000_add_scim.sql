-- +goose Up
-- +goose StatementBegin
CREATE TABLE scim_tokens (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id       UUID        NOT NULL,
    name         TEXT        NOT NULL,
    token_prefix TEXT        NOT NULL,
    token_hash   TEXT        NOT NULL,
    created_by   UUID,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at TIMESTAMPTZ
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE scim_tokens
    ADD CONSTRAINT fk_scim_tokens_org_id
    FOREIGN KEY (org_id)
    REFERENCES organizations (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX idx_scim_tokens_prefix ON scim_tokens (token_prefix);
-- +goose StatementEnd

-- The IdP's id for a member, per organization; a suspended member (SCIM
-- deprovisioned an account the organization doesn't own) gets nothing
-- through its teams.
-- +goose StatementBegin
ALTER TABLE organization_memberships
    ADD COLUMN external_id TEXT,
    ADD COLUMN suspended   BOOLEAN NOT NULL DEFAULT FALSE;
-- +goose StatementEnd

-- The organization whose SCIM created the account: only it may
-- deactivate it; others only remove it from themselves.
-- +goose StatementBegin
ALTER TABLE users ADD COLUMN scim_org_id UUID;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE users
    ADD CONSTRAINT fk_users_scim_org_id
    FOREIGN KEY (scim_org_id)
    REFERENCES organizations (id) ON DELETE SET NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE users DROP COLUMN scim_org_id;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE organization_memberships DROP COLUMN external_id, DROP COLUMN suspended;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE scim_tokens;
-- +goose StatementEnd
