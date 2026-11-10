package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"obzly/internal/synthetics/spec"
)

const (
	azureLogin      = "https://login.microsoftonline.com"
	azureManagement = "https://management.azure.com"
	azureIMDS       = "http://169.254.169.254"
	// Token resources: ARM, and Blob storage (the cost export).
	azureManagementResource = "https://management.azure.com"
	azureStorageResource    = "https://storage.azure.com"
	azureGraphAPIVersion    = "2022-10-01"
	azureGraphPageSize      = 1000
	maxAzureResources       = 50000
	// The AKS workload identity webhook mounts the service account token here.
	azureFederatedTokenEnv = "AZURE_FEDERATED_TOKEN_FILE"
	// IMDS answers in milliseconds where it exists; elsewhere don't wait long.
	azureIMDSTimeout = 5 * time.Second
)

// azureTypes maps Resource Graph types to ours.
var azureTypes = map[string]string{
	"microsoft.compute/virtualmachines":          "virtual_machine",
	"microsoft.compute/virtualmachinescalesets":  "vm_scale_set",
	"microsoft.network/loadbalancers":            "load_balancer",
	"microsoft.network/applicationgateways":      "application_gateway",
	"microsoft.containerservice/managedclusters": "aks_cluster",
	"microsoft.sql/servers/databases":            "sql_database",
	"microsoft.dbforpostgresql/flexibleservers":  "postgresql_server",
	"microsoft.storage/storageaccounts":          "storage_account",
	"microsoft.web/sites":                        "function_app",
}

// azureGraphQuery reads, per resource, what state and links the types have.
const azureGraphQuery = `Resources
| where type in~ (%s)
| where type !~ 'microsoft.web/sites' or kind contains 'functionapp'
| project id, name, type, location, subscriptionId, kind,
  vmId = tostring(properties.vmId),
  power = tostring(properties.extended.instanceView.powerState.code),
  aksPower = tostring(properties.powerState.code),
  status = coalesce(tostring(properties.status), tostring(properties.state), tostring(properties.provisioningState)),
  operational = tostring(properties.operationalState),
  vmSize = coalesce(tostring(properties.hardwareProfile.vmSize), tostring(sku.name)),
  k8sVersion = tostring(properties.kubernetesVersion)`

// azureHealthQuery reads Resource Health's availability per resource.
const azureHealthQuery = `HealthResources
| where type =~ 'microsoft.resourcehealth/availabilitystatuses'
| project id = tostring(split(tolower(id), '/providers/microsoft.resourcehealth/')[0]),
  availability = tolower(tostring(properties.availabilityState))`

var azureAvailability = map[string]string{
	"available": HealthHealthy, "degraded": HealthDegraded, "unavailable": HealthUnhealthy, "unknown": HealthUnknown,
}

type azureHealthRow struct {
	ID           string `json:"id"`
	Availability string `json:"availability"`
}

type azureRow struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Type           string `json:"type"`
	Location       string `json:"location"`
	SubscriptionID string `json:"subscriptionId"`
	VMID           string `json:"vmId"`
	Power          string `json:"power"`
	AKSPower       string `json:"aksPower"`
	Status         string `json:"status"`
	Operational    string `json:"operational"`
	VMSize         string `json:"vmSize"`
	K8sVersion     string `json:"k8sVersion"`
}

func discoverAzure(ctx context.Context, connection spec.CloudConnection, options Options, t *tracker) []Resource {
	token, err := azureToken(ctx, connection, options, options.HTTP)
	if err != nil {
		t.fail(fmt.Errorf("couldn't sign in to Microsoft Entra: %w", err))
		return nil
	}
	types := make([]string, 0, len(azureTypes))
	for name, kind := range azureTypes {
		if wants(connection, kind) {
			types = append(types, name)
		}
	}
	if len(types) == 0 {
		return nil
	}
	limit := maxAzureResources
	if options.Sample {
		limit = azureGraphPageSize
	}
	rows, _ := azureGraph[azureRow](ctx, connection, options, token, azureResourcesQuery(types), limit, t)
	resources := make([]Resource, 0, len(rows))
	for _, row := range rows {
		resources = append(resources, azureResource(row))
	}
	return resources
}

