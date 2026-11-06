-- +goose Up
-- +goose StatementBegin
-- Rows a pre-release build put in the default partition would block the new
-- partition; they're derived counts, rebuilt by the next compaction pass.
DELETE FROM stream_segments WHERE stream = 'logs_1m';
-- +goose StatementEnd

-- +goose StatementBegin
-- Per-minute log counts (logs_1m): per compacted logs segment, the count of
-- its logs per minute, level, service, namespace, container and message
-- pattern - what the Logs histogram reads instead of the raw logs. Own list
-- partition, by month like the rest.
CREATE TABLE stream_segments_logs_1m PARTITION OF stream_segments
    FOR VALUES IN ('logs_1m') PARTITION BY RANGE (max_time);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE stream_segments_logs_1m_default PARTITION OF stream_segments_logs_1m DEFAULT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE stream_segments_logs_1m;
-- +goose StatementEnd
