-- Synthetic monitoring (docs/SYNTHETICS.md). Control-plane tables: they
-- exist on Sites too and stay empty there.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE synthetic_probes (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name         TEXT        NOT NULL,
    location     TEXT        NOT NULL,
    description  TEXT        NOT NULL DEFAULT '',
    version      TEXT        NOT NULL DEFAULT '',
    last_seen_at TIMESTAMPTZ,
    created_by   UUID,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_synthetic_probes_name ON synthetic_probes (lower(name));

CREATE TABLE synthetic_probe_tokens (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    probe_id     UUID        NOT NULL,
    token_hash   TEXT        NOT NULL,
    token_prefix TEXT        NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at   TIMESTAMPTZ
);

ALTER TABLE synthetic_probe_tokens
    ADD CONSTRAINT fk_synthetic_probe_tokens_probe_id
    FOREIGN KEY (probe_id)
    REFERENCES synthetic_probes (id) ON DELETE CASCADE;

CREATE UNIQUE INDEX idx_synthetic_probe_tokens_hash ON synthetic_probe_tokens (token_hash);

CREATE TABLE synthetic_destinations (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name              TEXT        NOT NULL,
    site_id           TEXT        NOT NULL DEFAULT '',
    project_id        TEXT        NOT NULL DEFAULT '',
    otlp_url          TEXT        NOT NULL,
    api_key_encrypted TEXT        NOT NULL,
    api_key_hint      TEXT        NOT NULL DEFAULT '',
    created_by        UUID,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_synthetic_destinations_name ON synthetic_destinations (lower(name));

CREATE TABLE synthetic_checks (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name              TEXT        NOT NULL,
    type              TEXT        NOT NULL,
    enabled           BOOLEAN     NOT NULL DEFAULT TRUE,
    interval_seconds  INTEGER     NOT NULL DEFAULT 180,
    down_locations    INTEGER     NOT NULL DEFAULT 0,
    locations         JSONB       NOT NULL DEFAULT '[]',
    destination_id    UUID        NOT NULL,
    definition        JSONB       NOT NULL,
    secrets_encrypted TEXT        NOT NULL DEFAULT '',
    version           INTEGER     NOT NULL DEFAULT 1,
    created_by        UUID,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_synthetic_checks_name ON synthetic_checks (lower(name));

ALTER TABLE synthetic_checks
    ADD CONSTRAINT fk_synthetic_checks_destination_id
    FOREIGN KEY (destination_id)
    REFERENCES synthetic_destinations (id) ON DELETE RESTRICT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE synthetic_checks;
DROP TABLE synthetic_destinations;
DROP TABLE synthetic_probe_tokens;
DROP TABLE synthetic_probes;
-- +goose StatementEnd
