package mcp_server

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	users_models "obzly/internal/features/users/models"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Alerting tools: read-only views of a project's alerts, rules, rule activity and silences.
// Notification channels are never read - their configs can hold webhook secrets.

type listAlertsArgs struct {
	projectArgs
	State  string `json:"state,omitempty"  jsonschema:"active (firing and pending, default), firing, pending, resolved or all"`
	Limit  int    `json:"limit,omitempty"  jsonschema:"Max alerts, firing first then newest (default 50, max 200)"`
	Cursor string `json:"cursor,omitempty" jsonschema:"nextCursor from the previous page (same other arguments)"`
}

type listAlertRulesArgs struct {
	projectArgs
	Name        string `json:"name,omitempty"        jsonschema:"Only rules whose name contains this (case-insensitive)"`
	EnabledOnly bool   `json:"enabledOnly,omitempty" jsonschema:"Only enabled rules"`
	Limit       int    `json:"limit,omitempty"       jsonschema:"Max rules, by name (default 50, max 200)"`
	Cursor      string `json:"cursor,omitempty"      jsonschema:"nextCursor from the previous page (same other arguments)"`
}

type alertRuleActivityArgs struct {
	projectArgs
	Limit  int    `json:"limit,omitempty"  jsonschema:"Max rules, most firings first (default 50, max 200)"`
	Cursor string `json:"cursor,omitempty" jsonschema:"nextCursor from the previous page (same other arguments)"`
}

type listSilencesArgs struct {
	projectArgs
	Limit  int    `json:"limit,omitempty"  jsonschema:"Max silences, ending soonest first (default 50, max 200)"`
	Cursor string `json:"cursor,omitempty" jsonschema:"nextCursor from the previous page (same other arguments)"`
}

type alertState struct {
	RuleID         string            `json:"ruleId"`
	RuleName       string            `json:"ruleName"`
	State          string            `json:"state"`
	Labels         map[string]string `json:"labels"`
	Value          float64           `json:"value"`
	ActiveAt       time.Time         `json:"activeAt"`
	FiredAt        *time.Time        `json:"firedAt"`
	ResolvedAt     *time.Time        `json:"resolvedAt"`
	LastNotifiedAt *time.Time        `json:"lastNotifiedAt"`
}

type alertRule struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Enabled         bool              `json:"enabled"`
	Source          string            `json:"source"`
	Query           map[string]any    `json:"query"`
	Comparison      string            `json:"comparison"`
	Threshold       float64           `json:"threshold"`
	WindowSeconds   int               `json:"windowSeconds"`
	IntervalSeconds int               `json:"intervalSeconds"`
	ForSeconds      int               `json:"forSeconds"`
	RepeatSeconds   int               `json:"repeatSeconds"`
	Labels          map[string]string `json:"labels"`
	Summary         string            `json:"summary"`
	LastEvalAt      *time.Time        `json:"lastEvalAt"`
	LastError       string            `json:"lastError"`
	ManagedBy       string            `json:"managedBy"`
	ChannelIDs      []string          `json:"channelIds"`
}

type silence struct {
	ID        string           `json:"id"`
	Matchers  []map[string]any `json:"matchers"`
	StartsAt  time.Time        `json:"startsAt"`
	EndsAt    time.Time        `json:"endsAt"`
	Comment   string           `json:"comment"`
	ManagedBy string           `json:"managedBy"`
}

func init() {
	allowSiteCalls(http.MethodGet,
		"/projects/{project}/alerts", "/projects/{project}/alerts/summary", "/projects/{project}/alert-rules",
		"/projects/{project}/alert-rules/activity", "/projects/{project}/silences",
	)
}

