-- +goose Up
-- +goose StatementBegin

-- The Site registry (ARCHITECTURE.md "Multi-site: control plane and
-- Sites"). id is the same slug a Site's own SITE_ID env var carries -
-- system_siteinfo.GetController's "check connection" step compares them so
-- a typo'd api_url pointing at the wrong backend is caught before any grant
-- exists. These tables exist on every deployment (one migration set for
-- both roles) and simply stay empty on a plain Site or a standalone
-- install - only a control-plane-mode process ever reads or writes them.
CREATE TABLE sites (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    api_url    TEXT NOT NULL,
    enabled    BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- A grant with project_id NULL gives the user global ADMIN on that Site
-- (role must be 'ADMIN'); otherwise it gives a specific project role -
-- the exact "nullable project_id means global admin" shape
-- sso_group_mappings already uses. created_by records which central admin
-- made the grant (self-grants included), for audit.
CREATE TABLE site_grants (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    site_id    TEXT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    project_id UUID NULL,
    role       TEXT NOT NULL,
    created_by UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (
        (project_id IS NULL AND role = 'ADMIN')
        OR (project_id IS NOT NULL AND role IN ('PROJECT_ADMIN', 'PROJECT_MEMBER', 'PROJECT_VIEWER'))
    )
);

CREATE INDEX idx_site_grants_user_id ON site_grants (user_id);
CREATE INDEX idx_site_grants_site_id ON site_grants (site_id);

-- NULLs never collide in a plain UNIQUE - same COALESCE-a-sentinel trick
-- migration 20260921030000 uses for sso_group_mappings' own nullable-scope
-- uniqueness.
CREATE UNIQUE INDEX idx_site_grants_unique
    ON site_grants (user_id, site_id, COALESCE(project_id, '00000000-0000-0000-0000-000000000000'));

-- A control plane's own JWT signing keys for minting per-Site assertions
-- (AssertionService) - separate from the app-wide HS256 secret_keys row
-- every deployment already has, since assertions are asymmetric (ES256,
-- verified by Sites against the JWKS this table backs) and need to
-- support rotation with an overlap window (retired_at, not deleted, so a
-- Site's cached JWKS can still verify an assertion signed just before
-- rotation). private_key_encrypted uses the same AES-GCM scheme as any
-- other at-rest secret in this codebase (SecretKeyRepository.Encrypt).
CREATE TABLE control_plane_signing_keys (
    kid                   TEXT PRIMARY KEY,
    algorithm             TEXT NOT NULL,
    private_key_encrypted TEXT NOT NULL,
    public_key_pem        TEXT NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    retired_at            TIMESTAMPTZ NULL
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS control_plane_signing_keys;
DROP INDEX IF EXISTS idx_site_grants_unique;
DROP TABLE IF EXISTS site_grants;
DROP TABLE IF EXISTS sites;

-- +goose StatementEnd
