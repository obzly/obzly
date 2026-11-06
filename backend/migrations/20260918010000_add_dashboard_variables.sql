-- +goose Up
-- +goose StatementBegin

-- Dashboard-author-defined template variables (OpenObserve-style: e.g.
-- "Cluster"/"Namespace" dropdowns above the panel grid, substituted into
-- every panel's query as ${key}) - stored the same way Panels already is,
-- one JSONB document per dashboard.
ALTER TABLE dashboards ADD COLUMN variables JSONB NOT NULL DEFAULT '[]';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE dashboards DROP COLUMN IF EXISTS variables;

-- +goose StatementEnd
