package posture

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"obzly/internal/features/audit_logs"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/storage"
	"obzly/internal/synthetics/spec"
)

// Posture actions (docs/CLOUD_COST_POSTURE.md "Recommendations", Posture): ranked changes, each
// grouping the findings one change fixes; guardrails that stop findings coming back; coverage
// fixes where a scope falls back or isn't covered. Planned actions show what the standards would
// score with the plan done. Obzly never changes anything in the cloud.

const (
	ActionCoverage  = "coverage"
	ActionFix       = "fix"
	ActionGuardrail = "guardrail"

	ActionOpen         = "open"
	ActionDone         = "done"
	ActionAcceptedRisk = "accepted_risk"

	maxActionFindings = 200
)

// severityWeight: how much closing a finding of each severity is worth when ranking actions.
var severityWeight = map[string]float64{
	spec.SeverityCritical: 10, spec.SeverityHigh: 5, spec.SeverityMedium: 2, spec.SeverityLow: 1,
	spec.SeverityInformational: 0.2,
}

var effortCost = map[string]float64{"low": 1, "medium": 2, "high": 4}

// controlEffort: how much work fixing a control's finding is; medium when not listed.
var controlEffort = map[string]string{
	"S3.1": "low", "S3.2": "low", "S3.3": "low", "S3.8": "low", "S3.9": "low", "EC2.7": "low", "EC2.8": "medium",
	"EC2.13": "low", "EC2.14": "low", "IAM.4": "low", "IAM.9": "low", "IAM.15": "low", "CloudTrail.1": "low",
	"RDS.2": "medium", "RDS.3": "high", "azure-nsg-no-world-ssh": "low", "azure-nsg-no-world-rdp": "low",
	"azure-storage-https-only": "low", "azure-storage-min-tls": "low", "azure-storage-no-public-blob": "low",
	"azure-sql-no-public-network": "medium", "azure-keyvault-purge-protection": "low",
}

// guardrail is a setting that stops findings of some controls coming back (Prevents), and may
// itself close findings (Closes: the setting's own control).
type guardrail struct {
	ID       string
	Provider string
	Regional bool
	Title    string
	Prevents []string
	Closes   []string
	Effort   string
	Steps    func(account, region string) []string
}

var guardrails = []guardrail{
	{
		ID: "aws-s3-account-block-public-access", Provider: spec.ProviderAWS,
		Title:    "Turn on S3 Block Public Access for the whole account",
		Prevents: []string{"S3.2", "S3.3", "S3.8"}, Closes: []string{"S3.1"}, Effort: "low",
		Steps: func(account, _ string) []string {
			return []string{
				"Check no bucket needs to be public on purpose (static sites behind CloudFront don't).",
				"aws s3control put-public-access-block --account-id " + account +
					" --public-access-block-configuration BlockPublicAcls=true,IgnorePublicAcls=true," +
					"BlockPublicPolicy=true,RestrictPublicBuckets=true",
			}
		},
	},
	{
		ID: "aws-imdsv2-default", Provider: spec.ProviderAWS, Regional: true,
		Title:    "Make IMDSv2 the default for new instances",
		Prevents: []string{"EC2.8"}, Effort: "low",
		Steps: func(_, region string) []string {
			return []string{
				"New instances get IMDSv2 only; existing ones keep their setting until you change them.",
				"aws ec2 modify-instance-metadata-defaults --region " + region +
					" --http-tokens required --http-put-response-hop-limit 2",
			}
		},
	},
	{
		ID: "aws-ebs-encryption-default", Provider: spec.ProviderAWS, Regional: true,
		Title:    "Turn on EBS encryption by default",
		Prevents: []string{"EC2.3"}, Closes: []string{"EC2.7"}, Effort: "low",
		Steps: func(_, region string) []string {
			return []string{
				"New volumes and snapshot copies are encrypted; existing volumes aren't changed.",
				"aws ec2 enable-ebs-encryption-by-default --region " + region,
			}
		},
	},
	{
		ID: "azure-deny-open-management-ports", Provider: spec.ProviderAzure,
		Title:    "Assign an Azure Policy that denies SSH and RDP open to the internet",
		Prevents: []string{"azure-nsg-no-world-ssh", "azure-nsg-no-world-rdp"}, Effort: "medium",
		Steps: func(account, _ string) []string {
			return []string{
				"Create a custom policy with the deny effect for network security group rules that allow " +
					"inbound 22 or 3389 from *, Internet or 0.0.0.0/0, and assign it to the subscription.",
				"az policy definition create --name deny-open-management-ports --mode All --rules rules.json",
				"az policy assignment create --name deny-open-management-ports --policy deny-open-management-ports " +
					"--scope /subscriptions/" + account,
			}
		},
	},
	{
		ID: "azure-deny-public-blob", Provider: spec.ProviderAzure,
		Title:    "Assign the built-in policy that denies anonymous blob access",
		Prevents: []string{"azure-storage-no-public-blob"}, Effort: "low",
		Steps: func(account, _ string) []string {
			return []string{
				`az policy definition list --query "[?displayName=='Storage account public access should be ` +
					`disallowed'].name" -o tsv`,
				"az policy assignment create --name deny-public-blob --policy <the name above> " +
					`--params '{"effect": {"value": "Deny"}}' --scope /subscriptions/` + account,
			}
		},
	},
}

