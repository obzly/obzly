-- +goose Up
-- +goose StatementBegin
-- Metric rollups (Phase 14 item 5d): per series, each 5-minute or 1-hour
-- bucket's first, last, min and max samples plus count and sum, built from
-- cold (L2) metrics segments. Own list partitions, by month like the rest.
CREATE TABLE stream_segments_metrics_5m PARTITION OF stream_segments
    FOR VALUES IN ('metrics_5m') PARTITION BY RANGE (max_time);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE stream_segments_metrics_5m_default PARTITION OF stream_segments_metrics_5m DEFAULT;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE stream_segments_metrics_1h PARTITION OF stream_segments
    FOR VALUES IN ('metrics_1h') PARTITION BY RANGE (max_time);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE stream_segments_metrics_1h_default PARTITION OF stream_segments_metrics_1h DEFAULT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE stream_segments_metrics_1h;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE stream_segments_metrics_5m;
-- +goose StatementEnd
