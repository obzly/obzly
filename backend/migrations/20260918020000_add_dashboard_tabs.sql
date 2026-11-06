-- +goose Up
-- +goose StatementBegin

-- Replaces the flat `panels` column with `tabs` - a dashboard now groups
-- panels into one or more tabs (OpenObserve-style). Every existing
-- dashboard's panels move into a single "Default" tab so nothing currently
-- deployed loses data.
ALTER TABLE dashboards ADD COLUMN tabs JSONB NOT NULL DEFAULT '[]';

UPDATE dashboards
SET tabs = jsonb_build_array(
    jsonb_build_object(
        'id', gen_random_uuid()::text,
        'title', 'Default',
        'panels', panels
    )
);

ALTER TABLE dashboards DROP COLUMN panels;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE dashboards ADD COLUMN panels JSONB NOT NULL DEFAULT '[]';

UPDATE dashboards
SET panels = COALESCE(tabs->0->'panels', '[]'::jsonb);

ALTER TABLE dashboards DROP COLUMN tabs;

-- +goose StatementEnd
