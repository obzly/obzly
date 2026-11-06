-- Infrastructure inventory (docs/INFRASTRUCTURE.md). Site tables, built by
-- the inventory worker from each project's own data; they exist on a
-- control plane too and stay empty there.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE inventory_assets (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID        NOT NULL,
    kind       TEXT        NOT NULL,
    key        TEXT        NOT NULL,
    name       TEXT        NOT NULL,
    subtype    TEXT        NOT NULL DEFAULT '',
    parent_key TEXT        NOT NULL DEFAULT '',
    node_key   TEXT        NOT NULL DEFAULT '',
    cluster    TEXT        NOT NULL DEFAULT '',
    namespace  TEXT        NOT NULL DEFAULT '',
    node       TEXT        NOT NULL DEFAULT '',
    attributes JSONB       NOT NULL DEFAULT '{}',
    sources    JSONB       NOT NULL DEFAULT '[]',
    first_seen TIMESTAMPTZ NOT NULL,
    last_seen  TIMESTAMPTZ NOT NULL,
    state      TEXT        NOT NULL,
    health     TEXT        NOT NULL,
    reasons    JSONB       NOT NULL DEFAULT '[]',
    metrics    JSONB       NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE inventory_assets
    ADD CONSTRAINT fk_inventory_assets_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;

CREATE UNIQUE INDEX idx_inventory_assets_project_key ON inventory_assets (project_id, key);

CREATE INDEX idx_inventory_assets_project_parent ON inventory_assets (project_id, parent_key);

CREATE TABLE inventory_changes (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID        NOT NULL,
    asset_key  TEXT        NOT NULL,
    asset_name TEXT        NOT NULL,
    kind       TEXT        NOT NULL,
    change     TEXT        NOT NULL,
    from_value TEXT        NOT NULL DEFAULT '',
    to_value   TEXT        NOT NULL DEFAULT '',
    detail     TEXT        NOT NULL DEFAULT '',
    at         TIMESTAMPTZ NOT NULL
);

ALTER TABLE inventory_changes
    ADD CONSTRAINT fk_inventory_changes_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;

CREATE INDEX idx_inventory_changes_project_at ON inventory_changes (project_id, at DESC);

CREATE INDEX idx_inventory_changes_asset ON inventory_changes (project_id, asset_key, at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS inventory_changes;
DROP TABLE IF EXISTS inventory_assets;
-- +goose StatementEnd
