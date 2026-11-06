-- +goose Up
-- +goose StatementBegin

-- last_used_at tracks when an API key was last used to authenticate a
-- request (log ingestion, OTLP, etc). NULL means never used. Updated on a
-- throttled/best-effort basis (see api_keys.ApiKeyService.recordLastUsed) -
-- validation happens on every ingested request, so writing this on every
-- hit would be far too expensive; the value can lag reality by up to the
-- throttle window.
ALTER TABLE api_keys ADD COLUMN last_used_at TIMESTAMPTZ NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE api_keys DROP COLUMN last_used_at;

-- +goose StatementEnd
