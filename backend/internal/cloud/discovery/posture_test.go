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
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"obzly/internal/posture/baseline"
	"obzly/internal/synthetics/spec"
)

var signedRegion = regexp.MustCompile(`/\d{8}/([a-z0-9-]+)/securityhub/`)

func Test_CollectPosture_AWSNativeOnly_ReadsSecurityHub_AndReportsRegionsWithoutIt(t *testing.T) {
	isolateAWS(t)
	server := httptest.NewServer(fakeSecurityHub(t))
	defer server.Close()
	connection := spec.CloudConnection{
		ID:         "conn-aws",
		Name:       "Contoso-Prod",
		Provider:   spec.ProviderAWS,
		Scope:      "account",
		ScopeID:    "123456789012",
		Access:     spec.AccessRole,
		Identity:   "arn:aws:iam::123456789012:role/obzly-readonly",
		ExternalID: "obz-0123456789abcdef",
		Uses:       []string{spec.UsePosture},
		Regions:    []string{"eu-west-1", "us-east-1"},
		Posture:    &spec.PostureSettings{Source: spec.PostureSourceNative},
	}

	result := CollectPosture(context.Background(), connection, Options{AWSEndpoint: server.URL}, costNow)

	require.Equal(t, spec.UseOK, result.Scan.Uses[spec.UsePosture].State, result.Scan.Uses[spec.UsePosture].Message)
	batch := result.Batch
	require.Len(t, batch.Findings, 1)
	finding := batch.Findings[0]
	assert.Equal(t, "S3.8", finding.Control)
	assert.Equal(t, spec.SeverityHigh, finding.Severity)
	assert.Equal(t, "arn:aws:s3:::contoso-assets", finding.ResourceKey)
	assert.ElementsMatch(t, []spec.StandardRef{{Standard: "aws-fsbp", Control: "S3.8"},
		{Standard: "cis-aws", Control: "2.1.4"}}, finding.Standards)

	runs := map[string]spec.PostureRun{}
	for _, run := range batch.Runs {
		runs[run.Region+"|"+run.Standard] = run
	}
	fsbp := runs["eu-west-1|aws-fsbp"]
	assert.Equal(t, spec.PostureRunOK, fsbp.Result)
	assert.Equal(t, []string{"IAM.9"}, fsbp.Passed)
	assert.Equal(t, []string{"S3.8"}, fsbp.Failed)
	assert.Equal(t, []string{"2.1.4"}, runs["eu-west-1|cis-aws"].Failed)
	off := runs["us-east-1|aws-fsbp"]
	assert.Equal(t, spec.PostureRunNotEnabled, off.Result)
	assert.Equal(t, "Security Hub not enabled", off.Reason)
	assert.Equal(t, []string{"123456789012/eu-west-1", "123456789012/global"}, result.Scan.Posture.Native)
	assert.Equal(t, []string{"123456789012/us-east-1"}, result.Scan.Posture.NotCovered)
}

