-- +goose Up
-- +goose StatementBegin
CREATE TABLE organizations (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT        NOT NULL,
    slug       TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX idx_organizations_slug ON organizations (slug);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE organization_memberships (
    org_id     UUID        NOT NULL,
    user_id    UUID        NOT NULL,
    role       TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (org_id, user_id)
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE organization_memberships
    ADD CONSTRAINT fk_organization_memberships_org_id
    FOREIGN KEY (org_id)
    REFERENCES organizations (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE organization_memberships
    ADD CONSTRAINT fk_organization_memberships_user_id
    FOREIGN KEY (user_id)
    REFERENCES users (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE organization_memberships
    ADD CONSTRAINT chk_organization_memberships_role
    CHECK (role IN ('ORG_OWNER', 'ORG_ADMIN', 'ORG_MEMBER'));
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_organization_memberships_user_id ON organization_memberships (user_id);
-- +goose StatementEnd

-- A Site belongs to one organization (control plane).
-- +goose StatementBegin
CREATE TABLE organization_sites (
    org_id  UUID NOT NULL,
    site_id TEXT NOT NULL,
    PRIMARY KEY (site_id)
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE organization_sites
    ADD CONSTRAINT fk_organization_sites_org_id
    FOREIGN KEY (org_id)
    REFERENCES organizations (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE organization_sites
    ADD CONSTRAINT fk_organization_sites_site_id
    FOREIGN KEY (site_id)
    REFERENCES sites (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE teams (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     UUID        NOT NULL,
    name       TEXT        NOT NULL,
    external_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE teams
    ADD CONSTRAINT fk_teams_org_id
    FOREIGN KEY (org_id)
    REFERENCES organizations (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX idx_teams_org_name ON teams (org_id, lower(name));
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE team_memberships (
    team_id    UUID        NOT NULL,
    user_id    UUID        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (team_id, user_id)
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE team_memberships
    ADD CONSTRAINT fk_team_memberships_team_id
    FOREIGN KEY (team_id)
    REFERENCES teams (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE team_memberships
    ADD CONSTRAINT fk_team_memberships_user_id
    FOREIGN KEY (user_id)
    REFERENCES users (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_team_memberships_user_id ON team_memberships (user_id);
-- +goose StatementEnd

-- A team's role on a Site (site-wide when project_id is NULL) or a project
-- of it; site_id '' is this instance's own projects (a standalone install).
-- +goose StatementBegin
CREATE TABLE team_grants (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id    UUID        NOT NULL,
    site_id    TEXT        NOT NULL DEFAULT '',
    project_id UUID,
    role       TEXT        NOT NULL,
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE team_grants
    ADD CONSTRAINT fk_team_grants_team_id
    FOREIGN KEY (team_id)
    REFERENCES teams (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE team_grants
    ADD CONSTRAINT fk_team_grants_created_by
    FOREIGN KEY (created_by)
    REFERENCES users (id) ON DELETE SET NULL;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE team_grants
    ADD CONSTRAINT chk_team_grants_role
    CHECK ((project_id IS NULL AND role IN ('ADMIN', 'MEMBER', 'VIEWER'))
        OR (project_id IS NOT NULL AND role IN ('PROJECT_ADMIN', 'PROJECT_MEMBER', 'PROJECT_VIEWER')));
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX idx_team_grants_unique
    ON team_grants (team_id, site_id, COALESCE(project_id, '00000000-0000-0000-0000-000000000000'::uuid));
-- +goose StatementEnd

-- Every install gets a Default organization holding everything that exists:
-- admins own it, other users are members, every Site belongs to it.
-- Nobody's access changes (it only ever comes from grants and memberships).
-- +goose StatementBegin
INSERT INTO organizations (name, slug) VALUES ('Default', 'default');
-- +goose StatementEnd

-- +goose StatementBegin
INSERT INTO organization_memberships (org_id, user_id, role)
SELECT o.id, u.id, CASE WHEN u.role = 'ADMIN' THEN 'ORG_OWNER' ELSE 'ORG_MEMBER' END
FROM organizations o CROSS JOIN users u WHERE o.slug = 'default';
-- +goose StatementEnd

-- +goose StatementBegin
INSERT INTO organization_sites (org_id, site_id)
SELECT o.id, s.id FROM organizations o CROSS JOIN sites s WHERE o.slug = 'default';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE team_grants;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE team_memberships;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE teams;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE organization_sites;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE organization_memberships;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE organizations;
-- +goose StatementEnd
