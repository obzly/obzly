-- +goose Up
-- +goose StatementBegin
-- Rows a pre-release build put in the default partition would block the new
-- partitions; they're derived stats, rebuilt by the next compaction pass.
DELETE FROM stream_segments WHERE stream IN ('spans_1m', 'spans_1h', 'spans_1m_edges');
-- +goose StatementEnd

-- +goose StatementBegin
-- Span rollups built from each compacted spans segment: per-minute stats
-- (spans_1m), per-hour stats derived from them (spans_1h) and per-minute
-- service calls (spans_1m_edges). Own list partitions, by month like the rest.
CREATE TABLE stream_segments_spans_1m PARTITION OF stream_segments
    FOR VALUES IN ('spans_1m') PARTITION BY RANGE (max_time);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE stream_segments_spans_1m_default PARTITION OF stream_segments_spans_1m DEFAULT;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE stream_segments_spans_1h PARTITION OF stream_segments
    FOR VALUES IN ('spans_1h') PARTITION BY RANGE (max_time);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE stream_segments_spans_1h_default PARTITION OF stream_segments_spans_1h DEFAULT;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE stream_segments_spans_1m_edges PARTITION OF stream_segments
    FOR VALUES IN ('spans_1m_edges') PARTITION BY RANGE (max_time);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE stream_segments_spans_1m_edges_default PARTITION OF stream_segments_spans_1m_edges DEFAULT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE stream_segments_spans_1m_edges;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE stream_segments_spans_1h;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE stream_segments_spans_1m;
-- +goose StatementEnd
