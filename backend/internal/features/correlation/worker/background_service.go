// Package correlation_worker is the leader-locked worker that groups error
// logs and error spans into incident sessions. Per project and run it:
//  1. scans (watermark, now] for ERROR logs and non-OK spans (not metrics:
//     there is no metric-threshold signal to reuse yet), capped at
//     maxColdStartLookback so a cold start never backfills unbounded;
//  2. buckets candidates by (service, groupingWindow);
//  3. merges buckets across services only when they share a trace_id and
//     the spans-derived service graph connects them, so the merge is bounded
//     by observed edges rather than O(services²);
//  4. advances the watermark, unless the per-project budget ran out (then
//     the same window is retried next cycle).
//
// Correlation is dimensional and temporal only, not semantic/ML.
package correlation_worker

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"obzly/internal/config"
	correlation_core "obzly/internal/features/correlation/core"
	logs_core "obzly/internal/features/logs/core"
	projects_services "obzly/internal/features/projects/services"
	spans_core "obzly/internal/features/spans/core"
	"obzly/internal/util/leaderlock"

	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"
)

const (
	correlationInterval = 1 * time.Minute

	// maxColdStartLookback bounds a never-scanned (or stale) project's first scan.
	maxColdStartLookback = 24 * time.Hour

	// groupingWindow is a fixed, non-overlapping bucket: an incident that
	// straddles a boundary may split into two sessions (accepted tradeoff).
	groupingWindow = 5 * time.Minute

	perStreamQueryLimit = 500

	// A project whose scan crosses either cap keeps its watermark, so the
	// next cycle retries the same window rather than losing signals.
	maxSignalsPerProjectPerRun = 5000
	maxScanDurationPerProject  = 10 * time.Second
)

type CorrelationBackgroundService struct {
	projectService *projects_services.ProjectService
	logger         *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	leaderLockClient valkey.Client
}

func (s *CorrelationBackgroundService) StartWorkers() {
	s.ctx, s.cancel = context.WithCancel(context.Background())

	s.logger.Info("Starting correlation background worker",
		slog.Duration("interval", correlationInterval))

	s.wg.Add(1)
	go s.correlationWorker()

	s.logger.Info("Correlation worker started successfully")
}

func (s *CorrelationBackgroundService) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
}

// ExecuteAllTasksForTest runs one correlation pass synchronously.
func (s *CorrelationBackgroundService) ExecuteAllTasksForTest() error {
	return s.correlateAllProjects()
}

// ExecuteTaskForProjectForTest scans one project only: the shared test
// database holds thousands of other projects, which would swamp a
// per-project scan-cost assertion.
func (s *CorrelationBackgroundService) ExecuteTaskForProjectForTest(projectID uuid.UUID) error {
	return s.correlateProject(projectID)
}

func (s *CorrelationBackgroundService) correlationWorker() {
	defer s.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("Correlation worker panicked and recovered",
				slog.Any("panic", r),
				slog.String("worker", "correlation"))
		}
	}()

	ticker := time.NewTicker(correlationInterval)
	defer ticker.Stop()

	s.logger.Info("Correlation worker started", slog.Duration("interval", correlationInterval))

	for {
		if config.IsShouldShutdown() {
			s.logger.Info("Correlation worker shutting down due to shutdown signal")
			return
		}

		select {
		case <-s.ctx.Done():
			s.logger.Info("Correlation worker shutting down")
			return

		case <-ticker.C:
			ran, err := leaderlock.RunExclusive(s.ctx, s.leaderLockClient, "correlation-scan",
				correlationInterval,
				func(ctx context.Context) error { return s.correlateAllProjects() })
			if err != nil {
				s.logger.Error("Error during correlation scan", slog.String("error", err.Error()))
			} else if !ran {
				s.logger.Debug("Skipping correlation pass - another replica holds the lock")
			}
		}
	}
}

