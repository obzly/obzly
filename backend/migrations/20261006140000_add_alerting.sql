-- +goose Up
-- +goose StatementBegin
CREATE TABLE notification_channels (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id       UUID        NOT NULL,
    name             TEXT        NOT NULL,
    kind             TEXT        NOT NULL,
    config_encrypted TEXT        NOT NULL,
    created_by       UUID,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE notification_channels
    ADD CONSTRAINT fk_notification_channels_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE notification_channels
    ADD CONSTRAINT fk_notification_channels_created_by
    FOREIGN KEY (created_by)
    REFERENCES users (id) ON DELETE SET NULL;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE notification_channels
    ADD CONSTRAINT chk_notification_channels_kind
    CHECK (kind IN ('webhook', 'slack', 'pagerduty', 'email'));
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE alert_rules (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id       UUID             NOT NULL,
    name             TEXT             NOT NULL,
    enabled          BOOLEAN          NOT NULL DEFAULT TRUE,
    source           TEXT             NOT NULL,
    query            JSONB            NOT NULL,
    comparison       TEXT             NOT NULL,
    threshold        DOUBLE PRECISION NOT NULL,
    window_seconds   INT              NOT NULL DEFAULT 300,
    interval_seconds INT              NOT NULL DEFAULT 60,
    for_seconds      INT              NOT NULL DEFAULT 0,
    repeat_seconds   INT              NOT NULL DEFAULT 14400,
    labels           JSONB            NOT NULL DEFAULT '{}',
    summary          TEXT             NOT NULL DEFAULT '',
    last_eval_at     TIMESTAMPTZ,
    last_error       TEXT             NOT NULL DEFAULT '',
    created_by       UUID,
    created_at       TIMESTAMPTZ      NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ      NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE alert_rules
    ADD CONSTRAINT fk_alert_rules_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE alert_rules
    ADD CONSTRAINT fk_alert_rules_created_by
    FOREIGN KEY (created_by)
    REFERENCES users (id) ON DELETE SET NULL;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE alert_rules
    ADD CONSTRAINT chk_alert_rules_values
    CHECK (source IN ('metrics', 'logs', 'traces')
        AND comparison IN ('>', '>=', '<', '<=', '==', '!=')
        AND window_seconds > 0 AND interval_seconds >= 15 AND for_seconds >= 0 AND repeat_seconds >= 0);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE alert_rule_channels (
    rule_id    UUID NOT NULL,
    channel_id UUID NOT NULL,
    PRIMARY KEY (rule_id, channel_id)
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE alert_rule_channels
    ADD CONSTRAINT fk_alert_rule_channels_rule_id
    FOREIGN KEY (rule_id)
    REFERENCES alert_rules (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE alert_rule_channels
    ADD CONSTRAINT fk_alert_rule_channels_channel_id
    FOREIGN KEY (channel_id)
    REFERENCES notification_channels (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE alert_states (
    rule_id          UUID             NOT NULL,
    fingerprint      TEXT             NOT NULL,
    labels           JSONB            NOT NULL DEFAULT '{}',
    state            TEXT             NOT NULL,
    value            DOUBLE PRECISION NOT NULL DEFAULT 0,
    active_at        TIMESTAMPTZ      NOT NULL,
    fired_at         TIMESTAMPTZ,
    resolved_at      TIMESTAMPTZ,
    last_notified_at TIMESTAMPTZ,
    updated_at       TIMESTAMPTZ      NOT NULL DEFAULT NOW(),
    PRIMARY KEY (rule_id, fingerprint)
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE alert_states
    ADD CONSTRAINT fk_alert_states_rule_id
    FOREIGN KEY (rule_id)
    REFERENCES alert_rules (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE silences (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID        NOT NULL,
    matchers   JSONB       NOT NULL DEFAULT '[]',
    starts_at  TIMESTAMPTZ NOT NULL,
    ends_at    TIMESTAMPTZ NOT NULL,
    comment    TEXT        NOT NULL DEFAULT '',
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE silences
    ADD CONSTRAINT fk_silences_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE silences
    ADD CONSTRAINT fk_silences_created_by
    FOREIGN KEY (created_by)
    REFERENCES users (id) ON DELETE SET NULL;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE notification_outbox (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID        NOT NULL,
    channel_id      UUID        NOT NULL,
    rule_id         UUID,
    fingerprint     TEXT        NOT NULL DEFAULT '',
    status          TEXT        NOT NULL,
    payload         JSONB       NOT NULL,
    attempts        INT         NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    sent_at         TIMESTAMPTZ,
    failed_at       TIMESTAMPTZ,
    last_error      TEXT        NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE notification_outbox
    ADD CONSTRAINT fk_notification_outbox_channel_id
    FOREIGN KEY (channel_id)
    REFERENCES notification_channels (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE notification_outbox
    ADD CONSTRAINT fk_notification_outbox_rule_id
    FOREIGN KEY (rule_id)
    REFERENCES alert_rules (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_notification_outbox_due
    ON notification_outbox (next_attempt_at) WHERE sent_at IS NULL AND failed_at IS NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE notification_outbox;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE silences;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE alert_states;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE alert_rule_channels;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE alert_rules;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE notification_channels;
-- +goose StatementEnd
