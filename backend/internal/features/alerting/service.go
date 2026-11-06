package alerting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"obzly/internal/authz"
	"obzly/internal/features/audit_logs"
	metrics_promql "obzly/internal/features/metrics/promql"
	spans_core "obzly/internal/features/spans/core"
	users_models "obzly/internal/features/users/models"
	users_repositories "obzly/internal/features/users/repositories"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var (
	ErrForbidden = errors.New("insufficient permissions")
	ErrNotFound  = errors.New("not found")
	ErrManaged   = errors.New("this rule is managed by Synthetics: change its monitor's alerts on the control plane")
	// ErrManagedSilence: a maintenance window's silence, changed only on the control plane.
	ErrManagedSilence = errors.New(
		"this silence comes from a Synthetics maintenance window: change the window on the control plane",
	)
	// ErrManagedByBudget answers like ErrManaged (409).
	ErrManagedByBudget = errors.New(
		"this rule is kept by an AI budget: change the budget in AI Observability, Budgets")
)

// managedError is why a managed rule can't be changed here.
func managedError(rule *AlertRule) error {
	if strings.HasPrefix(rule.ManagedBy, ManagedByBudgetPrefix) {
		return ErrManagedByBudget
	}
	return ErrManaged
}

var (
	comparisons  = []string{">", ">=", "<", "<=", "==", "!="}
	traceMetrics = []string{
		string(spans_core.AggregationMetricCount), string(spans_core.AggregationMetricErrorCount),
		string(spans_core.AggregationMetricAvgDuration), string(spans_core.AggregationMetricP50Duration),
		string(spans_core.AggregationMetricP95Duration), string(spans_core.AggregationMetricP99Duration),
	}
	traceGroups = []string{"", string(spans_core.GroupFieldServiceName), string(spans_core.GroupFieldName),
		string(spans_core.GroupFieldStatusCode), string(spans_core.GroupFieldKind)}
	labelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

// Service is alerting's operations. Rules: project owners and admins; silences: members
// who can write; channels: the project owner or a global admin (they hold credentials);
// reading: any member. Routes enforce access with authz.Require (controller.go).
type Service struct {
	repo      *repository
	secrets   *users_repositories.SecretKeyRepository
	audit     *audit_logs.AuditLogService
	sender    *sender
	evaluator *Evaluator
}

// RuleRequest creates or replaces a rule.
type RuleRequest struct {
	Name            string      `json:"name"`
	Enabled         *bool       `json:"enabled"`
	Source          string      `json:"source"`
	Query           RuleQuery   `json:"query"`
	Comparison      string      `json:"comparison"`
	Threshold       float64     `json:"threshold"`
	WindowSeconds   int         `json:"windowSeconds"`
	IntervalSeconds int         `json:"intervalSeconds"`
	ForSeconds      int         `json:"forSeconds"`
	RepeatSeconds   *int        `json:"repeatSeconds"`
	Labels          Labels      `json:"labels"`
	Summary         string      `json:"summary"`
	ChannelIDs      []uuid.UUID `json:"channelIds"`
}

// SilenceRequest creates a silence; StartsAt defaults to now.
type SilenceRequest struct {
	Matchers Matchers   `json:"matchers"`
	StartsAt *time.Time `json:"startsAt"`
	EndsAt   time.Time  `json:"endsAt"`
	Comment  string     `json:"comment"`
}

// PreviewSeries is one series of a rule's query right now.
type PreviewSeries struct {
	Labels Labels  `json:"labels"`
	Value  float64 `json:"value"`
	Firing bool    `json:"firing"`
}

// AlertView is an alert with its rule's name.
type AlertView struct {
	AlertState
	RuleName string `json:"ruleName"`
}

func (s *Service) ListRules(projectID uuid.UUID, user *users_models.User) ([]*AlertRule, error) {
	return s.repo.rules(projectID)
}

func (s *Service) CreateRule(projectID uuid.UUID, request RuleRequest, user *users_models.User) (*AlertRule, error) {
	now := time.Now().UTC()
	rule := &AlertRule{ID: uuid.New(), ProjectID: projectID, CreatedBy: &user.ID, CreatedAt: now}
	if err := s.applyRule(rule, request); err != nil {
		return nil, err
	}
	rule.UpdatedAt = now
	if err := s.repo.saveRule(rule); err != nil {
		return nil, fmt.Errorf("failed to save rule: %w", err)
	}
	s.audit.WriteAuditLog("Alert rule created: "+rule.Name, &user.ID, &projectID)
	return rule, nil
}

func (s *Service) UpdateRule(
	projectID, ruleID uuid.UUID, request RuleRequest, user *users_models.User,
) (*AlertRule, error) {
	rule, err := s.repo.rule(projectID, ruleID)
	if err != nil {
		return nil, notFound(err)
	}
	if rule.ManagedBy != "" {
		return nil, managedError(rule)
	}
	if err := s.applyRule(rule, request); err != nil {
		return nil, err
	}
	rule.UpdatedAt = time.Now().UTC()
	rule.LastEvalAt = nil // evaluate the new definition on the next tick
	if err := s.repo.saveRule(rule); err != nil {
		return nil, fmt.Errorf("failed to save rule: %w", err)
	}
	s.audit.WriteAuditLog("Alert rule updated: "+rule.Name, &user.ID, &projectID)
	return rule, nil
}

func (s *Service) DeleteRule(projectID, ruleID uuid.UUID, user *users_models.User) error {
	if rule, err := s.repo.rule(projectID, ruleID); err == nil && rule.ManagedBy != "" {
		return managedError(rule)
	}
	deleted, err := s.repo.deleteRule(projectID, ruleID)
	if err != nil {
		return err
	}
	if !deleted {
		return ErrNotFound
	}
	s.audit.WriteAuditLog("Alert rule deleted: "+ruleID.String(), &user.ID, &projectID)
	return nil
}

// PreviewRule runs request's query once and shows which series would be
// past the threshold, without changing anything.
func (s *Service) PreviewRule(
	ctx context.Context, projectID uuid.UUID, request RuleRequest, user *users_models.User,
) ([]PreviewSeries, error) {
	rule := &AlertRule{ProjectID: projectID}
	if err := s.applyRule(rule, request); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, evalTimeout)
	defer cancel()
	samples, err := s.evaluator.query(ctx, rule, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	preview := make([]PreviewSeries, 0, len(samples))
	for _, sample := range samples {
		preview = append(preview, PreviewSeries{
			Labels: sample.labels, Value: sample.value,
			Firing: compare(rule.Comparison, sample.value, rule.Threshold),
		})
	}
	return preview, nil
}

func (s *Service) ListAlerts(projectID uuid.UUID, user *users_models.User) ([]AlertView, error) {
	rules, err := s.repo.rules(projectID)
	if err != nil {
		return nil, err
	}
	names := map[uuid.UUID]string{}
	for _, rule := range rules {
		names[rule.ID] = rule.Name
	}
	states, err := s.repo.projectStates(projectID)
	if err != nil {
		return nil, err
	}
	views := make([]AlertView, 0, len(states))
	for _, state := range states {
		views = append(views, AlertView{AlertState: *state, RuleName: names[state.RuleID]})
	}
	return views, nil
}

// ListChannels lists the channels projectID may use. Where they send
// (hosts, email recipients) is shown to rule editors only, and no caller
// learns which other projects a channel serves.
func (s *Service) ListChannels(projectID uuid.UUID, user *users_models.User) ([]*NotificationChannel, error) {
	channels, err := s.repo.channels(projectID)
	if err != nil {
		return nil, err
	}
	editor, err := authz.Authorize(context.Background(), user, authz.ProjectManage, authz.Project(projectID))
	if err != nil {
		return nil, err
	}
	for _, channel := range channels {
		channel.ProjectIDs = ProjectIDs{}
		if !channel.AllProjects {
			channel.ProjectIDs = ProjectIDs{projectID.String()}
		}
		if !editor.Allowed {
			continue
		}
		if cfg, err := s.decrypt(channel); err == nil {
			channel.Target = describe(channel.Kind, cfg)
		}
	}
	return channels, nil
}

// TestChannel sends a test notification now; the error says only what
// is safe to show (e.g. the receiver's HTTP status).
func (s *Service) TestChannel(ctx context.Context, projectID, channelID uuid.UUID, user *users_models.User) error {
	channel, err := s.repo.channel(projectID, channelID)
	if err != nil {
		return notFound(err)
	}
	cfg, err := s.decrypt(channel)
	if err != nil {
		return err
	}
	return s.sender.send(ctx, channel.Kind, cfg, Notification{
		Status: StatusTest, ProjectID: projectID, RuleName: "Test notification from Obzly",
		Summary: "If you can read this, the channel " + channel.Name + " works.",
		Labels:  Labels{"channel": channel.Name}, StartsAt: time.Now().UTC(),
	})
}

func (s *Service) ListSilences(projectID uuid.UUID, user *users_models.User) ([]*Silence, error) {
	return s.repo.silences(projectID)
}

func (s *Service) CreateSilence(
	projectID uuid.UUID,
	request SilenceRequest,
	user *users_models.User,
) (*Silence, error) {
	now := time.Now().UTC()
	startsAt := now
	if request.StartsAt != nil {
		startsAt = request.StartsAt.UTC()
	}
	if len(request.Matchers) == 0 || len(request.Matchers) > 20 {
		return nil, errors.New("between 1 and 20 matchers are required")
	}
	for _, matcher := range request.Matchers {
		if !labelName.MatchString(matcher.Name) {
			return nil, fmt.Errorf("invalid label name %q", matcher.Name)
		}
		if matcher.Regex {
			if _, err := regexp.Compile("^(?:" + matcher.Value + ")$"); err != nil {
				return nil, fmt.Errorf("invalid regex for %q", matcher.Name)
			}
		}
	}
	if !request.EndsAt.After(startsAt) || !request.EndsAt.After(now) {
		return nil, errors.New("a silence must end after it starts, and in the future")
	}
	silence := &Silence{
		ID: uuid.New(), ProjectID: projectID, Matchers: request.Matchers, StartsAt: startsAt,
		EndsAt: request.EndsAt.UTC(), Comment: truncate(request.Comment, 1000), CreatedBy: &user.ID, CreatedAt: now,
	}
	if err := s.repo.saveSilence(silence); err != nil {
		return nil, err
	}
	s.audit.WriteAuditLog("Silence created until "+silence.EndsAt.Format(time.RFC3339), &user.ID, &projectID)
	return silence, nil
}

func (s *Service) DeleteSilence(projectID, silenceID uuid.UUID, user *users_models.User) error {
	if silence, err := s.repo.silence(projectID, silenceID); err != nil {
		return err
	} else if silence != nil && silence.ManagedBy != "" {
		return ErrManagedSilence
	}
	deleted, err := s.repo.deleteSilence(projectID, silenceID)
	if err != nil {
		return err
	}
	if !deleted {
		return ErrNotFound
	}
	s.audit.WriteAuditLog("Silence deleted: "+silenceID.String(), &user.ID, &projectID)
	return nil
}

// defaultPatternBaselineSeconds is a log patterns rule's baseline when unset: a day,
// so "new" means not seen in the day before.
const defaultPatternBaselineSeconds = 86400

func (s *Service) applyRule(rule *AlertRule, request RuleRequest) error {
	request.Name = strings.TrimSpace(request.Name)
	switch {
	case request.Name == "" || len(request.Name) > 200:
		return errors.New("a name of up to 200 characters is required")
	case !slices.Contains(comparisons, request.Comparison):
		return errors.New("comparison must be one of > >= < <= == !=")
	case request.IntervalSeconds != 0 && (request.IntervalSeconds < 15 || request.IntervalSeconds > 86400):
		return errors.New("interval must be between 15 seconds and a day")
	case request.WindowSeconds != 0 && (request.WindowSeconds < 60 || request.WindowSeconds > 86400):
		return errors.New("window must be between a minute and a day")
	case request.ForSeconds < 0 || request.ForSeconds > 86400:
		return errors.New("for must be between 0 and a day")
	case request.RepeatSeconds != nil && (*request.RepeatSeconds < 0 || *request.RepeatSeconds > 7*86400):
		return errors.New("repeat must be between 0 and 7 days")
	case len(request.Labels) > 20 || len(request.ChannelIDs) > 20:
		return errors.New("at most 20 labels and 20 channels")
	}
	for name := range request.Labels {
		if !labelName.MatchString(name) || name == "alertname" {
			return fmt.Errorf("invalid label name %q", name)
		}
	}
	switch request.Source {
	case SourceMetrics:
		if strings.TrimSpace(request.Query.PromQL) == "" {
			return errors.New("a PromQL query is required")
		}
		if _, err := metrics_promql.GetParser().
			ParseExpr(metrics_promql.NormalizePromQL(request.Query.PromQL)); err != nil {
			return fmt.Errorf("invalid PromQL: %w", err)
		}
		request.Query = RuleQuery{PromQL: request.Query.PromQL}
	case SourceLogs:
		request.Query = RuleQuery{Logs: request.Query.Logs, GroupBy: strings.TrimSpace(request.Query.GroupBy)}
	case SourceLogPatterns:
		baseline := request.Query.BaselineSeconds
		if baseline == 0 {
			baseline = defaultPatternBaselineSeconds
		}
		if baseline < 3600 || baseline > 7*86400 {
			return errors.New("the baseline must be between an hour and 7 days")
		}
		if request.Query.MinCount < 0 {
			return errors.New("the minimum count can't be negative")
		}
		request.Query = RuleQuery{Logs: request.Query.Logs, BaselineSeconds: baseline, MinCount: request.Query.MinCount}
	case SourceTraces:
		if request.Query.Metric == "" {
			request.Query.Metric = string(spans_core.AggregationMetricCount)
		}
		if !slices.Contains(traceMetrics, request.Query.Metric) {
			return fmt.Errorf("metric must be one of %s", strings.Join(traceMetrics, ", "))
		}
		if !slices.Contains(traceGroups, request.Query.GroupBy) {
			return errors.New("traces can be grouped by service_name, name, status_code or kind")
		}
		request.Query.PromQL, request.Query.Logs = "", nil
	default:
		return errors.New("source must be metrics, logs, log_patterns or traces")
	}
	if len(request.ChannelIDs) > 0 && rule.ProjectID != uuid.Nil {
		count, err := s.repo.countChannels(rule.ProjectID, request.ChannelIDs)
		if err != nil {
			return err
		}
		if count != int64(len(uniqueIDs(request.ChannelIDs))) {
			return errors.New("every channel must belong to this project")
		}
	}

	rule.Name, rule.Source, rule.Query = request.Name, request.Source, request.Query
	rule.Comparison, rule.Threshold, rule.Summary = request.Comparison, request.Threshold, truncate(
		request.Summary,
		2000,
	)
	rule.Enabled = request.Enabled == nil || *request.Enabled
	rule.WindowSeconds = orDefault(request.WindowSeconds, 300)
	rule.IntervalSeconds = orDefault(request.IntervalSeconds, 60)
	rule.ForSeconds = request.ForSeconds
	rule.RepeatSeconds = 4 * 3600
	if request.RepeatSeconds != nil {
		rule.RepeatSeconds = *request.RepeatSeconds
	}
	rule.Labels = request.Labels
	if rule.Labels == nil {
		rule.Labels = Labels{}
	}
	rule.ChannelIDs = uniqueIDs(request.ChannelIDs)
	return nil
}

func (s *Service) decrypt(channel *NotificationChannel) (ChannelConfig, error) {
	var cfg ChannelConfig
	plain, err := s.secrets.Decrypt(channel.ConfigEncrypted)
	if err != nil {
		return cfg, err
	}
	return cfg, json.Unmarshal([]byte(plain), &cfg)
}

func notFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

func orDefault(value, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}

func uniqueIDs(ids []uuid.UUID) []uuid.UUID {
	unique := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if !slices.Contains(unique, id) {
			unique = append(unique, id)
		}
	}
	return unique
}
