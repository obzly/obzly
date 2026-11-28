-- +goose Up
-- +goose StatementBegin
-- Rows a pre-release build put in the default partition would block the new
-- partition; they're derived counts, rebuilt by the next compaction pass.
DELETE FROM stream_segments WHERE stream = 'spans_1m_traces';
-- +goose StatementEnd

-- +goose StatementBegin
-- Per-minute trace membership built from each compacted spans segment
-- (spans_1m_traces): root spans per service in their trace and whether the
-- trace has an error. Its own list partition, by month like the rest.
CREATE TABLE stream_segments_spans_1m_traces PARTITION OF stream_segments
    FOR VALUES IN ('spans_1m_traces') PARTITION BY RANGE (max_time);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE stream_segments_spans_1m_traces_default PARTITION OF stream_segments_spans_1m_traces DEFAULT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE stream_segments_spans_1m_traces;
-- +goose StatementEnd
