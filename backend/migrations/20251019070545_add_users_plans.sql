-- +goose Up
-- +goose StatementBegin
CREATE TABLE user_plans (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                     TEXT NOT NULL,
    type                     TEXT NOT NULL,
    is_public                BOOLEAN NOT NULL DEFAULT TRUE,
    warning_text             TEXT,
    upgrade_text             TEXT,
    logs_per_second_limit    INT NOT NULL,
    max_logs_amount          BIGINT NOT NULL,
    max_logs_size_mb         INT NOT NULL,
    max_logs_life_days       INT NOT NULL,
    max_log_size_kb          INT NOT NULL,
    allowed_projects_count   INT NOT NULL
);

ALTER TABLE users ADD COLUMN plan_id UUID;

ALTER TABLE users
    ADD CONSTRAINT fk_users_plan_id
    FOREIGN KEY (plan_id)
    REFERENCES user_plans (id);

CREATE INDEX idx_users_plan_id ON users(plan_id);

ALTER TABLE projects ADD COLUMN plan_id UUID;

ALTER TABLE projects
    ADD CONSTRAINT fk_projects_plan_id
    FOREIGN KEY (plan_id)
    REFERENCES user_plans (id);

CREATE INDEX idx_projects_plan_id ON projects(plan_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_projects_plan_id;

ALTER TABLE projects DROP CONSTRAINT IF EXISTS fk_projects_plan_id;

ALTER TABLE projects DROP COLUMN IF EXISTS plan_id;

DROP INDEX IF EXISTS idx_users_plan_id;

ALTER TABLE users DROP CONSTRAINT IF EXISTS fk_users_plan_id;

ALTER TABLE users DROP COLUMN IF EXISTS plan_id;

DROP TABLE IF EXISTS user_plans;
-- +goose StatementEnd
