package mcp_server_tests

import (
	"os"
	"testing"

	access_tokens "obzly/internal/features/access_tokens"
	logs_testsupport "obzly/internal/features/logs/testsupport"
	metrics_testsupport "obzly/internal/features/metrics/testsupport"
	spans_testsupport "obzly/internal/features/spans/testsupport"
)

func TestMain(m *testing.M) {
	logs_testsupport.SetupRepository()
	spans_testsupport.SetupRepository()
	metrics_testsupport.SetupRepository()
	// Off by default (MCP_WRITE_ENABLED); the write tests turn it on here.
	access_tokens.SetWriteEnabled(true)
	os.Exit(m.Run())
}
