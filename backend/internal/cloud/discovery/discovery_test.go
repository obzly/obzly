package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"obzly/internal/synthetics/spec"
)

func Test_DiscoverAzure_Federated_PagesTheResourceGraph_AndMapsResources(t *testing.T) {
	var queries []string
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/11111111-1111-1111-1111-111111111111/oauth2/v2.0/token", r.URL.Path)
		require.NoError(t, r.ParseForm())
		require.Equal(t, "control-plane-token", r.Form.Get("client_assertion"))
		require.Equal(t, "22222222-2222-2222-2222-222222222222", r.Form.Get("client_id"))
		_, _ = w.Write([]byte(`{"access_token":"arm-token"}`))
	}))
	defer login.Close()
	var mu sync.Mutex
	management := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer arm-token", r.Header.Get("Authorization"))
		var body struct {
			Query         string         `json:"query"`
			Subscriptions []string       `json:"subscriptions"`
			Options       map[string]any `json:"options"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		mu.Lock()
		queries = append(queries, body.Query)
		mu.Unlock()
		require.Equal(t, []string{"33333333-3333-3333-3333-333333333333"}, body.Subscriptions)
		if body.Options["$skipToken"] == nil {
			_, _ = w.Write(
				[]byte(
					`{"data":[{"id":"/subscriptions/333/resourceGroups/web/providers/Microsoft.Compute/virtualMachines/vm-1",
				"name":"vm-1","type":"Microsoft.Compute/virtualMachines","location":"westeurope","subscriptionId":"333",
				"vmId":"abcd-vm","power":"PowerState/running","vmSize":"Standard_D2s_v5"}],"$skipToken":"next"}`,
				),
			)
			return
		}
		_, _ = w.Write(
			[]byte(
				`{"data":[{"id":"/subscriptions/333/resourceGroups/k8s/providers/Microsoft.ContainerService/managedClusters/aks-prd",
			"name":"aks-prd","type":"microsoft.containerservice/managedclusters","location":"westeurope","subscriptionId":"333",
			"aksPower":"Running","k8sVersion":"1.33"}]}`,
			),
		)
	}))
	defer management.Close()

	connection := spec.CloudConnection{
		ID:            "conn-1",
		Provider:      spec.ProviderAzure,
		Scope:         "subscription",
		ScopeID:       "33333333-3333-3333-3333-333333333333",
		Access:        spec.AccessFederated,
		Identity:      "22222222-2222-2222-2222-222222222222",
		TenantID:      "11111111-1111-1111-1111-111111111111",
		Uses:          []string{spec.UseInventory},
		ResourceTypes: []string{"virtual_machine", "aks_cluster"},
	}
	result := Discover(context.Background(), connection, Options{
		AzureLogin: login.URL, AzureManagement: management.URL,
		Token: func(context.Context) (string, error) { return "control-plane-token", nil },
	})

	require.Equal(t, spec.UseOK, result.Scan.Uses[spec.UseInventory].State, result.Scan.Uses)
	require.Len(t, result.Resources, 2)
	require.Len(t, queries, 2, "follows the skip token")
	require.NotContains(t, queries[0], "storageaccounts", "only the types asked for")
	vm := result.Resources[0]
	require.Equal(t, "azure_virtual_machine", vm.Type)
	require.Equal(t, "running", vm.State)
	require.Equal(t, "abcd-vm", vm.Attributes["host_id"])
	require.Equal(t, "web", vm.Attributes["azure_resource_group"])
	require.Equal(t, "aks-prd", result.Resources[1].Attributes["k8s_cluster_name"])
	require.Equal(t, map[string]int{"azure_virtual_machine": 1, "azure_aks_cluster": 1}, result.Scan.Types)

	gauges := Gauges(connection, result.Resources, 900)
	require.Len(t, gauges, 2)
	require.Equal(t, "cloud_resource_info", gauges[0].Name)
	require.Equal(t, "conn-1", gauges[0].Labels["cloud_connection_id"])
	require.Equal(t, "abcd-vm", gauges[0].Labels["host_id"])
	require.Equal(t, "900", gauges[0].Labels["cloud_refresh_seconds"])
}

func Test_DiscoverAzure_WithoutReader_NeedsPermission(t *testing.T) {
	imds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "true", r.Header.Get("Metadata"))
		_, _ = w.Write([]byte(`{"access_token":"arm-token"}`))
	}))
	defer imds.Close()
	management := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"AuthorizationFailed","message":"no read on the subscription"}}`))
	}))
	defer management.Close()
	result := Discover(context.Background(), spec.CloudConnection{
		Provider: spec.ProviderAzure, Scope: "subscription", ScopeID: "33333333-3333-3333-3333-333333333333",
		Access: spec.AccessProbe, TenantID: "11111111-1111-1111-1111-111111111111", Uses: []string{spec.UseInventory},
	}, Options{AzureIMDS: imds.URL, AzureManagement: management.URL})
	use := result.Scan.Uses[spec.UseInventory]
	require.Equal(t, spec.UseNeedsPermission, use.State)
	require.Equal(t, []string{"Reader role"}, use.Missing)
}

