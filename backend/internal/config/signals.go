package config

import (
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
)

// Atomic: request goroutines (the readiness probe) read it while the signal
// goroutine writes it.
var isShutDownSignalReceived atomic.Bool

func StartListeningForShutdownSignal() {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-quit
		isShutDownSignalReceived.Store(true)
	}()
}

func IsShouldShutdown() bool {
	return isShutDownSignalReceived.Load()
}
