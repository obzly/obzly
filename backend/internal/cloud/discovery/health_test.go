package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"obzly/internal/synthetics/spec"
)

func Test_DiscoverAWS_Organization_ScansAccountsInParallel_AndSkipsOneWithoutTheRole(t *testing.T) {
	isolateAWS(t)
	fake := newFakeAWS(t, []string{"123456789012", "210987654321", "111122223333", "444455556666"})
	fake.denyRole["111122223333"] = true
	server := httptest.NewServer(fake)
	defer server.Close()

	connection := awsConnection()
	connection.Scope = "organization"
	result := Discover(context.Background(), connection, Options{AWSEndpoint: server.URL})

	ids := map[string]bool{}
	for _, resource := range result.Resources {
		ids[resource.ID] = true
	}
	require.Equal(t, map[string]bool{"i-123456789012": true, "i-210987654321": true, "i-444455556666": true}, ids)
	require.Greater(t, fake.maxInFlight.Load(), int32(1), "accounts are scanned at the same time")
	require.Equal(t, spec.UseOK, result.Scan.Uses[spec.UseInventory].State)
	require.Contains(t, strings.Join(result.Scan.Errors, "\n"), "sts:AssumeRole into account 111122223333")
	require.Equal(t, spec.ScanInventory, result.Scan.Kind)

	fake.listed.Store(0)
	sample := Discover(context.Background(), connection, Options{AWSEndpoint: server.URL, Sample: true})
	require.Zero(t, fake.listed.Load(), "Test access signs in to the first account only")
	require.Len(t, sample.Resources, 2, "and reads two of the three regions")
}

func Test_CheckHealthAWS_RereadsKnownResourcesOnly_AndDropsGoneOnes(t *testing.T) {
	isolateAWS(t)
	fake := newFakeAWS(t, nil)
	fake.instances = []string{"i-0abc", "i-0new"}
	fake.impaired["i-0abc"] = true
	server := httptest.NewServer(fake)
	defer server.Close()

	known := []Resource{
		{Provider: spec.ProviderAWS, AccountID: "123456789012", Region: "ap-south-1", Type: "aws_ec2_instance",
			ID: "i-0abc", Name: "web-1", State: "running"},
		{Provider: spec.ProviderAWS, AccountID: "123456789012", Region: "ap-south-1", Type: "aws_ec2_instance",
			ID: "i-0gone", Name: "web-2", State: "running"},
		{Provider: spec.ProviderAWS, AccountID: "123456789012", Region: "us-east-1", Type: "aws_s3_bucket",
			ID: "arn:aws:s3:::contoso-logs", Name: "contoso-logs", State: "available"},
	}
	result := CheckHealth(context.Background(), awsConnection(), known, Options{AWSEndpoint: server.URL})

	require.Equal(t, spec.ScanHealth, result.Scan.Kind)
	byID := map[string]Resource{}
	for _, resource := range result.Resources {
		byID[resource.ID] = resource
	}
	require.Len(t, byID, 2, "the gone instance isn't sent, the new one waits for discovery")
	require.Equal(t, HealthUnhealthy, byID["i-0abc"].Health)
	require.Equal(t, "running", byID["i-0abc"].State)
	require.Contains(t, byID, "arn:aws:s3:::contoso-logs", "buckets keep what discovery found")
	require.NotContains(t, fake.actions(), "ListBuckets")
	require.NotContains(t, fake.actions(), "DescribeRegions")
	require.Contains(t, fake.actions(), "DescribeInstanceStatus")
}

