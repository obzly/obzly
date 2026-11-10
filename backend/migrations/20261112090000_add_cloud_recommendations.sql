-- +goose Up
-- +goose StatementBegin
-- Chunks of an advice batch until all have arrived (Site).
CREATE TABLE cost_advice_batches (
    batch_id    TEXT        NOT NULL,
    seq         INTEGER     NOT NULL,
    project_id  UUID        NOT NULL,
    total       INTEGER     NOT NULL,
    payload     JSONB       NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (batch_id, seq)
);
-- +goose StatementEnd

-- +goose StatementBegin
-- How each part of a connection's last advice refresh went; a part that isn't ok keeps its
-- last good data.
CREATE TABLE cost_advice_sections (
    project_id    UUID        NOT NULL,
    connection_id UUID        NOT NULL,
    section       TEXT        NOT NULL,
    state         TEXT        NOT NULL,
    reason        TEXT        NOT NULL DEFAULT '',
    refreshed_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (project_id, connection_id, section)
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE cost_advice_sections
    ADD CONSTRAINT fk_cost_advice_sections_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
-- The latest recommendations per connection, replaced on each refresh. Savings are the
-- provider's estimate, or worked out from the resource's own cost (savings_share) when NULL.
CREATE TABLE cost_recommendations (
    project_id      UUID          NOT NULL,
    connection_id   UUID          NOT NULL,
    key             TEXT          NOT NULL,
    connection_name TEXT          NOT NULL,
    provider        TEXT          NOT NULL,
    source          TEXT          NOT NULL,
    kind            TEXT          NOT NULL,
    account         TEXT          NOT NULL DEFAULT '',
    region          TEXT          NOT NULL DEFAULT '',
    resource_key    TEXT          NOT NULL,
    resource_name   TEXT          NOT NULL DEFAULT '',
    resource_type   TEXT          NOT NULL DEFAULT '',
    action          TEXT          NOT NULL,
    current_config  TEXT          NOT NULL DEFAULT '',
    target_config   TEXT          NOT NULL DEFAULT '',
    currency        TEXT          NOT NULL DEFAULT '',
    monthly_savings NUMERIC(20,6),
    savings_share   DOUBLE PRECISION NOT NULL DEFAULT 0,
    effort          TEXT          NOT NULL DEFAULT 'medium',
    risk            TEXT          NOT NULL DEFAULT 'low',
    evidence        JSONB         NOT NULL DEFAULT '{}',
    refreshed_at    TIMESTAMPTZ   NOT NULL,
    PRIMARY KEY (project_id, connection_id, key)
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE cost_recommendations
    ADD CONSTRAINT fk_cost_recommendations_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_cost_recommendations_resource ON cost_recommendations (project_id, resource_key);
-- +goose StatementEnd

-- +goose StatementBegin
-- What people decided about a recommendation, by its stable key, so it survives refreshes;
-- a done one keeps what it was expected to save and what it did save.
CREATE TABLE cost_recommendation_states (
    project_id       UUID          NOT NULL,
    key              TEXT          NOT NULL,
    status           TEXT          NOT NULL,
    owner            TEXT          NOT NULL DEFAULT '',
    due_date         DATE,
    reason           TEXT          NOT NULL DEFAULT '',
    action           TEXT          NOT NULL DEFAULT '',
    resource_key     TEXT          NOT NULL DEFAULT '',
    resource_name    TEXT          NOT NULL DEFAULT '',
    currency         TEXT          NOT NULL DEFAULT '',
    expected_monthly NUMERIC(20,6),
    done_at          TIMESTAMPTZ,
    done_by          TEXT          NOT NULL DEFAULT '',
    realized_monthly NUMERIC(20,6),
    measured_at      TIMESTAMPTZ,
    updated_by       TEXT          NOT NULL DEFAULT '',
    updated_at       TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    PRIMARY KEY (project_id, key)
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE cost_recommendation_states
    ADD CONSTRAINT fk_cost_recommendation_states_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE cost_recommendation_states
    ADD CONSTRAINT chk_cost_recommendation_states_status
    CHECK (status IN ('open', 'planned', 'done', 'dismissed'));
-- +goose StatementEnd

-- +goose StatementBegin
-- Existing Savings Plans and reservations per connection, replaced on each refresh.
CREATE TABLE cost_commitments (
    project_id      UUID          NOT NULL,
    connection_id   UUID          NOT NULL,
    id              TEXT          NOT NULL,
    connection_name TEXT          NOT NULL,
    provider        TEXT          NOT NULL,
    type            TEXT          NOT NULL,
    account         TEXT          NOT NULL DEFAULT '',
    region          TEXT          NOT NULL DEFAULT '',
    description     TEXT          NOT NULL,
    term            TEXT          NOT NULL DEFAULT '',
    payment         TEXT          NOT NULL DEFAULT '',
    starts_at       TIMESTAMPTZ,
    ends_at         TIMESTAMPTZ,
    currency        TEXT          NOT NULL DEFAULT '',
    monthly_cost    NUMERIC(20,6),
    utilization     DOUBLE PRECISION,
    unused_monthly  NUMERIC(20,6),
    refreshed_at    TIMESTAMPTZ   NOT NULL,
    PRIMARY KEY (project_id, connection_id, id)
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE cost_commitments
    ADD CONSTRAINT fk_cost_commitments_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE cost_commitment_coverage (
    project_id        UUID             NOT NULL,
    connection_id     UUID             NOT NULL,
    type              TEXT             NOT NULL,
    currency          TEXT             NOT NULL DEFAULT '',
    covered_percent   DOUBLE PRECISION NOT NULL,
    on_demand_monthly NUMERIC(20,6),
    refreshed_at      TIMESTAMPTZ      NOT NULL,
    PRIMARY KEY (project_id, connection_id, type)
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE cost_commitment_coverage
    ADD CONSTRAINT fk_cost_commitment_coverage_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
-- The providers' purchase recommendations per type, term, payment and look-back.
CREATE TABLE cost_purchase_options (
    project_id          UUID             NOT NULL,
    connection_id       UUID             NOT NULL,
    type                TEXT             NOT NULL,
    term                TEXT             NOT NULL,
    payment             TEXT             NOT NULL,
    lookback_days       INTEGER          NOT NULL,
    connection_name     TEXT             NOT NULL,
    provider            TEXT             NOT NULL,
    offered             BOOLEAN          NOT NULL,
    reason              TEXT             NOT NULL DEFAULT '',
    currency            TEXT             NOT NULL DEFAULT '',
    commitment          TEXT             NOT NULL DEFAULT '',
    hourly_commitment   NUMERIC(20,6),
    upfront             NUMERIC(20,6),
    monthly_recurring   NUMERIC(20,6),
    monthly_savings     NUMERIC(20,6),
    on_demand_monthly   NUMERIC(20,6),
    savings_percent     DOUBLE PRECISION NOT NULL DEFAULT 0,
    break_even_months   DOUBLE PRECISION NOT NULL DEFAULT 0,
    average_utilization DOUBLE PRECISION NOT NULL DEFAULT 0,
    coverage_percent    DOUBLE PRECISION NOT NULL DEFAULT 0,
    lines               JSONB            NOT NULL DEFAULT '[]',
    refreshed_at        TIMESTAMPTZ      NOT NULL,
    PRIMARY KEY (project_id, connection_id, type, term, payment, lookback_days)
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE cost_purchase_options
    ADD CONSTRAINT fk_cost_purchase_options_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
-- Posture actions in a plan or dismissed, by their stable key. finding_ids are the findings the
-- action had when it was planned: it's done when they resolve.
CREATE TABLE posture_action_states (
    project_id  UUID        NOT NULL,
    key         TEXT        NOT NULL,
    title       TEXT        NOT NULL DEFAULT '',
    planned     BOOLEAN     NOT NULL DEFAULT FALSE,
    dismissed   BOOLEAN     NOT NULL DEFAULT FALSE,
    reason      TEXT        NOT NULL DEFAULT '',
    finding_ids JSONB       NOT NULL DEFAULT '[]',
    updated_by  TEXT        NOT NULL DEFAULT '',
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (project_id, key)
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE posture_action_states
    ADD CONSTRAINT fk_posture_action_states_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS posture_action_states;
DROP TABLE IF EXISTS cost_purchase_options;
DROP TABLE IF EXISTS cost_commitment_coverage;
DROP TABLE IF EXISTS cost_commitments;
DROP TABLE IF EXISTS cost_recommendation_states;
DROP TABLE IF EXISTS cost_recommendations;
DROP TABLE IF EXISTS cost_advice_sections;
DROP TABLE IF EXISTS cost_advice_batches;
-- +goose StatementEnd
