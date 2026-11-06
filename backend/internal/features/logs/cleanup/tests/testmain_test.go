package logs_cleanup_tests

import (
	"os"
	"testing"

	logs_testsupport "obzly/internal/features/logs/testsupport"
)

func TestMain(m *testing.M) {
	logs_testsupport.SetupRepository()
	os.Exit(m.Run())
}
