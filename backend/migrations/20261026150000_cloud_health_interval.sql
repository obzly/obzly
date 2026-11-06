-- +goose Up
-- +goose StatementBegin
ALTER TABLE cloud_connections
    ADD COLUMN health_interval_seconds INTEGER NOT NULL DEFAULT 900;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE cloud_connections
    ALTER COLUMN interval_seconds SET DEFAULT 14400;
-- +goose StatementEnd

-- Full discovery every 4 hours unless an admin chose otherwise; health
-- scans keep resources fresh in between.
-- +goose StatementBegin
UPDATE cloud_connections SET interval_seconds = 14400 WHERE interval_seconds = 900;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE cloud_connections
    ALTER COLUMN interval_seconds SET DEFAULT 900;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE cloud_connections DROP COLUMN health_interval_seconds;
-- +goose StatementEnd
