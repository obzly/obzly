-- +goose Up
-- +goose StatementBegin

-- A mapping with a NULL project_id grants the global ADMIN role instead of a
-- project role (only the root admin may create these - see the service).
ALTER TABLE sso_group_mappings ALTER COLUMN project_id DROP NOT NULL;

ALTER TABLE sso_group_mappings
    DROP CONSTRAINT sso_group_mappings_connector_id_group_name_project_id_key;

-- NULLs never collide in a plain UNIQUE, so key the global mapping explicitly.
CREATE UNIQUE INDEX idx_sso_group_mappings_unique
    ON sso_group_mappings (connector_id, group_name, COALESCE(project_id, '00000000-0000-0000-0000-000000000000'));

ALTER TABLE sso_group_mappings ADD CONSTRAINT sso_group_mappings_scope_role_check CHECK (
    (project_id IS NULL AND role = 'ADMIN')
    OR (project_id IS NOT NULL AND role IN ('PROJECT_ADMIN', 'PROJECT_MEMBER', 'PROJECT_VIEWER'))
);

-- Set when a user's global ADMIN role was granted by group sync. Only such
-- admins are ever demoted by a later sync; manually promoted admins and the
-- root admin are never touched. Deleting the connector keeps the role.
ALTER TABLE users
    ADD COLUMN sso_admin_connector_id UUID REFERENCES oauth_connectors(id) ON DELETE SET NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE users DROP COLUMN IF EXISTS sso_admin_connector_id;
ALTER TABLE sso_group_mappings DROP CONSTRAINT IF EXISTS sso_group_mappings_scope_role_check;
DELETE FROM sso_group_mappings WHERE project_id IS NULL;
DROP INDEX IF EXISTS idx_sso_group_mappings_unique;
ALTER TABLE sso_group_mappings ADD CONSTRAINT sso_group_mappings_connector_id_group_name_project_id_key
    UNIQUE (connector_id, group_name, project_id);
ALTER TABLE sso_group_mappings ALTER COLUMN project_id SET NOT NULL;

-- +goose StatementEnd