func (t *toolSet) registerAlerts(server *mcp.Server, add func(name, description string) *mcp.Tool) {
	mcp.AddTool(server, add("list_alerts",
		"A project's alerts now: firing and pending by default (resolved on request), with rule, labels, value "+
			"and when they started. Use it first when asked what is alerting."+paged+untrustedNote),
		wrap("list_alerts", untrusted(t.listAlerts)))
	mcp.AddTool(server, add("alert_summary",
		"Counts for a project's Alerts page header: alerts firing and pending now, rules, and current or "+
			"upcoming silences. Cheapest way to check whether anything is alerting."),
		wrap("alert_summary", t.alertSummary))
	mcp.AddTool(server, add("list_alert_rules",
		"A project's alert rules: source, query, threshold, timing, labels, last evaluation and error. "+
			"Notification channels are only counted, never shown."+paged+untrustedNote),
		wrap("list_alert_rules", untrusted(t.listAlertRules)))
	mcp.AddTool(server, add("alert_rule_activity",
		"Per rule, firings and delivered notifications per UTC day over the last 7 days (today so far "+
			"included). Days before trackedSince or before a rule existed are unknown, not zero. Use it to find "+
			"noisy or flapping rules."+paged+untrustedNote),
		wrap("alert_rule_activity", untrusted(t.alertRuleActivity)))
	mcp.AddTool(server, add("list_silences",
		"A project's current and upcoming silences (expired ones are not listed) with their matchers, window "+
			"and comment. Use it when an expected alert didn't notify."+paged+untrustedNote),
		wrap("list_silences", untrusted(t.listSilences)))
}

func (t *toolSet) listAlerts(ctx context.Context, user *users_models.User, args listAlertsArgs) (any, error) {
	projectID, err := t.resolveProject(ctx, user, args.projectArgs)
	if err != nil {
		return nil, err
	}
	var states []alertState
	if err := t.exec.siteCall(ctx, user, args.Site, http.MethodGet,
		"/projects/"+projectID+"/alerts", nil, nil, &states); err != nil {
		return nil, err
	}

	wanted := map[string]bool{}
	switch state := strings.ToLower(strings.TrimSpace(args.State)); state {
	case "", "active":
		wanted["firing"], wanted["pending"] = true, true
	case "all":
	default:
		wanted[state] = true
	}
	counts := map[string]int{}
	alerts := make([]alertState, 0, len(states))
	for _, state := range states {
		counts[state.State]++
		if len(wanted) == 0 || wanted[state.State] {
			alerts = append(alerts, state)
		}
	}
	filter := listAlertsArgs{projectArgs: args.projectArgs, State: args.State}
	page, offset, err := pageOf("list_alerts", filter, args.Cursor, alerts, clampRows(args.Limit, defaultRows))
	if err != nil {
		return nil, err
	}
	return pageResult("list_alerts", filter, "alerts", page, offset, len(alerts),
		map[string]any{"countsByState": counts}), nil
}

func (t *toolSet) alertSummary(ctx context.Context, user *users_models.User, args fieldsArgs) (any, error) {
	projectID, err := t.resolveProject(ctx, user, args.projectArgs)
	if err != nil {
		return nil, err
	}
	var response map[string]any
	return response, t.exec.siteCall(ctx, user, args.Site, http.MethodGet,
		"/projects/"+projectID+"/alerts/summary", nil, nil, &response)
}

