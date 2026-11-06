package alerting

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"obzly/internal/cache"
	"obzly/internal/config"
	logs_core "obzly/internal/features/logs/core"
	metrics_promql "obzly/internal/features/metrics/promql"
	spans_core "obzly/internal/features/spans/core"
	"obzly/internal/storage"
	"obzly/internal/util/leaderlock"

	"github.com/google/uuid"
	prom_labels "github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
	"gorm.io/gorm"
)

const (
	// evalTick: how often each shard looks for due rules; a rule runs
	// within this of its interval.
	evalTick = 10 * time.Second
	// evalTimeout bounds one rule's query.
	evalTimeout = 30 * time.Second
	// resolvedKeep: how long a resolved alert stays listed.
	resolvedKeep = 24 * time.Hour
	// maxSeriesPerRule caps the alerts one evaluation can produce.
	maxSeriesPerRule = 500
	// maxPatternSeries: new or spiking patterns a log patterns rule checks,
	// most logs / largest rise first.
	maxPatternSeries = 100
	// maxPatternLabelLength bounds the pattern label (a template).
	maxPatternLabelLength = 200
	// evalParallelism: rules of one shard evaluated at once, so a few slow
	// queries don't hold up the rest.
	evalParallelism = 8
)

// sample is one series' value from a rule's query.
type sample struct {
	labels Labels
	value  float64
}

// Evaluator runs alert rules on worker replicas, each of
// ALERT_EVAL_SHARDS shards under its own leader lock.
type Evaluator struct {
	repo *repository
	log  *slog.Logger
	now  func() time.Time
}

// Start runs every shard's loop until ctx ends.
func (e *Evaluator) Start(ctx context.Context) {
	shards := max(config.GetEnv().AlertEvalShards, 1)
	for shard := range shards {
		go e.loop(ctx, shard, shards)
	}
}

func (e *Evaluator) loop(ctx context.Context, shard, shards int) {
	ticker := time.NewTicker(evalTick)
	defer ticker.Stop()
	name := fmt.Sprintf("alert-eval-%d-of-%d", shard, shards)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if _, err := leaderlock.RunExclusive(ctx, cache.GetCache(), name, 2*evalTimeout,
			func(ctx context.Context) error { return e.runShard(ctx, shard, shards) }); err != nil {
			e.log.Error("alert evaluation failed", "shard", shard, "error", err)
		}
	}
}

