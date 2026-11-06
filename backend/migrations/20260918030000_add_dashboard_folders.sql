-- +goose Up
-- +goose StatementBegin

-- Dashboard folders (navigation grouping only, no query/permission
-- semantics) - a dashboard's folder_id is nullable, meaning "unfiled".
CREATE TABLE dashboard_folders (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL,
    title      TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_dashboard_folders_project_id ON dashboard_folders (project_id);

ALTER TABLE dashboards ADD COLUMN folder_id UUID REFERENCES dashboard_folders(id) ON DELETE SET NULL;

CREATE INDEX idx_dashboards_folder_id ON dashboards (folder_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE dashboards DROP COLUMN IF EXISTS folder_id;
DROP TABLE IF EXISTS dashboard_folders;

-- +goose StatementEnd
