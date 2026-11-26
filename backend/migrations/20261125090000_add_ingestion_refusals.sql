-- +goose Up
-- +goose StatementBegin
-- Ingestion status: data a receiver refused, per project, hour, signal and
-- reason (a logs_core error code), so the status page can say what was
-- refused and whether the sender retries it. Kept a week.
CREATE TABLE ingestion_refusals (
    project_id UUID        NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    hour       TIMESTAMPTZ NOT NULL,
    signal     TEXT        NOT NULL,
    reason     TEXT        NOT NULL,
    count      BIGINT      NOT NULL,
    last_at    TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (project_id, hour, signal, reason)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_ingestion_refusals_hour ON ingestion_refusals (hour);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE ingestion_refusals;
-- +goose StatementEnd
