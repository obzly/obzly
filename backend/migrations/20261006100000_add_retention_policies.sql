-- +goose Up
-- +goose StatementBegin
CREATE TABLE retention_policies (
    project_id     UUID             NOT NULL,
    signal         TEXT             NOT NULL,
    retention_days INT              NOT NULL DEFAULT 0,
    rollup_days    INT              NOT NULL DEFAULT 0,
    sample_rate    DOUBLE PRECISION NOT NULL DEFAULT 1,
    slow_ms        INT              NOT NULL DEFAULT 0,
    sample_rules   JSONB            NOT NULL DEFAULT '[]',
    updated_at     TIMESTAMPTZ      NOT NULL DEFAULT NOW(),
    PRIMARY KEY (project_id, signal)
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE retention_policies
    ADD CONSTRAINT fk_retention_policies_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE retention_policies
    ADD CONSTRAINT chk_retention_policies_values
    CHECK (signal IN ('logs', 'traces', 'metrics')
        AND retention_days >= 0 AND rollup_days >= 0 AND slow_ms >= 0
        AND sample_rate >= 0 AND sample_rate <= 1);
-- +goose StatementEnd

-- metric_rollup_life_days (added unreleased, 20261006090000) moves here.
-- +goose StatementBegin
INSERT INTO retention_policies (project_id, signal, rollup_days)
SELECT id, 'metrics', metric_rollup_life_days FROM projects WHERE metric_rollup_life_days > 0;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE projects DROP COLUMN metric_rollup_life_days;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE projects
    ADD COLUMN metric_rollup_life_days INT NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose StatementBegin
UPDATE projects p SET metric_rollup_life_days = r.rollup_days
FROM retention_policies r WHERE r.project_id = p.id AND r.signal = 'metrics';
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE retention_policies;
-- +goose StatementEnd
