-- +goose Up
-- +goose StatementBegin

-- Phase 14 item 3: partition the segment catalog so queries and retention
-- touch only the months they need. LIST(stream), then RANGE(max_time) by
-- UTC calendar month per stream.
--
-- Why max_time, not min_time: a query window is "min_time <= to AND
-- max_time >= from", so with max_time as the key Postgres skips every month
-- that ended before `from` - all history, for the usual recent-window
-- query - without needing a bound on how long a segment can span (segment
-- times come from client timestamps, so one backfilled batch can span
-- years). Retention also deletes by max_time, so a whole month can be
-- dropped once every project's retention has passed its upper bound.
--
-- DEFAULT partitions catch any other stream (tests use ad-hoc names) and,
-- per stream, max_time outside the months created so far.
-- obzly_ensure_segment_partition adds a month, moving any rows the stream's
-- default partition already holds for it.

-- Writers and the uploader must not add rows to the old table between the
-- copy and the rename below; reads continue.
LOCK TABLE stream_segments IN SHARE ROW EXCLUSIVE MODE;

CREATE FUNCTION obzly_ensure_segment_partition(p_stream TEXT, p_month DATE) RETURNS VOID AS $$
DECLARE
    parent_name  TEXT := 'stream_segments_' || p_stream;
    default_name TEXT := 'stream_segments_' || p_stream || '_default';
    child_name   TEXT := 'stream_segments_' || p_stream || '_' || to_char(p_month, 'YYYYMM');
    lower_bound  TIMESTAMPTZ := date_trunc('month', p_month)::TIMESTAMP AT TIME ZONE 'UTC';
    upper_bound  TIMESTAMPTZ := (date_trunc('month', p_month) + INTERVAL '1 month')::TIMESTAMP AT TIME ZONE 'UTC';
BEGIN
    IF to_regclass(parent_name) IS NULL THEN
        RAISE EXCEPTION 'stream % has no partitioned catalog table', p_stream;
    END IF;
    -- Replicas calling this at once would otherwise race on the new table's
    -- catalog entries; the lock is released when the calling transaction ends.
    PERFORM pg_advisory_xact_lock(hashtext('obzly_ensure_segment_partition'));
    IF to_regclass(child_name) IS NOT NULL THEN
        RETURN;
    END IF;

    EXECUTE format('CREATE TABLE %I (LIKE %I INCLUDING DEFAULTS)', child_name, parent_name);
    EXECUTE format(
        'WITH moved AS (DELETE FROM %I WHERE max_time >= $1 AND max_time < $2 RETURNING *) '
        'INSERT INTO %I SELECT * FROM moved',
        default_name, child_name
    ) USING lower_bound, upper_bound;
    EXECUTE format(
        'ALTER TABLE %I ATTACH PARTITION %I FOR VALUES FROM (%L) TO (%L)',
        parent_name, child_name, lower_bound, upper_bound
    );
END;
$$ LANGUAGE plpgsql;

CREATE TABLE stream_segments_partitioned (
    id             UUID        NOT NULL DEFAULT gen_random_uuid(),
    project_id     UUID        NOT NULL,
    stream         TEXT        NOT NULL,
    path           TEXT        NOT NULL,
    row_count      BIGINT      NOT NULL,
    size_bytes     BIGINT      NOT NULL,
    min_time       TIMESTAMPTZ NOT NULL,
    max_time       TIMESTAMPTZ NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    uploaded_at    TIMESTAMPTZ,
    object_key     TEXT,
    raw_size_bytes BIGINT      NOT NULL DEFAULT 0,
    schema_version INTEGER     NOT NULL DEFAULT 2,
    writer_id      TEXT,
    stats          JSONB,
    PRIMARY KEY (id, stream, max_time)
) PARTITION BY LIST (stream);

CREATE TABLE stream_segments_logs PARTITION OF stream_segments_partitioned
    FOR VALUES IN ('logs') PARTITION BY RANGE (max_time);
CREATE TABLE stream_segments_ai_logs PARTITION OF stream_segments_partitioned
    FOR VALUES IN ('ai_logs') PARTITION BY RANGE (max_time);
CREATE TABLE stream_segments_spans PARTITION OF stream_segments_partitioned
    FOR VALUES IN ('spans') PARTITION BY RANGE (max_time);
