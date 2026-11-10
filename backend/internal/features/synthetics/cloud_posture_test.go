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

func Test_CloudConnection_PostureUse_Validated_SentToTheProbe_AndItsRefreshReported(t *testing.T) {
	router, admin, _ := setup(t)
	destination := createDestination(t, router, admin, "cloud")
	probe := createProbe(t, router, admin, "probe-emea-01", "aws-eu-west-1")
	request := awsRequest(probe.Probe.ID, destination.ID)
	request.Name, request.ScanTypes = "Contoso-Posture", []string{spec.UsePosture}

	for _, broken := range []*spec.PostureSettings{
		{Source: "everything"},
		{Source: spec.PostureSourceNative, RefreshHours: 1},
	} {
		invalid := request
		invalid.Posture = broken
		test_utils.MakePostRequest(t, router, "/api/v1/cloud-connections", bearer(admin), invalid,
			http.StatusBadRequest)
	}

	var created synthetics.CloudConnectionView
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/cloud-connections", bearer(admin), request,
		http.StatusOK, &created)
	require.Equal(t, &spec.PostureSettings{Source: spec.PostureSourceAuto, RefreshHours: 12}, created.Posture,
		"defaults: Automatic, every 12 hours")
	require.Nil(t, created.Cost)

	response := probeRequest(t, router, probe.Token, "GET", "/api/v1/synthetics/probe/config", nil, nil,
		http.StatusOK)
	var config spec.ProbeConfig
	require.NoError(t, json.Unmarshal(response.Body, &config))
	var sent *spec.CloudConnection
	for i := range config.Cloud {
		if config.Cloud[i].Name == "Contoso-Posture" {
			sent = &config.Cloud[i]
		}
	}
	require.NotNil(t, sent)
	require.Equal(t, spec.PostureSourceAuto, sent.Posture.Source)

	posture := spec.CloudScan{Kind: spec.ScanPosture, StartedAt: time.Now().UTC(),
		Uses:    map[string]spec.UseResult{spec.UsePosture: {State: spec.UseOK}},
		Posture: &spec.PostureScanInfo{Findings: 7, Native: []string{"111122223333/eu-west-1"}}}
	probeRequest(t, router, probe.Token, "POST", "/api/v1/synthetics/probe/cloud/"+created.ID.String()+"/scan",
		posture, nil, http.StatusOK)
	var view synthetics.CloudConnectionView
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/cloud-connections/"+created.ID.String(),
		bearer(admin), http.StatusOK, &view)
	require.Equal(t, 7, view.Status.LastPosture.Posture.Findings)
	require.Nil(t, view.Status.LastScan, "a posture refresh isn't a discovery")
}
