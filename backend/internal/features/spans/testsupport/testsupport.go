// Package spans_testsupport wires a real SpanParquetStore into spans_core
// for test binaries (see logs_testsupport).
package spans_testsupport

import (
	"fmt"
	"os"

	spans_core "obzly/internal/features/spans/core"
	spans_parquetstore "obzly/internal/features/spans/parquetstore"
)

func SetupRepository() {
	dataDir, err := os.MkdirTemp("", "obzly-test-spans-*")
	if err != nil {
		panic(fmt.Sprintf("spans_testsupport: failed to create temp data dir: %v", err))
	}

	repository, err := spans_parquetstore.New(dataDir)
	if err != nil {
		panic(fmt.Sprintf("spans_testsupport: failed to create span repository: %v", err))
	}

	spans_core.SetRepository(repository)
}
