-- +goose Up
-- +goose StatementBegin
CREATE TABLE inventory_counts_hourly (
    project_id UUID        NOT NULL,
    hour       TIMESTAMPTZ NOT NULL,
    kind       TEXT        NOT NULL,
    health     TEXT        NOT NULL,
    count      BIGINT      NOT NULL,
    PRIMARY KEY (project_id, hour, kind, health)
);

ALTER TABLE inventory_counts_hourly
    ADD CONSTRAINT fk_inventory_counts_hourly_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;

CREATE INDEX idx_inventory_counts_hourly_hour ON inventory_counts_hourly (hour);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS inventory_counts_hourly;
-- +goose StatementEnd
