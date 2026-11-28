-- +goose Up
-- +goose StatementBegin
-- Per-segment 5-minute metric partials (segments.MetricPartialsStream), in
-- their own list partition by month like the other streams.
CREATE TABLE stream_segments_metrics_partials_5m PARTITION OF stream_segments
    FOR VALUES IN ('metrics_partials_5m') PARTITION BY RANGE (max_time);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE stream_segments_metrics_partials_5m_default PARTITION OF stream_segments_metrics_partials_5m DEFAULT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE stream_segments_metrics_partials_5m;
-- +goose StatementEnd
