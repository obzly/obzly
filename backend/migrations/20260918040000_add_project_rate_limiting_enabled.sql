-- +goose Up
-- +goose StatementBegin

-- Lets a project turn off rate limiting/quota enforcement entirely (per-
-- second rate limit, per-log size cap, total log count/storage size
-- quotas) so a high-volume streaming producer can't have logs rejected or
-- auto-deleted for capacity reasons - log retention (max_logs_life_days)
-- is a separate, always-active setting, not gated by this. Existing
-- projects default to TRUE (current behavior: limits enforced) so this
-- migration doesn't silently change anyone's existing enforcement.
ALTER TABLE projects ADD COLUMN is_rate_limiting_enabled BOOLEAN NOT NULL DEFAULT TRUE;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE projects DROP COLUMN IF EXISTS is_rate_limiting_enabled;

-- +goose StatementEnd
