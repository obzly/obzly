-- Notification channels and SMTP managed in Obzly settings: central on a
-- control plane (shared_channels, smtp_settings 'central'), pulled by its
-- Sites into notification_channels / smtp_settings. A standalone Site keeps
-- its own ('local'). Existing project channels stay where they are, with
-- the same ids, scoped to their project.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE notification_channels
    ALTER COLUMN project_id DROP NOT NULL;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE notification_channels
    ADD COLUMN source       TEXT    NOT NULL DEFAULT 'local',
    ADD COLUMN all_projects BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN project_ids  JSONB   NOT NULL DEFAULT '[]';
-- +goose StatementEnd

-- +goose StatementBegin
UPDATE notification_channels
SET project_ids = jsonb_build_array(project_id::text)
WHERE project_id IS NOT NULL;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE shared_channels (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name             TEXT        NOT NULL,
    kind             TEXT        NOT NULL,
    config_encrypted TEXT        NOT NULL,
    scope            JSONB       NOT NULL DEFAULT '{"allSites": true}',
    created_by       UUID,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX idx_shared_channels_name ON shared_channels (lower(name));
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE smtp_settings (
    source             TEXT PRIMARY KEY,
    host               TEXT        NOT NULL,
    port               INTEGER     NOT NULL DEFAULT 587,
    username           TEXT        NOT NULL DEFAULT '',
    password_encrypted TEXT        NOT NULL DEFAULT '',
    from_address       TEXT        NOT NULL,
    tls                TEXT        NOT NULL DEFAULT 'starttls',
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE shared_config_keys (
    id                    BOOLEAN PRIMARY KEY DEFAULT TRUE,
    public_key            TEXT        NOT NULL,
    private_key_encrypted TEXT        NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE shared_config_state (
    id         BOOLEAN PRIMARY KEY DEFAULT TRUE,
    version    TEXT        NOT NULL,
    issued_at  TIMESTAMPTZ NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE sites
    ADD COLUMN shared_config_key        TEXT NOT NULL DEFAULT '',
    ADD COLUMN shared_config_version    TEXT NOT NULL DEFAULT '',
    ADD COLUMN shared_config_fetched_at TIMESTAMPTZ;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE sites
    DROP COLUMN shared_config_fetched_at,
    DROP COLUMN shared_config_version,
    DROP COLUMN shared_config_key;
DROP TABLE shared_config_state;
DROP TABLE shared_config_keys;
DROP TABLE smtp_settings;
DROP TABLE shared_channels;
DELETE FROM notification_channels WHERE project_id IS NULL;
ALTER TABLE notification_channels
    DROP COLUMN project_ids,
    DROP COLUMN all_projects,
    DROP COLUMN source;
ALTER TABLE notification_channels
    ALTER COLUMN project_id SET NOT NULL;
-- +goose StatementEnd