// ActionFinding is one finding an action covers.
type ActionFinding struct {
	ID          uuid.UUID `json:"id"`
	Control     string    `json:"control"`
	Title       string    `json:"title"`
	Severity    string    `json:"severity"`
	ResourceKey string    `json:"resourceKey"`
	Status      string    `json:"status"`
	Suppressed  bool      `json:"suppressed"`
}

// CostLink is a cost recommendation on a resource an action touches.
type CostLink struct {
	Key            string   `json:"key"`
	Action         string   `json:"action"`
	Currency       string   `json:"currency"`
	MonthlySavings *float64 `json:"monthlySavings"`
}

// Action is one ranked change.
type Action struct {
	Key            string          `json:"key"`
	ProjectID      uuid.UUID       `json:"projectId"`
	Kind           string          `json:"kind"`
	Title          string          `json:"title"`
	Detail         string          `json:"detail,omitempty"`
	ConnectionName string          `json:"connectionName,omitempty"`
	Provider       string          `json:"provider,omitempty"`
	Account        string          `json:"account,omitempty"`
	Region         string          `json:"region,omitempty"`
	Severity       string          `json:"severity,omitempty"`
	Effort         string          `json:"effort"`
	Rank           float64         `json:"rank"`
	Findings       []ActionFinding `json:"findings"`
	Prevents       []string        `json:"prevents,omitempty"`
	Scopes         []string        `json:"scopes,omitempty"`
	Steps          []string        `json:"steps,omitempty"`
	CostLinks      []CostLink      `json:"costLinks,omitempty"`
	Status         string          `json:"status"`
	Planned        bool            `json:"planned"`
	Dismissed      bool            `json:"dismissed"`
	DismissReason  string          `json:"dismissReason,omitempty"`
}

// SimulatedScore is a standard's score now, with the plan done and with every action done.
type SimulatedScore struct {
	Standard  string  `json:"standard"`
	Name      string  `json:"name"`
	Evaluated int     `json:"evaluated"`
	Current   float64 `json:"current"`
	WithPlan  float64 `json:"withPlan"`
	WithAll   float64 `json:"withAll"`
}

// ActionsDTO is the Posture Recommendations tab.
type ActionsDTO struct {
	Actions   []Action         `json:"actions"`
	Standards []SimulatedScore `json:"standards"`
}

// ActionUpdate adds an action to the plan or takes it out, or dismisses it (with a reason).
type ActionUpdate struct {
	ProjectID uuid.UUID `json:"projectId"           binding:"required"`
	Key       string    `json:"key"                 binding:"required"`
	Planned   *bool     `json:"planned,omitempty"`
	Dismissed *bool     `json:"dismissed,omitempty"`
	Reason    string    `json:"reason,omitempty"`
}

type actionState struct {
	ProjectID  uuid.UUID `gorm:"column:project_id;primaryKey"`
	Key        string    `gorm:"column:key;primaryKey"`
	Title      string    `gorm:"column:title"`
	Planned    bool      `gorm:"column:planned"`
	Dismissed  bool      `gorm:"column:dismissed"`
	Reason     string    `gorm:"column:reason"`
	FindingIDs uuidList  `gorm:"column:finding_ids"`
	UpdatedBy  string    `gorm:"column:updated_by"`
	UpdatedAt  time.Time `gorm:"column:updated_at"`
}