func (s *CorrelationBackgroundService) correlateAllProjects() error {
	projects, err := s.projectService.GetAllProjects()
	if err != nil {
		return fmt.Errorf("failed to get all projects: %w", err)
	}

	failures := 0
	processed := 0

	for _, project := range projects {
		if err := s.correlateProject(project.ID); err != nil {
			failures++
			s.logger.Error("Failed to correlate signals for project",
				slog.String("projectId", project.ID.String()),
				slog.String("error", err.Error()))
		}
		processed++
	}

	s.logger.Info("Correlation scan completed",
		slog.Int("processedProjects", processed),
		slog.Int("failures", failures))

	if failures > 0 {
		return fmt.Errorf("correlation scan failed for %d projects", failures)
	}
	return nil
}

// correlateProject runs one incremental pass (see the package doc).
func (s *CorrelationBackgroundService) correlateProject(projectID uuid.UUID) error {
	scan := newProjectScan(projectID, s.logger)

	watermark, err := correlation_core.GetIncidentSessionRepository().GetWatermark(projectID)
	if err != nil {
		return fmt.Errorf("failed to load watermark: %w", err)
	}

	now := time.Now().UTC()
	windowStart := watermark
	if windowStart.IsZero() || now.Sub(windowStart) > maxColdStartLookback {
		windowStart = now.Add(-maxColdStartLookback)
	}
	windowEnd := now

	if !windowStart.Before(windowEnd) {
		// Nothing new to scan; still a successful pass.
		return correlation_core.GetIncidentSessionRepository().SetWatermark(projectID, windowEnd)
	}

	groups, budgetExceeded, err := scan.run(windowStart, windowEnd)
	if err != nil {
		return fmt.Errorf("failed to scan project signals: %w", err)
	}

	for _, group := range groups {
		session, signals := group.toSession(projectID)
		if err := correlation_core.GetIncidentSessionRepository().CreateSession(session, signals); err != nil {
			s.logger.Error("Failed to persist incident session",
				slog.String("projectId", projectID.String()),
				slog.String("error", err.Error()))
		}
	}

	if budgetExceeded {
		s.logger.Warn(
			"Correlation scan budget exceeded - watermark not advanced, will retry next cycle",
			slog.String("projectId", projectID.String()),
			slog.Time("windowStart", windowStart),
			slog.Time("windowEnd", windowEnd),
		)
		return nil
	}

	return correlation_core.GetIncidentSessionRepository().SetWatermark(projectID, windowEnd)
}

type candidateSignal struct {
	Type       correlation_core.SignalType
	Ref        correlation_core.JSONMap
	OccurredAt time.Time
	TraceID    string
	Service    string
	Anomaly    bool
}

type bucketKey struct {
	Service string
	Bucket  int64
}

func bucketFor(service string, t time.Time) bucketKey {
	return bucketKey{Service: service, Bucket: t.Unix() / int64(groupingWindow.Seconds())}
}

// sessionGroup is one session to persist: a single bucket, or buckets
// merged across connected services via a shared trace_id.
type sessionGroup struct {
	services map[string]bool
	signals  []candidateSignal
	trigger  correlation_core.TriggerType
}

func (g *sessionGroup) toSession(
	projectID uuid.UUID,
) (*correlation_core.IncidentSession, []*correlation_core.IncidentSessionSignal) {
	serviceName := g.primaryService()

	windowStart := g.signals[0].OccurredAt
	windowEnd := g.signals[0].OccurredAt
	logCount, spanCount := 0, 0
	for _, sig := range g.signals {
		if sig.OccurredAt.Before(windowStart) {
			windowStart = sig.OccurredAt
		}
		if sig.OccurredAt.After(windowEnd) {
			windowEnd = sig.OccurredAt
		}
		switch sig.Type {
		case correlation_core.SignalTypeLog:
			logCount++
		case correlation_core.SignalTypeSpan:
			spanCount++
		}
	}

	session := &correlation_core.IncidentSession{
		ID:             uuid.New(),
		ProjectID:      projectID,
		ServiceName:    serviceName,
		WindowStart:    windowStart,
		WindowEnd:      windowEnd,
		TriggerType:    g.trigger,
		TriggerSummary: g.summary(logCount, spanCount),
	}

	signals := make([]*correlation_core.IncidentSessionSignal, 0, len(g.signals))
	for _, sig := range g.signals {
		signals = append(signals, &correlation_core.IncidentSessionSignal{
			SessionID:  session.ID,
			SignalType: sig.Type,
			SignalRef:  sig.Ref,
			OccurredAt: sig.OccurredAt,
		})
	}

	return session, signals
}

