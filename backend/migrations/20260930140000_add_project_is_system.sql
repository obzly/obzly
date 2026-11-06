-- +goose Up
-- +goose StatementBegin

-- The reserved project Obzly's own sampled request traces are written to
-- (Phase 14 item 7). No members, so only global admins see it.
ALTER TABLE projects
    ADD COLUMN is_system BOOLEAN NOT NULL DEFAULT FALSE;

-- At most one system project per Site, even when replicas create it at once.
CREATE UNIQUE INDEX idx_projects_single_system
    ON projects (is_system)
    WHERE is_system;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_projects_single_system;

ALTER TABLE projects DROP COLUMN is_system;

-- +goose StatementEnd
