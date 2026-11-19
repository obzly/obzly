-- +goose Up
-- +goose StatementBegin
-- Rows a pre-release build put in the default partition would block the new
-- partitions; they're derived counts, rebuilt by the next compaction pass.
DELETE FROM stream_segments WHERE stream IN ('logs_1m_pods', 'logs_1h_patterns');
-- +goose StatementEnd

-- +goose StatementBegin
-- Rollups derived from each logs_1m rollup: per-minute counts without the
-- message pattern (logs_1m_pods) and per-hour counts by level, service and
-- pattern (logs_1h_patterns). Own list partitions, by month like the rest.
CREATE TABLE stream_segments_logs_1m_pods PARTITION OF stream_segments
    FOR VALUES IN ('logs_1m_pods') PARTITION BY RANGE (max_time);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE stream_segments_logs_1m_pods_default PARTITION OF stream_segments_logs_1m_pods DEFAULT;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE stream_segments_logs_1h_patterns PARTITION OF stream_segments
    FOR VALUES IN ('logs_1h_patterns') PARTITION BY RANGE (max_time);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE stream_segments_logs_1h_patterns_default PARTITION OF stream_segments_logs_1h_patterns DEFAULT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE stream_segments_logs_1h_patterns;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE stream_segments_logs_1m_pods;
-- +goose StatementEnd