// primaryService is the lexicographically first member: a session has one
// ServiceName column, and the other services remain in its signals.
func (g *sessionGroup) primaryService() string {
	names := make([]string, 0, len(g.services))
	for name := range g.services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names[0]
}

func (g *sessionGroup) summary(logCount, spanCount int) string {
	names := make([]string, 0, len(g.services))
	for name := range g.services {
		names = append(names, name)
	}
	sort.Strings(names)

	return fmt.Sprintf(
		"%d error log(s), %d error span(s) across service(s): %v (dimensional+temporal correlation only, not semantic/ML)",
		logCount,
		spanCount,
		names,
	)
}

// projectScan is one run's state for one project; never reused.
type projectScan struct {
	projectID uuid.UUID
	logger    *slog.Logger

	deadline    time.Time
	rowsScanned int
}

func newProjectScan(projectID uuid.UUID, logger *slog.Logger) *projectScan {
	return &projectScan{
		projectID: projectID,
		logger:    logger,
		deadline:  time.Now().Add(maxScanDurationPerProject),
	}
}

func (s *projectScan) overBudget() bool {
	return s.rowsScanned >= maxSignalsPerProjectPerRun || time.Now().After(s.deadline)
}

// run returns the sessions to persist, and true if the budget ran out
// (the caller must then not advance the watermark).
func (s *projectScan) run(windowStart, windowEnd time.Time) ([]*sessionGroup, bool, error) {
	timeRangeSpans := &spans_core.TimeRangeDTO{From: &windowStart, To: &windowEnd}
	timeRangeLogs := &logs_core.TimeRangeDTO{From: &windowStart, To: &windowEnd}

	services, connected, err := s.loadServiceGraph(timeRangeSpans, timeRangeLogs)
	if err != nil {
		return nil, false, err
	}

	buckets := make(map[bucketKey][]candidateSignal)
	budgetExceeded := false

	for _, service := range services {
		if s.overBudget() {
			budgetExceeded = true
			break
		}

		logSignals, err := s.loadErrorLogs(service, timeRangeLogs)
		if err != nil {
			return nil, false, err
		}
		spanSignals, err := s.loadErrorSpans(service, timeRangeSpans)
		if err != nil {
			return nil, false, err
		}

		for _, sig := range append(logSignals, spanSignals...) {
			key := bucketFor(sig.Service, sig.OccurredAt)
			buckets[key] = append(buckets[key], sig)
		}
	}

	groups := buildSessionGroups(buckets, connected)
	return groups, budgetExceeded, nil
}

func (s *projectScan) loadServiceGraph(
	spansRange *spans_core.TimeRangeDTO,
	logsRange *logs_core.TimeRangeDTO,
) ([]string, map[[2]string]bool, error) {
	serviceSet := make(map[string]bool)
	connected := make(map[[2]string]bool)

	servicesResp, err := spans_core.GetSpanCoreRepository().ListServices(s.projectID, spansRange)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list services: %w", err)
	}
	for _, name := range servicesResp.Services {
		serviceSet[name] = true
	}

	spanEdges, err := spans_core.GetSpanCoreRepository().ServiceCallEdges(s.projectID, spansRange)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load span service edges: %w", err)
	}
	for _, edge := range spanEdges {
		serviceSet[edge.SrcService] = true
		serviceSet[edge.DstService] = true
		markConnected(connected, edge.SrcService, edge.DstService)
	}

	// Log rows don't carry service_name on the read path, so services with
	// ERROR logs come from an aggregation grouped by service_name.
	errorLogServices, err := logs_core.GetLogCoreRepository().
		ExecuteAggregation(context.Background(), s.projectID, &logs_core.AggregationRequestDTO{
			Query: &logs_core.QueryNode{
				Type: logs_core.QueryNodeTypeCondition,
				Condition: &logs_core.ConditionNode{
					Field:    "level",
					Operator: logs_core.ConditionOperatorEquals,
					Value:    string(logs_core.LogLevelError),
				},
			},
			TimeRange:  logsRange,
			GroupField: "service_name",
			Limit:      perStreamQueryLimit,
		})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to discover error-logging services: %w", err)
	}
	s.rowsScanned += len(errorLogServices.Buckets)
	for _, bucket := range errorLogServices.Buckets {
		if bucket.GroupKey != "" {
			serviceSet[bucket.GroupKey] = true
		}
	}

	services := make([]string, 0, len(serviceSet))
	for name := range serviceSet {
		services = append(services, name)
	}
	sort.Strings(services)

	return services, connected, nil
}

