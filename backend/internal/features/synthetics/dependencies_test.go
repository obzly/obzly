package synthetics_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/prometheus/prometheus/promql/parser"
	"github.com/stretchr/testify/require"

	"obzly/internal/features/synthetics"
	"obzly/internal/storage"
	test_utils "obzly/internal/util/testing"
)

func Test_Dependencies_LoopsAndSelfRefused_DeleteForgetsTheParent(t *testing.T) {
	router, admin, _ := setup(t)
	destination := createDestination(t, router, admin, "results")
	gateway := createCheck(t, router, admin, httpCheckRequest("API gateway", destination.ID))
	request := httpCheckRequest("Checkout", destination.ID)
	request.ParentIDs = []uuid.UUID{gateway.ID}
	request.Folder = " Payments /  EU "
	checkout := createCheck(t, router, admin, request)
	require.Equal(t, []uuid.UUID{gateway.ID}, checkout.ParentIDs)
	require.Equal(t, "Payments/EU", checkout.Folder)

	loop := httpCheckRequest("API gateway", destination.ID)
	loop.ParentIDs, loop.Version = []uuid.UUID{checkout.ID}, gateway.Version
	response := test_utils.MakePutRequest(t, router, "/api/v1/synthetics/checks/"+gateway.ID.String(), bearer(admin),
		loop, http.StatusBadRequest)
	require.Contains(t, string(response.Body), "loop")
	self := httpCheckRequest("API gateway", destination.ID)
	self.ParentIDs, self.Version = []uuid.UUID{gateway.ID}, gateway.Version
	test_utils.MakePutRequest(t, router, "/api/v1/synthetics/checks/"+gateway.ID.String(), bearer(admin),
		self, http.StatusBadRequest)
	missing := httpCheckRequest("Orders", destination.ID)
	missing.ParentIDs = []uuid.UUID{uuid.New()}
	test_utils.MakePostRequest(t, router, "/api/v1/synthetics/checks", bearer(admin), missing, http.StatusBadRequest)

	test_utils.MakeDeleteRequest(t, router, "/api/v1/synthetics/checks/"+gateway.ID.String(), bearer(admin),
		http.StatusOK)
	var after synthetics.CheckView
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/synthetics/checks/"+checkout.ID.String(), bearer(admin),
		http.StatusOK, &after)
	require.Empty(t, after.ParentIDs, "a deleted parent leaves its children")
}

func Test_Import_FoldersDependsOnAndAlerts_RoundTripThroughYAML(t *testing.T) {
	router, admin, _ := setup(t)
	createDestination(t, router, admin, "synthetics")
	channel := createSharedChannel(t, "#ops-alerts")
	yaml := `apiVersion: obzly.com/synthetics/v1
kind: MonitorList
defaults:
  destination: synthetics
monitors:
  - name: Checkout
    type: http
    folder: Payments/EU
    depends_on: [API gateway]
    request: {url: "https://shop.example.com/checkout"}
    alerts: {channels: ["#ops-alerts"], notify: degraded, cert_days: 14, repeat: 1h}
  - name: API gateway
    type: tcp
    folder: Platform
    target: gateway.example.com:443
`
	var preview synthetics.ImportPreview
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/synthetics/checks/import", bearer(admin),
		synthetics.ImportRequest{YAML: yaml}, http.StatusOK, &preview)
	require.Empty(t, preview.Errors)
	require.ElementsMatch(t, []string{"Checkout", "API gateway"}, preview.Create)

	var checks []synthetics.CheckView
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/synthetics/checks", bearer(admin), http.StatusOK, &checks)
	byName := map[string]synthetics.CheckView{}
	for _, check := range checks {
		byName[check.Name] = check
	}
	require.Equal(t, []uuid.UUID{byName["API gateway"].ID}, byName["Checkout"].ParentIDs,
		"a parent later in the file resolves")
	require.Equal(t, []uuid.UUID{channel}, byName["Checkout"].Alerts.ChannelIDs)
	require.Equal(t, "degraded", byName["Checkout"].Alerts.Notify)
	require.Equal(t, 3600, byName["Checkout"].Alerts.RepeatSeconds)

	exported := test_utils.MakeGetRequest(t, router, "/api/v1/synthetics/checks/export", bearer(admin), http.StatusOK)
	body := string(exported.Body)
	require.Contains(t, body, "folder: Payments/EU")
	require.Contains(t, body, "- API gateway")
	require.Contains(t, body, "'#ops-alerts'")

	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/synthetics/checks/import", bearer(admin),
		synthetics.ImportRequest{YAML: body}, http.StatusOK, &preview)
	require.Empty(t, preview.Update, "re-importing the export changes nothing")

	broken := strings.Replace(yaml, "depends_on: [API gateway]", "depends_on: [Nope]", 1)
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/synthetics/checks/import/preview", bearer(admin),
		synthetics.ImportRequest{YAML: broken}, http.StatusOK, &preview)
	require.Len(t, preview.Errors, 1)
	require.Contains(t, preview.Errors[0].Message, `no monitor named "Nope"`)
}

