package webanalytics

import (
	"testing"

	logs_testsupport "obzly/internal/features/logs/testsupport"
)

func TestMain(m *testing.M) {
	logs_testsupport.SetupRepository()
	m.Run()
}
