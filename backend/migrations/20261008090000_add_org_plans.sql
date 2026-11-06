-- +goose Up
-- +goose StatementBegin
-- An organization's plan (control plane and standalone installs): quotas a
-- Site enforces on its projects, and Site/member counts the control plane
-- enforces. 0 is unlimited; an organization without a row has no plan.
CREATE TABLE org_plans (
    org_id                UUID        PRIMARY KEY,
    logs_per_second_limit INTEGER     NOT NULL DEFAULT 0,
    max_logs_amount       BIGINT      NOT NULL DEFAULT 0,
    max_logs_size_mb      INTEGER     NOT NULL DEFAULT 0,
    max_logs_life_days    INTEGER     NOT NULL DEFAULT 0,
    max_log_size_kb       INTEGER     NOT NULL DEFAULT 0,
    max_projects          INTEGER     NOT NULL DEFAULT 0,
    max_sites             INTEGER     NOT NULL DEFAULT 0,
    max_members           INTEGER     NOT NULL DEFAULT 0,
    updated_by            UUID,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE org_plans
    ADD CONSTRAINT fk_org_plans_org_id
    FOREIGN KEY (org_id)
    REFERENCES organizations (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE org_plans
    ADD CONSTRAINT chk_org_plans_non_negative
    CHECK (logs_per_second_limit >= 0 AND max_logs_amount >= 0 AND max_logs_size_mb >= 0
        AND max_logs_life_days >= 0 AND max_log_size_kb >= 0 AND max_projects >= 0
        AND max_sites >= 0 AND max_members >= 0);
-- +goose StatementEnd

-- +goose StatementBegin
-- A Site's copy of the plan its control plane last published for it (one
-- row), kept so the plan holds across restarts and control-plane outages.
CREATE TABLE site_org_plan (
    id         BOOLEAN     PRIMARY KEY DEFAULT TRUE,
    org_id     UUID,
    limits     JSONB       NOT NULL,
    issued_at  TIMESTAMPTZ NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE site_org_plan
    ADD CONSTRAINT chk_site_org_plan_single_row
    CHECK (id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE site_org_plan;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE org_plans;
-- +goose StatementEnd
