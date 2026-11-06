-- +goose Up
-- +goose StatementBegin

-- Which ID-token claim carries the user's groups for this connector (Cognito:
-- "cognito:groups"). Empty = group sync disabled for the connector.
ALTER TABLE oauth_connectors ADD COLUMN groups_claim TEXT NOT NULL DEFAULT '';

-- Maps one IdP group to a role in one project. A user in several mapped
-- groups gets the highest role per project. OWNER is never assignable here.
CREATE TABLE sso_group_mappings (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    connector_id UUID NOT NULL REFERENCES oauth_connectors(id) ON DELETE CASCADE,
    group_name   TEXT NOT NULL,
    project_id   UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    role         TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (connector_id, group_name, project_id)
);

CREATE INDEX idx_sso_group_mappings_connector_id ON sso_group_mappings (connector_id);

-- Set on memberships created by group sync: only these are ever updated or
-- removed by a later sync. Manually added members stay NULL and are never
-- touched. Deleting the connector keeps the access (becomes a manual member).
ALTER TABLE project_memberships
    ADD COLUMN sso_connector_id UUID REFERENCES oauth_connectors(id) ON DELETE SET NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE project_memberships DROP COLUMN IF EXISTS sso_connector_id;
DROP TABLE IF EXISTS sso_group_mappings;
ALTER TABLE oauth_connectors DROP COLUMN IF EXISTS groups_claim;

-- +goose StatementEnd
