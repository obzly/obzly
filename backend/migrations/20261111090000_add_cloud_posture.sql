-- +goose Up
-- +goose StatementBegin
-- The posture use's settings per connection (control plane); NULL without the use.
ALTER TABLE cloud_connections ADD COLUMN posture_settings JSONB;
-- +goose StatementEnd

-- +goose StatementBegin
-- One row per connection, control and resource (Site): a finding keeps its identity, and its
-- history, when its source changes between native and baseline.
CREATE TABLE posture_findings (
    id                 UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id         UUID        NOT NULL,
    connection_id      UUID        NOT NULL,
    connection_name    TEXT        NOT NULL,
    provider           TEXT        NOT NULL,
    account            TEXT        NOT NULL,
    region             TEXT        NOT NULL,
    source             TEXT        NOT NULL,
    control            TEXT        NOT NULL,
    check_id           TEXT        NOT NULL DEFAULT '',
    title              TEXT        NOT NULL,
    severity           TEXT        NOT NULL,
    resource_key       TEXT        NOT NULL,
    resource_type      TEXT        NOT NULL DEFAULT '',
    standards          JSONB       NOT NULL DEFAULT '[]',
    evidence           JSONB       NOT NULL DEFAULT '{}',
    remediation_url    TEXT        NOT NULL DEFAULT '',
    status             TEXT        NOT NULL,
    first_seen         TIMESTAMPTZ NOT NULL,
    last_seen          TIMESTAMPTZ NOT NULL,
    resolved_at        TIMESTAMPTZ,
    -- Suppressed findings stay tracked but are left out of scores and alerts until they expire.
    suppressed         BOOLEAN     NOT NULL DEFAULT FALSE,
    suppress_reason    TEXT        NOT NULL DEFAULT '',
    suppressed_until   TIMESTAMPTZ,
    suppressed_by      TEXT        NOT NULL DEFAULT ''
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE posture_findings
    ADD CONSTRAINT fk_posture_findings_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE posture_findings
    ADD CONSTRAINT chk_posture_findings_values
    CHECK (status IN ('open', 'resolved') AND source IN ('native', 'baseline')
        AND severity IN ('critical', 'high', 'medium', 'low', 'informational'));
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX idx_posture_findings_key
    ON posture_findings (project_id, connection_id, control, resource_key);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_posture_findings_open ON posture_findings (project_id, status, severity);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_posture_findings_resource ON posture_findings (project_id, resource_key);
-- +goose StatementEnd

-- +goose StatementBegin
-- A finding's history: opened, resolved, reopened, suppressed, unsuppressed (by hand or on
-- expiry). Trends count open findings at any time from it.
CREATE TABLE posture_finding_events (
    id          BIGSERIAL   PRIMARY KEY,
    finding_id  UUID        NOT NULL,
    project_id  UUID        NOT NULL,
    kind        TEXT        NOT NULL,
    severity    TEXT        NOT NULL,
    at          TIMESTAMPTZ NOT NULL,
    by_user     TEXT        NOT NULL DEFAULT '',
    note        TEXT        NOT NULL DEFAULT ''
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE posture_finding_events
    ADD CONSTRAINT fk_posture_finding_events_finding_id
    FOREIGN KEY (finding_id)
    REFERENCES posture_findings (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_posture_finding_events_project ON posture_finding_events (project_id, at);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_posture_finding_events_finding ON posture_finding_events (finding_id, at);
-- +goose StatementEnd

-- +goose StatementBegin
-- The latest read of each scope from each source: what Coverage shows, and what resolves
-- findings (only an ok run does).
CREATE TABLE posture_scope_runs (
    project_id      UUID        NOT NULL,
    connection_id   UUID        NOT NULL,
    connection_name TEXT        NOT NULL,
    provider        TEXT        NOT NULL,
    account         TEXT        NOT NULL,
    region          TEXT        NOT NULL,
    standard        TEXT        NOT NULL,
    source          TEXT        NOT NULL,
    result          TEXT        NOT NULL,
    reason          TEXT        NOT NULL DEFAULT '',
    passed          JSONB       NOT NULL DEFAULT '[]',
    failed          JSONB       NOT NULL DEFAULT '[]',
    scanned_at      TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (project_id, connection_id, account, region, standard, source)
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE posture_scope_runs
    ADD CONSTRAINT fk_posture_scope_runs_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
-- A standard's score per connection and day: passed and failed controls, for Trends.
CREATE TABLE posture_scores_daily (
    project_id    UUID    NOT NULL,
    connection_id UUID    NOT NULL,
    standard      TEXT    NOT NULL,
    day           DATE    NOT NULL,
    passed        INTEGER NOT NULL,
    failed        INTEGER NOT NULL,
    PRIMARY KEY (project_id, connection_id, standard, day)
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE posture_scores_daily
    ADD CONSTRAINT fk_posture_scores_daily_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
-- Chunks of a posture batch until all have arrived.
CREATE TABLE posture_batches (
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
-- Where a project's new or reopened findings are sent (a managed alert rule, source cloud_posture).
CREATE TABLE posture_alert_settings (
    project_id   UUID        PRIMARY KEY,
    channel_ids  JSONB       NOT NULL DEFAULT '[]',
    min_severity TEXT        NOT NULL DEFAULT 'high',
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE posture_alert_settings
    ADD CONSTRAINT fk_posture_alert_settings_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE posture_alert_settings
    ADD CONSTRAINT chk_posture_alert_settings_severity
    CHECK (min_severity IN ('critical', 'high', 'medium', 'low'));
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE alert_rules DROP CONSTRAINT chk_alert_rules_values;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE alert_rules
    ADD CONSTRAINT chk_alert_rules_values
    CHECK (source IN ('metrics', 'logs', 'traces', 'log_patterns', 'ai_cost', 'cloud_cost', 'cloud_posture')
        AND comparison IN ('>', '>=', '<', '<=', '==', '!=')
        AND window_seconds > 0 AND interval_seconds >= 15 AND for_seconds >= 0 AND repeat_seconds >= 0);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM alert_rules WHERE source = 'cloud_posture';
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE alert_rules DROP CONSTRAINT chk_alert_rules_values;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE alert_rules
    ADD CONSTRAINT chk_alert_rules_values
    CHECK (source IN ('metrics', 'logs', 'traces', 'log_patterns', 'ai_cost', 'cloud_cost')
        AND comparison IN ('>', '>=', '<', '<=', '==', '!=')
        AND window_seconds > 0 AND interval_seconds >= 15 AND for_seconds >= 0 AND repeat_seconds >= 0);
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS posture_alert_settings;
DROP TABLE IF EXISTS posture_batches;
DROP TABLE IF EXISTS posture_scores_daily;
DROP TABLE IF EXISTS posture_scope_runs;
DROP TABLE IF EXISTS posture_finding_events;
DROP TABLE IF EXISTS posture_findings;
ALTER TABLE cloud_connections DROP COLUMN IF EXISTS posture_settings;
-- +goose StatementEnd
