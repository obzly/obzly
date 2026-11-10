package synthetics_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"obzly/internal/features/synthetics"
	"obzly/internal/synthetics/spec"
	test_utils "obzly/internal/util/testing"
)

func Test_CloudConnection_CostUse_Validated_SentToTheProbeWithTrackedTags_AndViaBlocksDeletes(t *testing.T) {
	router, admin, member := setup(t)
	destination := createDestination(t, router, admin, "cloud")
	probe := createProbe(t, router, admin, "probe-apac-01", "aws-ap-south-1")
	payer := awsRequest(probe.Probe.ID, destination.ID)
	payer.Name, payer.ScanTypes = "Contoso-Payer", []string{spec.UseInventory, spec.UseCost}
	payer.Cost = &spec.CostSettings{Scope: spec.CostScopeBilling, Source: spec.CostSourceAuto,
		Export: &spec.CostExport{Bucket: "contoso-billing-exports", Prefix: "/focus/", Region: "us-east-1"}}

	for name, broken := range map[string]func(*spec.CostSettings){
		"bucket":                   func(c *spec.CostSettings) { c.Export.Bucket = "Bad_Bucket" },
		"source":                   func(c *spec.CostSettings) { c.Source = "cache" },
		"scope":                    func(c *spec.CostSettings) { c.Scope = "everything" },
		"via nothing":              func(c *spec.CostSettings) { c.Scope = spec.CostScopeVia },
		"refresh too often":        func(c *spec.CostSettings) { c.RefreshHours = 2 },
		"export only, no location": func(c *spec.CostSettings) { c.Source, c.Export = spec.CostSourceExport, nil },
	} {
		invalid := payer
		settings := *payer.Cost
		export := *payer.Cost.Export
		settings.Export = &export
		broken(&settings)
		invalid.Cost = &settings
		test_utils.MakePostRequest(t, router, "/api/v1/cloud-connections", bearer(admin), invalid,
			http.StatusBadRequest)
		_ = name
	}

	var created synthetics.CloudConnectionView
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/cloud-connections", bearer(admin), payer,
		http.StatusOK, &created)
	require.Equal(t, "focus", created.Cost.Export.Prefix, "slashes trimmed")
	require.Equal(t, spec.DefaultCostRefreshHours, created.Cost.RefreshHours)

	memberAccount := awsRequest(probe.Probe.ID, destination.ID)
	memberAccount.Name, memberAccount.ScopeID = "Contoso-Prod", "444455556666"
	memberAccount.Identity = "arn:aws:iam::444455556666:role/obzly-readonly"
	memberAccount.ScanTypes = []string{spec.UseInventory, spec.UseCost}
	memberAccount.Cost = &spec.CostSettings{Scope: spec.CostScopeVia, ViaConnectionID: created.ID.String()}
	var memberView synthetics.CloudConnectionView
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/cloud-connections", bearer(admin), memberAccount,
		http.StatusOK, &memberView)
	require.Nil(t, memberView.Cost.Export, "via reads nothing itself")

	notBilling := memberAccount
	notBilling.Name, notBilling.Cost = "Contoso-Dev", &spec.CostSettings{Scope: spec.CostScopeVia,
		ViaConnectionID: memberView.ID.String()}
	test_utils.MakePostRequest(t, router, "/api/v1/cloud-connections", bearer(admin), notBilling,
		http.StatusBadRequest)

	test_utils.MakePutRequest(t, router, "/api/v1/cloud-connections/cost-settings", bearer(member),
		synthetics.CloudCostSettings{TrackedTags: []string{"team"}}, http.StatusForbidden)
	test_utils.MakePutRequest(t, router, "/api/v1/cloud-connections/cost-settings", bearer(admin),
		synthetics.CloudCostSettings{TrackedTags: []string{"bad<tag>"}}, http.StatusBadRequest)
	var settings synthetics.CloudCostSettings
	test_utils.MakePutRequestAndUnmarshal(t, router, "/api/v1/cloud-connections/cost-settings", bearer(admin),
		synthetics.CloudCostSettings{TrackedTags: []string{"team", " env ", "team"}}, http.StatusOK, &settings)
	require.Equal(t, []string{"team", "env"}, settings.TrackedTags)
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/cloud-connections/cost-settings", bearer(member),
		http.StatusOK, &settings)
	require.Equal(t, []string{"team", "env"}, settings.TrackedTags)

	response := probeRequest(t, router, probe.Token, "GET", "/api/v1/synthetics/probe/config", nil, nil,
		http.StatusOK)
	var config spec.ProbeConfig
	require.NoError(t, json.Unmarshal(response.Body, &config))
	byName := map[string]spec.CloudConnection{}
	for _, connection := range config.Cloud {
		byName[connection.Name] = connection
	}
	require.Equal(t, []string{"team", "env"}, byName["Contoso-Payer"].Cost.TrackedTags)
	require.Equal(t, "contoso-billing-exports", byName["Contoso-Payer"].Cost.Export.Bucket)
	require.Equal(t, spec.CostScopeVia, byName["Contoso-Prod"].Cost.Scope)

	cost := spec.CloudScan{Kind: spec.ScanCost, Uses: map[string]spec.UseResult{spec.UseCost: {State: spec.UseOK}},
		StartedAt: time.Now().UTC(), Cost: &spec.CostScanInfo{Source: spec.CostSourceExport, Rows: 1234}}
	probeRequest(t, router, probe.Token, "POST", "/api/v1/synthetics/probe/cloud/"+created.ID.String()+"/scan",
		cost, nil, http.StatusOK)
	var view synthetics.CloudConnectionView
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/cloud-connections/"+created.ID.String(),
		bearer(admin), http.StatusOK, &view)
	require.Equal(t, 1234, view.Status.LastCost.Cost.Rows)
	require.Nil(t, view.Status.LastScan, "a cost refresh isn't a discovery")

	test_utils.MakeDeleteRequest(t, router, "/api/v1/cloud-connections/"+created.ID.String(), bearer(admin),
		http.StatusConflict)
	test_utils.MakeDeleteRequest(t, router, "/api/v1/cloud-connections/"+memberView.ID.String(), bearer(admin),
		http.StatusOK)
	test_utils.MakeDeleteRequest(t, router, "/api/v1/cloud-connections/"+created.ID.String(), bearer(admin),
		http.StatusOK)
}
