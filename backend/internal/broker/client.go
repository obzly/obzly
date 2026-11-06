package broker

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// duplicateWindow is how long JetStream remembers a publish's Nats-Msg-Id,
// so a client retrying after a lost ack does not store the batch twice.
const duplicateWindow = 2 * time.Minute

// Signal tokens of the ingest subjects.
const (
	SignalLogs    = "logs"
	SignalSpans   = "spans"
	SignalMetrics = "metrics"
)

// IngestStream is one signal's durable buffer.
type IngestStream struct {
	// Name of the JetStream stream.
	Name string
	// Signal is the subject token after "ingest.".
	Signal string
}

// IngestStreams: one stream per signal, subjects
// ingest.<signal>.<shard>.<project>, so each can be sized and back-pressured
// on its own.
var IngestStreams = []IngestStream{
	{Name: "INGEST_LOGS", Signal: SignalLogs},
	{Name: "INGEST_SPANS", Signal: SignalSpans},
	{Name: "INGEST_METRICS", Signal: SignalMetrics},
}

// StreamSettings size the ingest streams.
type StreamSettings struct {
	// Replicas: 3 on the broker cluster, 1 in-process.
	Replicas int
	// MaxBytes per stream: the worker lag budget. A full stream rejects new
	// publishes (DiscardNew), which the receivers turn into 429s.
	MaxBytes int64
}

// Client is a connection to the broker and its JetStream API.
type Client struct {
	Conn      *nats.Conn
	JetStream jetstream.JetStream
}

// defaultClient is this process's broker connection, set at startup when
// JetStream is in use.
var defaultClient *Client

// SetDefault installs the process's broker connection (cmd/main.go).
func SetDefault(client *Client) {
	defaultClient = client
}

// Default returns the process's broker connection, or nil when JetStream
// is not in use.
func Default() *Client {
	return defaultClient
}

// Connect dials a broker at url with the shared token.
func Connect(url, token, name string) (*Client, error) {
	conn, err := nats.Connect(url,
		nats.Token(token),
		nats.Name(name),
		nats.MaxReconnects(-1),
		nats.RetryOnFailedConnect(true),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to NATS at %s: %w", url, err)
	}
	return newClient(conn)
}

// ConnectInProcess connects to a server started with InProcess, without a
// network socket.
func ConnectInProcess(srv *server.Server, name string) (*Client, error) {
	conn, err := nats.Connect("", nats.InProcessServer(srv), nats.Name(name))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to the in-process NATS server: %w", err)
	}
	return newClient(conn)
}

// Close drains pending publishes and closes the connection.
func (c *Client) Close() {
	if err := c.Conn.Drain(); err != nil {
		c.Conn.Close()
	}
}

// EnsureIngestStreams creates or updates every ingest stream. Idempotent,
// so every replica can run it at startup.
func (c *Client) EnsureIngestStreams(ctx context.Context, settings StreamSettings) error {
	for _, stream := range IngestStreams {
		_, err := c.JetStream.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
			Name:     stream.Name,
			Subjects: []string{"ingest." + stream.Signal + ".>"},
			// A message leaves the stream only when a worker acks it, after
			// its rows are in a catalogued segment.
			Retention:  jetstream.WorkQueuePolicy,
			Storage:    jetstream.FileStorage,
			Replicas:   settings.Replicas,
			MaxBytes:   settings.MaxBytes,
			Discard:    jetstream.DiscardNew,
			Duplicates: duplicateWindow,
			// Queries read a project's pending batches (PendingIngest).
			AllowDirect: true,
		})
		if err != nil {
			return fmt.Errorf("failed to create or update stream %s: %w", stream.Name, err)
		}
	}
	return nil
}

func newClient(conn *nats.Conn) (*Client, error) {
	js, err := jetstream.New(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to open JetStream: %w", err)
	}
	return &Client{Conn: conn, JetStream: js}, nil
}
