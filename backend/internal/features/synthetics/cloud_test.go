package synthetics_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	control_plane_services "obzly/internal/features/control_plane/services"
	"obzly/internal/features/synthetics"
	"obzly/internal/synthetics/spec"
	test_utils "obzly/internal/util/testing"
)

func Test_CloudConnections_AdminsManage_Validated_AndInUseBlocksDeletes(t *testing.T) {
	router, admin, member := setup(t)
	destination := createDestination(t, router, admin, "cloud")
	probe := createProbe(t, router, admin, "probe-apac-01", "aws-ap-south-1")

	request := awsRequest(probe.Probe.ID, destination.ID)
	test_utils.MakePostRequest(t, router, "/api/v1/cloud-connections", bearer(member), request, http.StatusForbidden)

	var setupValues synthetics.CloudSetup
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/cloud-connections/setup", bearer(admin), http.StatusOK,
		&setupValues)
	require.Regexp(t, `^obz-[0-9a-f]{32}-[0-9a-f]{16}$`, setupValues.ExternalID)
	require.Equal(t, "sts.amazonaws.com", setupValues.Audiences[spec.ProviderAWS])

	for name, broken := range map[string]func(*synthetics.CloudConnectionRequest){
		"account":             func(r *synthetics.CloudConnectionRequest) { r.ScopeID = "12345" },
		"role":                func(r *synthetics.CloudConnectionRequest) { r.Identity = "arn:aws:iam::1:user/bob" },
		"later use":           func(r *synthetics.CloudConnectionRequest) { r.ScanTypes = []string{"cost"} },
		"region":              func(r *synthetics.CloudConnectionRequest) { r.Regions = []string{"mars"} },
		"type":                func(r *synthetics.CloudConnectionRequest) { r.ResourceTypes = []string{"virtual_machine"} },
		"probe":               func(r *synthetics.CloudConnectionRequest) { r.ProbeID = uuid.New() },
		"discovery too often": func(r *synthetics.CloudConnectionRequest) { r.IntervalSeconds = 600 },
		"health too rare":     func(r *synthetics.CloudConnectionRequest) { r.HealthIntervalSeconds = 7200 },
		"health rarer than discovery": func(r *synthetics.CloudConnectionRequest) {
			r.IntervalSeconds, r.HealthIntervalSeconds = 1800, 3600
		},
		"org via the probe's own identity": func(r *synthetics.CloudConnectionRequest) {
			r.Scope, r.ScopeID, r.Access = "organization", "o-a1b2c3d4e5", spec.AccessProbe
		},
	} {
		invalid := request
		broken(&invalid)
		test_utils.MakePostRequest(t, router, "/api/v1/cloud-connections", bearer(admin), invalid,
			http.StatusBadRequest)
		_ = name
	}

	var created synthetics.CloudConnectionView
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/cloud-connections", bearer(admin), request,
		http.StatusOK, &created)
	require.Equal(t, "probe-apac-01", created.Probe.Name)
	require.Equal(t, "cloud", created.Destination.Name)
	require.Equal(t, 4*3600, created.IntervalSeconds, "full discovery every 4 hours by default")
	require.Equal(t, 900, created.HealthIntervalSeconds, "health scans every 15 minutes by default")
	require.Regexp(t, `^obz-`, created.ExternalID, "generated when the request has none")
	test_utils.MakePostRequest(t, router, "/api/v1/cloud-connections", bearer(admin), request, http.StatusBadRequest)

	var connections []synthetics.CloudConnectionView
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/cloud-connections", bearer(member), http.StatusOK,
		&connections)
	require.Len(t, connections, 1)
	require.Equal(t, "123456789012", connections[0].ScopeID)
	require.Empty(t, connections[0].Identity, "members don't see who it signs in as")
	require.Empty(t, connections[0].ExternalID)
	var single synthetics.CloudConnectionView
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/cloud-connections/"+created.ID.String(),
		bearer(member), http.StatusOK, &single)
	require.Empty(t, single.Identity)
	require.Empty(t, single.ExternalID)
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/cloud-connections/"+created.ID.String(),
		bearer(admin), http.StatusOK, &single)
	require.Equal(t, created.ExternalID, single.ExternalID)

	update := request
	update.Regions, update.Version = []string{"ap-south-1"}, created.Version
	var updated synthetics.CloudConnectionView
	test_utils.MakePutRequestAndUnmarshal(t, router, "/api/v1/cloud-connections/"+created.ID.String(),
		bearer(admin), update, http.StatusOK, &updated)
	require.Equal(t, []string{"ap-south-1"}, updated.Regions)
	require.Equal(t, created.ExternalID, updated.ExternalID, "kept when the update leaves it out")
	test_utils.MakePutRequest(t, router, "/api/v1/cloud-connections/"+created.ID.String(), bearer(admin), update,
		http.StatusConflict)

	test_utils.MakeDeleteRequest(t, router, "/api/v1/synthetics/probes/"+probe.Probe.ID.String(), bearer(admin),
		http.StatusConflict)
	test_utils.MakeDeleteRequest(t, router, "/api/v1/synthetics/destinations/"+destination.ID.String(),
		bearer(admin), http.StatusConflict)
	test_utils.MakeDeleteRequest(t, router, "/api/v1/cloud-connections/"+created.ID.String(), bearer(admin),
		http.StatusOK)
}