func markConnected(connected map[[2]string]bool, a, b string) {
	connected[[2]string{a, b}] = true
	connected[[2]string{b, a}] = true
}

func (s *projectScan) loadErrorLogs(
	service string,
	timeRange *logs_core.TimeRangeDTO,
) ([]candidateSignal, error) {
	response, err := logs_core.GetLogCoreRepository().
		ExecuteQueryForProject(s.projectID, &logs_core.LogQueryRequestDTO{
			Query: &logs_core.QueryNode{
				Type: logs_core.QueryNodeTypeLogical,
				Logic: &logs_core.LogicalNode{
					Operator: logs_core.LogicalOperatorAnd,
					Children: []logs_core.QueryNode{
						{
							Type: logs_core.QueryNodeTypeCondition,
							Condition: &logs_core.ConditionNode{
								Field:    "level",
								Operator: logs_core.ConditionOperatorEquals,
								Value:    string(logs_core.LogLevelError),
							},
						},
						{
							Type: logs_core.QueryNodeTypeCondition,
							Condition: &logs_core.ConditionNode{
								Field:    "service_name",
								Operator: logs_core.ConditionOperatorEquals,
								Value:    service,
							},
						},
					},
				},
			},
			TimeRange: timeRange,
			Limit:     perStreamQueryLimit,
		})
	if err != nil {
		return nil, fmt.Errorf("failed to query error logs for service %q: %w", service, err)
	}
	s.rowsScanned += len(response.Logs)

	signals := make([]candidateSignal, 0, len(response.Logs))
	for _, log := range response.Logs {
		signals = append(signals, candidateSignal{
			Type:       correlation_core.SignalTypeLog,
			Ref:        correlation_core.SignalRefLog(log.ID),
			OccurredAt: log.Timestamp,
			TraceID:    log.TraceID,
			Service:    service,
			Anomaly:    true,
		})
	}
	return signals, nil
}

func (s *projectScan) loadErrorSpans(
	service string,
	timeRange *spans_core.TimeRangeDTO,
) ([]candidateSignal, error) {
	traces, err := spans_core.GetSpanCoreRepository().
		ListTraces(context.Background(), s.projectID, &spans_core.ListTracesRequestDTO{
			TimeRange:   timeRange,
			ServiceName: service,
			ErrorsOnly:  true,
			Limit:       perStreamQueryLimit,
		})
	if err != nil {
		return nil, fmt.Errorf("failed to list error traces for service %q: %w", service, err)
	}
	s.rowsScanned += len(traces.Traces)

	var signals []candidateSignal
	for _, trace := range traces.Traces {
		if s.overBudget() {
			break
		}

		detail, err := spans_core.GetSpanCoreRepository().
			GetTrace(s.projectID, trace.TraceID, timeRange)
		if err != nil {
			return nil, fmt.Errorf("failed to load trace %q: %w", trace.TraceID, err)
		}
		s.rowsScanned += len(detail.Spans)

		for _, span := range detail.Spans {
			if span.StatusCode != "ERROR" || span.ServiceName != service {
				continue
			}
			signals = append(signals, candidateSignal{
				Type:       correlation_core.SignalTypeSpan,
				Ref:        correlation_core.SignalRefSpan(span.TraceID, span.SpanID),
				OccurredAt: span.StartTime,
				TraceID:    span.TraceID,
				Service:    service,
				Anomaly:    true,
			})
		}
	}
	return signals, nil
}

