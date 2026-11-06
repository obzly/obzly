-- +goose Up
-- +goose StatementBegin

-- Lets a user pin a dashboard so it sorts to the top of the project's
-- dashboard list, mirroring OpenObserve's dashboard "pin" toolbar action.
ALTER TABLE dashboards ADD COLUMN is_pinned BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE dashboards DROP COLUMN IF EXISTS is_pinned;

-- +goose StatementEnd