func Test_CloudConnection_InTheProbeConfig_ScanReported_TestAccessRoundTrip(t *testing.T) {
	router, admin, _ := setup(t)
	destination := createDestination(t, router, admin, "cloud")
	probe := createProbe(t, router, admin, "probe-apac-01", "aws-ap-south-1")
	other := createProbe(t, router, admin, "probe-other", "aws-us-east-1")
	request := awsRequest(probe.Probe.ID, destination.ID)
	var created synthetics.CloudConnectionView
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/cloud-connections", bearer(admin), request,
		http.StatusOK, &created)

	response := probeRequest(t, router, probe.Token, "GET", "/api/v1/synthetics/probe/config", nil, nil,
		http.StatusOK)
	var config spec.ProbeConfig
	require.NoError(t, json.Unmarshal(response.Body, &config))
	require.Len(t, config.Cloud, 1)
	require.Equal(t, "arn:aws:iam::123456789012:role/obzly-readonly", config.Cloud[0].Identity)
	require.Equal(t, []string{spec.UseInventory}, config.Cloud[0].Uses)
	require.Len(t, config.Destinations, 1, "the connection's destination comes with it")
	response = probeRequest(t, router, other.Token, "GET", "/api/v1/synthetics/probe/config", nil, nil, http.StatusOK)
	var otherConfig spec.ProbeConfig
	require.NoError(t, json.Unmarshal(response.Body, &otherConfig))
	require.Empty(t, otherConfig.Cloud, "only its own probe group scans it")

	scan := spec.CloudScan{
		Uses:      map[string]spec.UseResult{spec.UseInventory: {State: spec.UseOK}},
		Resources: 42, Types: map[string]int{"aws_ec2_instance": 40, "aws_s3_bucket": 2}, StartedAt: time.Now().UTC(),
	}
	probeRequest(t, router, other.Token, "POST", "/api/v1/synthetics/probe/cloud/"+created.ID.String()+"/scan",
		scan, nil, http.StatusNotFound)
	probeRequest(t, router, probe.Token, "POST", "/api/v1/synthetics/probe/cloud/"+created.ID.String()+"/scan",
		scan, nil, http.StatusOK)
	var view synthetics.CloudConnectionView
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/cloud-connections/"+created.ID.String(),
		bearer(admin), http.StatusOK, &view)
	require.Equal(t, 42, view.Status.LastScan.Resources)
	require.NotNil(t, view.Status.ReportedAt)
	require.Equal(t, 900, config.Cloud[0].HealthIntervalSeconds)

	health := spec.CloudScan{
		Kind: spec.ScanHealth, Uses: map[string]spec.UseResult{spec.UseInventory: {State: spec.UseOK}},
		Resources: 41, Throttled: 3, StartedAt: time.Now().UTC(),
	}
	probeRequest(t, router, probe.Token, "POST", "/api/v1/synthetics/probe/cloud/"+created.ID.String()+"/scan",
		health, nil, http.StatusOK)
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/cloud-connections/"+created.ID.String(),
		bearer(admin), http.StatusOK, &view)
	require.Equal(t, 42, view.Status.LastScan.Resources, "a health scan keeps the last discovery")
	require.Equal(t, 41, view.Status.LastHealth.Resources)
	require.Equal(t, 3, view.Status.LastHealth.Throttled)

	var test synthetics.CloudTest
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/cloud-connections/test", bearer(admin), request,
		http.StatusOK, &test)
	require.Equal(t, synthetics.TaskQueued, test.Status)
	response = probeRequest(t, router, probe.Token, "GET", "/api/v1/synthetics/probe/tasks?wait=2", nil, nil,
		http.StatusOK)
	var task spec.Task
	require.NoError(t, json.Unmarshal(response.Body, &task))
	require.Equal(t, test.ID, task.ID)
	require.NotNil(t, task.Cloud)
	require.Equal(t, "123456789012", task.Cloud.ScopeID)
	missing := spec.CloudScan{Uses: map[string]spec.UseResult{spec.UseInventory: {
		State: spec.UseNeedsPermission, Missing: []string{"elasticloadbalancing:DescribeTargetHealth"},
	}}}
	probeRequest(t, router, other.Token, "POST", "/api/v1/synthetics/probe/tasks/"+task.ID+"/cloud-result",
		missing, nil, http.StatusNotFound)
	probeRequest(t, router, probe.Token, "POST", "/api/v1/synthetics/probe/tasks/"+task.ID+"/cloud-result",
		missing, nil, http.StatusOK)
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/cloud-connections/tests/"+test.ID, bearer(admin),
		http.StatusOK, &test)
	require.Equal(t, synthetics.TaskDone, test.Status)
	require.Equal(t, spec.UseNeedsPermission, test.Result.Uses[spec.UseInventory].State)
}