// buildSessionGroups makes each anomalous bucket a group, then merges
// groups whose signals share a trace_id only when the service graph
// connects their services.
func buildSessionGroups(
	buckets map[bucketKey][]candidateSignal,
	connected map[[2]string]bool,
) []*sessionGroup {
	groupByKey := make(map[bucketKey]*sessionGroup)

	var keys []bucketKey
	for key, signals := range buckets {
		if !hasAnomaly(signals) {
			continue
		}
		keys = append(keys, key)
		groupByKey[key] = &sessionGroup{
			services: map[string]bool{key.Service: true},
			signals:  append([]candidateSignal{}, signals...),
			trigger:  triggerFor(signals),
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Service != keys[j].Service {
			return keys[i].Service < keys[j].Service
		}
		return keys[i].Bucket < keys[j].Bucket
	})

	// Indexing buckets by trace ID finds merge candidates in O(signals).
	traceToKeys := make(map[string]map[bucketKey]bool)
	for _, key := range keys {
		for _, sig := range groupByKey[key].signals {
			if sig.TraceID == "" {
				continue
			}
			if traceToKeys[sig.TraceID] == nil {
				traceToKeys[sig.TraceID] = make(map[bucketKey]bool)
			}
			traceToKeys[sig.TraceID][key] = true
		}
	}

	parent := make(map[bucketKey]bucketKey)
	for _, key := range keys {
		parent[key] = key
	}
	var find func(bucketKey) bucketKey
	find = func(k bucketKey) bucketKey {
		if parent[k] == k {
			return k
		}
		parent[k] = find(parent[k])
		return parent[k]
	}
	union := func(a, b bucketKey) {
		rootA, rootB := find(a), find(b)
		if rootA != rootB {
			parent[rootA] = rootB
		}
	}

	for _, memberKeys := range traceToKeys {
		var members []bucketKey
		for k := range memberKeys {
			members = append(members, k)
		}
		for i := 0; i < len(members); i++ {
			for j := i + 1; j < len(members); j++ {
				a, b := members[i], members[j]
				if a.Service == b.Service {
					continue
				}
				if connected[[2]string{a.Service, b.Service}] {
					union(a, b)
				}
			}
		}
	}

	merged := make(map[bucketKey]*sessionGroup)
	for _, key := range keys {
		root := find(key)
		target, ok := merged[root]
		if !ok {
			target = &sessionGroup{services: make(map[string]bool)}
			merged[root] = target
		}
		src := groupByKey[key]
		for service := range src.services {
			target.services[service] = true
		}
		target.signals = append(target.signals, src.signals...)
		if len(target.services) > 1 {
			target.trigger = correlation_core.TriggerTypeTraceIDLink
		} else if target.trigger == "" {
			target.trigger = src.trigger
		}
	}

	groups := make([]*sessionGroup, 0, len(merged))
	for _, group := range merged {
		groups = append(groups, group)
	}
	return groups
}

func hasAnomaly(signals []candidateSignal) bool {
	for _, sig := range signals {
		if sig.Anomaly {
			return true
		}
	}
	return false
}

// triggerFor prefers error_log over span_error_status, just to be deterministic.
func triggerFor(signals []candidateSignal) correlation_core.TriggerType {
	hasLog, hasSpan := false, false
	for _, sig := range signals {
		switch sig.Type {
		case correlation_core.SignalTypeLog:
			hasLog = hasLog || sig.Anomaly
		case correlation_core.SignalTypeSpan:
			hasSpan = hasSpan || sig.Anomaly
		}
	}
	if hasLog {
		return correlation_core.TriggerTypeErrorLog
	}
	if hasSpan {
		return correlation_core.TriggerTypeSpanErrorStatus
	}
	return correlation_core.TriggerTypeErrorLog
}
