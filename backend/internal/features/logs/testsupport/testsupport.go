// Package logs_testsupport wires a real LogParquetStore into logs_core for
// test binaries, as cmd/main.go does at startup; without it
// logs_core.GetLogCoreRepository() panics. Call SetupRepository from TestMain
// in any package whose tests touch log storage.
package logs_testsupport

import (
	"fmt"
	"os"

	logs_core "obzly/internal/features/logs/core"
	logs_parquetstore "obzly/internal/features/logs/parquetstore"
)

func SetupRepository() {
	dataDir, err := os.MkdirTemp("", "obzly-test-logs-*")
	if err != nil {
		panic(fmt.Sprintf("logs_testsupport: failed to create temp data dir: %v", err))
	}

	repository, err := logs_parquetstore.New(dataDir)
	if err != nil {
		panic(fmt.Sprintf("logs_testsupport: failed to create log repository: %v", err))
	}

	logs_core.SetRepository(repository)

	aiRepository, err := logs_parquetstore.New(dataDir, logs_parquetstore.WithStream(logs_core.StreamAILogs))
	if err != nil {
		panic(fmt.Sprintf("logs_testsupport: failed to create AI log repository: %v", err))
	}

	logs_core.SetAIRepository(aiRepository)
}
