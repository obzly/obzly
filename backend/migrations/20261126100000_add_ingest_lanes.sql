-- +goose Up
-- +goose StatementBegin
-- Ingest lanes (docs/INGEST_LANES.md): each project's lane count per signal,
-- set by the lane controller. Only projects with two or more lanes have a
-- row. Kept here rather than only in Valkey: Valkey restarting empty in a
-- node-kill drill put a busy project back on one lane, whose shard then
-- built a 919-batch backlog. No foreign key, as stream_segments: a deleted
-- project's row fades out as the controller shrinks it.
CREATE TABLE ingest_lanes (
    signal     TEXT        NOT NULL,
    project_id UUID        NOT NULL,
    lanes      INT         NOT NULL CHECK (lanes >= 2),
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (signal, project_id)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE ingest_lanes;
-- +goose StatementEnd
