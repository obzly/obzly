package main

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"time"

	"obzly/internal/cloud/discovery"
	"obzly/internal/synthetics/spec"
)

const (
	// costFirstRunWithin: a connection's first cost refresh comes soon, spread out.
	costFirstRunWithin = 3 * time.Minute
	costRunTimeout     = 30 * time.Minute
	costSendTimeout    = 5 * time.Minute
	adviceRunTimeout   = 20 * time.Minute
)

// costFileState is a connection's cost state on disk; Scope resets it when what the connection
// reads changes (another export location, source or billing scope).
type costFileState struct {
	Scope     string              `json:"scope"`
	LastRunAt time.Time           `json:"lastRunAt"`
	State     discovery.CostState `json:"state"`
}

func hasUse(connection spec.CloudConnection, use string) bool {
	return slices.Contains(connection.Uses, use)
}

// readsCost: the connection reads its own billing data (not via another connection).
func readsCost(connection spec.CloudConnection) bool {
	return hasUse(connection, spec.UseCost) && connection.Cost != nil && connection.Cost.Scope != spec.CostScopeVia
}

// costLoop refreshes a connection's cost every RefreshHours on the member that owns it.
func (p *probe) costLoop(ctx context.Context, connection spec.CloudConnection, destination spec.Destination) {
	every := costInterval(connection)
	saved := p.loadCostState(connection)
	wait := time.Duration(rand.Int64N(int64(costFirstRunWithin)))
	if !saved.LastRunAt.IsZero() {
		wait = max(time.Until(saved.LastRunAt.Add(every)), wait)
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
		if !p.owns(cloudKey(connection.ID)) {
			continue
		}
		saved = p.runCost(ctx, connection, destination, saved)
	}
}

// runCost reads what changed, sends it and reports; a batch the Site didn't take is read again
// next time.
func (p *probe) runCost(
	ctx context.Context, connection spec.CloudConnection, destination spec.Destination, saved costFileState,
) costFileState {
	runCtx, cancel := context.WithTimeout(ctx, costRunTimeout)
	result := discovery.CollectCost(runCtx, connection, saved.State, p.cloudOptions(connection, false), time.Now())
	cancel()
	if ctx.Err() != nil {
		return saved
	}
	next := saved.State
	next.Periods = map[string]string{}
	for period, fingerprint := range saved.State.Periods {
		next.Periods[period] = fingerprint
	}
	sentAll := true
	for _, delivery := range result.Deliveries {
		sendCtx, cancelSend := context.WithTimeout(ctx, costSendTimeout)
		err := p.sender.out.SendCost(sendCtx, destination, delivery.Batch)
		cancelSend()
		if err != nil {
			sentAll = false
			result.Scan.Errors = append(result.Scan.Errors, "sending to the destination: "+err.Error())
			result.Scan.Uses[spec.UseCost] = spec.UseResult{
				State: spec.UseError, Message: "couldn't send cost to the Site: " + err.Error(),
			}
			break
		}
		switch {
		case delivery.Period != "":
			next.Periods[delivery.Period] = delivery.Fingerprint
		case delivery.Batch.Source == spec.CostSourceAPI && delivery.Batch.From != "":
			next.APIBackfilled = true
		}
	}
	if sentAll {
		next.ExportThrough = result.ExportThrough
	}
	if ctx.Err() == nil && adviceDue(saved.State.AdviceAt, time.Now()) {
		if p.runAdvice(ctx, connection, destination, &result.Scan) {
			next.AdviceAt = time.Now().UTC()
		}
	}
	result.Scan.Probe = p.instance
	reportCtx, cancelReport := context.WithTimeout(ctx, cloudReportTimeout)
	if err := p.api.reportCloudScan(reportCtx, connection.ID, result.Scan); err != nil {
		p.log.Warn("Failed to report a cost refresh", "connection", connection.Name, "error", err)
	}
	cancelReport()
	p.log.Info(
		"Cloud cost refresh",
		"connection",
		connection.Name,
		"source",
		result.Scan.Cost.Source,
		"rows",
		result.Scan.Cost.Rows,
		"requests",
		result.Scan.Cost.Requests,
		"state",
		result.Scan.Uses[spec.UseCost].State,
	)
	saved = costFileState{Scope: costScope(connection), LastRunAt: time.Now().UTC(), State: next}
	p.saveCostState(connection, saved)
	return saved
}