func Test_CloudToken_FederatedOnly_SignedRS256ForTheProbeGroup_VerifiesWithTheJWKS(t *testing.T) {
	router, admin, _ := setup(t)
	require.NoError(t, control_plane_services.EnsureFederationKey())
	synthetics.SetCloudTokenSigner(control_plane_services.SignCloudToken)
	destination := createDestination(t, router, admin, "cloud")
	probe := createProbe(t, router, admin, "probe-outside", "on-prem")
	role := awsRequest(probe.Probe.ID, destination.ID)
	var roleConnection synthetics.CloudConnectionView
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/cloud-connections", bearer(admin), role,
		http.StatusOK, &roleConnection)
	federated := role
	federated.Name, federated.Access, federated.ExternalID = "Federated", spec.AccessFederated, ""
	var connection synthetics.CloudConnectionView
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/cloud-connections", bearer(admin), federated,
		http.StatusOK, &connection)
	require.Empty(t, connection.ExternalID)

	probeRequest(t, router, probe.Token, "POST", "/api/v1/synthetics/probe/cloud/"+roleConnection.ID.String()+"/token",
		nil, nil, http.StatusNotFound)
	response := probeRequest(t, router, probe.Token, "POST",
		"/api/v1/synthetics/probe/cloud/"+connection.ID.String()+"/token", nil, nil, http.StatusOK)
	var token spec.CloudToken
	require.NoError(t, json.Unmarshal(response.Body, &token))

	parsed, err := jwt.ParseSigned(token.Token, []jose.SignatureAlgorithm{jose.RS256})
	require.NoError(t, err)
	jwks, err := control_plane_services.GetSigningKeyService().JWKS()
	require.NoError(t, err)
	keys := jwks.Key(parsed.Headers[0].KeyID)
	require.Len(t, keys, 1, "the federation key is published")
	var claims jwt.Claims
	require.NoError(t, parsed.Claims(keys[0].Key, &claims))
	require.Equal(t, "probe:"+probe.Probe.ID.String(), claims.Subject, "the group's ID, not its name")
	require.Equal(t, jwt.Audience{"sts.amazonaws.com"}, claims.Audience)
	require.WithinDuration(t, time.Now().Add(10*time.Minute), claims.Expiry.Time(), time.Minute)
}

func Test_CloudConnection_ExternalID_OnlyOneTheControlPlaneIssued(t *testing.T) {
	router, admin, _ := setup(t)
	destination := createDestination(t, router, admin, "cloud")
	probe := createProbe(t, router, admin, "probe-apac-01", "aws-ap-south-1")
	var setupValues synthetics.CloudSetup
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/cloud-connections/setup", bearer(admin), http.StatusOK,
		&setupValues)

	fromWizard := awsRequest(probe.Probe.ID, destination.ID)
	fromWizard.ExternalID = setupValues.ExternalID
	var created synthetics.CloudConnectionView
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/cloud-connections", bearer(admin), fromWizard,
		http.StatusOK, &created)
	require.Equal(t, setupValues.ExternalID, created.ExternalID, "the wizard's, which the role trusts")

	madeUp := awsRequest(probe.Probe.ID, destination.ID)
	madeUp.Name, madeUp.ExternalID = "Made up", "another-tenants-external-id"
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/cloud-connections", bearer(admin), madeUp,
		http.StatusOK, &created)
	require.NotEqual(t, madeUp.ExternalID, created.ExternalID, "a made-up one is replaced")
	require.Regexp(t, `^obz-[0-9a-f]{32}-[0-9a-f]{16}$`, created.ExternalID)

	update := madeUp
	update.ExternalID, update.Version = setupValues.ExternalID, created.Version
	var updated synthetics.CloudConnectionView
	test_utils.MakePutRequestAndUnmarshal(t, router, "/api/v1/cloud-connections/"+created.ID.String(),
		bearer(admin), update, http.StatusOK, &updated)
	require.Equal(t, created.ExternalID, updated.ExternalID, "an update keeps the connection's own")
}