func (actionState) TableName() string { return "posture_action_states" }

// Actions ranks the projects' actions and simulates the standards' scores.
func (s *Service) Actions(ctx context.Context, projects []uuid.UUID) (*ActionsDTO, error) {
	db := storage.GetDb().WithContext(ctx)
	var open []Finding
	if err := db.Where("project_id IN ? AND status = ?", projects, StatusOpen).Find(&open).Error; err != nil {
		return nil, err
	}
	var runs []scopeRun
	if err := db.Where("project_id IN ?", projects).Find(&runs).Error; err != nil {
		return nil, err
	}
	var states []actionState
	if err := db.Where("project_id IN ?", projects).Find(&states).Error; err != nil {
		return nil, err
	}
	rails := guardrailActions(open)
	actions := append(coverageActions(runs), append(withoutGuardedFixes(fixActions(open), rails), rails...)...)
	if err := attachCostLinks(db, projects, actions); err != nil {
		return nil, err
	}
	stateOf := map[string]actionState{}
	for _, state := range states {
		stateOf[state.ProjectID.String()+"|"+state.Key] = state
	}
	listed := map[string]bool{}
	for i := range actions {
		id := actions[i].ProjectID.String() + "|" + actions[i].Key
		listed[id] = true
		if state, ok := stateOf[id]; ok {
			actions[i].Planned, actions[i].Dismissed, actions[i].DismissReason = state.Planned, state.Dismissed,
				state.Reason
		}
	}
	// A planned action whose findings all closed is done (resolved) or an accepted risk
	// (suppressed instead); it stays in the plan to show that.
	var planned []actionState
	for id, state := range stateOf {
		if state.Planned && !listed[id] && len(state.FindingIDs) > 0 {
			planned = append(planned, state)
		}
	}
	closed, err := plannedOutcomes(db, planned)
	if err != nil {
		return nil, err
	}
	actions = append(actions, closed...)
	sort.SliceStable(actions, func(i, j int) bool { return actions[i].Rank > actions[j].Rank })
	if actions == nil {
		actions = []Action{}
	}
	for i := range actions {
		// A guardrail that closes nothing today still lists its findings as [], never null.
		if actions[i].Findings == nil {
			actions[i].Findings = []ActionFinding{}
		}
	}

	fixedByPlan, fixedByAll := map[uuid.UUID]bool{}, map[uuid.UUID]bool{}
	for _, action := range actions {
		if action.Dismissed || action.Kind == ActionCoverage || action.Status != ActionOpen {
			continue
		}
		for _, finding := range action.Findings {
			if finding.Suppressed {
				continue
			}
			fixedByAll[finding.ID] = true
			if action.Planned {
				fixedByPlan[finding.ID] = true
			}
		}
	}
	return &ActionsDTO{Actions: actions, Standards: simulate(runs, open, fixedByPlan, fixedByAll)}, nil
}

