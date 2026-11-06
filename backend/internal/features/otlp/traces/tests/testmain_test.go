package otlp_traces_tests

import (
	"os"
	"testing"

	spans_testsupport "obzly/internal/features/spans/testsupport"
)

func TestMain(m *testing.M) {
	spans_testsupport.SetupRepository()
	os.Exit(m.Run())
}
