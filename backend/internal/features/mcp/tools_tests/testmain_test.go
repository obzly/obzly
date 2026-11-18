package mcp_tools_tests

import (
	"os"
	"testing"

	correlation_core "obzly/internal/features/correlation/core"
	correlation_repository "obzly/internal/features/correlation/repository"
	logs_testsupport "obzly/internal/features/logs/testsupport"
	spans_testsupport "obzly/internal/features/spans/testsupport"
)

func TestMain(m *testing.M) {
	logs_testsupport.SetupRepository()
	spans_testsupport.SetupRepository()
	correlation_core.SetRepository(correlation_repository.New())
	os.Exit(m.Run())
}