// UpdateAction plans, unplans, dismisses or restores an action; it goes into the audit log.
func (s *Service) UpdateAction(ctx context.Context, user *users_models.User, update ActionUpdate) (*Action, error) {
	if update.Planned == nil && update.Dismissed == nil {
		return nil, errors.New("set planned or dismissed")
	}
	if update.Dismissed != nil && *update.Dismissed && strings.TrimSpace(update.Reason) == "" {
		return nil, errors.New("say why it's dismissed")
	}
	if len(update.Reason) > maxNoteChars {
		return nil, errors.New("the reason is too long")
	}
	current, err := s.Actions(ctx, []uuid.UUID{update.ProjectID})
	if err != nil {
		return nil, err
	}
	index := slices.IndexFunc(current.Actions, func(a Action) bool { return a.Key == update.Key })
	if index < 0 {
		return nil, ErrNotFound
	}
	action := current.Actions[index]
	db := storage.GetDb().WithContext(ctx)
	var state actionState
	db.Where("project_id = ? AND key = ?", update.ProjectID, update.Key).Limit(1).Find(&state)
	state.ProjectID, state.Key, state.Title = update.ProjectID, update.Key, action.Title
	state.UpdatedBy, state.UpdatedAt = user.Email, s.now()
	verb := ""
	if update.Planned != nil {
		state.Planned = *update.Planned
		verb = map[bool]string{true: "Planned", false: "Took out of the plan"}[state.Planned]
		if state.Planned && action.Status == ActionOpen {
			state.FindingIDs = uuidList{}
			for _, finding := range action.Findings {
				state.FindingIDs = append(state.FindingIDs, finding.ID)
			}
		}
	}
	if update.Dismissed != nil {
		state.Dismissed = *update.Dismissed
		state.Reason = strings.TrimSpace(update.Reason)
		if !state.Dismissed {
			state.Reason = ""
		}
		verb = map[bool]string{true: "Dismissed", false: "Restored"}[state.Dismissed]
	}
	if err := db.Save(&state).Error; err != nil {
		return nil, err
	}
	message := fmt.Sprintf("%s posture action %q", verb, action.Title)
	if state.Dismissed && update.Dismissed != nil {
		message += ": " + state.Reason
	}
	audit_logs.GetAuditLogService().WriteAuditLog(message, &user.ID, &update.ProjectID)
	updated, err := s.Actions(ctx, []uuid.UUID{update.ProjectID})
	if err != nil {
		return nil, err
	}
	for _, item := range updated.Actions {
		if item.Key == update.Key {
			return &item, nil
		}
	}
	return nil, ErrNotFound
}

// fixActions groups open, unsuppressed findings: one action per resource with several findings,
// one per control where several resources have only that finding, one per finding otherwise.
func fixActions(open []Finding) []Action {
	byResource := map[string][]Finding{}
	var resourceOrder []string
	for _, finding := range open {
		if finding.Suppressed {
			continue
		}
		key := finding.ProjectID.String() + "|" + finding.ConnectionID.String() + "|" + finding.ResourceKey
		if _, seen := byResource[key]; !seen {
			resourceOrder = append(resourceOrder, key)
		}
		byResource[key] = append(byResource[key], finding)
	}
	var actions []Action
	singles := map[string][]Finding{}
	var singleOrder []string
	for _, key := range resourceOrder {
		group := byResource[key]
		if len(group) == 1 {
			finding := group[0]
			controlKey := finding.ProjectID.String() + "|" + finding.ConnectionID.String() + "|" + finding.Account +
				"|" + finding.Control
			if _, seen := singles[controlKey]; !seen {
				singleOrder = append(singleOrder, controlKey)
			}
			singles[controlKey] = append(singles[controlKey], finding)
			continue
		}
		first := group[0]
		action := newFixAction(group, "fix:resource:"+first.ConnectionID.String()+":"+first.ResourceKey)
		action.Title = fmt.Sprintf("Fix %d findings on %s", len(group), shortName(first.ResourceKey))
		titles := make([]string, 0, len(group))
		for _, finding := range group {
			titles = append(titles, finding.Title)
		}
		action.Detail = strings.Join(titles, "; ")
		action.Region = first.Region
		actions = append(actions, action)
	}
	for _, key := range singleOrder {
		group := singles[key]
		first := group[0]
		if len(group) == 1 {
			action := newFixAction(group, "fix:finding:"+first.ConnectionID.String()+":"+first.Control+":"+
				first.ResourceKey)
			action.Title, action.Region = first.Title, first.Region
			action.Detail = shortName(first.ResourceKey)
			actions = append(actions, action)
			continue
		}
		action := newFixAction(group, "fix:control:"+first.ConnectionID.String()+":"+first.Account+":"+first.Control)
		action.Title = fmt.Sprintf("%s (%d resources)", first.Title, len(group))
		names := make([]string, 0, min(len(group), 5))
		for _, finding := range group[:min(len(group), 5)] {
			names = append(names, shortName(finding.ResourceKey))
		}
		action.Detail = strings.Join(names, ", ")
		if len(group) > 5 {
			action.Detail += fmt.Sprintf(" and %d more", len(group)-5)
		}
		actions = append(actions, action)
	}
	return actions
}