func Test_CheckHealthAWS_KeepsSignIns_AsksForKnownInstances_ReadsTargetsEveryThirdScan(t *testing.T) {
	isolateAWS(t)
	fake := newFakeAWS(t, nil)
	fake.instances = []string{"i-0abc", "i-0other"}
	fake.loadBalancer = "arn:aws:elasticloadbalancing:ap-south-1:123456789012:loadbalancer/app/web/1"
	server := httptest.NewServer(fake)
	defer server.Close()
	connection := awsConnection()
	connection.ResourceTypes = []string{"ec2_instance", "load_balancer"}
	known := []Resource{
		{Provider: spec.ProviderAWS, AccountID: "123456789012", Region: "ap-south-1", Type: "aws_ec2_instance",
			ID: "i-0abc", Name: "web-1", State: "running"},
		{Provider: spec.ProviderAWS, AccountID: "123456789012", Region: "ap-south-1", Type: "aws_load_balancer",
			ID: fake.loadBalancer, Name: "web", State: "active", Targets: map[string]int{"healthy": 1}},
	}
	options := Options{AWSEndpoint: server.URL, AWS: NewAWSCache()}

	for range 3 {
		result := CheckHealth(context.Background(), connection, known, options)
		require.Len(t, result.Resources, 2)
		for _, resource := range result.Resources {
			if resource.Type == "aws_load_balancer" {
				require.Equal(t, map[string]int{"healthy": 1}, resource.Targets, "kept between target reads")
			}
		}
	}

	require.Equal(t, 1, fake.count("AssumeRole"), "the sign-in is kept between health scans")
	require.Equal(t, 1, fake.count("GetCallerIdentity"))
	require.Equal(t, 1, fake.count("DescribeTargetHealth"), "target health on the third scan only")
	require.Len(t, fake.formsOf("DescribeInstances"), 3)
	require.Len(t, fake.formsOf("DescribeInstanceStatus"), 3)
	for _, form := range fake.formsOf("DescribeInstances") {
		require.Equal(t, "instance-id", form.Get("Filter.1.Name"))
		require.Equal(t, "i-0abc", form.Get("Filter.1.Value.1"))
		require.Empty(t, form.Get("Filter.1.Value.2"))
	}
	for _, form := range fake.formsOf("DescribeInstanceStatus") {
		require.Equal(t, "i-0abc", form.Get("InstanceId.1"))
		require.Empty(t, form.Get("MaxResults"))
	}

	unhealthy := append([]Resource{}, known...)
	unhealthy[1].Targets = map[string]int{"unhealthy": 1}
	CheckHealth(context.Background(), connection, unhealthy, options)
	require.Equal(t, 2, fake.count("DescribeTargetHealth"), "read on every scan while a target is unhealthy")
}

func Test_CheckHealthAzure_ReadsAvailability_ForKnownResources(t *testing.T) {
	imds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"arm-token"}`))
	}))
	defer imds.Close()
	vm := "/subscriptions/333/resourcegroups/web/providers/microsoft.compute/virtualmachines/vm-1"
	management := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query string `json:"query"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		if strings.HasPrefix(body.Query, "HealthResources") {
			fmt.Fprintf(w, `{"data":[{"id":%q,"availability":"degraded"}]}`, vm)
			return
		}
		require.Contains(t, body.Query, "microsoft.compute/virtualmachines")
		require.NotContains(t, body.Query, "storageaccounts", "only the types it knows")
		fmt.Fprintf(w, `{"data":[{"id":%q,"name":"vm-1","type":"Microsoft.Compute/virtualMachines",
			"location":"westeurope","subscriptionId":"333","power":"PowerState/running"}]}`, vm)
	}))
	defer management.Close()

	known := []Resource{
		{Provider: spec.ProviderAzure, Type: "azure_virtual_machine", ID: vm, Name: "vm-1", State: "running"},
		{Provider: spec.ProviderAzure, Type: "azure_virtual_machine", ID: vm + "-gone", Name: "vm-2"},
	}
	result := CheckHealth(context.Background(), spec.CloudConnection{
		Provider: spec.ProviderAzure, Scope: "subscription", ScopeID: "33333333-3333-3333-3333-333333333333",
		Access: spec.AccessProbe, Uses: []string{spec.UseInventory},
	}, known, Options{AzureIMDS: imds.URL, AzureManagement: management.URL})

	require.Len(t, result.Resources, 1)
	require.Equal(t, HealthDegraded, result.Resources[0].Health)
	require.Equal(t, spec.UseOK, result.Scan.Uses[spec.UseInventory].State)
}

func Test_Discover_AtItsTimeLimit_KeepsWhatItFound_AndSaysPartial(t *testing.T) {
	imds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"arm-token"}`))
	}))
	defer imds.Close()
	management := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Options map[string]any `json:"options"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Options["$skipToken"] != nil {
			<-r.Context().Done()
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"/subscriptions/333/resourceGroups/web/providers/Microsoft.Compute/` +
			`virtualMachines/vm-1","name":"vm-1","type":"Microsoft.Compute/virtualMachines"}],"$skipToken":"next"}`))
	}))
	defer management.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	result := Discover(ctx, spec.CloudConnection{
		Provider: spec.ProviderAzure, Scope: "subscription", ScopeID: "33333333-3333-3333-3333-333333333333",
		Access: spec.AccessProbe, Uses: []string{spec.UseInventory},
	}, Options{AzureIMDS: imds.URL, AzureManagement: management.URL})

	require.True(t, result.Scan.Partial)
	require.Len(t, result.Resources, 1, "the first page is kept")
	use := result.Scan.Uses[spec.UseInventory]
	require.Equal(t, spec.UseError, use.State)
	require.Contains(t, use.Message, "time limit")
}

