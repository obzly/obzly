package logs_livetail_tests

import (
	"os"
	"testing"

	logs_testsupport "obzly/internal/features/logs/testsupport"
)

// Submitted logs are written to storage directly, so the tail tests need a
// logs store too.
func TestMain(m *testing.M) {
	logs_testsupport.SetupRepository()
	os.Exit(m.Run())
}
