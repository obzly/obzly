package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"obzly/internal/posture/baseline"
	"obzly/internal/synthetics/spec"
)

// Defender for Cloud's assessments: its recommendations per resource, under the Microsoft cloud
// security benchmark it assesses by default.
const azureAssessmentsQuery = `securityresources
| where type == 'microsoft.security/assessments'
| extend code = tostring(properties.status.code)
| where code in~ ('Healthy', 'Unhealthy')
| project subscriptionId, assessment = name, code,
  resourceId = tolower(tostring(properties.resourceDetails.Id)),
  title = tostring(properties.displayName),
  severity = tolower(tostring(properties.metadata.severity)),
  portal = tostring(properties.links.azurePortal),
  cause = tostring(properties.status.cause)`

const azureSubscriptionsQuery = `resourcecontainers
| where type == 'microsoft.resources/subscriptions'
| project subscriptionId, name`

const azurePostureResourcesQuery = `resources
| where type in~ ('microsoft.storage/storageaccounts', 'microsoft.network/networksecuritygroups',
  'microsoft.keyvault/vaults', 'microsoft.sql/servers')
| project id = tolower(id), name, type = tolower(type), location, subscriptionId, resourceGroup, properties`

var (
	azureFactTypes = map[string]string{
		"microsoft.storage/storageaccounts":       "azure_storage_account",
		"microsoft.network/networksecuritygroups": "azure_network_security_group",
		"microsoft.keyvault/vaults":               "azure_key_vault",
		"microsoft.sql/servers":                   "azure_sql_server",
	}
	azureNativeStandards = []string{"mcsb"}
	// Sources an NSG rule treats as the whole internet.
	azureWorldSources = []string{"*", "internet", "any", "0.0.0.0/0", "::/0"}
)

type azureAssessmentRow struct {
	SubscriptionID string `json:"subscriptionId"`
	Assessment     string `json:"assessment"`
	Code           string `json:"code"`
	ResourceID     string `json:"resourceId"`
	Title          string `json:"title"`
	Severity       string `json:"severity"`
	Portal         string `json:"portal"`
	Cause          string `json:"cause"`
}

type azureSubscriptionRow struct {
	SubscriptionID string `json:"subscriptionId"`
	Name           string `json:"name"`
}

type azurePostureRow struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Type           string          `json:"type"`
	Location       string          `json:"location"`
	SubscriptionID string          `json:"subscriptionId"`
	ResourceGroup  string          `json:"resourceGroup"`
	Properties     json.RawMessage `json:"properties"`
}

