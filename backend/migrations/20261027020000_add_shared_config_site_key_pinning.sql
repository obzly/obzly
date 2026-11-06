-- Shared-config sync per Site: when the control plane last read the Site's
-- key (its own column, so fetching the document no longer resets it), a
-- changed key waiting for an admin to accept it, and the version the Site
-- reports it applied (status is driven by this, not by what was served).

-- +goose Up
-- +goose StatementBegin
ALTER TABLE sites
    ADD COLUMN shared_config_key_checked_at      TIMESTAMPTZ,
    ADD COLUMN shared_config_pending_key         TEXT NOT NULL DEFAULT '',
    ADD COLUMN shared_config_applied_version     TEXT NOT NULL DEFAULT '',
    ADD COLUMN shared_config_applied_reported_at TIMESTAMPTZ;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE sites
    DROP COLUMN shared_config_applied_reported_at,
    DROP COLUMN shared_config_applied_version,
    DROP COLUMN shared_config_pending_key,
    DROP COLUMN shared_config_key_checked_at;
-- +goose StatementEnd