func Test_DiscoverAWS_AssumesTheRole_ScansARegion_AndReportsADeniedCall(t *testing.T) {
	isolateAWS(t)
	var assumed url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		w.Header().Set("Content-Type", "text/xml")
		switch form.Get("Action") {
		case "AssumeRole":
			assumed = form
			fmt.Fprint(w, `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult>
				<Credentials><AccessKeyId>ASIATEST</AccessKeyId><SecretAccessKey>assumed</SecretAccessKey>
				<SessionToken>session</SessionToken><Expiration>2099-01-01T00:00:00Z</Expiration></Credentials>
				<AssumedRoleUser><Arn>arn:aws:sts::123456789012:assumed-role/obzly-readonly/obzly-probe</Arn>
				<AssumedRoleId>AROA:obzly-probe</AssumedRoleId></AssumedRoleUser></AssumeRoleResult>
				<ResponseMetadata><RequestId>1</RequestId></ResponseMetadata></AssumeRoleResponse>`)
		case "GetCallerIdentity":
			fmt.Fprint(w, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
				<GetCallerIdentityResult><Arn>arn:aws:sts::123456789012:assumed-role/obzly-readonly/obzly-probe</Arn>
				<UserId>AROA:obzly-probe</UserId><Account>123456789012</Account></GetCallerIdentityResult>
				<ResponseMetadata><RequestId>1</RequestId></ResponseMetadata></GetCallerIdentityResponse>`)
		case "DescribeInstances":
			fmt.Fprint(
				w,
				`<DescribeInstancesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>1</requestId>
				<reservationSet><item><reservationId>r-1</reservationId><instancesSet><item>
				<instanceId>i-0abc</instanceId><instanceType>t3.large</instanceType>
				<instanceState><code>16</code><name>running</name></instanceState>
				<placement><availabilityZone>ap-south-1a</availabilityZone></placement>
				<tagSet><item><key>Name</key><value>web-1</value></item></tagSet><vpcId>vpc-1</vpcId>
				</item></instancesSet></item></reservationSet></DescribeInstancesResponse>`,
			)
		case "DescribeDBInstances":
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `<ErrorResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><Error><Type>Sender</Type>
				<Code>AccessDenied</Code><Message>not authorized</Message></Error><RequestId>1</RequestId></ErrorResponse>`)
		default:
			t.Errorf("unexpected call %q", form.Get("Action"))
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()

	result := Discover(context.Background(), spec.CloudConnection{
		ID: "conn-aws", Provider: spec.ProviderAWS, Scope: "account", ScopeID: "123456789012", Access: spec.AccessRole,
		Identity: "arn:aws:iam::123456789012:role/obzly-readonly", ExternalID: "obz-0123456789abcdef",
		Uses: []string{spec.UseInventory}, Regions: []string{"ap-south-1"},
		ResourceTypes: []string{"ec2_instance", "rds_instance"},
	}, Options{AWSEndpoint: server.URL})

	require.Equal(t, "obz-0123456789abcdef", assumed.Get("ExternalId"))
	require.Equal(t, "arn:aws:iam::123456789012:role/obzly-readonly", assumed.Get("RoleArn"))
	require.Len(t, result.Resources, 1)
	instance := result.Resources[0]
	require.Equal(t, "aws_ec2_instance", instance.Type)
	require.Equal(t, "web-1", instance.Name)
	require.Equal(t, "123456789012", instance.AccountID)
	require.Equal(t, "i-0abc", instance.Attributes["host_id"])
	use := result.Scan.Uses[spec.UseInventory]
	require.Equal(t, spec.UseNeedsPermission, use.State)
	require.Equal(t, []string{"rds:DescribeDBInstances"}, use.Missing)
}

func Test_MemberRoleARN_SwapsTheAccount(t *testing.T) {
	require.Equal(t, "arn:aws:iam::222233334444:role/obzly-readonly",
		memberRoleARN("arn:aws:iam::123456789012:role/obzly-readonly", "222233334444"))
}

// isolateAWS keeps the SDK off this machine's AWS profile and instance
// metadata: only the fake endpoint, with dummy keys, is ever called.
func isolateAWS(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	require.NoError(t, os.WriteFile(empty, nil, 0o600))
	t.Setenv("AWS_CONFIG_FILE", empty)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", empty)
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDTESTONLY")
	t.Setenv("AWS_SECRET_ACCESS_KEY", strings.Repeat("x", 20))
	t.Setenv("AWS_SESSION_TOKEN", "")
}
