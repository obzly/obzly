package servicemap_tests

import (
	"os"
	"testing"

	metrics_testsupport "obzly/internal/features/metrics/testsupport"
	spans_testsupport "obzly/internal/features/spans/testsupport"
)

func TestMain(m *testing.M) {
	spans_testsupport.SetupRepository()
	metrics_testsupport.SetupRepository()
	os.Exit(m.Run())
}