func Test_AppendCapped_StopsAtTheLimit_AndSaysSo(t *testing.T) {
	tracker := newTracker()
	resources := appendCapped(nil, make([]Resource, 3), 5, tracker)
	resources = appendCapped(resources, make([]Resource, 4), 5, tracker)

	require.Len(t, resources, 5)
	require.Contains(t, tracker.errorList()[0], "stopped at 5 resources")
	require.Len(t, appendCapped(resources, make([]Resource, 1), 5, tracker), 5)
}

func Test_Merge_And_CarryHealth(t *testing.T) {
	base := []Resource{{ID: "a", State: "old", Health: HealthHealthy}, {ID: "b"}}
	merged := Merge(base, []Resource{{ID: "a", State: "new"}, {ID: "c"}})
	require.Equal(t, []string{"a", "c", "b"}, []string{merged[0].ID, merged[1].ID, merged[2].ID})
	require.Equal(t, "new", merged[0].State)
	carried := CarryHealth(base, merged)
	require.Equal(t, HealthHealthy, carried[0].Health)
}

func awsConnection() spec.CloudConnection {
	return spec.CloudConnection{
		ID: "conn-aws", Provider: spec.ProviderAWS, Scope: "account", ScopeID: "123456789012", Access: spec.AccessRole,
		Identity: "arn:aws:iam::123456789012:role/obzly-readonly", ExternalID: "obz-0123456789abcdef",
		Uses: []string{spec.UseInventory}, Regions: []string{"ap-south-1", "eu-west-1", "us-east-1"},
		ResourceTypes: []string{"ec2_instance"},
	}
}

// fakeAWS answers STS, Organizations and EC2 for several accounts: each
// account's credentials name it, and each has one instance i-<account>.
type fakeAWS struct {
	t           *testing.T
	accounts    []string
	denyRole    map[string]bool
	instances   []string
	impaired    map[string]bool
	inFlight    atomic.Int32
	maxInFlight atomic.Int32
	listed      atomic.Int32
	mu          sync.Mutex
	seen        []string
	forms       []url.Values
	// loadBalancer, when set, is one ALB whose target group has one healthy
	// target.
	loadBalancer string
}

var credentialAccount = regexp.MustCompile(`Credential=ASIA(\d{12})`)

func newFakeAWS(t *testing.T, accounts []string) *fakeAWS {
	return &fakeAWS{t: t, accounts: accounts, denyRole: map[string]bool{}, impaired: map[string]bool{}}
}

func (f *fakeAWS) actions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.seen...)
}

