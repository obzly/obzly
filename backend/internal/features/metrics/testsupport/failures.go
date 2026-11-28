package metrics_testsupport

import (
	"context"
	"errors"
	"testing"

	metrics_core "obzly/internal/features/metrics/core"

	"github.com/google/uuid"
)

// StorageFailure is a failed read as storage reports it: the fragment, the
// executor and the segment file are in the text, which no caller should see.
const StorageFailure = `query fragment failed: fragment f1: executor querier-0: IO Error: ` +
	`Cannot open file "/var/lib/obzly/segments/metrics/contoso/1.parquet"`

// failingRepository is the repository with every read of samples failing.
type failingRepository struct {
	metrics_core.MetricRepository
	err error
}

// FailSampleReads makes every read of samples (the engine's selects and
// pushed-down range functions) fail with err until the test ends; nil is
// StorageFailure. Not for parallel tests: the repository is process-wide.
func FailSampleReads(t testing.TB, err error) {
	t.Helper()
	if err == nil {
		err = errors.New(StorageFailure)
	}
	previous := metrics_core.GetMetricCoreRepository()
	metrics_core.SetRepository(failingRepository{previous, err})
	t.Cleanup(func() { metrics_core.SetRepository(previous) })
}

func (r failingRepository) SelectSeries(
	context.Context, uuid.UUID, *metrics_core.SelectSeriesRequestDTO,
) (*metrics_core.SelectSeriesResponseDTO, error) {
	return nil, r.err
}

func (r failingRepository) EvaluateRangeFunction(
	context.Context, uuid.UUID, *metrics_core.RangeFunctionRequestDTO,
) (*metrics_core.RangeFunctionResponseDTO, bool, error) {
	return nil, false, r.err
}
