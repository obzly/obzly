-- +goose Up
-- +goose StatementBegin
-- 1-minute metric partials (segments.MetricPartials1mStream), folded with
-- the 5-minute ones; their own list partition by month like the rest.
CREATE TABLE stream_segments_metrics_partials_1m PARTITION OF stream_segments
    FOR VALUES IN ('metrics_partials_1m') PARTITION BY RANGE (max_time);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE stream_segments_metrics_partials_1m_default PARTITION OF stream_segments_metrics_partials_1m DEFAULT;
-- +goose StatementEnd

-- +goose StatementBegin
-- backfillable: the project had no metrics segments when its series
-- stream started, so every sample has series rows and a backfill may move
-- since back to its oldest sample.
ALTER TABLE metric_series_coverage ADD COLUMN backfillable BOOLEAN NOT NULL DEFAULT false;
-- +goose StatementEnd

-- +goose StatementBegin
-- An existing project qualifies when none of its samples predates since:
-- samples ingested before the series stream are no newer than its start.
UPDATE metric_series_coverage c SET backfillable = true
WHERE NOT EXISTS (
    SELECT 1 FROM stream_segments s WHERE s.project_id = c.project_id AND s.stream = 'metrics' AND s.min_time < c.since
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE metric_series_coverage DROP COLUMN backfillable;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE stream_segments_metrics_partials_1m;
-- +goose StatementEnd
