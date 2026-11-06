-- +goose Up
-- +goose StatementBegin
-- Label index (Phase 14 item 5c): which label values and metric names a
-- project's series used, and when. Serves label names/values and metric
-- names without reading any segment; never lists series (that's the
-- metrics_series stream). Written from each series flush.
CREATE TABLE metric_label_index (
    project_id  UUID        NOT NULL,
    metric_name TEXT        NOT NULL,
    label_name  TEXT        NOT NULL,
    label_value TEXT        NOT NULL,
    first_seen  TIMESTAMPTZ NOT NULL,
    last_seen   TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (project_id, metric_name, label_name, label_value)
);
-- +goose StatementEnd

-- +goose StatementBegin
-- Label names and values across every metric of a project.
CREATE INDEX idx_metric_label_index_label
    ON metric_label_index (project_id, label_name, label_value);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE metric_name_index (
    project_id  UUID        NOT NULL,
    metric_name TEXT        NOT NULL,
    metric_type TEXT        NOT NULL,
    first_seen  TIMESTAMPTZ NOT NULL,
    last_seen   TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (project_id, metric_name)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE metric_name_index;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE metric_label_index;
-- +goose StatementEnd