func Test_TestAccess_Federated_TheProbeGetsATokenForItsOwnTaskOnly(t *testing.T) {
	router, admin, _ := setup(t)
	require.NoError(t, control_plane_services.EnsureFederationKey())
	synthetics.SetCloudTokenSigner(control_plane_services.SignCloudToken)
	destination := createDestination(t, router, admin, "cloud")
	probe := createProbe(t, router, admin, "probe-outside", "on-prem")
	other := createProbe(t, router, admin, "probe-other", "aws-us-east-1")
	for _, token := range []string{probe.Token, other.Token} {
		probeRequest(t, router, token, "GET", "/api/v1/synthetics/probe/config", nil, nil, http.StatusOK)
	}
	federated := awsRequest(probe.Probe.ID, destination.ID)
	federated.Access = spec.AccessFederated

	var test synthetics.CloudTest
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/cloud-connections/test", bearer(admin), federated,
		http.StatusOK, &test)
	path := "/api/v1/synthetics/probe/tasks/" + test.ID + "/cloud-token"
	probeRequest(t, router, other.Token, "POST", path, nil, nil, http.StatusNotFound)
	response := probeRequest(t, router, probe.Token, "POST", path, nil, nil, http.StatusOK)
	var token spec.CloudToken
	require.NoError(t, json.Unmarshal(response.Body, &token))
	parsed, err := jwt.ParseSigned(token.Token, []jose.SignatureAlgorithm{jose.RS256})
	require.NoError(t, err)
	var claims jwt.Claims
	require.NoError(t, parsed.UnsafeClaimsWithoutVerification(&claims))
	require.Equal(t, "probe:"+probe.Probe.ID.String(), claims.Subject)

	probeRequest(t, router, probe.Token, "POST", "/api/v1/synthetics/probe/tasks/"+test.ID+"/cloud-result",
		spec.CloudScan{Uses: map[string]spec.UseResult{spec.UseInventory: {State: spec.UseOK}}}, nil, http.StatusOK)
	probeRequest(t, router, probe.Token, "POST", path, nil, nil, http.StatusNotFound)

	role := awsRequest(probe.Probe.ID, destination.ID)
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/cloud-connections/test", bearer(admin), role,
		http.StatusOK, &test)
	probeRequest(t, router, probe.Token, "POST", "/api/v1/synthetics/probe/tasks/"+test.ID+"/cloud-token", nil, nil,
		http.StatusNotFound)
}

func Test_ReportCloudScan_OversizedLists_AreTrimmed(t *testing.T) {
	router, admin, _ := setup(t)
	destination := createDestination(t, router, admin, "cloud")
	probe := createProbe(t, router, admin, "probe-apac-01", "aws-ap-south-1")
	var created synthetics.CloudConnectionView
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/cloud-connections", bearer(admin),
		awsRequest(probe.Probe.ID, destination.ID), http.StatusOK, &created)
	scan := spec.CloudScan{
		Uses:  map[string]spec.UseResult{spec.UseInventory: {State: spec.UseOK}},
		Types: map[string]int{}, StartedAt: time.Now().UTC(),
	}
	for i := range 300 {
		if i < 30 {
			scan.Errors = append(scan.Errors, strings.Repeat("e", 5000))
		}
		scan.Regions = append(scan.Regions, fmt.Sprintf("region-%d", i))
		scan.Types[fmt.Sprintf("type_%03d", i)] = i
	}

	probeRequest(t, router, probe.Token, "POST", "/api/v1/synthetics/probe/cloud/"+created.ID.String()+"/scan",
		scan, nil, http.StatusOK)

	var view synthetics.CloudConnectionView
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/cloud-connections/"+created.ID.String(),
		bearer(admin), http.StatusOK, &view)
	require.Len(t, view.Status.LastScan.Errors, 20)
	require.Len(t, view.Status.LastScan.Errors[0], 2000)
	require.Len(t, view.Status.LastScan.Regions, 200)
	require.Len(t, view.Status.LastScan.Types, 100)
}

func awsRequest(probeID, destinationID uuid.UUID) synthetics.CloudConnectionRequest {
	return synthetics.CloudConnectionRequest{
		Name: "Production AWS", Provider: spec.ProviderAWS, Scope: "account", ScopeID: "123456789012",
		Access: spec.AccessRole, Identity: "arn:aws:iam::123456789012:role/obzly-readonly",
		ProbeID: probeID, DestinationID: destinationID, ScanTypes: []string{spec.UseInventory},
		Regions: []string{"ap-south-1", "eu-west-1"},
	}
}
