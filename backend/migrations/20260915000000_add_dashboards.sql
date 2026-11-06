-- +goose Up
-- +goose StatementBegin

-- Dashboards (ARCHITECTURE.md, Phase 4). One row per dashboard; panels are
-- stored as a single JSONB document (dashboards_models.Panels) rather than
-- a child table, since a dashboard's panel list is always read/written as
-- one unit and never queried into server-side.
CREATE TABLE dashboards (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id        UUID NOT NULL,
    title             TEXT NOT NULL,
    description       TEXT NOT NULL DEFAULT '',
    panels            JSONB NOT NULL DEFAULT '[]',
    -- Marks the one auto-generated "Overview" dashboard a project can have
    -- (dashboards_services.GenerateOverview) — regeneration replaces this
    -- row rather than creating a duplicate.
    is_auto_generated BOOLEAN NOT NULL DEFAULT FALSE,
    created_by        UUID,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_dashboards_project_id ON dashboards (project_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS dashboards;

-- +goose StatementEnd
