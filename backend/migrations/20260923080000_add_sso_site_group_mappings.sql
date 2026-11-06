-- +goose Up
-- +goose StatementBegin

-- SSO group -> Site access, for a control plane (ARCHITECTURE.md
-- "Multi-site"). sso_group_mappings maps a group to a project on THIS
-- deployment (project_id is a foreign key to the local projects table), so
-- on a control plane - which has no projects of its own; they live on the
-- Sites - it can't express "group X gets viewer on Site A". This table
-- mirrors site_grants' shape instead: a Site-wide role (project_id NULL:
-- ADMIN / MEMBER / VIEWER) or a role on one project of that Site
-- (project_id is the Site's project id - no foreign key, it's remote).
-- Exists on every deployment (one migration set) and stays empty except on
-- a control plane.
CREATE TABLE sso_site_group_mappings (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    connector_id UUID NOT NULL REFERENCES oauth_connectors(id) ON DELETE CASCADE,
    group_name   TEXT NOT NULL,
    site_id      TEXT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    project_id   UUID NULL,
    role         TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (
        (project_id IS NULL AND role IN ('ADMIN', 'MEMBER', 'VIEWER'))
        OR (project_id IS NOT NULL AND role IN ('PROJECT_ADMIN', 'PROJECT_MEMBER', 'PROJECT_VIEWER'))
    )
);

CREATE UNIQUE INDEX idx_sso_site_group_mappings_unique
    ON sso_site_group_mappings (
        connector_id, group_name, site_id,
        COALESCE(project_id, '00000000-0000-0000-0000-000000000000')
    );

-- Grants created by SSO group sync carry the connector that owns them, so
-- the sync only ever changes its own grants: a grant an admin added by
-- hand (NULL here) is never modified or removed by a sign-in. Deleting
-- the connector removes the access it granted.
ALTER TABLE site_grants
    ADD COLUMN sso_connector_id UUID NULL REFERENCES oauth_connectors(id) ON DELETE CASCADE;

CREATE INDEX idx_site_grants_sso_connector ON site_grants (sso_connector_id)
    WHERE sso_connector_id IS NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_site_grants_sso_connector;
ALTER TABLE site_grants DROP COLUMN IF EXISTS sso_connector_id;
DROP INDEX IF EXISTS idx_sso_site_group_mappings_unique;
DROP TABLE IF EXISTS sso_site_group_mappings;

-- +goose StatementEnd
