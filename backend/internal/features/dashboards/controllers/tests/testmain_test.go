package dashboards_controllers_tests

import (
	"os"
	"testing"

	logs_testsupport "obzly/internal/features/logs/testsupport"
	metrics_testsupport "obzly/internal/features/metrics/testsupport"
	spans_testsupport "obzly/internal/features/spans/testsupport"
)

func TestMain(m *testing.M) {
	logs_testsupport.SetupRepository()
	spans_testsupport.SetupRepository()
	metrics_testsupport.SetupRepository()
	os.Exit(m.Run())
}