func (f *fakeAWS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	account := "123456789012"
	if match := credentialAccount.FindStringSubmatch(r.Header.Get("Authorization")); match != nil {
		account = match[1]
	}
	if r.Header.Get("X-Amz-Target") == "AWSOrganizationsV20161128.ListAccounts" {
		f.listed.Add(1)
		accounts := make([]map[string]string, 0, len(f.accounts))
		for _, id := range f.accounts {
			accounts = append(accounts, map[string]string{"Id": id, "Status": "ACTIVE"})
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		_ = json.NewEncoder(w).Encode(map[string]any{"Accounts": accounts})
		return
	}
	body, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(body))
	action := form.Get("Action")
	f.mu.Lock()
	f.seen = append(f.seen, action)
	f.forms = append(f.forms, form)
	f.mu.Unlock()
	w.Header().Set("Content-Type", "text/xml")
	switch action {
	case "AssumeRole":
		role := form.Get("RoleArn")
		target := strings.Split(role, ":")[4]
		if f.denyRole[target] {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `<ErrorResponse><Error><Type>Sender</Type><Code>AccessDenied</Code>
				<Message>not authorized</Message></Error><RequestId>1</RequestId></ErrorResponse>`)
			return
		}
		fmt.Fprintf(w, `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult>
			<Credentials><AccessKeyId>ASIA%s</AccessKeyId><SecretAccessKey>assumed</SecretAccessKey>
			<SessionToken>session</SessionToken><Expiration>2099-01-01T00:00:00Z</Expiration></Credentials>
			<AssumedRoleUser><Arn>%s/obzly-probe</Arn><AssumedRoleId>AROA:obzly-probe</AssumedRoleId></AssumedRoleUser>
			</AssumeRoleResult><ResponseMetadata><RequestId>1</RequestId></ResponseMetadata></AssumeRoleResponse>`,
			target, role)
	case "GetCallerIdentity":
		fmt.Fprintf(w, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
			<GetCallerIdentityResult><Arn>arn:aws:sts::%[1]s:assumed-role/obzly-readonly/obzly-probe</Arn>
			<UserId>AROA:obzly-probe</UserId><Account>%[1]s</Account></GetCallerIdentityResult>
			<ResponseMetadata><RequestId>1</RequestId></ResponseMetadata></GetCallerIdentityResponse>`, account)
	case "DescribeInstances":
		inFlight := f.inFlight.Add(1)
		defer f.inFlight.Add(-1)
		for current := f.maxInFlight.Load(); inFlight > current; current = f.maxInFlight.Load() {
			if f.maxInFlight.CompareAndSwap(current, inFlight) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		instances := f.instances
		if instances == nil {
			instances = []string{"i-" + account}
		}
		if form.Get("Filter.1.Name") == "instance-id" {
			instances = filterValues(instances, form, "Filter.1.Value.")
		}
		var items strings.Builder
		for _, id := range instances {
			fmt.Fprintf(&items, `<item><instanceId>%s</instanceId><instanceType>t3.large</instanceType>
				<instanceState><code>16</code><name>running</name></instanceState></item>`, id)
		}
		fmt.Fprintf(w, `<DescribeInstancesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/">
			<requestId>1</requestId><reservationSet><item><reservationId>r-1</reservationId>
			<instancesSet>%s</instancesSet></item></reservationSet></DescribeInstancesResponse>`, items.String())
	case "DescribeInstanceStatus":
		var items strings.Builder
		instances := f.instances
		if form.Get("InstanceId.1") != "" {
			instances = filterValues(instances, form, "InstanceId.")
		}
		for _, id := range instances {
			status := "ok"
			if f.impaired[id] {
				status = "impaired"
			}
			fmt.Fprintf(&items, `<item><instanceId>%s</instanceId><instanceState><code>16</code><name>running</name>
				</instanceState><systemStatus><status>ok</status></systemStatus>
				<instanceStatus><status>%s</status></instanceStatus></item>`, id, status)
		}
		fmt.Fprintf(w, `<DescribeInstanceStatusResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/">
			<requestId>1</requestId><instanceStatusSet>%s</instanceStatusSet></DescribeInstanceStatusResponse>`,
			items.String())
	case "DescribeLoadBalancers":
		fmt.Fprintf(
			w,
			`<DescribeLoadBalancersResponse xmlns="http://elasticloadbalancing.amazonaws.com/doc/2015-12-01/">
			<DescribeLoadBalancersResult><LoadBalancers><member><LoadBalancerArn>%s</LoadBalancerArn>
			<LoadBalancerName>web</LoadBalancerName><State><Code>active</Code></State><Type>application</Type>
			</member></LoadBalancers></DescribeLoadBalancersResult><ResponseMetadata><RequestId>1</RequestId>
			</ResponseMetadata></DescribeLoadBalancersResponse>`,
			f.loadBalancer,
		)
	case "DescribeTargetGroups":
		fmt.Fprintf(w, `<DescribeTargetGroupsResponse xmlns="http://elasticloadbalancing.amazonaws.com/doc/2015-12-01/">
			<DescribeTargetGroupsResult><TargetGroups><member><TargetGroupArn>tg-web</TargetGroupArn>
			<LoadBalancerArns><member>%s</member></LoadBalancerArns></member></TargetGroups>
			</DescribeTargetGroupsResult><ResponseMetadata><RequestId>1</RequestId></ResponseMetadata>
			</DescribeTargetGroupsResponse>`, f.loadBalancer)
	case "DescribeTargetHealth":
		fmt.Fprint(w, `<DescribeTargetHealthResponse xmlns="http://elasticloadbalancing.amazonaws.com/doc/2015-12-01/">
			<DescribeTargetHealthResult><TargetHealthDescriptions><member><Target><Id>i-0abc</Id></Target>
			<TargetHealth><State>healthy</State></TargetHealth></member></TargetHealthDescriptions>
			</DescribeTargetHealthResult><ResponseMetadata><RequestId>1</RequestId></ResponseMetadata>
			</DescribeTargetHealthResponse>`)
	default:
		f.t.Errorf("unexpected call %q", action)
		w.WriteHeader(http.StatusBadRequest)
	}
}

func (f *fakeAWS) count(action string) int {
	count := 0
	for _, seen := range f.actions() {
		if seen == action {
			count++
		}
	}
	return count
}

func (f *fakeAWS) formsOf(action string) []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	var forms []url.Values
	for i, seen := range f.seen {
		if seen == action {
			forms = append(forms, f.forms[i])
		}
	}
	return forms
}

// filterValues keeps the instances a query's numbered values name.
func filterValues(instances []string, form url.Values, prefix string) []string {
	wanted := map[string]bool{}
	for i := 1; form.Get(fmt.Sprintf("%s%d", prefix, i)) != ""; i++ {
		wanted[form.Get(fmt.Sprintf("%s%d", prefix, i))] = true
	}
	var kept []string
	for _, id := range instances {
		if wanted[id] {
			kept = append(kept, id)
		}
	}
	return kept
}
