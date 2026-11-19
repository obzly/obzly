-- +goose Up
-- +goose StatementBegin

-- When each message-pattern version started on this Site. Logs keep the
-- pattern_hash they were stored with; Patterns, Anomalies and log pattern
-- alerts compare a range with a baseline, and a baseline from before a
-- version can't tell a regrouped pattern from a new or vanished one.
CREATE TABLE log_pattern_versions (
    version INTEGER     PRIMARY KEY,
    since   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Version 2 starts with this upgrade. A new Site never stored version 1
-- hashes, so it has no start to compare against.
INSERT INTO log_pattern_versions (version, since)
SELECT 2, NOW()
WHERE EXISTS (SELECT 1 FROM stream_segments WHERE stream = 'logs');

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE log_pattern_versions;
-- +goose StatementEnd
