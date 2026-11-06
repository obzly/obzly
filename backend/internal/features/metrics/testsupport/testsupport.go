// Package metrics_testsupport wires a real MetricParquetStore into
// metrics_core for test binaries.
package metrics_testsupport

import (
	"fmt"
	"os"

	metrics_core "obzly/internal/features/metrics/core"
	metrics_parquetstore "obzly/internal/features/metrics/parquetstore"
)

func SetupRepository() {
	dataDir, err := os.MkdirTemp("", "obzly-test-metrics-*")
	if err != nil {
		panic(fmt.Sprintf("metrics_testsupport: failed to create temp data dir: %v", err))
	}

	repository, err := metrics_parquetstore.New(dataDir)
	if err != nil {
		panic(fmt.Sprintf("metrics_testsupport: failed to create metric repository: %v", err))
	}

	metrics_core.SetRepository(repository)
}
