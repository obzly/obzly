package main

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"os"
	"path/filepath"
	"time"

	"obzly/internal/cloud/discovery"
	"obzly/internal/synthetics/spec"
)

const (
	// postureFirstRunWithin: a connection's first posture refresh comes soon, spread out.
	postureFirstRunWithin = 5 * time.Minute
	postureRunTimeout     = 45 * time.Minute
	postureSendTimeout    = 5 * time.Minute
)

// postureFileState keeps the last refresh's time across restarts; the Site keeps the findings.
type postureFileState struct {
	LastRunAt time.Time `json:"lastRunAt"`
}

// postureLoop refreshes a connection's posture every RefreshHours on the member that owns it.
func (p *probe) postureLoop(ctx context.Context, connection spec.CloudConnection, destination spec.Destination) {
	every := postureInterval(connection)
	wait := time.Duration(rand.Int64N(int64(postureFirstRunWithin)))
	if last := p.loadPostureState(connection).LastRunAt; !last.IsZero() {
		wait = max(time.Until(last.Add(every)), wait)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		timer.Reset(every)
		if p.owns(cloudKey(connection.ID)) {
			p.runPosture(ctx, connection, destination)
		}
	}
}

// runPosture reads, sends and reports one refresh. A refresh the Site didn't take changes
// nothing there; the next one replaces it.
func (p *probe) runPosture(ctx context.Context, connection spec.CloudConnection, destination spec.Destination) {
	runCtx, cancel := context.WithTimeout(ctx, postureRunTimeout)
	result := discovery.CollectPosture(runCtx, connection, p.cloudOptions(connection, false), time.Now())
	cancel()
	if ctx.Err() != nil {
		return
	}
	if len(result.Batch.Runs) > 0 {
		sendCtx, cancelSend := context.WithTimeout(ctx, postureSendTimeout)
		if err := p.sender.out.SendPosture(sendCtx, destination, result.Batch); err != nil {
			result.Scan.Errors = append(result.Scan.Errors, "sending to the destination: "+err.Error())
			result.Scan.Uses[spec.UsePosture] = spec.UseResult{
				State: spec.UseError, Message: "couldn't send posture to the Site: " + err.Error(),
			}
		}
		cancelSend()
	}
	result.Scan.Probe = p.instance
	reportCtx, cancelReport := context.WithTimeout(ctx, cloudReportTimeout)
	if err := p.api.reportCloudScan(reportCtx, connection.ID, result.Scan); err != nil {
		p.log.Warn("Failed to report a posture refresh", "connection", connection.Name, "error", err)
	}
	cancelReport()
	p.log.Info("Cloud posture refresh", "connection", connection.Name, "findings", result.Scan.Posture.Findings,
		"native", len(result.Scan.Posture.Native), "baseline", len(result.Scan.Posture.Baseline),
		"state", result.Scan.Uses[spec.UsePosture].State)
	p.savePostureState(connection, postureFileState{LastRunAt: time.Now().UTC()})
}

func (p *probe) loadPostureState(connection spec.CloudConnection) postureFileState {
	var saved postureFileState
	if data, err := os.ReadFile(p.postureStatePath(connection)); err == nil {
		_ = json.Unmarshal(data, &saved)
	}
	return saved
}

func (p *probe) savePostureState(connection spec.CloudConnection, saved postureFileState) {
	path := p.postureStatePath(connection)
	data, err := json.Marshal(saved)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(path), 0o700)
	}
	if err == nil {
		err = os.WriteFile(path, data, 0o600)
	}
	if err != nil {
		p.log.Warn("Failed to save a connection's posture state", "connection", connection.Name, "error", err)
	}
}

func (p *probe) postureStatePath(connection spec.CloudConnection) string {
	return filepath.Join(filepath.Dir(p.cachePath), "cloud", connection.ID+".posture.json")
}

func postureInterval(connection spec.CloudConnection) time.Duration {
	hours := spec.DefaultPostureRefreshHours
	if connection.Posture != nil && connection.Posture.RefreshHours > 0 {
		hours = connection.Posture.RefreshHours
	}
	hours = min(max(hours, spec.MinPostureRefreshHours), spec.MaxPostureRefreshHours)
	return time.Duration(hours) * time.Hour
}