func (t *toolSet) listAlertRules(ctx context.Context, user *users_models.User, args listAlertRulesArgs) (any, error) {
	projectID, err := t.resolveProject(ctx, user, args.projectArgs)
	if err != nil {
		return nil, err
	}
	var rules []alertRule
	if err := t.exec.siteCall(ctx, user, args.Site, http.MethodGet,
		"/projects/"+projectID+"/alert-rules", nil, nil, &rules); err != nil {
		return nil, err
	}

	filter := strings.ToLower(strings.TrimSpace(args.Name))
	out := make([]map[string]any, 0, len(rules))
	for _, rule := range rules {
		if (filter != "" && !strings.Contains(strings.ToLower(rule.Name), filter)) ||
			(args.EnabledOnly && !rule.Enabled) {
			continue
		}
		entry := map[string]any{
			"id": rule.ID, "name": rule.Name, "enabled": rule.Enabled, "source": rule.Source, "query": rule.Query,
			"comparison": rule.Comparison, "threshold": rule.Threshold, "windowSeconds": rule.WindowSeconds,
			"intervalSeconds": rule.IntervalSeconds, "forSeconds": rule.ForSeconds,
			"repeatSeconds": rule.RepeatSeconds, "labels": rule.Labels, "summary": rule.Summary,
			"lastEvalAt": rule.LastEvalAt, "notificationChannels": len(rule.ChannelIDs),
		}
		if rule.LastError != "" {
			entry["lastError"] = short(rule.LastError, maxFieldChars)
		}
		if rule.ManagedBy != "" {
			entry["managedBy"] = rule.ManagedBy
		}
		out = append(out, entry)
	}
	pageArgs := listAlertRulesArgs{projectArgs: args.projectArgs, Name: args.Name, EnabledOnly: args.EnabledOnly}
	page, offset, err := pageOf("list_alert_rules", pageArgs, args.Cursor, out, clampRows(args.Limit, defaultRows))
	if err != nil {
		return nil, err
	}
	return pageResult("list_alert_rules", pageArgs, "rules", page, offset, len(out), nil), nil
}

func (t *toolSet) alertRuleActivity(
	ctx context.Context, user *users_models.User, args alertRuleActivityArgs,
) (any, error) {
	projectID, err := t.resolveProject(ctx, user, args.projectArgs)
	if err != nil {
		return nil, err
	}
	var activity struct {
		Days         []string         `json:"days"`
		TrackedSince *time.Time       `json:"trackedSince"`
		Rules        []map[string]any `json:"rules"`
	}
	if err := t.exec.siteCall(ctx, user, args.Site, http.MethodGet,
		"/projects/"+projectID+"/alert-rules/activity", nil, nil, &activity); err != nil {
		return nil, err
	}
	var rules []alertRule
	if err := t.exec.siteCall(ctx, user, args.Site, http.MethodGet,
		"/projects/"+projectID+"/alert-rules", nil, nil, &rules); err != nil {
		return nil, err
	}
	names := make(map[string]string, len(rules))
	for _, rule := range rules {
		names[rule.ID] = rule.Name
	}

	slices.SortStableFunc(activity.Rules, func(a, b map[string]any) int {
		return compareNumber(b["firedTotal"], a["firedTotal"])
	})
	filter := alertRuleActivityArgs{projectArgs: args.projectArgs}
	page, offset, err := pageOf("alert_rule_activity", filter, args.Cursor, activity.Rules,
		clampRows(args.Limit, defaultRows))
	if err != nil {
		return nil, err
	}
	for _, rule := range page {
		id, _ := rule["ruleId"].(string)
		rule["ruleName"] = names[id]
	}
	return pageResult("alert_rule_activity", filter, "rules", page, offset, len(activity.Rules), map[string]any{
		"days": activity.Days, "trackedSince": activity.TrackedSince,
	}), nil
}

func (t *toolSet) listSilences(ctx context.Context, user *users_models.User, args listSilencesArgs) (any, error) {
	projectID, err := t.resolveProject(ctx, user, args.projectArgs)
	if err != nil {
		return nil, err
	}
	var silences []silence
	if err := t.exec.siteCall(ctx, user, args.Site, http.MethodGet,
		"/projects/"+projectID+"/silences", nil, nil, &silences); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	filter := listSilencesArgs{projectArgs: args.projectArgs}
	page, offset, err := pageOf("list_silences", filter, args.Cursor, silences, clampRows(args.Limit, defaultRows))
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(page))
	for _, s := range page {
		entry := map[string]any{
			"id": s.ID, "matchers": s.Matchers, "startsAt": s.StartsAt, "endsAt": s.EndsAt,
			"active": !s.StartsAt.After(now), "comment": short(s.Comment, maxFieldChars),
		}
		if s.ManagedBy != "" {
			entry["managedBy"] = s.ManagedBy
		}
		out = append(out, entry)
	}
	return pageResult("list_silences", filter, "silences", out, offset, len(silences), nil), nil
}
