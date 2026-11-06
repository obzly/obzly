package segments

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"obzly/internal/storage"
	"obzly/internal/telemetry"

	"gorm.io/gorm"
)

// PartitionedStreams have their own catalog partition, split by month of
// max_time (migration 20260930150000). Must match that migration's list;
// any other stream lands in stream_segments_other.
var PartitionedStreams = []string{
	"logs", "ai_logs", "spans", "metrics", "metrics_series", "metrics_5m", "metrics_1h", "logs_1m",
}

// partitionMonthsAhead: months created ahead of time, so new rows never land
// in a stream's default partition in normal operation.
const partitionMonthsAhead = 3

// dropLockTimeout bounds how long dropping a month may wait for the
// stream's catalog lock. Queries on that stream queue behind the waiting
// lock, so it stays short; a pass that times out retries later.
const dropLockTimeout = "2s"

// monthPartitionName matches a stream's month partition and captures YYYYMM.
var monthPartitionName = regexp.MustCompile(`^stream_segments_[a-z_]+_(\d{6})$`)

// errPartitionNotEmpty makes the drop transaction roll back, releasing the
// stream's lock without dropping anything.
var errPartitionNotEmpty = errors.New("partition not empty")

// EnsurePartition creates stream's catalog partition for the UTC month
// containing month, moving any rows its default partition already holds for
// that month. Idempotent, and safe on several replicas at once.
func (r *SegmentRepository) EnsurePartition(stream string, month time.Time) error {
	start := time.Date(month.UTC().Year(), month.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	err := storage.GetDb().Exec(
		"SELECT obzly_ensure_segment_partition(?, ?::DATE)", stream, start.Format(time.DateOnly),
	).Error
	if err != nil {
		return fmt.Errorf("failed to ensure %s catalog partition for %s: %w", stream, start.Format("2006-01"), err)
	}
	return nil
}

// EnsurePartitions creates every partitioned stream's partitions from the
// current month through partitionMonthsAhead months after it.
func (r *SegmentRepository) EnsurePartitions(now time.Time) error {
	for _, stream := range PartitionedStreams {
		for ahead := 0; ahead <= partitionMonthsAhead; ahead++ {
			if err := r.EnsurePartition(stream, now.UTC().AddDate(0, ahead, 0)); err != nil {
				return err
			}
		}
	}
	return nil
}

// DropEmptyPastPartitions drops partitioned streams' month partitions that
// ended before the current month and hold no rows, returning their names.
// Retention deletes segments one by one, so months empty on their own; dropping
// them keeps partition count (and planning time) bounded. A later backfilled
// row for a dropped month lands in the default partition.
func (r *SegmentRepository) DropEmptyPastPartitions(now time.Time) ([]string, error) {
	currentMonth := time.Date(now.UTC().Year(), now.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	var dropped []string
	for _, stream := range PartitionedStreams {
		parent := "stream_segments_" + stream
		var children []string
		err := storage.GetDb().Raw(`
			SELECT child.relname FROM pg_inherits
			JOIN pg_class child ON child.oid = pg_inherits.inhrelid
			JOIN pg_class parent ON parent.oid = pg_inherits.inhparent
			WHERE parent.relname = ?
			ORDER BY child.relname`, parent).Scan(&children).Error
		if err != nil {
			return dropped, fmt.Errorf("failed to list %s catalog partitions: %w", stream, err)
		}

		for _, child := range children {
			match := monthPartitionName.FindStringSubmatch(child)
			if match == nil {
				continue // the default partition
			}
			month, err := time.Parse("200601", match[1])
			// Only months that have fully ended; never the current or a future one.
			if err != nil || month.AddDate(0, 1, 0).After(currentMonth) {
				continue
			}
			wasDropped, err := dropIfEmpty(parent, child)
			if err != nil {
				return dropped, err
			}
			if wasDropped {
				dropped = append(dropped, child)
				telemetry.CatalogPartitionsDropped.WithLabelValues(stream).Inc()
			}
		}
	}
	return dropped, nil
}

// dropIfEmpty drops child in one transaction that first locks its stream's
// parent table: no row can be inserted between the emptiness check and the
// drop, and queries (which lock parent before child) cannot deadlock with it.
func dropIfEmpty(parent, child string) (bool, error) {
	err := storage.GetDb().Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET LOCAL lock_timeout = '" + dropLockTimeout + "'").Error; err != nil {
			return err
		}
		if err := tx.Exec("LOCK TABLE " + quoteIdentifier(parent) + " IN ACCESS EXCLUSIVE MODE").Error; err != nil {
			return err
		}
		var hasRows bool
		if err := tx.Raw("SELECT EXISTS (SELECT 1 FROM " + quoteIdentifier(child) + ")").
			Scan(&hasRows).Error; err != nil {
			return err
		}
		if hasRows {
			return errPartitionNotEmpty
		}
		return tx.Exec("DROP TABLE " + quoteIdentifier(child)).Error
	})
	if errors.Is(err, errPartitionNotEmpty) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to drop catalog partition %s: %w", child, err)
	}
	return true, nil
}

func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