// healthAzure re-reads the known resources' state from Resource Graph and
// their availability from Resource Health.
func healthAzure(
	ctx context.Context,
	connection spec.CloudConnection,
	known []Resource,
	options Options,
	t *tracker,
) []Resource {
	graphTypes := map[string]string{}
	for name, kind := range azureTypes {
		graphTypes["azure_"+kind] = name
	}
	wanted := map[string]bool{}
	for _, resource := range known {
		if name, ok := graphTypes[resource.Type]; ok {
			wanted[name] = true
		}
	}
	if len(wanted) == 0 {
		return known
	}
	token, err := azureToken(ctx, connection, options, options.HTTP)
	if err != nil {
		t.fail(fmt.Errorf("couldn't sign in to Microsoft Entra: %w", err))
		return known
	}
	rows, ok := azureGraph[azureRow](ctx, connection, options, token,
		azureResourcesQuery(slices.Sorted(maps.Keys(wanted))), maxAzureResources, t)
	if !ok {
		return known
	}
	current := make(map[string]Resource, len(rows))
	for _, row := range rows {
		resource := azureResource(row)
		current[resource.ID] = resource
	}
	statuses, healthOK := azureGraph[azureHealthRow](ctx, connection, options, token, azureHealthQuery,
		maxAzureResources, t)
	availability := make(map[string]string, len(statuses))
	for _, status := range statuses {
		availability[status.ID] = azureAvailability[status.Availability]
	}

	out := make([]Resource, 0, len(known))
	for _, resource := range known {
		if _, checked := graphTypes[resource.Type]; !checked {
			out = append(out, resource)
			continue
		}
		found, ok := current[resource.ID]
		if !ok {
			// Gone: no longer sent, so the Site sees it stop reporting.
			continue
		}
		found.Health = resource.Health
		if healthOK {
			found.Health = availability[found.ID]
		}
		out = append(out, found)
	}
	return out
}

// azureGraph runs query over the connection's subscription or management
// group, a page at a time, up to limit rows; false when it didn't finish.
func azureGraph[T any](
	ctx context.Context,
	connection spec.CloudConnection,
	options Options,
	token, query string,
	limit int,
	t *tracker,
) ([]T, bool) {
	body := map[string]any{"query": query}
	if connection.Scope == "management_group" {
		body["managementGroups"] = []string{connection.ScopeID}
	} else {
		body["subscriptions"] = []string{connection.ScopeID}
	}
	endpoint := orDefault(options.AzureManagement, azureManagement) +
		"/providers/Microsoft.ResourceGraph/resources?api-version=" + azureGraphAPIVersion
	var rows []T
	skipToken := ""
	for len(rows) < limit {
		pageOptions := map[string]any{"$top": azureGraphPageSize, "resultFormat": "objectArray"}
		if skipToken != "" {
			pageOptions["$skipToken"] = skipToken
		}
		body["options"] = pageOptions
		var page struct {
			Data      []T    `json:"data"`
			SkipToken string `json:"$skipToken"`
		}
		status, err := azureJSON(ctx, options.HTTP, http.MethodPost, endpoint, token, body, &page)
		if status == http.StatusForbidden || status == http.StatusUnauthorized {
			t.call("Reader role", err, true)
			return rows, false
		}
		if !t.call("Microsoft.ResourceGraph/resources/read", err, false) {
			return rows, false
		}
		rows = append(rows, page.Data...)
		if page.SkipToken == "" {
			return rows, true
		}
		skipToken = page.SkipToken
	}
	return rows, true
}

func azureResourcesQuery(types []string) string {
	quoted := make([]string, 0, len(types))
	for _, name := range types {
		quoted = append(quoted, "'"+name+"'")
	}
	return fmt.Sprintf(azureGraphQuery, strings.Join(quoted, ", "))
}

func azureResource(row azureRow) Resource {
	kind := azureTypes[strings.ToLower(row.Type)]
	state := strings.ToLower(row.Status)
	attributes := map[string]string{"azure_resource_group": resourceGroupOf(row.ID)}
	switch kind {
	case "virtual_machine":
		state = strings.TrimPrefix(strings.ToLower(row.Power), "powerstate/")
		attributes["host_id"], attributes["instance_type"] = row.VMID, row.VMSize
	case "aks_cluster":
		state = strings.ToLower(row.AKSPower)
		attributes["k8s_cluster_name"], attributes["k8s_version"] = row.Name, row.K8sVersion
	case "application_gateway":
		state = strings.ToLower(row.Operational)
	case "vm_scale_set":
		attributes["instance_type"] = row.VMSize
	}
	return Resource{
		Provider: spec.ProviderAzure, AccountID: row.SubscriptionID, Region: row.Location, Type: "azure_" + kind,
		ID: strings.ToLower(row.ID), Name: row.Name, State: state, Attributes: attributes,
	}
}