// runAdvice reads the connection's recommendations, commitments and purchase options and sends
// them to the Site; false when they weren't sent (tried again on the next cost refresh).
func (p *probe) runAdvice(
	ctx context.Context, connection spec.CloudConnection, destination spec.Destination, scan *spec.CloudScan,
) bool {
	runCtx, cancel := context.WithTimeout(ctx, adviceRunTimeout)
	result := discovery.CollectAdvice(runCtx, connection, p.cloudOptions(connection, false), time.Now())
	cancel()
	if ctx.Err() != nil {
		return false
	}
	if scan.Cost != nil {
		scan.Cost.Requests += result.Requests
		scan.Cost.Advice = &result.Info
	}
	sendCtx, cancelSend := context.WithTimeout(ctx, costSendTimeout)
	err := p.sender.out.SendAdvice(sendCtx, destination, result.Batch)
	cancelSend()
	if err != nil {
		scan.Errors = append(scan.Errors, "sending recommendations to the destination: "+err.Error())
		p.log.Warn("Failed to send cost recommendations", "connection", connection.Name, "error", err)
		return false
	}
	p.log.Info("Cloud cost recommendations", "connection", connection.Name,
		"recommendations", result.Info.Recommendations, "commitments", result.Info.Commitments,
		"options", result.Info.Options, "requests", result.Requests)
	return true
}

// adviceDue: recommendations are read once a week.
func adviceDue(last, now time.Time) bool {
	return last.IsZero() || now.Sub(last) >= spec.AdviceRefreshDays*24*time.Hour
}

func (p *probe) loadCostState(connection spec.CloudConnection) costFileState {
	data, err := os.ReadFile(p.costStatePath(connection))
	if err != nil {
		return costFileState{}
	}
	var saved costFileState
	if json.Unmarshal(data, &saved) != nil || saved.Scope != costScope(connection) {
		return costFileState{}
	}
	return saved
}

func (p *probe) saveCostState(connection spec.CloudConnection, saved costFileState) {
	path := p.costStatePath(connection)
	data, err := json.Marshal(saved)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(path), 0o700)
	}
	if err == nil {
		err = os.WriteFile(path+".tmp", data, 0o600)
	}
	if err == nil {
		err = os.Rename(path+".tmp", path)
	}
	if err != nil {
		p.log.Warn("Failed to save a connection's cost state", "connection", connection.Name, "error", err)
	}
}

func (p *probe) costStatePath(connection spec.CloudConnection) string {
	return filepath.Join(filepath.Dir(p.cachePath), "cloud", connection.ID+".cost.json")
}

// costScope identifies what a connection reads for cost: its account and sign-in, and the cost
// settings except how often they refresh.
func costScope(connection spec.CloudConnection) string {
	scope := struct {
		Provider, ScopeID, Access, Identity string
		Cost                                spec.CostSettings
	}{Provider: connection.Provider, ScopeID: connection.ScopeID, Access: connection.Access,
		Identity: connection.Identity}
	if connection.Cost != nil {
		scope.Cost = *connection.Cost
		scope.Cost.RefreshHours = 0
	}
	encoded, _ := json.Marshal(scope)
	return string(encoded)
}

func costInterval(connection spec.CloudConnection) time.Duration {
	hours := spec.DefaultCostRefreshHours
	if connection.Cost != nil && connection.Cost.RefreshHours > 0 {
		hours = connection.Cost.RefreshHours
	}
	hours = min(max(hours, spec.MinCostRefreshHours), spec.MaxCostRefreshHours)
	return time.Duration(hours) * time.Hour
}
