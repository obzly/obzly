-- +goose Up
-- +goose StatementBegin

-- Version 3 starts with this upgrade: access log lines get one template per
-- method and status class, and quoted values in prose are masked. A Site
-- without logs yet has no older hashes to compare against.
INSERT INTO log_pattern_versions (version, since)
SELECT 3, NOW()
WHERE EXISTS (SELECT 1 FROM stream_segments WHERE stream = 'logs')
ON CONFLICT (version) DO NOTHING;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM log_pattern_versions WHERE version = 3;
-- +goose StatementEnd
