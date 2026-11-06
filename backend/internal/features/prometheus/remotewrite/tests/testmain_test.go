package prometheus_remotewrite_tests

import (
	"os"
	"testing"

	metrics_testsupport "obzly/internal/features/metrics/testsupport"
)

func TestMain(m *testing.M) {
	metrics_testsupport.SetupRepository()
	os.Exit(m.Run())
}
