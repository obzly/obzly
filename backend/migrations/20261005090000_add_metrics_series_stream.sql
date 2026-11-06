-- +goose Up
-- +goose StatementBegin
-- The metrics_series stream (Phase 14 item 5b): one row per series, beside
-- the metrics samples stream. Its own list partition, by month like the
-- others (obzly_ensure_segment_partition adds the months).
CREATE TABLE stream_segments_metrics_series PARTITION OF stream_segments
    FOR VALUES IN ('metrics_series') PARTITION BY RANGE (max_time);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE stream_segments_metrics_series_default PARTITION OF stream_segments_metrics_series DEFAULT;
-- +goose StatementEnd

-- +goose StatementBegin
-- When a project's series stream started: label lookups for a window that
-- starts later read the series stream; earlier windows also hold samples
-- ingested before it existed, so they read the samples.
CREATE TABLE metric_series_coverage (
    project_id UUID        PRIMARY KEY,
    since      TIMESTAMPTZ NOT NULL
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE metric_series_coverage;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE stream_segments_metrics_series;
-- +goose StatementEnd
