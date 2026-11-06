// Package logs_livetail is the Logs page's push-based Live Tail: accepted logs
// are published per project, and each open tail matches them against the
// viewer's filter with logs_core.Matches (the in-memory twin of the search
// SQL). Transport is core NATS with the broker (only while a tail is open),
// else Valkey Pub/Sub; see bus.go.
package logs_livetail

import (
	"context"
	"log/slog"

	logs_core "obzly/internal/features/logs/core"

	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"
)

type Publisher struct {
	client valkey.Client
	logger *slog.Logger
}

func NewPublisher(client valkey.Client, logger *slog.Logger) *Publisher {
	return &Publisher{client: client, logger: logger}
}

// PublishBatch fire-and-forgets validLogs to the project's open tails. Run in
// a goroutine so a slow transport never delays ingest; a dropped publish only
// means no tail saw those logs live.
func (p *Publisher) PublishBatch(ctx context.Context, projectID uuid.UUID, validLogs []*logs_core.LogItem) {
	if len(validLogs) == 0 {
		return
	}
	if err := currentBus(p.client, p.logger).publish(ctx, projectID, validLogs); err != nil {
		p.logger.Warn("live tail: publish failed", slog.String("error", err.Error()))
	}
}
