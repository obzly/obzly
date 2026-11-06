// Package system_selfmetrics refreshes the cluster-wide telemetry gauges -
// the Valkey logs queue depth and catalog backlog - that no request path
// updates on its own. Every sampling replica reports the same values, so
// dashboards and alerts aggregate them with max(), not sum().
package system_selfmetrics

import (
	"context"
	"log/slog"
	"time"

	"obzly/internal/broker"
	"obzly/internal/storage/segments"
	"obzly/internal/telemetry"
)

// SampleInterval is short enough for a 5-minute alert window and cheap: one
// LLEN, and one aggregate query per stream against an indexed table.
const SampleInterval = 15 * time.Second

// jetStreamSampleTimeout bounds one stream info request.
const jetStreamSampleTimeout = 5 * time.Second

// smallSegmentThresholdBytes matches the compactors' own threshold.
const smallSegmentThresholdBytes = segments.DefaultMaxBytes / 4

var catalogStreams = []string{"logs", "ai_logs", "spans", "metrics", "metrics_series", "metrics_5m", "metrics_1h"}

// Options selects what this replica samples.
type Options struct {
	// Catalog: replicas running background workers (worker, all), which
	// keeps the catalog queries to a few replicas.
	Catalog bool
	// ObjectStorageEnabled: without object storage every segment is
	// "pending upload" forever, so the upload gauges stay 0.
	ObjectStorageEnabled bool
	// JetStream: sample the ingest streams (replicas running background
	// workers, when the JetStream buffer is in use).
	JetStream bool
}

type sampler struct {
	log  *slog.Logger
	opts Options
	repo *segments.SegmentRepository
}

// Start samples once immediately, then every SampleInterval until ctx ends.
func Start(ctx context.Context, log *slog.Logger, opts Options) {
	if !opts.Catalog && !opts.JetStream {
		return
	}
	s := &sampler{log: log, opts: opts, repo: &segments.SegmentRepository{}}
	go func() {
		ticker := time.NewTicker(SampleInterval)
		defer ticker.Stop()
		for {
			s.sampleOnce()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (s *sampler) sampleOnce() {
	if s.opts.Catalog {
		for _, stream := range catalogStreams {
			if err := s.sampleCatalog(stream); err != nil {
				s.log.Warn("self-metrics: failed to sample segment catalog", "stream", stream, "error", err)
			}
		}
	}

	if client := broker.Default(); s.opts.JetStream && client != nil {
		for _, stream := range broker.IngestStreams {
			if err := sampleJetStream(client, stream.Name); err != nil {
				s.log.Warn("self-metrics: failed to sample ingest stream", "stream", stream.Name, "error", err)
			}
		}
	}
}

func sampleJetStream(client *broker.Client, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), jetStreamSampleTimeout)
	defer cancel()
	stream, err := client.JetStream.Stream(ctx, name)
	if err != nil {
		return err
	}
	info, err := stream.Info(ctx)
	if err != nil {
		return err
	}

	telemetry.JetStreamStreamBytes.WithLabelValues(name).Set(float64(info.State.Bytes))
	telemetry.JetStreamStreamMaxBytes.WithLabelValues(name).Set(float64(info.Config.MaxBytes))
	telemetry.JetStreamStreamMessages.WithLabelValues(name).Set(float64(info.State.Msgs))
	lagging := 0
	if info.Cluster != nil {
		for _, replica := range info.Cluster.Replicas {
			if replica.Offline || !replica.Current {
				lagging++
			}
		}
	}
	telemetry.JetStreamReplicasLagging.WithLabelValues(name).Set(float64(lagging))
	return nil
}

func (s *sampler) sampleCatalog(stream string) error {
	backlog, err := s.repo.CountBacklog(stream, smallSegmentThresholdBytes)
	if err != nil {
		return err
	}
	telemetry.CatalogSegments.WithLabelValues(stream).Set(float64(backlog.Total))
	telemetry.CatalogSmallSegments.WithLabelValues(stream).Set(float64(backlog.SmallSegments))

	var pending, oldestAge float64
	if s.opts.ObjectStorageEnabled {
		pending = float64(backlog.PendingUpload)
		if backlog.OldestPendingUploadAt != nil {
			oldestAge = time.Since(*backlog.OldestPendingUploadAt).Seconds()
		}
	}
	telemetry.CatalogPendingUpload.WithLabelValues(stream).Set(pending)
	telemetry.CatalogOldestPendingUploadAge.WithLabelValues(stream).Set(oldestAge)
	return nil
}