CREATE TABLE stream_segments_metrics PARTITION OF stream_segments_partitioned
    FOR VALUES IN ('metrics') PARTITION BY RANGE (max_time);

CREATE TABLE stream_segments_logs_default PARTITION OF stream_segments_logs DEFAULT;
CREATE TABLE stream_segments_ai_logs_default PARTITION OF stream_segments_ai_logs DEFAULT;
CREATE TABLE stream_segments_spans_default PARTITION OF stream_segments_spans DEFAULT;
CREATE TABLE stream_segments_metrics_default PARTITION OF stream_segments_metrics DEFAULT;
CREATE TABLE stream_segments_other PARTITION OF stream_segments_partitioned DEFAULT;

-- Query planning: a project's segments overlapping a window.
CREATE INDEX idx_segments_project_max_time
    ON stream_segments_partitioned (project_id, max_time, min_time);

-- Compaction, retention and quotas walk a project's segments oldest first.
CREATE INDEX idx_segments_project_min_time
    ON stream_segments_partitioned (project_id, min_time);

CREATE INDEX idx_segments_pending_upload
    ON stream_segments_partitioned (stream, created_at)
    WHERE uploaded_at IS NULL;

CREATE INDEX idx_segments_pending_upload_by_writer
    ON stream_segments_partitioned (stream, writer_id, created_at)
    WHERE uploaded_at IS NULL;

CREATE INDEX idx_segments_schema_version
    ON stream_segments_partitioned (schema_version)
    WHERE schema_version < 2;

-- Months that already hold data (up to two years back, three months ahead;
-- anything outside stays in the default partition), plus the current month
-- and the next three.
DO $$
DECLARE
    known_stream TEXT;
    month        DATE;
BEGIN
    FOREACH known_stream IN ARRAY ARRAY['logs', 'ai_logs', 'spans', 'metrics'] LOOP
        FOR month IN
            SELECT DISTINCT date_trunc('month', max_time AT TIME ZONE 'UTC')::DATE
            FROM stream_segments
            WHERE stream = known_stream
              AND max_time >= date_trunc('month', NOW() AT TIME ZONE 'UTC') - INTERVAL '24 months'
              AND max_time < date_trunc('month', NOW() AT TIME ZONE 'UTC') + INTERVAL '4 months'
            UNION
            SELECT generate_series(
                date_trunc('month', NOW() AT TIME ZONE 'UTC'),
                date_trunc('month', NOW() AT TIME ZONE 'UTC') + INTERVAL '3 months',
                INTERVAL '1 month'
            )::DATE
        LOOP
            PERFORM obzly_ensure_segment_partition(known_stream, month);
        END LOOP;
    END LOOP;
END;
$$;

INSERT INTO stream_segments_partitioned (
    id, project_id, stream, path, row_count, size_bytes, min_time, max_time, created_at,
    uploaded_at, object_key, raw_size_bytes, schema_version, writer_id, stats
)
SELECT
    id, project_id, stream, path, row_count, size_bytes, min_time, max_time, created_at,
    uploaded_at, object_key, raw_size_bytes, schema_version, writer_id, stats
FROM stream_segments;

-- Kept for one release (rollback boundary), then dropped by a later migration.
ALTER TABLE stream_segments RENAME TO stream_segments_unpartitioned;
ALTER TABLE stream_segments_partitioned RENAME TO stream_segments;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

LOCK TABLE stream_segments IN SHARE ROW EXCLUSIVE MODE;

-- Rows written since the upgrade go back too.
TRUNCATE stream_segments_unpartitioned;
INSERT INTO stream_segments_unpartitioned (
    id, project_id, stream, path, row_count, size_bytes, min_time, max_time, created_at,
    uploaded_at, object_key, raw_size_bytes, schema_version, writer_id, stats
)
SELECT
    id, project_id, stream, path, row_count, size_bytes, min_time, max_time, created_at,
    uploaded_at, object_key, raw_size_bytes, schema_version, writer_id, stats
FROM stream_segments;

DROP TABLE stream_segments;
ALTER TABLE stream_segments_unpartitioned RENAME TO stream_segments;
DROP FUNCTION obzly_ensure_segment_partition(TEXT, DATE);

-- +goose StatementEnd
