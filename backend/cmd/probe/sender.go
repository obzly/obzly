package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"obzly/internal/synthetics/report"
	"obzly/internal/synthetics/spec"
)

const (
	flushEvery  = 10 * time.Second
	sendTimeout = 20 * time.Second
	// maxBuffered bounds results kept per destination while it is
	// unreachable; the oldest go first.
	maxBuffered = 5000
	// maxPerPost keeps each export well under ingest's body limit.
	maxPerPost   = 500
	firstBackoff = 10 * time.Second
	maxBackoff   = 5 * time.Minute
)

// sender batches results per destination and retries what a Site couldn't take.
type sender struct {
	out *report.Sender
	log *slog.Logger

	mu      sync.Mutex
	pending map[string]*batch
}

// batch is one destination's queue, sent from the front in chunks.
type batch struct {
	destination spec.Destination
	probe       spec.ProbeInfo
	results     []spec.RunResult
	dropped     int
	// metricsSent leading results already have their metrics delivered, so a retry after a
	// failed logs post sends only the logs (no duplicate points).
	metricsSent int
	// sending leading results are in a post now; trimming leaves them alone.
	sending int
	busy    bool
	backoff time.Duration
	retryAt time.Time
}

func newSender(out *report.Sender, log *slog.Logger) *sender {
	return &sender{out: out, log: log, pending: map[string]*batch{}}
}

// setDestination keeps the destination as the config has it now. Results of a run started under
// an older config (with a since rotated key) are sent with this one.
func (s *sender) setDestination(destination spec.Destination, probe spec.ProbeInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending := s.batchLocked(destination, probe)
	pending.destination, pending.probe = destination, probe
}

func (s *sender) add(destination spec.Destination, probe spec.ProbeInfo, result spec.RunResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending := s.batchLocked(destination, probe)
	pending.results = append(pending.results, result)
	pending.trim()
}

func (s *sender) run(ctx context.Context) {
	ticker := time.NewTicker(flushEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.flush(ctx, false)
		}
	}
}

// flush sends every destination's results, all destinations at once. Destinations backing off
// after a failure wait, unless this is the final flush.
func (s *sender) flush(ctx context.Context, final bool) {
	now := time.Now()
	s.mu.Lock()
	var due []*batch
	for _, pending := range s.pending {
		if len(pending.results) > 0 && !pending.busy && (final || !now.Before(pending.retryAt)) {
			pending.busy = true
			due = append(due, pending)
		}
	}
	s.mu.Unlock()

	var wait sync.WaitGroup
	for _, pending := range due {
		wait.Go(func() { s.drain(ctx, pending) })
	}
	wait.Wait()
}

// drain sends pending's results a chunk at a time until none are left or a post fails.
func (s *sender) drain(ctx context.Context, pending *batch) {
	defer func() {
		s.mu.Lock()
		pending.busy = false
		s.mu.Unlock()
	}()
	for {
		s.mu.Lock()
		destination, probe, metricsSent, dropped := pending.destination, pending.probe, pending.metricsSent,
			pending.dropped
		size := min(len(pending.results), maxPerPost)
		if metricsSent > 0 {
			size = metricsSent
		}
		chunk := pending.results[:size:size]
		pending.sending, pending.dropped = size, 0
		s.mu.Unlock()
		if dropped > 0 {
			s.log.Warn("Dropped the oldest results while the Site was unreachable",
				"destination", destination.OTLPURL, "dropped", dropped)
		}
		if size == 0 {
			return
		}

		sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
		var err error
		if metricsSent == 0 {
			err = s.out.SendMetrics(sendCtx, destination, probe, chunk)
			if err == nil {
				s.mu.Lock()
				pending.metricsSent = size
				s.mu.Unlock()
			}
		}
		if err == nil {
			err = s.out.SendLogs(sendCtx, destination, probe, chunk)
		}
		cancel()

		var refused *report.StatusError
		s.mu.Lock()
		pending.sending = 0
		switch {
		case err == nil:
			pending.results, pending.metricsSent = pending.results[size:], 0
			pending.backoff, pending.retryAt = 0, time.Time{}
		case errors.As(err, &refused) && !refused.Retryable():
			pending.results, pending.metricsSent = pending.results[size:], 0
		case ctx.Err() == nil:
			pending.backoff = min(max(2*pending.backoff, firstBackoff), maxBackoff)
			pending.retryAt = time.Now().Add(pending.backoff)
		}
		backoff := pending.backoff
		s.mu.Unlock()

		switch {
		case err == nil:
			continue
		case refused != nil && !refused.Retryable():
			s.log.Warn("The Site refused results; dropping them", "destination", destination.OTLPURL,
				"results", size, "status", refused.Status, "error", err)
			continue
		case ctx.Err() == nil:
			s.log.Warn("Failed to send results; will retry", "destination", destination.OTLPURL,
				"results", size, "retryIn", backoff, "error", err)
		}
		return
	}
}

func (s *sender) batchLocked(destination spec.Destination, probe spec.ProbeInfo) *batch {
	pending, ok := s.pending[destination.ID]
	if !ok {
		pending = &batch{destination: destination, probe: probe}
		s.pending[destination.ID] = pending
	}
	return pending
}

// trim drops the oldest results over maxBuffered, except those in a post or whose metrics went
// out already.
func (b *batch) trim() {
	keep := max(b.sending, b.metricsSent)
	over := min(len(b.results)-maxBuffered, len(b.results)-keep)
	if over <= 0 {
		return
	}
	b.results = append(b.results[:keep], b.results[keep+over:]...)
	b.dropped += over
}