// azureToken gets an ARM token: the probe's managed identity from IMDS, or
// a federated client assertion (the cluster's service account token, or
// the control plane's) exchanged at Microsoft Entra.
func azureToken(
	ctx context.Context,
	connection spec.CloudConnection,
	options Options,
	client *http.Client,
) (string, error) {
	return azureTokenFor(ctx, connection, options, client, azureManagementResource)
}

// azureTokenFor is azureToken for another resource, e.g. azureStorageResource.
func azureTokenFor(
	ctx context.Context,
	connection spec.CloudConnection,
	options Options,
	client *http.Client,
	resource string,
) (string, error) {
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if connection.Access == spec.AccessProbe {
		ctx, cancel := context.WithTimeout(ctx, azureIMDSTimeout)
		defer cancel()
		query := url.Values{"api-version": {"2018-02-01"}, "resource": {resource + "/"}}
		if connection.Identity != "" {
			query.Set("client_id", connection.Identity)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet,
			orDefault(options.AzureIMDS, azureIMDS)+"/metadata/identity/oauth2/token?"+query.Encode(), nil)
		if err != nil {
			return "", err
		}
		request.Header.Set("Metadata", "true")
		if err := doJSON(client, request, &token); err != nil {
			return "", fmt.Errorf("no managed identity here (is the probe on Azure with one assigned?): %w", err)
		}
		return token.AccessToken, nil
	}
	assertion, err := azureAssertion(ctx, connection, options)
	if err != nil {
		return "", err
	}
	form := url.Values{
		"client_id": {connection.Identity}, "scope": {resource + "/.default"}, "grant_type": {"client_credentials"},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
		"client_assertion":      {assertion},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		orDefault(options.AzureLogin, azureLogin)+"/"+url.PathEscape(connection.TenantID)+"/oauth2/v2.0/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := doJSON(client, request, &token); err != nil {
		return "", err
	}
	return token.AccessToken, nil
}

func azureAssertion(ctx context.Context, connection spec.CloudConnection, options Options) (string, error) {
	if connection.Access == spec.AccessFederated {
		if options.Token == nil {
			return "", fmt.Errorf("no control plane token source")
		}
		return options.Token(ctx)
	}
	getenv, readFile := options.Getenv, options.FederatedTokenFS
	if getenv == nil {
		getenv = os.Getenv
	}
	if readFile == nil {
		readFile = os.ReadFile
	}
	path := getenv(azureFederatedTokenEnv)
	if path == "" {
		return "", fmt.Errorf(
			"%s isn't set: label the probe's pods azure.workload.identity/use=true",
			azureFederatedTokenEnv,
		)
	}
	data, err := readFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func azureJSON(
	ctx context.Context,
	client *http.Client,
	method, endpoint, token string,
	body, target any,
) (int, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer func() { _ = response.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 64<<20))
	if response.StatusCode != http.StatusOK {
		return response.StatusCode, fmt.Errorf("%s answered %d: %s", endpoint, response.StatusCode, azureError(data))
	}
	return response.StatusCode, json.Unmarshal(data, target)
}

func doJSON(client *http.Client, request *http.Request, target any) error {
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d: %s", request.URL.Host, response.StatusCode, azureError(data))
	}
	return json.Unmarshal(data, target)
}

// azureError is the readable part of an Azure or Entra error body.
func azureError(data []byte) string {
	var body struct {
		Error            any    `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if json.Unmarshal(data, &body) == nil {
		if body.ErrorDescription != "" {
			return firstLine(body.ErrorDescription)
		}
		if detail, ok := body.Error.(map[string]any); ok {
			if message, ok := detail["message"].(string); ok {
				return firstLine(message)
			}
		}
	}
	return firstLine(string(data))
}

func firstLine(text string) string {
	text, _, _ = strings.Cut(strings.TrimSpace(text), "\n")
	if len(text) > 300 {
		text = text[:300]
	}
	return text
}

func resourceGroupOf(id string) string {
	parts := strings.Split(id, "/")
	for i := 0; i+1 < len(parts); i++ {
		if strings.EqualFold(parts[i], "resourceGroups") {
			return parts[i+1]
		}
	}
	return ""
}

func orDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