// collectAzurePosture: Defender for Cloud per subscription where it has assessments, the
// baseline for the rest. Azure's scopes are whole subscriptions (region "global").
func collectAzurePosture(
	ctx context.Context,
	connection spec.CloudConnection,
	options Options,
	t *tracker,
	c *postureCollector,
	source string,
) {
	token, err := azureToken(ctx, connection, options, options.HTTP)
	if err != nil {
		t.fail(fmt.Errorf("couldn't sign in to Microsoft Entra: %w", err))
		return
	}
	subscriptions := []string{connection.ScopeID}
	if connection.Scope == "management_group" {
		rows, ok := azureGraph[azureSubscriptionRow](ctx, connection, options, token, azureSubscriptionsQuery,
			maxAzureResources, t)
		if !ok {
			return
		}
		subscriptions = subscriptions[:0]
		for _, row := range rows {
			subscriptions = append(subscriptions, row.SubscriptionID)
		}
	}
	if options.Sample {
		subscriptions = subscriptions[:min(len(subscriptions), 1)]
	}

	needBaseline := subscriptions
	if source != spec.PostureSourceBaseline {
		needBaseline = nil
		rows, problem := azureAssessments(ctx, connection, options, token, t)
		bySubscription := map[string][]azureAssessmentRow{}
		for _, row := range rows {
			bySubscription[row.SubscriptionID] = append(bySubscription[row.SubscriptionID], row)
		}
		for _, subscription := range subscriptions {
			reason := problem
			if reason == nil && len(bySubscription[subscription]) == 0 {
				reason = &postureProblem{result: spec.PostureRunNotEnabled,
					reason: "Defender for Cloud has no assessments for this subscription"}
			}
			if reason == nil {
				runs, findings := azureAssessmentResults(subscription, bySubscription[subscription])
				c.add(runs, findings)
				c.cover(subscription, spec.GlobalRegion, spec.PostureSourceNative)
				continue
			}
			c.add(unreadRuns(subscription, spec.GlobalRegion, spec.PostureSourceNative, azureNativeStandards,
				*reason), nil)
			if source == spec.PostureSourceNative {
				c.cover(subscription, spec.GlobalRegion, "")
			} else {
				needBaseline = append(needBaseline, subscription)
			}
		}
	}
	if len(needBaseline) == 0 {
		return
	}
	rows, ok := azureGraph[azurePostureRow](ctx, connection, options, token, azurePostureResourcesQuery,
		maxAzureResources, t)
	var problem *postureProblem
	if !ok {
		problem = &postureProblem{result: spec.PostureRunError, reason: "Resource Graph query failed"}
	}
	facts := map[string][]baseline.Fact{}
	for _, row := range rows {
		if fact, ok := azureFact(row); ok {
			facts[row.SubscriptionID] = append(facts[row.SubscriptionID], fact)
		}
	}
	types := make([]string, 0, len(azureFactTypes))
	for _, kind := range azureFactTypes {
		types = append(types, kind)
	}
	slices.Sort(types)
	for _, subscription := range needBaseline {
		runs, findings := baselineScope(spec.ProviderAzure, subscription, spec.GlobalRegion, types,
			facts[subscription], problem)
		c.add(runs, findings)
		c.cover(subscription, spec.GlobalRegion, spec.PostureSourceBaseline)
	}
}

// azureAssessments reads Defender's assessments; Resource Graph only shows them with Security
// Reader, so a denial needs that role.
func azureAssessments(
	ctx context.Context, connection spec.CloudConnection, options Options, token string, t *tracker,
) ([]azureAssessmentRow, *postureProblem) {
	sub := newTracker()
	rows, ok := azureGraph[azureAssessmentRow](ctx, connection, options, token, azureAssessmentsQuery,
		postureMaxFindings, sub)
	if ok {
		return rows, nil
	}
	use := sub.result()
	if use.State == spec.UseNeedsPermission {
		t.failPermission(errors.New("the Defender for Cloud assessments aren't readable"), "Security Reader role")
		return nil, &postureProblem{result: spec.PostureRunNeedsPermission, reason: "Security Reader role missing"}
	}
	t.merge(sub)
	return nil, &postureProblem{result: spec.PostureRunError, reason: firstLine(use.Message)}
}

// azureAssessmentResults: one run under MCSB per subscription; each unhealthy assessment of a
// resource is a finding, with the assessment as its control.
func azureAssessmentResults(subscription string, rows []azureAssessmentRow) ([]spec.PostureRun, []spec.PostureFinding) {
	passed, failed := map[string]bool{}, map[string]bool{}
	var findings []spec.PostureFinding
	for _, row := range rows {
		if !strings.EqualFold(row.Code, "Unhealthy") {
			passed[row.Assessment] = true
			continue
		}
		failed[row.Assessment] = true
		evidence := map[string]string{}
		if row.Cause != "" {
			evidence["status_reason"] = row.Cause
		}
		if group := resourceGroupOf(row.ResourceID); group != "" {
			evidence["resourceGroup"] = group
		}
		findings = append(findings, spec.PostureFinding{
			Source: spec.PostureSourceNative, Account: subscription, Region: spec.GlobalRegion,
			Control: row.Assessment, Title: row.Title, Severity: azureSeverity(row.Severity),
			ResourceKey: row.ResourceID, Standards: []spec.StandardRef{{Standard: "mcsb", Control: row.Assessment}},
			Evidence: evidence, RemediationURL: portalURL(row.Portal),
		})
	}
	run := spec.PostureRun{Account: subscription, Region: spec.GlobalRegion, Standard: "mcsb",
		Source: spec.PostureSourceNative, Result: spec.PostureRunOK}
	run.Passed, run.Failed = controlLists(passed, failed)
	return []spec.PostureRun{run}, findings
}

