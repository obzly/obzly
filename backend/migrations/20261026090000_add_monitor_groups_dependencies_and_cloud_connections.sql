-- v0.56.0 (docs/SYNTHETICS.md "v0.56.0 additions", docs/INFRASTRUCTURE.md
-- "Cloud connections"): monitor folders, dependencies and alerts; probe
-- groups with members; managed alert rules; cloud connections.
-- Control-plane tables except alert_rules.managed_by (Sites).

-- +goose Up
-- +goose StatementBegin
ALTER TABLE synthetic_checks
    ADD COLUMN folder     TEXT  NOT NULL DEFAULT '',
    ADD COLUMN parent_ids JSONB NOT NULL DEFAULT '[]',
    ADD COLUMN alerts     JSONB NOT NULL DEFAULT '{}';

-- A rotated token keeps working until expires_at, so a group's running
-- members can move to the new one.
ALTER TABLE synthetic_probe_tokens
    ADD COLUMN expires_at TIMESTAMPTZ;

CREATE TABLE synthetic_probe_members (
    probe_id     UUID        NOT NULL,
    name         TEXT        NOT NULL,
    version      TEXT        NOT NULL DEFAULT '',
    checks       INTEGER     NOT NULL DEFAULT 0,
    first_seen   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (probe_id, name)
);

ALTER TABLE synthetic_probe_members
    ADD CONSTRAINT fk_synthetic_probe_members_probe_id
    FOREIGN KEY (probe_id)
    REFERENCES synthetic_probes (id) ON DELETE CASCADE;

-- Rules a feature maintains (synthetics:<monitor>:<kind>); read-only in Alerts.
ALTER TABLE alert_rules
    ADD COLUMN managed_by TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_alert_rules_managed_by ON alert_rules (managed_by) WHERE managed_by <> '';

CREATE TABLE cloud_connections (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name             TEXT        NOT NULL,
    provider         TEXT        NOT NULL,
    scope            TEXT        NOT NULL,
    scope_id         TEXT        NOT NULL DEFAULT '',
    access           TEXT        NOT NULL,
    identity         TEXT        NOT NULL DEFAULT '',
    external_id      TEXT        NOT NULL DEFAULT '',
    tenant_id        TEXT        NOT NULL DEFAULT '',
    probe_id         UUID        NOT NULL,
    destination_id   UUID        NOT NULL,
    scan_types       JSONB       NOT NULL DEFAULT '[]',
    regions          JSONB       NOT NULL DEFAULT '[]',
    resource_types   JSONB       NOT NULL DEFAULT '[]',
    interval_seconds INTEGER     NOT NULL DEFAULT 900,
    enabled          BOOLEAN     NOT NULL DEFAULT TRUE,
    status           JSONB       NOT NULL DEFAULT '{}',
    version          INTEGER     NOT NULL DEFAULT 1,
    created_by       UUID,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_cloud_connections_name ON cloud_connections (lower(name));

ALTER TABLE cloud_connections
    ADD CONSTRAINT fk_cloud_connections_probe_id
    FOREIGN KEY (probe_id)
    REFERENCES synthetic_probes (id) ON DELETE RESTRICT;

ALTER TABLE cloud_connections
    ADD CONSTRAINT fk_cloud_connections_destination_id
    FOREIGN KEY (destination_id)
    REFERENCES synthetic_destinations (id) ON DELETE RESTRICT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS cloud_connections;
DROP INDEX IF EXISTS idx_alert_rules_managed_by;
ALTER TABLE alert_rules DROP COLUMN IF EXISTS managed_by;
DROP TABLE IF EXISTS synthetic_probe_members;
ALTER TABLE synthetic_probe_tokens DROP COLUMN IF EXISTS expires_at;
ALTER TABLE synthetic_checks
    DROP COLUMN IF EXISTS alerts,
    DROP COLUMN IF EXISTS parent_ids,
    DROP COLUMN IF EXISTS folder;
-- +goose StatementEnd
