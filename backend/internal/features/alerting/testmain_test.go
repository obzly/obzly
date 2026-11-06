package alerting

import (
	"testing"

	logs_testsupport "obzly/internal/features/logs/testsupport"
	spans_testsupport "obzly/internal/features/spans/testsupport"
)

func TestMain(m *testing.M) {
	logs_testsupport.SetupRepository()
	spans_testsupport.SetupRepository()
	m.Run()
}