func newFixAction(group []Finding, key string) Action {
	first := group[0]
	action := Action{Key: key, ProjectID: first.ProjectID, Kind: ActionFix, ConnectionName: first.ConnectionName,
		Provider: first.Provider, Account: first.Account, Status: ActionOpen, Effort: "low"}
	var worth float64
	for _, finding := range group {
		action.Findings = appendFinding(action.Findings, finding)
		worth += severityWeight[finding.Severity]
		if effortCost[effortOf(finding.Control)] > effortCost[action.Effort] {
			action.Effort = effortOf(finding.Control)
		}
		if action.Severity == "" || severityRank[finding.Severity] < severityRank[action.Severity] {
			action.Severity = finding.Severity
		}
	}
	action.Rank = worth / effortCost[action.Effort]
	return action
}

// withoutGuardedFixes drops fix actions whose findings a guardrail closes: turning the setting on
// is the fix, so the same finding isn't offered twice.
func withoutGuardedFixes(fixes, rails []Action) []Action {
	closed := map[uuid.UUID]bool{}
	for _, rail := range rails {
		for _, finding := range rail.Findings {
			closed[finding.ID] = true
		}
	}
	return slices.DeleteFunc(fixes, func(action Action) bool {
		return len(action.Findings) > 0 && !slices.ContainsFunc(action.Findings,
			func(finding ActionFinding) bool { return !closed[finding.ID] })
	})
}

// guardrailActions offers each guardrail in the accounts (and regions) where findings it would
// have prevented or closes are open.
func guardrailActions(open []Finding) []Action {
	byKey := map[string]*Action{}
	var order []string
	for _, finding := range open {
		for _, rail := range guardrails {
			if rail.Provider != finding.Provider {
				continue
			}
			prevents := slices.Contains(rail.Prevents, finding.Control)
			closes := slices.Contains(rail.Closes, finding.Control)
			if !prevents && !closes {
				continue
			}
			region := ""
			if rail.Regional {
				region = finding.Region
			}
			key := fmt.Sprintf("guardrail:%s:%s:%s:%s", rail.ID, finding.ConnectionID, finding.Account, region)
			id := finding.ProjectID.String() + "|" + key
			action := byKey[id]
			if action == nil {
				action = &Action{Key: key, ProjectID: finding.ProjectID, Kind: ActionGuardrail, Title: rail.Title,
					ConnectionName: finding.ConnectionName, Provider: finding.Provider, Account: finding.Account,
					Region: region, Effort: rail.Effort, Prevents: append(slices.Clone(rail.Prevents), rail.Closes...),
					Steps: rail.Steps(finding.Account, region), Status: ActionOpen}
				byKey[id] = action
				order = append(order, id)
			}
			// Only the guardrail's own control closes; the rest stop coming back on new resources.
			if closes {
				action.Findings = appendFinding(action.Findings, finding)
			}
			action.Rank += severityWeight[finding.Severity] / 2
		}
	}
	actions := make([]Action, 0, len(order))
	for _, id := range order {
		action := byKey[id]
		where := action.Account
		if action.Region != "" {
			where += " / " + action.Region
		}
		action.Detail = "Stops " + strings.Join(action.Prevents, ", ") + " coming back in " + where
		action.Rank /= effortCost[action.Effort]
		actions = append(actions, *action)
	}
	return actions
}

// coverageActions: scopes a source couldn't read, grouped per connection, source and result.
// Not covered at all ranks first; a fallback to the baseline after.
func coverageActions(runs []scopeRun) []Action {
	okScope := map[string]bool{}
	for _, run := range runs {
		if run.Result == spec.PostureRunOK {
			okScope[run.ProjectID.String()+"|"+run.ConnectionID.String()+"|"+run.Account+"|"+run.Region] = true
		}
	}
	byKey := map[string]*Action{}
	var order []string
	for _, run := range runs {
		if run.Result == spec.PostureRunOK {
			continue
		}
		scope := run.ProjectID.String() + "|" + run.ConnectionID.String() + "|" + run.Account + "|" + run.Region
		key := fmt.Sprintf("coverage:%s:%s:%s", run.ConnectionID, run.Source, run.Result)
		id := run.ProjectID.String() + "|" + key
		action := byKey[id]
		if action == nil {
			action = &Action{
				Key:            key,
				ProjectID:      run.ProjectID,
				Kind:           ActionCoverage,
				ConnectionName: run.ConnectionName,
				Provider:       run.Provider,
				Effort:         "low",
				Status:         ActionOpen,
				Findings:       []ActionFinding{},
			}
			action.Title, action.Steps = coverageText(run)
			action.Detail = run.Reason
			byKey[id] = action
			order = append(order, id)
		}
		name := run.Account + "/" + run.Region
		if !slices.Contains(action.Scopes, name) {
			action.Scopes = append(action.Scopes, name)
			// A scope nothing else read is a real gap; one the baseline covered is a fallback.
			if okScope[scope] {
				action.Rank = max(action.Rank, 100)
			} else {
				action.Rank = max(action.Rank, 1000)
			}
		}
	}
	actions := make([]Action, 0, len(order))
	for _, id := range order {
		action := byKey[id]
		sort.Strings(action.Scopes)
		action.Rank += float64(len(action.Scopes))
		if len(action.Scopes) > 1 {
			action.Title += fmt.Sprintf(" (%d scopes)", len(action.Scopes))
		}
		actions = append(actions, *action)
	}
	return actions
}

