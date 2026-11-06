-- +goose Up
-- +goose StatementBegin

-- Links a local user to their identity at a control plane (ARCHITECTURE.md
-- "Multi-site: control plane and Sites"). Modelled on user_sso_identities:
-- one row per (issuer, subject) the local exchange endpoint has ever
-- accepted an assertion for. issuer is the control plane's PUBLIC_URL, so a
-- Site enrolled with multiple control planes over its lifetime (unlikely,
-- but not prevented) keeps each one's identity separate.
CREATE TABLE central_identities (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    issuer     TEXT NOT NULL,
    subject    TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (issuer, subject)
);

CREATE INDEX idx_central_identities_user_id ON central_identities (user_id);

-- Set when the global ADMIN role was granted by a central assertion's
-- site_role=ADMIN claim - twin of sso_admin_connector_id. Only such admins
-- are ever demoted by a later exchange; manually promoted admins and the
-- root admin are never touched.
ALTER TABLE users ADD COLUMN central_admin BOOLEAN NOT NULL DEFAULT FALSE;

-- Twin of sso_connector_id: set on memberships created/updated by a central
-- assertion's projects claim, so only those are ever removed by a later
-- exchange. Manually added members and OWNER rows are never touched.
ALTER TABLE project_memberships ADD COLUMN central_managed BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE project_memberships DROP COLUMN IF EXISTS central_managed;
ALTER TABLE users DROP COLUMN IF EXISTS central_admin;
DROP TABLE IF EXISTS central_identities;

-- +goose StatementEnd
