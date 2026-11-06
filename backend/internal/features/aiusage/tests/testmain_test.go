package aiusage_tests

import (
	"os"
	"testing"

	"obzly/internal/features/aiusage"
	"obzly/internal/features/alerting"
	logs_testsupport "obzly/internal/features/logs/testsupport"
	spans_testsupport "obzly/internal/features/spans/testsupport"
)

func TestMain(m *testing.M) {
	logs_testsupport.SetupRepository()
	spans_testsupport.SetupRepository()
	alerting.SetAICostSource(aiusage.GetAIUsageService().EvaluateBudgetRule)
	os.Exit(m.Run())
}