func coverageText(run scopeRun) (string, []string) {
	aws := run.Provider == spec.ProviderAWS
	switch {
	case run.Result == spec.PostureRunNotEnabled && run.Source == spec.PostureSourceNative && aws:
		return "Turn on Security Hub", []string{
			"Security Hub charges about $0.001 per check; the Obzly baseline covers these regions meanwhile.",
			"aws securityhub enable-security-hub --enable-default-standards --region <region>",
		}
	case run.Result == spec.PostureRunNotEnabled && run.Source == spec.PostureSourceNative:
		return "Turn on Microsoft Defender for Cloud", []string{
			"Foundational CSPM is free; it assesses the subscription against the Microsoft cloud security " +
				"benchmark once the Microsoft.Security resource provider is registered.",
			"az provider register --namespace Microsoft.Security --subscription <subscription>",
		}
	case run.Result == spec.PostureRunNeedsPermission && aws:
		return "Add the posture permissions to " + run.ConnectionName + "'s role", []string{
			"Update the connection's CloudFormation stack from Settings → Cloud connections; it adds only the " +
				"read actions posture uses.",
		}
	case run.Result == spec.PostureRunNeedsPermission:
		return "Assign Security Reader to " + run.ConnectionName, []string{
			"az role assignment create --role \"Security Reader\" --assignee <the connection's client ID> " +
				"--scope /subscriptions/<subscription>",
		}
	}
	return "Check why " + run.ConnectionName + " couldn't read posture", []string{
		"Run Test access on the connection in Settings → Cloud connections; the reason is shown per scope.",
	}
}

// plannedOutcomes are planned actions no longer computed: all their findings closed.
func plannedOutcomes(db *gorm.DB, planned []actionState) ([]Action, error) {
	if len(planned) == 0 {
		return nil, nil
	}
	var ids []uuid.UUID
	for _, state := range planned {
		ids = append(ids, state.FindingIDs...)
	}
	var findings []Finding
	if err := db.Where("id IN ?", ids).Find(&findings).Error; err != nil {
		return nil, err
	}
	byID := map[uuid.UUID]Finding{}
	for _, finding := range findings {
		byID[finding.ID] = finding
	}
	var actions []Action
	for _, state := range planned {
		action := Action{Key: state.Key, ProjectID: state.ProjectID, Kind: kindOf(state.Key), Title: state.Title,
			Effort: "low", Planned: true, Dismissed: state.Dismissed, DismissReason: state.Reason, Status: ActionDone,
			Rank: -1}
		suppressed, open := false, false
		for _, id := range state.FindingIDs {
			finding, ok := byID[id]
			if !ok {
				continue
			}
			action.Findings = appendFinding(action.Findings, finding)
			action.ConnectionName, action.Provider, action.Account = finding.ConnectionName, finding.Provider,
				finding.Account
			switch {
			case finding.Status == StatusOpen && finding.Suppressed:
				suppressed = true
			case finding.Status == StatusOpen:
				open = true
			}
		}
		if open {
			action.Status, action.Rank = ActionOpen, 0
		} else if suppressed {
			action.Status = ActionAcceptedRisk
		}
		actions = append(actions, action)
	}
	return actions, nil
}