// azureFact turns a resource's properties into the fields the Azure checks read. Defaults follow
// Azure's own: a property that isn't set is the less safe setting where Azure treats it so.
func azureFact(row azurePostureRow) (baseline.Fact, bool) {
	kind, ok := azureFactTypes[row.Type]
	if !ok {
		return baseline.Fact{}, false
	}
	var properties map[string]any
	_ = json.Unmarshal(row.Properties, &properties)
	values := map[string]any{"name": row.Name, "resourceGroup": row.ResourceGroup, "location": row.Location}
	switch kind {
	case "azure_storage_account":
		values["https_only"] = boolProperty(properties, "supportsHttpsTrafficOnly", true)
		values["min_tls"] = stringProperty(properties, "minimumTlsVersion", "TLS1_0")
		values["allow_blob_public_access"] = boolProperty(properties, "allowBlobPublicAccess", true)
	case "azure_network_security_group":
		ports, all := nsgWorldPorts(properties)
		values["world_ports"], values["world_all_traffic"] = ports, all
	case "azure_key_vault":
		values["soft_delete"] = boolProperty(properties, "enableSoftDelete", true)
		values["purge_protection"] = boolProperty(properties, "enablePurgeProtection", false)
	case "azure_sql_server":
		values["public_network_access"] = stringProperty(properties, "publicNetworkAccess", "Enabled")
	}
	return baseline.Fact{Type: kind, Key: row.ID, Account: row.SubscriptionID, Region: spec.GlobalRegion,
		Values: values}, true
}

// nsgWorldPorts: the admin ports an NSG's inbound allow rules open to the internet, and whether
// one opens every port.
func nsgWorldPorts(properties map[string]any) ([]int, bool) {
	rules, _ := properties["securityRules"].([]any)
	ports := map[int]bool{}
	all := false
	for _, item := range rules {
		rule, _ := item.(map[string]any)
		props, _ := rule["properties"].(map[string]any)
		if !strings.EqualFold(stringProperty(props, "direction", ""), "Inbound") ||
			!strings.EqualFold(stringProperty(props, "access", ""), "Allow") {
			continue
		}
		protocol := strings.ToLower(stringProperty(props, "protocol", "*"))
		if protocol != "*" && protocol != "tcp" {
			continue
		}
		sources := append([]string{stringProperty(props, "sourceAddressPrefix", "")},
			stringList(props["sourceAddressPrefixes"])...)
		world := slices.ContainsFunc(sources, func(source string) bool {
			return slices.Contains(azureWorldSources, strings.ToLower(source))
		})
		if !world {
			continue
		}
		ranges := append([]string{stringProperty(props, "destinationPortRange", "")},
			stringList(props["destinationPortRanges"])...)
		for _, portRange := range ranges {
			if portRange == "*" {
				all = true
				continue
			}
			from, to, ok := parsePortRange(portRange)
			if !ok {
				continue
			}
			for _, port := range portsOpen(from, to) {
				ports[port] = true
			}
		}
	}
	open := []int{}
	for _, port := range adminPorts {
		if ports[port] {
			open = append(open, port)
		}
	}
	return open, all
}

func parsePortRange(value string) (int, int, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, 0, false
	}
	low, high, isRange := strings.Cut(value, "-")
	from, err := strconv.Atoi(strings.TrimSpace(low))
	if err != nil {
		return 0, 0, false
	}
	if !isRange {
		return from, from, true
	}
	to, err := strconv.Atoi(strings.TrimSpace(high))
	return from, to, err == nil
}

func boolProperty(properties map[string]any, name string, fallback bool) bool {
	if value, ok := properties[name].(bool); ok {
		return value
	}
	return fallback
}

func stringProperty(properties map[string]any, name, fallback string) string {
	if value, ok := properties[name].(string); ok && value != "" {
		return value
	}
	return fallback
}

func stringList(value any) []string {
	items, _ := value.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func azureSeverity(value string) string {
	switch value {
	case "high":
		return spec.SeverityHigh
	case "low":
		return spec.SeverityLow
	}
	return spec.SeverityMedium
}

func portalURL(link string) string {
	if link == "" || strings.HasPrefix(link, "http") {
		return link
	}
	return "https://" + link
}
