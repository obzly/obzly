-- +goose Up
-- +goose StatementBegin
CREATE TABLE web_analytics_settings (
    project_id        UUID PRIMARY KEY,
    ip_mode           TEXT        NOT NULL DEFAULT 'truncate',
    hash_salt         BYTEA       NOT NULL,
    retention_days    INT         NOT NULL DEFAULT 0,
    exclude_bots      BOOLEAN     NOT NULL DEFAULT TRUE,
    static_extensions TEXT        NOT NULL DEFAULT '',
    geo_enabled       BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE web_analytics_settings
    ADD CONSTRAINT fk_web_analytics_settings_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE web_analytics_sources (
    project_id    UUID        NOT NULL,
    source_key    TEXT        NOT NULL,
    name          TEXT        NOT NULL DEFAULT '',
    merged_into   TEXT,
    identified_by TEXT        NOT NULL DEFAULT '',
    identity      JSONB       NOT NULL DEFAULT '{}',
    format        TEXT        NOT NULL DEFAULT '',
    first_seen    TIMESTAMPTZ NOT NULL,
    last_seen     TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (project_id, source_key)
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE web_analytics_sources
    ADD CONSTRAINT fk_web_analytics_sources_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE web_analytics_builds (
    project_id   UUID        NOT NULL,
    granularity  CHAR(1)     NOT NULL,
    bucket_start TIMESTAMPTZ NOT NULL,
    built_at     TIMESTAMPTZ NOT NULL,
    duration_ms  BIGINT      NOT NULL DEFAULT 0,
    rows_read    BIGINT      NOT NULL DEFAULT 0,
    PRIMARY KEY (project_id, granularity, bucket_start)
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE web_analytics_builds
    ADD CONSTRAINT fk_web_analytics_builds_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE web_analytics_totals (
    project_id     UUID        NOT NULL,
    granularity    CHAR(1)     NOT NULL,
    bucket_start   TIMESTAMPTZ NOT NULL,
    source_key     TEXT        NOT NULL,
    host           TEXT        NOT NULL,
    format         TEXT        NOT NULL DEFAULT '',
    requests       BIGINT      NOT NULL DEFAULT 0,
    bytes          BIGINT      NOT NULL DEFAULT 0,
    status_2xx     BIGINT      NOT NULL DEFAULT 0,
    status_3xx     BIGINT      NOT NULL DEFAULT 0,
    status_4xx     BIGINT      NOT NULL DEFAULT 0,
    status_5xx     BIGINT      NOT NULL DEFAULT 0,
    status_other   BIGINT      NOT NULL DEFAULT 0,
    not_found      BIGINT      NOT NULL DEFAULT 0,
    bot_requests   BIGINT      NOT NULL DEFAULT 0,
    duration_hist  TEXT        NOT NULL DEFAULT '',
    with_client    BIGINT      NOT NULL DEFAULT 0,
    with_bytes     BIGINT      NOT NULL DEFAULT 0,
    with_referer   BIGINT      NOT NULL DEFAULT 0,
    with_ua        BIGINT      NOT NULL DEFAULT 0,
    with_duration  BIGINT      NOT NULL DEFAULT 0,
    with_tls       BIGINT      NOT NULL DEFAULT 0,
    with_host      BIGINT      NOT NULL DEFAULT 0,
    with_protocol  BIGINT      NOT NULL DEFAULT 0,
    visitors_hll   BYTEA,
    PRIMARY KEY (project_id, granularity, bucket_start, source_key, host)
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE web_analytics_totals
    ADD CONSTRAINT fk_web_analytics_totals_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE web_analytics_keys (
    project_id   UUID        NOT NULL,
    granularity  CHAR(1)     NOT NULL,
    bucket_start TIMESTAMPTZ NOT NULL,
    source_key   TEXT        NOT NULL,
    host         TEXT        NOT NULL,
    dim          SMALLINT    NOT NULL,
    key          TEXT        NOT NULL,
    hits         BIGINT      NOT NULL DEFAULT 0,
    bytes        BIGINT      NOT NULL DEFAULT 0,
    errors_5xx   BIGINT      NOT NULL DEFAULT 0,
    referred     BIGINT      NOT NULL DEFAULT 0,
    last_seen    TIMESTAMPTZ,
    extra        TEXT        NOT NULL DEFAULT '',
    visitors_hll BYTEA,
    PRIMARY KEY (project_id, granularity, bucket_start, source_key, host, dim, key)
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE web_analytics_keys
    ADD CONSTRAINT fk_web_analytics_keys_project_id
    FOREIGN KEY (project_id)
    REFERENCES projects (id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE web_analytics_keys;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE web_analytics_totals;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE web_analytics_builds;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE web_analytics_sources;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE web_analytics_settings;
-- +goose StatementEnd
