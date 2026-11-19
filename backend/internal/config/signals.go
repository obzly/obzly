package config

import (
	"os"
	"os/signal"
	"sync"
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

var (
	servingStopped     = make(chan struct{})
	servingStoppedOnce sync.Once
)

// StopServing marks that this replica's HTTP server stopped accepting
// requests (after the drain): long-lived responses such as Live Tail end, so
// their clients reconnect to another replica instead of holding this one's
// shutdown for its whole request grace.
func StopServing() {
	servingStoppedOnce.Do(func() { close(servingStopped) })
}

// ServingStopped is closed once StopServing has run.
func ServingStopped() <-chan struct{} {
	return servingStopped
}
