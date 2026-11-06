package broker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats-server/v2/server"
)

// Stream setup at startup: a request sent while the broker cluster elects a
// leader can go unanswered, so each attempt is bounded and retried.
const (
	ensureAttemptTimeout = 10 * time.Second
	ensureDeadline       = 2 * time.Minute
)

// inProcessServer is the server StartInProcess started, if any.
var inProcessServer *server.Server

// StartInProcess starts the in-process server of SERVICE_MODE=all and
// returns a client connected to it without a socket.
func StartInProcess(name, storeDir string, log *slog.Logger) (*Client, error) {
	srv, err := StartServer(ServerConfig{Name: name, StoreDir: storeDir, InProcess: true}, log)
	if err != nil {
		return nil, err
	}
	client, err := ConnectInProcess(srv, name)
	if err != nil {
		srv.Shutdown()
		return nil, err
	}
	inProcessServer = srv
	return client, nil
}

// EnsureIngestStreamsWithRetry retries EnsureIngestStreams until it succeeds
// or ensureDeadline passes.
func (c *Client) EnsureIngestStreamsWithRetry(settings StreamSettings, log *slog.Logger) error {
	deadline := time.Now().Add(ensureDeadline)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), ensureAttemptTimeout)
		err := c.EnsureIngestStreams(ctx, settings)
		cancel()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("ingest streams not ready after %s: %w", ensureDeadline, err)
		}
		log.Warn("Waiting for the NATS broker to accept the ingest streams", "error", err)
		time.Sleep(2 * time.Second)
	}
}

// ShutdownDefault closes the process's broker connection and stops the
// in-process server, if any. Call after everything that publishes or
// consumes has stopped.
func ShutdownDefault() {
	if defaultClient != nil {
		defaultClient.Close()
		defaultClient = nil
	}
	if inProcessServer != nil {
		inProcessServer.Shutdown()
		inProcessServer.WaitForShutdown()
		inProcessServer = nil
	}
}
