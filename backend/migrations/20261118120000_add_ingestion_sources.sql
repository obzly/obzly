-- +goose Up
-- +goose StatementBegin
-- Ingestion status: each source sending to a project (identified from its
-- resource attributes) per signal, and when it was seen. Written by the
-- receiving replicas every few seconds at accept time, so unlike
-- log_source_index (written on segment flush) it is current.
CREATE TABLE ingestion_sources (
    project_id       UUID        NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    source_key       TEXT        NOT NULL,
    signal           TEXT        NOT NULL,
    identified_by    TEXT        NOT NULL,
    data_source      TEXT        NOT NULL DEFAULT '',
    data_source_rank SMALLINT    NOT NULL DEFAULT 0,
    attributes       JSONB       NOT NULL,
    first_seen       TIMESTAMPTZ NOT NULL,
    last_seen        TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (project_id, source_key, signal)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_ingestion_sources_last_seen ON ingestion_sources (project_id, last_seen);
-- +goose StatementEnd

-- +goose StatementBegin
-- Per-minute accepted counts per source and signal, kept about an hour: the
-- status page's recent rates.
CREATE TABLE ingestion_source_minutes (
    project_id UUID        NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    minute     TIMESTAMPTZ NOT NULL,
    source_key TEXT        NOT NULL,
    signal     TEXT        NOT NULL,
    count      BIGINT      NOT NULL,
    PRIMARY KEY (project_id, minute, source_key, signal)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_ingestion_source_minutes_minute ON ingestion_source_minutes (minute);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE ingestion_source_minutes;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE ingestion_sources;
-- +goose StatementEnd