func Test_CollectPosture_AzureAuto_UsesDefenderWhereItHasAssessments_AndTheBaselineElsewhere(t *testing.T) {
	imds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"arm-token"}`))
	}))
	defer imds.Close()
	management := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query string `json:"query"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		switch {
		case strings.HasPrefix(body.Query, "resourcecontainers"):
			fmt.Fprint(w, `{"data":[{"subscriptionId":"sub-defender","name":"Fabrikam-Prod"},
				{"subscriptionId":"sub-plain","name":"Fabrikam-Dev"}]}`)
		case strings.HasPrefix(body.Query, "securityresources"):
			fmt.Fprint(w, `{"data":[
				{"subscriptionId":"sub-defender","assessment":"a1","code":"Unhealthy","title":"Storage accounts should restrict network access",
				 "resourceId":"/subscriptions/sub-defender/resourcegroups/web/providers/microsoft.storage/storageaccounts/fabrikamweb",
				 "severity":"high","portal":"portal.azure.com/#blade/a1"},
				{"subscriptionId":"sub-defender","assessment":"a2","code":"Healthy","title":"MFA for owners","resourceId":"/subscriptions/sub-defender"}]}`)
		case strings.HasPrefix(body.Query, "resources"):
			fmt.Fprint(w, `{"data":[
				{"id":"/subscriptions/sub-plain/resourcegroups/dev/providers/microsoft.storage/storageaccounts/fabrikamdev",
				 "name":"fabrikamdev","type":"microsoft.storage/storageaccounts","location":"westeurope","subscriptionId":"sub-plain",
				 "resourceGroup":"dev","properties":{"supportsHttpsTrafficOnly":true,"minimumTlsVersion":"TLS1_2"}},
				{"id":"/subscriptions/sub-plain/resourcegroups/dev/providers/microsoft.network/networksecuritygroups/jump",
				 "name":"jump","type":"microsoft.network/networksecuritygroups","location":"westeurope","subscriptionId":"sub-plain",
				 "resourceGroup":"dev","properties":{"securityRules":[{"properties":{"direction":"Inbound","access":"Allow",
				 "protocol":"Tcp","sourceAddressPrefix":"Internet","destinationPortRanges":["20-23","443"]}}]}}]}`)
		default:
			t.Errorf("unexpected query %q", body.Query)
		}
	}))
	defer management.Close()
	connection := spec.CloudConnection{
		ID:       "conn-az",
		Name:     "Fabrikam",
		Provider: spec.ProviderAzure,
		Scope:    "management_group",
		ScopeID:  "mg-fabrikam",
		Access:   spec.AccessProbe,
		TenantID: "11111111-1111-1111-1111-111111111111",
		Uses:     []string{spec.UsePosture},
	}

	result := CollectPosture(context.Background(), connection,
		Options{AzureIMDS: imds.URL, AzureManagement: management.URL}, costNow)

	require.Equal(t, spec.UseOK, result.Scan.Uses[spec.UsePosture].State, result.Scan.Errors)
	assert.Equal(t, []string{"sub-defender/global"}, result.Scan.Posture.Native)
	assert.Equal(t, []string{"sub-plain/global"}, result.Scan.Posture.Baseline)
	controls := map[string]spec.PostureFinding{}
	for _, finding := range result.Batch.Findings {
		controls[finding.Account+"|"+finding.Control] = finding
	}
	native := controls["sub-defender|a1"]
	assert.Equal(t, spec.SeverityHigh, native.Severity)
	assert.Equal(t, "https://portal.azure.com/#blade/a1", native.RemediationURL)
	assert.Equal(t, "web", native.Evidence["resourceGroup"])
	blob := controls["sub-plain|azure-storage-no-public-blob"]
	assert.Equal(t, "true", blob.Evidence["allow_blob_public_access"], "unset means allowed, as Azure treats it")
	assert.Equal(t, "dev", blob.Evidence["resourceGroup"])
	assert.Contains(t, controls, "sub-plain|azure-nsg-no-world-ssh", "20-23 from the internet covers 22")
	assert.NotContains(t, controls, "sub-plain|azure-storage-https-only")
	var fallback, cis spec.PostureRun
	for _, run := range result.Batch.Runs {
		switch {
		case run.Account == "sub-plain" && run.Source == spec.PostureSourceNative:
			fallback = run
		case run.Account == "sub-plain" && run.Standard == "cis-azure":
			cis = run
		}
	}
	assert.Equal(t, spec.PostureRunNotEnabled, fallback.Result, "kept so Coverage can say why it fell back")
	assert.Equal(t, spec.PostureRunOK, cis.Result)
	assert.Contains(t, cis.Passed, "3.1")
	assert.Contains(t, cis.Failed, "6.2")
}

func Test_WorldPorts_FindAdminPortsOpenToTheInternet(t *testing.T) {
	ports, all := securityGroupWorldPorts([]ec2types.IpPermission{
		{IpProtocol: aws.String("tcp"), FromPort: aws.Int32(0), ToPort: aws.Int32(1024),
			IpRanges: []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}}},
		{IpProtocol: aws.String("tcp"), FromPort: aws.Int32(3389), ToPort: aws.Int32(3389),
			IpRanges: []ec2types.IpRange{{CidrIp: aws.String("10.0.0.0/8")}}},
	})
	assert.Equal(t, []int{22}, ports)
	assert.False(t, all)
	_, all = securityGroupWorldPorts([]ec2types.IpPermission{{IpProtocol: aws.String("-1"),
		Ipv6Ranges: []ec2types.Ipv6Range{{CidrIpv6: aws.String("::/0")}}}})
	assert.True(t, all)

	ports, all = nsgWorldPorts(map[string]any{"securityRules": []any{
		map[string]any{"properties": map[string]any{"direction": "Inbound", "access": "Allow", "protocol": "*",
			"sourceAddressPrefix": "*", "destinationPortRange": "3389"}},
		map[string]any{"properties": map[string]any{"direction": "Inbound", "access": "Deny", "protocol": "*",
			"sourceAddressPrefix": "*", "destinationPortRange": "*"}},
	}})
	assert.Equal(t, []int{3389}, ports)
	assert.False(t, all, "a deny rule opens nothing")
}

func Test_BaselineScope_FailedReadsResolveNothing_AndAControlFailedAnywhereIsntPassed(t *testing.T) {
	facts := []baseline.Fact{
		{Type: "aws_rds_instance", Key: "db-a", Values: map[string]any{"publicly_accessible": false,
			"storage_encrypted": true}},
		{Type: "aws_rds_instance", Key: "db-b", Values: map[string]any{"publicly_accessible": true,
			"storage_encrypted": true}},
	}
	runs, findings := baselineScope(spec.ProviderAWS, "111122223333", "eu-west-1", []string{"aws_rds_instance"},
		facts, &postureProblem{result: spec.PostureRunNeedsPermission, reason: "ec2:DescribeInstances denied"})
	require.Len(t, findings, 1)
	assert.Equal(t, "db-b", findings[0].ResourceKey)
	for _, run := range runs {
		assert.Equal(t, spec.PostureRunNeedsPermission, run.Result)
		if run.Standard == "aws-fsbp" {
			assert.Equal(t, []string{"RDS.3"}, run.Passed)
			assert.Equal(t, []string{"RDS.2"}, run.Failed)
		}
	}
	assert.Equal(t, []string{"EC2.8", "EC2.13", "IAM.4", "IAM.15"}, sortControls([]string{"IAM.15", "EC2.13", "EC2.8",
		"IAM.4"}))
}

