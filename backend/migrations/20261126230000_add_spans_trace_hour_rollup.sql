-- +goose Up
-- +goose StatementBegin
-- Rows a pre-release build put in the default partition would block the new
-- partition; they're derived counts, rebuilt by the next compaction pass.
DELETE FROM stream_segments WHERE stream = 'spans_1h_traces';
-- +goose StatementEnd

-- +goose StatementBegin
-- Per-hour trace membership derived from each spans_1m_traces rollup
-- (spans_1h_traces). Its own list partition, by month like the rest.
CREATE TABLE stream_segments_spans_1h_traces PARTITION OF stream_segments
    FOR VALUES IN ('spans_1h_traces') PARTITION BY RANGE (max_time);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE stream_segments_spans_1h_traces_default PARTITION OF stream_segments_spans_1h_traces DEFAULT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE stream_segments_spans_1h_traces;
-- +goose StatementEnd
