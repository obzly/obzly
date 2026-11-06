-- +goose Up
-- +goose StatementBegin

-- Existing segments store OTel names as sent (dotted keys, "resource."
-- prefix, uppercase levels): version 1. New segments use normalized names
-- and lowercase levels: version 2. Queries read version 1 through a
-- rewrite until the data migration has converted them.
ALTER TABLE stream_segments
    ADD COLUMN schema_version INT NOT NULL DEFAULT 1;

ALTER TABLE stream_segments
    ALTER COLUMN schema_version SET DEFAULT 2;

CREATE INDEX idx_stream_segments_schema_version ON stream_segments (schema_version)
    WHERE schema_version < 2;

-- One-time data migrations run by the application (not goose), by name.
CREATE TABLE data_migrations (
    name         TEXT PRIMARY KEY,
    completed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE data_migrations;
DROP INDEX idx_stream_segments_schema_version;
ALTER TABLE stream_segments DROP COLUMN schema_version;

-- +goose StatementEnd
