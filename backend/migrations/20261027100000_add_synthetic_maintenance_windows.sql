-- Synthetics maintenance windows live on the control plane (empty on Sites);
-- a Site keeps their occurrences as managed silences.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE synthetic_maintenance_windows (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT        NOT NULL,
    reason      TEXT        NOT NULL DEFAULT '',
    scope       JSONB       NOT NULL DEFAULT '{}',
    schedule    JSONB       NOT NULL,
    adjustments JSONB       NOT NULL DEFAULT '[]',
    enabled     BOOLEAN     NOT NULL DEFAULT TRUE,
    version     INTEGER     NOT NULL DEFAULT 1,
    created_by  UUID,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX idx_synthetic_maintenance_windows_name ON synthetic_maintenance_windows (lower(name));
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE silences
    ADD COLUMN managed_by TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE silences DROP COLUMN managed_by;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE synthetic_maintenance_windows;
-- +goose StatementEnd
