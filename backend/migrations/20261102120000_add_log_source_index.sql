-- +goose Up
-- +goose StatementBegin
-- Log sources: each combination of resource attributes (service, namespace,
-- workload, container, pod, node, cluster, host) a project's logs came from,
-- and when. Lets the Logs sidebar offer values that logged recently but not
-- in the search window. Written from each logs segment flush.
CREATE TABLE log_source_index (
    project_id UUID        NOT NULL,
    source_key TEXT        NOT NULL,
    attributes JSONB       NOT NULL,
    first_seen TIMESTAMPTZ NOT NULL,
    last_seen  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (project_id, source_key)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_log_source_index_last_seen
    ON log_source_index (project_id, last_seen);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE log_source_index;
-- +goose StatementEnd
