-- +goose Up
-- Cloud cost (docs/CLOUD_COST_POSTURE.md "Cost"). One migration set for every SERVICE_MODE:
-- the control plane uses cloud_connections.cost_settings, cloud_cost_settings, saved_views and
-- user_default_views; Sites use the cloud_cost_* tables, cost_budgets and cost_anomalies.

-- +goose StatementBegin
ALTER TABLE cloud_connections ADD COLUMN cost_settings JSONB;
-- +goose StatementEnd

-- +goose StatementBegin
-- One row: the tag keys kept on cost rows, sent to every probe.
CREATE TABLE cloud_cost_settings (
    id           SMALLINT    PRIMARY KEY DEFAULT 1,
    tracked_tags JSONB       NOT NULL DEFAULT '[]',
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE cloud_cost_settings ADD CONSTRAINT chk_cloud_cost_settings_single CHECK (id = 1);
-- +goose StatementEnd

-- +goose StatementBegin
-- Saved Cost Explorer and Posture Trends views: private, or shared read-only with everyone.
CREATE TABLE saved_views (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id   UUID        NOT NULL,
    kind       TEXT        NOT NULL,
    name       TEXT        NOT NULL,
    shared     BOOLEAN     NOT NULL DEFAULT FALSE,
    state      JSONB       NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE saved_views
    ADD CONSTRAINT fk_saved_views_owner_id
    FOREIGN KEY (owner_id)
    REFERENCES users (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE saved_views ADD CONSTRAINT chk_saved_views_kind CHECK (kind IN ('cost', 'posture'));
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX idx_saved_views_owner_kind_name ON saved_views (owner_id, kind, lower(name));
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE user_default_views (
    user_id UUID NOT NULL,
    kind    TEXT NOT NULL,
    view_id UUID NOT NULL,
    PRIMARY KEY (user_id, kind)
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE user_default_views
    ADD CONSTRAINT fk_user_default_views_user_id
    FOREIGN KEY (user_id)
    REFERENCES users (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE user_default_views
    ADD CONSTRAINT fk_user_default_views_view_id
    FOREIGN KEY (view_id)
    REFERENCES saved_views (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
-- Daily cost in FOCUS terms, aggregated by the probe. A day of a connection holds rows of one
-- source only: export rows replace api rows for the days they cover.
CREATE TABLE cloud_cost_daily (
    project_id       UUID          NOT NULL,
    connection_id    UUID          NOT NULL,
    source           TEXT          NOT NULL,
    day              DATE          NOT NULL,
    provider         TEXT          NOT NULL,
    billing_account  TEXT          NOT NULL DEFAULT '',
    sub_account      TEXT          NOT NULL DEFAULT '',
    sub_account_name TEXT          NOT NULL DEFAULT '',
    service          TEXT          NOT NULL DEFAULT '',
    region           TEXT          NOT NULL DEFAULT '',
    resource         TEXT          NOT NULL DEFAULT '',
    charge_category  TEXT          NOT NULL DEFAULT '',
    pricing_category TEXT          NOT NULL DEFAULT '',
    currency         TEXT          NOT NULL,
    billed           NUMERIC(20,6) NOT NULL,
    effective        NUMERIC(20,6) NOT NULL,
    list             NUMERIC(20,6) NOT NULL DEFAULT 0,
    tags             JSONB         NOT NULL DEFAULT '{}',
    estimated        BOOLEAN       NOT NULL DEFAULT FALSE
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE cloud_cost_daily
    ADD CONSTRAINT fk_cloud_cost_daily_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE cloud_cost_daily ADD CONSTRAINT chk_cloud_cost_daily_source CHECK (source IN ('export', 'api'));
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_cloud_cost_daily_project_day ON cloud_cost_daily (project_id, day);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_cloud_cost_daily_connection ON cloud_cost_daily (connection_id, source, day);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_cloud_cost_daily_resource ON cloud_cost_daily (project_id, resource, day) WHERE resource <> '';
-- +goose StatementEnd

-- +goose StatementBegin
-- Chunks of a batch wait here until all have arrived; then they replace the batch's days at once.
CREATE TABLE cloud_cost_batches (
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
ALTER TABLE cloud_cost_batches
    ADD CONSTRAINT fk_cloud_cost_batches_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
-- What each connection and source last delivered.
CREATE TABLE cloud_cost_sources (
    project_id      UUID        NOT NULL,
    connection_id   UUID        NOT NULL,
    source          TEXT        NOT NULL,
    connection_name TEXT        NOT NULL,
    provider        TEXT        NOT NULL,
    from_day        DATE,
    to_day          DATE,
    final_through   DATE,
    rows            INTEGER     NOT NULL DEFAULT 0,
    -- covered_from..covered_to: every day this source has delivered, across batches; days outside
    -- it are shown as n/a, not 0.
    covered_from    DATE,
    covered_to      DATE,
    delivered_at    TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (project_id, connection_id, source)
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE cloud_cost_sources
    ADD CONSTRAINT fk_cloud_cost_sources_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE cloud_cost_forecasts (
    project_id    UUID          NOT NULL,
    connection_id UUID          NOT NULL,
    month         DATE          NOT NULL,
    currency      TEXT          NOT NULL,
    actual        NUMERIC(20,6) NOT NULL,
    remaining     NUMERIC(20,6) NOT NULL,
    updated_at    TIMESTAMPTZ   NOT NULL,
    PRIMARY KEY (project_id, connection_id, month, currency)
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE cloud_cost_forecasts
    ADD CONSTRAINT fk_cloud_cost_forecasts_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
-- Monthly budgets in one currency, over the project's cost with optional filters; each keeps a
-- managed alert rule (source cloud_cost).
CREATE TABLE cost_budgets (
    id                UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id        UUID          NOT NULL,
    name              TEXT          NOT NULL,
    filters           JSONB         NOT NULL DEFAULT '{}',
    currency          TEXT          NOT NULL,
    amount            NUMERIC(20,6) NOT NULL,
    threshold_percent INTEGER       NOT NULL DEFAULT 80,
    channel_ids       JSONB         NOT NULL DEFAULT '[]',
    created_by        UUID,
    created_at        TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE cost_budgets
    ADD CONSTRAINT fk_cost_budgets_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE cost_budgets
    ADD CONSTRAINT chk_cost_budgets_values
    CHECK (amount > 0 AND threshold_percent BETWEEN 1 AND 100);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX idx_cost_budgets_project_name ON cost_budgets (project_id, lower(name));
-- +goose StatementEnd

-- +goose StatementBegin
-- Where a project's new cost anomalies are sent (a managed alert rule, source cloud_cost).
CREATE TABLE cost_alert_settings (
    project_id          UUID        PRIMARY KEY,
    anomaly_channel_ids JSONB       NOT NULL DEFAULT '[]',
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE cost_alert_settings
    ADD CONSTRAINT fk_cost_alert_settings_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
-- A day whose cost for a service (or a whole account, service '') was far from the usual for
-- that weekday.
CREATE TABLE cost_anomalies (
    id            UUID             PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id    UUID             NOT NULL,
    connection_id UUID             NOT NULL,
    day           DATE             NOT NULL,
    sub_account   TEXT             NOT NULL DEFAULT '',
    service       TEXT             NOT NULL DEFAULT '',
    currency      TEXT             NOT NULL,
    expected      NUMERIC(20,6)    NOT NULL,
    actual        NUMERIC(20,6)    NOT NULL,
    score         DOUBLE PRECISION NOT NULL,
    status        TEXT             NOT NULL DEFAULT 'open',
    note          TEXT             NOT NULL DEFAULT '',
    detected_at   TIMESTAMPTZ      NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE cost_anomalies
    ADD CONSTRAINT fk_cost_anomalies_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE cost_anomalies ADD CONSTRAINT chk_cost_anomalies_status CHECK (status IN ('open', 'acknowledged'));
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX idx_cost_anomalies_key
    ON cost_anomalies (project_id, connection_id, day, sub_account, service, currency);
-- +goose StatementEnd

-- +goose StatementBegin
-- Cost budgets keep a managed alert rule each (source cloud_cost).
ALTER TABLE alert_rules DROP CONSTRAINT chk_alert_rules_values;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE alert_rules
    ADD CONSTRAINT chk_alert_rules_values
    CHECK (source IN ('metrics', 'logs', 'traces', 'log_patterns', 'ai_cost', 'cloud_cost')
        AND comparison IN ('>', '>=', '<', '<=', '==', '!=')
        AND window_seconds > 0 AND interval_seconds >= 15 AND for_seconds >= 0 AND repeat_seconds >= 0);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM alert_rules WHERE source = 'cloud_cost';
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE alert_rules DROP CONSTRAINT chk_alert_rules_values;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE alert_rules
    ADD CONSTRAINT chk_alert_rules_values
    CHECK (source IN ('metrics', 'logs', 'traces', 'log_patterns', 'ai_cost')
        AND comparison IN ('>', '>=', '<', '<=', '==', '!=')
        AND window_seconds > 0 AND interval_seconds >= 15 AND for_seconds >= 0 AND repeat_seconds >= 0);
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS cost_anomalies;
DROP TABLE IF EXISTS cost_alert_settings;
DROP TABLE IF EXISTS cost_budgets;
DROP TABLE IF EXISTS cloud_cost_forecasts;
DROP TABLE IF EXISTS cloud_cost_sources;
DROP TABLE IF EXISTS cloud_cost_batches;
DROP TABLE IF EXISTS cloud_cost_daily;
DROP TABLE IF EXISTS user_default_views;
DROP TABLE IF EXISTS saved_views;
DROP TABLE IF EXISTS cloud_cost_settings;
ALTER TABLE cloud_connections DROP COLUMN IF EXISTS cost_settings;
-- +goose StatementEnd