// fakeSecurityHub: STS signs in; eu-west-1 has Security Hub with FSBP and CIS, us-east-1 doesn't.
func fakeSecurityHub(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/" {
			body, _ := io.ReadAll(r.Body)
			form, _ := url.ParseQuery(string(body))
			if form.Get("Action") == "AssumeRole" {
				w.Header().Set("Content-Type", "text/xml")
				fmt.Fprint(w, `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult>
					<Credentials><AccessKeyId>ASIATEST</AccessKeyId><SecretAccessKey>assumed</SecretAccessKey>
					<SessionToken>session</SessionToken><Expiration>2099-01-01T00:00:00Z</Expiration></Credentials>
					<AssumedRoleUser><Arn>arn:aws:sts::123456789012:assumed-role/obzly-readonly/obzly-probe</Arn>
					<AssumedRoleId>AROA:obzly-probe</AssumedRoleId></AssumedRoleUser></AssumeRoleResult>
					<ResponseMetadata><RequestId>1</RequestId></ResponseMetadata></AssumeRoleResponse>`)
				return
			}
			if form.Get("Action") == "GetCallerIdentity" {
				w.Header().Set("Content-Type", "text/xml")
				fmt.Fprint(w, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
					<GetCallerIdentityResult><Arn>arn:aws:sts::123456789012:assumed-role/obzly-readonly/obzly-probe</Arn>
					<UserId>AROA:obzly-probe</UserId><Account>123456789012</Account></GetCallerIdentityResult>
					<ResponseMetadata><RequestId>1</RequestId></ResponseMetadata></GetCallerIdentityResponse>`)
				return
			}
			t.Errorf("unexpected STS call %q", form.Get("Action"))
			return
		}
		region := ""
		if match := signedRegion.FindStringSubmatch(r.Header.Get("Authorization")); match != nil {
			region = match[1]
		}
		w.Header().Set("Content-Type", "application/json")
		if region != "eu-west-1" {
			w.Header().Set("X-Amzn-ErrorType", "InvalidAccessException")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"Code":"InvalidAccessException","Message":"Account is not subscribed to AWS Security Hub"}`)
			return
		}
		switch r.URL.Path {
		case "/accounts":
			fmt.Fprint(w, `{"HubArn":"arn:aws:securityhub:eu-west-1:123456789012:hub/default"}`)
		case "/standards/get":
			fmt.Fprint(w, `{"StandardsSubscriptions":[
				{"StandardsArn":"arn:aws:securityhub:eu-west-1::standards/aws-foundational-security-best-practices/v/1.0.0","StandardsStatus":"READY"},
				{"StandardsArn":"arn:aws:securityhub:::standards/cis-aws-foundations-benchmark/v/3.0.0","StandardsStatus":"READY"},
				{"StandardsArn":"arn:aws:securityhub:eu-west-1::standards/pci-dss/v/3.2.1","StandardsStatus":"READY"}]}`)
		case "/findings":
			fmt.Fprint(w, `{"Findings":[
				{"AwsAccountId":"123456789012","Title":"S3 general purpose buckets should block public access",
				 "Severity":{"Label":"HIGH"},"Resources":[{"Id":"arn:aws:s3:::contoso-assets","Type":"AwsS3Bucket"}],
				 "Compliance":{"Status":"FAILED","SecurityControlId":"S3.8",
				  "AssociatedStandards":[{"StandardsId":"standards/aws-foundational-security-best-practices/v/1.0.0"},
				   {"StandardsId":"standards/cis-aws-foundations-benchmark/v/3.0.0"}],
				  "RelatedRequirements":["CIS AWS Foundations Benchmark v3.0.0/2.1.4"]},
				 "Remediation":{"Recommendation":{"Url":"https://docs.aws.amazon.com/console/securityhub/S3.8/remediation"}}},
				{"AwsAccountId":"123456789012","Title":"MFA should be enabled for the root user","Severity":{"Label":"CRITICAL"},
				 "Resources":[{"Id":"AWS::::Account:123456789012","Type":"AwsAccount"}],
				 "Compliance":{"Status":"PASSED","SecurityControlId":"IAM.9",
				  "AssociatedStandards":[{"StandardsId":"standards/aws-foundational-security-best-practices/v/1.0.0"}]}}]}`)
		default:
			t.Errorf("unexpected Security Hub call %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}
