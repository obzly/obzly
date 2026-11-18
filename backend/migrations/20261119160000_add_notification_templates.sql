-- Alert rule severity and runbook, editable message templates (organisation
-- defaults per channel kind plus per-channel overrides, central on a control
-- plane and synced to its Sites like the channels), and incident notifications.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE alert_rules
    ADD COLUMN severity    TEXT NOT NULL DEFAULT 'medium',
    ADD COLUMN description TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- Existing rules take their severity from their severity label.
-- +goose StatementBegin
UPDATE alert_rules
SET severity = CASE lower(trim(labels ->> 'severity'))
    WHEN 'critical' THEN 'critical'
    WHEN 'high' THEN 'high'
    WHEN 'error' THEN 'high'
    WHEN 'warning' THEN 'medium'
    WHEN 'medium' THEN 'medium'
    WHEN 'info' THEN 'low'
    WHEN 'low' THEN 'low'
    ELSE 'medium'
END;
-- +goose StatementEnd

-- Managed rules without a severity label: what their feature now sets.
-- +goose StatementBegin
UPDATE alert_rules
SET severity = CASE
    WHEN managed_by LIKE 'ai-budget:%' THEN 'high'
    WHEN managed_by LIKE 'cloud-posture:%' THEN 'high'
    WHEN managed_by LIKE 'cloud-cost:commitments:%' THEN 'low'
    ELSE severity
END
WHERE managed_by <> '' AND coalesce(labels ->> 'severity', '') = '';
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE alert_rules
    ADD CONSTRAINT chk_alert_rules_severity
    CHECK (severity IN ('critical', 'high', 'medium', 'low'));
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE notification_templates (
    source        TEXT        NOT NULL,
    kind          TEXT        NOT NULL,
    subject       TEXT        NOT NULL DEFAULT '',
    body          TEXT        NOT NULL DEFAULT '',
    updated_by    UUID,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_error    TEXT        NOT NULL DEFAULT '',
    last_error_at TIMESTAMPTZ,
    PRIMARY KEY (source, kind)
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE notification_templates
    ADD CONSTRAINT chk_notification_templates_values
    CHECK (source IN ('central', 'local') AND kind IN ('webhook', 'slack', 'pagerduty', 'email'));
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE shared_channels
    ADD COLUMN use_emoji         BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN template_override BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN template_subject  TEXT    NOT NULL DEFAULT '',
    ADD COLUMN template_body     TEXT    NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE notification_channels
    ADD COLUMN use_emoji         BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN template_override BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN template_subject  TEXT    NOT NULL DEFAULT '',
    ADD COLUMN template_body     TEXT    NOT NULL DEFAULT '',
    ADD COLUMN template_error    TEXT    NOT NULL DEFAULT '',
    ADD COLUMN template_error_at TIMESTAMPTZ;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE incident_notification_settings (
    project_id   UUID PRIMARY KEY,
    notify_open  BOOLEAN     NOT NULL DEFAULT FALSE,
    notify_end   BOOLEAN     NOT NULL DEFAULT FALSE,
    min_signals  INT         NOT NULL DEFAULT 3,
    min_services INT         NOT NULL DEFAULT 2,
    severity     TEXT        NOT NULL DEFAULT 'high',
    channel_ids  JSONB       NOT NULL DEFAULT '[]',
    updated_by   UUID,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE incident_notification_settings
    ADD CONSTRAINT fk_incident_notification_settings_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE incident_notification_settings
    ADD CONSTRAINT chk_incident_notification_settings_values
    CHECK (severity IN ('critical', 'high', 'medium', 'low') AND min_signals >= 1 AND min_services >= 1);
-- +goose StatementEnd

-- One row per notified incident: the sessions the correlation scanner keeps
-- creating while it lasts are folded into it, so it opens and ends once.
-- +goose StatementBegin
CREATE TABLE incident_notifications (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id       UUID        NOT NULL,
    session_id       UUID        NOT NULL,
    services         JSONB       NOT NULL DEFAULT '[]',
    signal_count     INT         NOT NULL DEFAULT 0,
    error_logs       INT         NOT NULL DEFAULT 0,
    error_spans      INT         NOT NULL DEFAULT 0,
    top_services     JSONB       NOT NULL DEFAULT '[]',
    severity         TEXT        NOT NULL DEFAULT 'high',
    opened_at        TIMESTAMPTZ NOT NULL,
    last_seen_at     TIMESTAMPTZ NOT NULL,
    ended_at         TIMESTAMPTZ,
    notified_open_at TIMESTAMPTZ,
    notified_end_at  TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE incident_notifications
    ADD CONSTRAINT fk_incident_notifications_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE incident_notifications
    ADD CONSTRAINT uq_incident_notifications_session_id
    UNIQUE (session_id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_incident_notifications_open
    ON incident_notifications (project_id, last_seen_at) WHERE ended_at IS NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE incident_notifications;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE incident_notification_settings;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE notification_channels
    DROP COLUMN use_emoji,
    DROP COLUMN template_override,
    DROP COLUMN template_subject,
    DROP COLUMN template_body,
    DROP COLUMN template_error,
    DROP COLUMN template_error_at;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE shared_channels
    DROP COLUMN use_emoji,
    DROP COLUMN template_override,
    DROP COLUMN template_subject,
    DROP COLUMN template_body;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE notification_templates;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE alert_rules DROP CONSTRAINT chk_alert_rules_severity;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE alert_rules
    DROP COLUMN severity,
    DROP COLUMN description;
-- +goose StatementEnd