// runShard evaluates the shard's rules that are due, evalParallelism at
// once. Each rule is claimed first, so a rule another runner already took
// (an overlapping pass) isn't evaluated twice.
func (e *Evaluator) runShard(ctx context.Context, shard, shards int) error {
	rules, err := e.repo.enabledRules()
	if err != nil {
		return err
	}
	now := e.now()
	work := make(chan *AlertRule)
	var wg sync.WaitGroup
	for range evalParallelism {
		wg.Go(func() {
			for rule := range work {
				claimed, err := e.repo.claimRule(rule.ID, rule.LastEvalAt, e.now())
				if err != nil {
					e.log.Error("alert rule claim failed", "ruleId", rule.ID, "error", err)
					continue
				}
				if !claimed {
					continue
				}
				if err := e.Evaluate(ctx, rule); err != nil {
					e.log.Error("alert rule evaluation failed", "ruleId", rule.ID, "error", err)
				}
			}
		})
	}
	for _, rule := range rules {
		if shardOf(rule.ID, shards) != shard {
			continue
		}
		if rule.LastEvalAt != nil && now.Before(rule.LastEvalAt.Add(time.Duration(rule.IntervalSeconds)*time.Second)) {
			continue
		}
		select {
		case work <- rule:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(work)
	wg.Wait()
	return ctx.Err()
}

// Evaluate runs one rule now: query, compare, advance each series' state,
// and queue the notifications - states and outbox in one transaction.
func (e *Evaluator) Evaluate(ctx context.Context, rule *AlertRule) error {
	now := e.now()
	queryCtx, cancel := context.WithTimeout(ctx, evalTimeout)
	samples, queryErr := e.query(queryCtx, rule, now)
	cancel()
	return storage.GetDb().Transaction(func(tx *gorm.DB) error {
		lastError := ""
		if queryErr != nil {
			// As in Prometheus, a failed evaluation changes no state.
			lastError = queryErr.Error()
		} else if err := e.advance(tx, rule, samples, now); err != nil {
			return err
		}
		return tx.Model(&AlertRule{}).Where("id = ?", rule.ID).
			Updates(map[string]any{"last_eval_at": now, "last_error": truncate(lastError, 1000)}).Error
	})
}

// advance applies the state machine: a series past the threshold becomes
// pending, then firing after ForSeconds (notify, and again every
// RepeatSeconds); a firing series no longer past it resolves (notify); a
// pending one is dropped.
func (e *Evaluator) advance(tx *gorm.DB, rule *AlertRule, samples []sample, now time.Time) error {
	var existing []*AlertState
	if err := tx.Where("rule_id = ?", rule.ID).Find(&existing).Error; err != nil {
		return err
	}
	states := make(map[string]*AlertState, len(existing))
	for _, state := range existing {
		states[state.Fingerprint] = state
	}
	var silences []*Silence
	silencesLoaded := false
	mutedNow := func(labels Labels) (bool, error) {
		if !silencesLoaded {
			if err := tx.Where("project_id = ? AND starts_at <= ? AND ends_at > ?", rule.ProjectID, now, now).
				Find(&silences).Error; err != nil {
				return false, err
			}
			silencesLoaded = true
		}
		return silenced(silences, labels), nil
	}
	active := map[string]bool{}
	for _, s := range samples {
		if !compare(rule.Comparison, s.value, rule.Threshold) {
			continue
		}
		labels := alertLabels(rule, s.labels)
		fp := fingerprint(labels)
		active[fp] = true
		state, ok := states[fp]
		if !ok || state.State == StateResolved {
			state = &AlertState{RuleID: rule.ID, Fingerprint: fp, State: StatePending, ActiveAt: now}
		}
		state.Labels, state.Value, state.UpdatedAt = labels, s.value, now
		if state.State == StatePending && !now.Before(state.ActiveAt.Add(time.Duration(rule.ForSeconds)*time.Second)) {
			state.State, state.FiredAt = StateFiring, &now
		}
		if state.State == StateFiring && dueToNotify(rule, state, now) {
			muted, err := mutedNow(labels)
			if err != nil {
				return err
			}
			if muted {
				// Held, not dropped: the first evaluation after the silence
				// (a maintenance window) ends notifies if it still fires.
				state.LastNotifiedAt = nil
			} else {
				if err := e.enqueue(tx, rule, state, StatusFiring, now); err != nil {
					return err
				}
				state.LastNotifiedAt = &now
			}
		}
		if err := tx.Save(state).Error; err != nil {
			return err
		}
	}
	for fp, state := range states {
		if active[fp] {
			continue
		}
		switch state.State {
		case StatePending:
			if err := tx.Delete(state).Error; err != nil {
				return err
			}
		case StateFiring:
			state.State, state.ResolvedAt, state.UpdatedAt = StateResolved, &now, now
			if err := e.enqueue(tx, rule, state, StatusResolved, now); err != nil {
				return err
			}
			if err := tx.Save(state).Error; err != nil {
				return err
			}
		case StateResolved:
			if state.ResolvedAt != nil && now.Sub(*state.ResolvedAt) > resolvedKeep {
				if err := tx.Delete(state).Error; err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func dueToNotify(rule *AlertRule, state *AlertState, now time.Time) bool {
	if state.LastNotifiedAt == nil {
		return true
	}
	return rule.RepeatSeconds > 0 &&
		!now.Before(state.LastNotifiedAt.Add(time.Duration(rule.RepeatSeconds)*time.Second))
}

func (e *Evaluator) enqueue(tx *gorm.DB, rule *AlertRule, state *AlertState, status string, now time.Time) error {
	ruleID := rule.ID
	notification := Notification{
		Status: status, ProjectID: rule.ProjectID, RuleID: &ruleID, RuleName: rule.Name, Source: rule.Source,
		Summary: rule.Summary, Comparison: rule.Comparison, Threshold: rule.Threshold, Measured: state.Value,
		Labels: state.Labels, Fingerprint: state.Fingerprint, StartsAt: state.ActiveAt, EndsAt: state.ResolvedAt,
	}
	for _, channelID := range rule.ChannelIDs {
		item := &OutboxItem{
			ID: uuid.New(), ProjectID: rule.ProjectID, ChannelID: channelID, RuleID: &ruleID,
			Fingerprint: state.Fingerprint, Status: status, Payload: notification, NextAttemptAt: now, CreatedAt: now,
		}
		if err := tx.Create(item).Error; err != nil {
			return err
		}
	}
	return nil
}

func (e *Evaluator) query(ctx context.Context, rule *AlertRule, now time.Time) ([]sample, error) {
	from := now.Add(-time.Duration(rule.WindowSeconds) * time.Second)
	switch rule.Source {
	case SourceMetrics:
		return promQLSamples(ctx, rule.ProjectID, rule.Query.PromQL, now)
	case SourceLogs:
		response, err := logs_core.GetLogCoreRepository().
			ExecuteAggregation(ctx, rule.ProjectID, &logs_core.AggregationRequestDTO{
				Query: rule.Query.Logs, TimeRange: &logs_core.TimeRangeDTO{From: &from, To: &now},
				GroupField: rule.Query.GroupBy, Limit: maxSeriesPerRule, ScanBudget: config.GetEnv().QueryMaxScanBytes,
			})
		if err != nil {
			return nil, err
		}
		var samples []sample
		for _, bucket := range response.Buckets {
			samples = append(
				samples,
				sample{labels: groupLabels(rule.Query.GroupBy, bucket.GroupKey), value: float64(bucket.Count)},
			)
		}
		if rule.Query.GroupBy == "" && len(samples) == 0 {
			samples = []sample{{labels: Labels{}, value: 0}}
		}
		return samples, nil
	case SourceLogPatterns:
		baselineFrom := from.Add(-time.Duration(rule.Query.BaselineSeconds) * time.Second)
		baselineTo := from.Add(-time.Nanosecond)
		response, err := logs_core.GetLogCoreRepository().
			ExecutePatternChanges(ctx, rule.ProjectID, &logs_core.PatternChangesRequestDTO{
				Query:    rule.Query.Logs,
				Current:  &logs_core.TimeRangeDTO{From: &from, To: &now},
				Baseline: &logs_core.TimeRangeDTO{From: &baselineFrom, To: &baselineTo},
				MinCount: rule.Query.MinCount, Limit: maxPatternSeries, ScanBudget: config.GetEnv().QueryMaxScanBytes,
			})
		if err != nil {
			return nil, err
		}
		var samples []sample
		for _, change := range response.Changes {
			if change.Change == logs_core.PatternChangeDrop {
				continue
			}
			samples = append(samples, sample{
				labels: Labels{
					"pattern_hash": change.Hash, "change": change.Change,
					"pattern": truncate(change.Pattern, maxPatternLabelLength),
				},
				value: change.Ratio,
			})
		}
		return samples, nil
	case SourceTraces:
		metric := spans_core.AggregationMetric(rule.Query.Metric)
		if metric == "" {
			metric = spans_core.AggregationMetricCount
		}
		response, err := spans_core.GetSpanCoreRepository().
			ExecuteAggregation(rule.ProjectID, &spans_core.AggregationRequestDTO{
				TimeRange:   &spans_core.TimeRangeDTO{From: &from, To: &now},
				ServiceName: rule.Query.ServiceName, OperationName: rule.Query.OperationName,
				RootSpansOnly: rule.Query.RootSpansOnly, GroupField: spans_core.GroupField(rule.Query.GroupBy),
				Metric: metric, Limit: maxSeriesPerRule, ScanBudget: config.GetEnv().QueryMaxScanBytes,
			})
		if err != nil {
			return nil, err
		}
		var samples []sample
		for _, bucket := range response.Buckets {
			samples = append(
				samples,
				sample{labels: groupLabels(rule.Query.GroupBy, bucket.GroupKey), value: bucket.Value},
			)
		}
		countLike := metric == spans_core.AggregationMetricCount || metric == spans_core.AggregationMetricErrorCount
		if rule.Query.GroupBy == "" && len(samples) == 0 && countLike {
			samples = []sample{{labels: Labels{}, value: 0}}
		}
		return samples, nil
	case SourceAICost:
		if aiCostSource == nil {
			return nil, errors.New("AI budgets aren't available on this Site")
		}
		computed, err := aiCostSource(ctx, rule, now)
		if err != nil {
			return nil, err
		}
		samples := make([]sample, 0, len(computed))
		for _, s := range computed {
			samples = append(samples, sample{labels: s.Labels, value: s.Value})
		}
		if len(samples) > maxSeriesPerRule {
			samples = samples[:maxSeriesPerRule]
		}
		return samples, nil
	}
	return nil, fmt.Errorf("unknown rule source %q", rule.Source)
}

func promQLSamples(ctx context.Context, projectID uuid.UUID, expression string, now time.Time) ([]sample, error) {
	query, err := metrics_promql.NewInstantQuery(ctx, projectID, metrics_promql.NormalizePromQL(expression), now)
	if err != nil {
		return nil, err
	}
	defer query.Close()
	result := query.Exec(ctx)
	if result.Err != nil {
		return nil, result.Err
	}
	switch value := result.Value.(type) {
	case promql.Vector:
		samples := make([]sample, 0, len(value))
		for _, point := range value {
			labels := Labels{}
			point.Metric.Range(func(label prom_labels.Label) {
				if label.Name != "__name__" {
					labels[label.Name] = label.Value
				}
			})
			samples = append(samples, sample{labels: labels, value: point.F})
		}
		if len(samples) > maxSeriesPerRule {
			return nil, fmt.Errorf(
				"query returns %d series; a rule may watch at most %d",
				len(samples),
				maxSeriesPerRule,
			)
		}
		return samples, nil
	case promql.Scalar:
		return []sample{{labels: Labels{}, value: value.V}}, nil
	default:
		return nil, errors.New("the query must return an instant vector or a scalar")
	}
}

func groupLabels(field, key string) Labels {
	if field == "" {
		return Labels{}
	}
	return Labels{field: key}
}

// alertLabels identify an alert: the rule's labels, the series', and its
// name.
func alertLabels(rule *AlertRule, series Labels) Labels {
	labels := Labels{}
	for key, value := range rule.Labels {
		labels[key] = value
	}
	for key, value := range series {
		labels[key] = value
	}
	labels["alertname"] = rule.Name
	return labels
}

func fingerprint(labels Labels) string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hash := sha256.New()
	for _, key := range keys {
		hash.Write([]byte(key + "\x00" + labels[key] + "\x00"))
	}
	return hex.EncodeToString(hash.Sum(nil))[:16]
}

func compare(op string, value, threshold float64) bool {
	if math.IsNaN(value) {
		return false
	}
	switch op {
	case ">":
		return value > threshold
	case ">=":
		return value >= threshold
	case "<":
		return value < threshold
	case "<=":
		return value <= threshold
	case "==":
		return value == threshold
	case "!=":
		return value != threshold
	}
	return false
}

func shardOf(id uuid.UUID, shards int) int {
	hash := fnv.New32a()
	hash.Write(id[:])
	return int(hash.Sum32() % uint32(shards))
}

// truncate cuts value to at most limit bytes of valid UTF-8: Postgres
// rejects invalid UTF-8, which would fail every write of the value.
func truncate(value string, limit int) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}