func Test_MonitorAlertsFor_DownRuleMutedByAParentInTheSameProject(t *testing.T) {
	router, admin, _ := setup(t)
	destination := createDestination(t, router, admin, "results")
	other := createDestination(t, router, admin, "elsewhere")
	channel := createSharedChannel(t, "pagerduty")
	alerts := &synthetics.CheckAlerts{ChannelIDs: []uuid.UUID{channel}, Notify: "degraded", CertDays: 14}
	gatewayRequest := httpCheckRequest("API gateway", destination.ID)
	gatewayRequest.Alerts = alerts
	gateway := createCheck(t, router, admin, gatewayRequest)
	remote := createCheck(t, router, admin, httpCheckRequest("Remote dependency", other.ID))
	childRequest := httpCheckRequest("Checkout", destination.ID)
	childRequest.ParentIDs, childRequest.Alerts = []uuid.UUID{gateway.ID, remote.ID}, alerts
	child := createCheck(t, router, admin, childRequest)
	createCheck(t, router, admin, httpCheckRequest("No alerts", destination.ID))

	rules, err := synthetics.GetService().MonitorAlertsFor("site-a")
	require.NoError(t, err)

	byKey := map[string]string{}
	for _, rule := range rules {
		byKey[rule.Key] = rule.PromQL
		_, err := parser.NewParser(parser.Options{}).ParseExpr(rule.PromQL)
		require.NoError(t, err, rule.PromQL)
		require.Equal(t, []uuid.UUID{channel}, rule.ChannelIDs)
	}
	require.Len(t, rules, 6, "down, degraded and certificate for each monitor with channels")
	childDown := byKey["synthetics:"+child.ID.String()+":down"]
	require.Contains(t, childDown, "unless on()")
	require.Contains(t, childDown, gateway.ID.String(), "muted while its parent is down")
	require.NotContains(t, childDown, remote.ID.String(), "a parent in another project can't mute it")
	require.NotContains(t, byKey["synthetics:"+gateway.ID.String()+":down"], "unless")
	require.Contains(t, byKey["synthetics:"+child.ID.String()+":degraded"], gateway.ID.String(),
		"degraded is muted by a down parent too")

	none, err := synthetics.GetService().MonitorAlertsFor("site-b")
	require.NoError(t, err)
	require.Empty(t, none, "only the Site the results go to")
}

func createSharedChannel(t *testing.T, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	require.NoError(t, storage.GetDb().Exec(
		`INSERT INTO shared_channels (id, name, kind, config_encrypted) VALUES (?, ?, 'webhook', 'x')`,
		id, name+" "+id.String()[:6]).Error)
	t.Cleanup(func() { storage.GetDb().Exec("DELETE FROM shared_channels WHERE id = ?", id) })
	// Imports refer to it by name.
	require.NoError(t, storage.GetDb().Exec("UPDATE shared_channels SET name = ? WHERE id = ?", name, id).Error)
	return id
}
