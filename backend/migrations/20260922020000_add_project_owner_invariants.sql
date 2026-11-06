-- +goose Up
-- +goose StatementBegin

-- Before this migration, AddMember/ChangeMemberRole could set a role of
-- OWNER (no different from any other role string as far as they were
-- concerned), so a project could already have accumulated more than one
-- OWNER row. De-duplicate first (keep the oldest, demote the rest to
-- PROJECT_ADMIN) or the unique index below would simply fail to create.
WITH ranked_owners AS (
    SELECT id, ROW_NUMBER() OVER (PARTITION BY project_id ORDER BY created_at ASC) AS rn
    FROM project_memberships
    WHERE role = 'OWNER'
)
UPDATE project_memberships
SET role = 'PROJECT_ADMIN'
WHERE id IN (SELECT id FROM ranked_owners WHERE rn > 1);

-- At most one OWNER per project from here on - the application already
-- enforces this (TransferOwnership is the only path that can set OWNER,
-- and it moves the role in one transaction rather than adding a second),
-- this is the backstop against any code path that doesn't.
CREATE UNIQUE INDEX idx_project_memberships_single_owner
    ON project_memberships (project_id)
    WHERE role = 'OWNER';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_project_memberships_single_owner;

-- +goose StatementEnd