// attachCostLinks adds open cost recommendations on the resources an action touches (e.g. an
// idle, unencrypted volume is both a finding and a saving).
func attachCostLinks(db *gorm.DB, projects []uuid.UUID, actions []Action) error {
	resources := map[string]bool{}
	for _, action := range actions {
		for _, finding := range action.Findings {
			resources[finding.ResourceKey] = true
		}
	}
	if len(resources) == 0 {
		return nil
	}
	var rows []struct {
		ProjectID      uuid.UUID
		Key            string
		ResourceKey    string
		Action         string
		Currency       string
		MonthlySavings *float64
	}
	err := db.Raw(`SELECT r.project_id, r.key, r.resource_key, r.action, r.currency,
			r.monthly_savings::float8 AS monthly_savings
		FROM cost_recommendations r
		LEFT JOIN cost_recommendation_states st ON st.project_id = r.project_id AND st.key = r.key
		WHERE r.project_id IN ? AND r.resource_key IN ? AND COALESCE(st.status, 'open') IN ('open', 'planned')`,
		projects, slices.Collect(maps.Keys(resources))).Scan(&rows).Error
	if err != nil {
		return err
	}
	links := map[string][]CostLink{}
	for _, row := range rows {
		id := row.ProjectID.String() + "|" + row.ResourceKey
		links[id] = append(links[id], CostLink{Key: row.Key, Action: row.Action, Currency: row.Currency,
			MonthlySavings: row.MonthlySavings})
	}
	for i := range actions {
		for _, finding := range actions[i].Findings {
			for _, link := range links[actions[i].ProjectID.String()+"|"+finding.ResourceKey] {
				if !slices.ContainsFunc(actions[i].CostLinks, func(l CostLink) bool { return l.Key == link.Key }) {
					actions[i].CostLinks = append(actions[i].CostLinks, link)
				}
			}
		}
	}
	return nil
}

// simulate scores each standard as standardScores does, now, with the plan's findings fixed and
// with every action's findings fixed.
func simulate(runs []scopeRun, open []Finding, byPlan, byAll map[uuid.UUID]bool) []SimulatedScore {
	evaluated := map[string]map[string]bool{}
	for _, run := range runs {
		if run.Result != spec.PostureRunOK {
			continue
		}
		if evaluated[run.Standard] == nil {
			evaluated[run.Standard] = map[string]bool{}
		}
		for _, control := range append(slices.Clone([]string(run.Passed)), run.Failed...) {
			evaluated[run.Standard][control] = true
		}
	}
	failed := func(fixed map[uuid.UUID]bool) map[string]map[string]bool {
		out := map[string]map[string]bool{}
		for _, finding := range open {
			if finding.Suppressed || fixed[finding.ID] {
				continue
			}
			for _, ref := range finding.Standards {
				if evaluated[ref.Standard][ref.Control] {
					if out[ref.Standard] == nil {
						out[ref.Standard] = map[string]bool{}
					}
					out[ref.Standard][ref.Control] = true
				}
			}
		}
		return out
	}
	now, plan, all := failed(nil), failed(byPlan), failed(byAll)
	scores := make([]SimulatedScore, 0, len(evaluated))
	for _, standard := range slices.Sorted(maps.Keys(evaluated)) {
		total := len(evaluated[standard])
		scores = append(scores, SimulatedScore{
			Standard: standard, Name: orDefault(spec.PostureStandardName[standard], standard), Evaluated: total,
			Current:  percent(total-len(now[standard]), total),
			WithPlan: percent(total-len(plan[standard]), total), WithAll: percent(total-len(all[standard]), total),
		})
	}
	return scores
}

func appendFinding(list []ActionFinding, finding Finding) []ActionFinding {
	if len(list) >= maxActionFindings {
		return list
	}
	return append(list, ActionFinding{ID: finding.ID, Control: finding.Control, Title: finding.Title,
		Severity: finding.Severity, ResourceKey: finding.ResourceKey, Status: finding.Status,
		Suppressed: finding.Suppressed})
}

func effortOf(control string) string {
	if effort, ok := controlEffort[control]; ok {
		return effort
	}
	return "medium"
}

func kindOf(key string) string {
	kind, _, _ := strings.Cut(key, ":")
	return kind
}

// shortName is the last part of an ARN or Azure resource ID.
func shortName(resource string) string {
	if i := strings.LastIndexAny(resource, "/:"); i >= 0 && i < len(resource)-1 {
		return resource[i+1:]
	}
	return resource
}
