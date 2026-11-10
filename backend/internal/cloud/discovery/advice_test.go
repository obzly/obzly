package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/smithy-go/encoding/cbor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"obzly/internal/synthetics/spec"
)

func Test_CollectAdvice_AWS_ReadsOptimizerChecksCommitmentsAndPurchaseOptions(t *testing.T) {
	isolateAWS(t)
	fake := &fakeAdviceAWS{t: t, calls: map[string]int{}, enrolled: true}
	server := httptest.NewServer(fake)
	defer server.Close()

	result := CollectAdvice(context.Background(), awsAdviceConnection(spec.CostScopeBilling),
		Options{AWSEndpoint: server.URL}, costNow)

	sections := map[string]spec.AdviceSection{}
	for _, section := range result.Batch.Sections {
		sections[section.Section] = section
	}
	for _, name := range []string{spec.AdviceRightsizing, spec.AdviceChecks, spec.AdviceCommitments, spec.AdvicePlanner} {
		assert.Equal(t, spec.AdviceOK, sections[name].State, name+": "+sections[name].Reason)
	}

	byResource := map[string]spec.CostRecommendation{}
	for _, rec := range result.Batch.Recommendations {
		byResource[rec.ResourceKey+"|"+rec.Kind] = rec
	}
	require.Len(t, result.Batch.Recommendations, 4, "Compute Optimizer's wins over the same Obzly check")
	instance := byResource["arn:aws:ec2:eu-west-1:123456789012:instance/i-0a1b2c3d4e5f60001|rightsizing"]
	assert.Equal(t, "Change m5.2xlarge to m5.xlarge", instance.Action)
	assert.Equal(t, int64(140_160_000), instance.MonthlySavingsMicros)
	assert.Equal(t, "low", instance.Effort)
	assert.Equal(t, "22.5", instance.Evidence["cpu_maximum"])
	unattached := byResource["arn:aws:ec2:eu-west-1:123456789012:volume/vol-0a1|idle"]
	assert.Equal(t, spec.RecommendFromComputeOptimizer, unattached.Source)
	gp2 := byResource["arn:aws:ec2:eu-west-1:123456789012:volume/vol-0b2|modernize"]
	assert.Equal(t, spec.RecommendFromComputeOptimizer, gp2.Source)
	eip := byResource["arn:aws:ec2:eu-west-1:123456789012:elastic-ip/eipalloc-0a1|idle"]
	assert.Equal(t, spec.RecommendFromObzly, eip.Source)
	assert.Equal(t, int64(3_650_000), eip.MonthlySavingsMicros)

	require.Len(t, result.Batch.Commitments, 2)
	plan := result.Batch.Commitments[0]
	assert.Equal(t, spec.CommitComputeSP, plan.Type)
	require.NotNil(t, plan.UtilizationPercent)
	assert.Zero(t, *plan.UtilizationPercent)
	assert.Equal(t, spec.Term1Year, plan.Term)
	reservation := result.Batch.Commitments[1]
	assert.Equal(t, spec.CommitEC2RI, reservation.Type)
	assert.Equal(t, "EC2 reservation: 4 × m5.xlarge", reservation.Description)
	coverage := map[string]float64{}
	for _, row := range result.Batch.Coverage {
		coverage[row.Type] = row.CoveredPercent
	}
	assert.Equal(t, map[string]float64{spec.CommitEC2RI: 40, spec.CommitComputeSP: 25}, coverage,
		"no RDS data is no RDS coverage row, not 0%")

	require.Len(t, result.Batch.Options, 72, "2 plans and 2 reservation types × 2 terms × 3 payments × 3 look-backs")
	options := map[string]spec.PurchaseOption{}
	for _, option := range result.Batch.Options {
		options[optionKey(option)] = option
	}
	rds := options["rds_ri|3y|no_upfront|030"]
	assert.False(t, rds.Offered)
	assert.Contains(t, rds.Reason, "3-year RDS")
	compute := options["compute_sp|1y|all_upfront|030"]
	assert.True(t, compute.Offered)
	assert.Equal(t, int64(2_000_000), compute.HourlyCommitmentMicros)
	assert.Equal(t, int64(17_520_000_000), compute.UpfrontMicros)
	assert.Equal(t, int64(500_000_000), compute.MonthlySavingsMicros)
	assert.Equal(t, int64(0), compute.MonthlyRecurringMicros, "all upfront leaves nothing monthly")
	assert.InDelta(t, 8.94, compute.BreakEvenMonths, 0.01)
	assert.Equal(t, "No purchase recommended for this usage", options["ec2_instance_sp|1y|no_upfront|030"].Reason)
	ec2 := options["ec2_ri|1y|no_upfront|007"]
	assert.Equal(t, "6 × m5.large in eu-west-1", ec2.Commitment)
	assert.InDelta(t, 28.57, ec2.SavingsPercent, 0.01)

	assert.Equal(t, fake.ceCalls(), result.Requests, "every Cost Explorer request is counted (each is charged)")
	assert.Equal(t, "PAYER", fake.scope)
}

func Test_CollectAdvice_AWS_NotOptedIn_AndCostExplorerDenied(t *testing.T) {
	isolateAWS(t)
	fake := &fakeAdviceAWS{t: t, calls: map[string]int{}, denyCE: true}
	server := httptest.NewServer(fake)
	defer server.Close()

	result := CollectAdvice(context.Background(), awsAdviceConnection(spec.CostScopeAccount),
		Options{AWSEndpoint: server.URL}, costNow)

	sections := map[string]spec.AdviceSection{}
	for _, section := range result.Batch.Sections {
		sections[section.Section] = section
	}
	assert.Equal(t, spec.AdviceNotEnabled, sections[spec.AdviceRightsizing].State)
	assert.Contains(t, sections[spec.AdviceRightsizing].Reason, "opted in")
	assert.Equal(t, spec.AdviceOK, sections[spec.AdviceChecks].State)
	assert.Equal(t, spec.AdviceNeedsPermission, sections[spec.AdviceCommitments].State)
	assert.Equal(t, spec.AdviceNeedsPermission, sections[spec.AdvicePlanner].State)
	assert.Contains(t, sections[spec.AdvicePlanner].Reason, "ce:GetSavingsPlansPurchaseRecommendation")
	assert.Equal(t, 2, fake.ceCalls(), "a denial stops its section: each call would cost money")
	assert.Zero(t, fake.calls["GetEC2InstanceRecommendations"])
	assert.Equal(t, "LINKED", fake.scope)
}

func Test_CollectAdvice_Azure_ReadsAdvisorPurchaseRecommendationsAndIdleResources(t *testing.T) {
	imds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"arm-token"}`))
	}))
	defer imds.Close()
	var mu sync.Mutex
	filters := map[string]int{}
	management := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		filter := r.URL.Query().Get("$filter")
		switch {
		case strings.HasSuffix(r.URL.Path, "/Microsoft.Advisor/recommendations"):
			assert.Equal(t, "Category eq 'Cost'", filter)
			fmt.Fprint(w, `{"value":[
				{"id":"/subscriptions/sub-1/providers/Microsoft.Advisor/recommendations/r1","properties":{
				 "impactedField":"Microsoft.Compute/virtualMachines","impactedValue":"build-01","impact":"High",
				 "shortDescription":{"problem":"Right-size or shutdown underutilized virtual machines","solution":"Right-size or shutdown underutilized virtual machines"},
				 "resourceMetadata":{"resourceId":"/subscriptions/SUB-1/resourceGroups/CI/providers/Microsoft.Compute/virtualMachines/build-01"},
				 "extendedProperties":{"currentSku":"Standard_D8s_v5","targetSku":"Standard_D4s_v5","annualSavingsAmount":"1200","savingsCurrency":"EUR","regionId":"westeurope"}}},
				{"id":"/subscriptions/sub-1/providers/Microsoft.Advisor/recommendations/r2","properties":{
				 "impactedField":"Microsoft.Subscriptions/subscriptions","impactedValue":"sub-1",
				 "shortDescription":{"problem":"Buy virtual machine reserved instances to save money over pay-as-you-go costs","solution":"Buy reserved instances"},
				 "resourceMetadata":{"resourceId":"/subscriptions/sub-1"},
				 "extendedProperties":{"term":"P1Y","savingsAmount":"300","savingsCurrency":"EUR"}}}]}`)
		case strings.HasSuffix(r.URL.Path, "/Microsoft.Consumption/reservationRecommendations"):
			filters["reservations"]++
			if strings.Contains(filter, "Last30Days") && strings.Contains(filter, "P1Y") {
				fmt.Fprint(w, `{"value":[{"kind":"modern","location":"westeurope","sku":"Standard_D4s_v5","properties":{
					"recommendedQuantity":2,"term":"P1Y","costWithNoReservedInstances":{"currency":"EUR","value":300},
					"totalCostWithReservedInstances":{"currency":"EUR","value":180},"netSavings":{"currency":"EUR","value":120}}}]}`)
				return
			}
			fmt.Fprint(w, `{"value":[]}`)
		case strings.HasSuffix(r.URL.Path, "/Microsoft.CostManagement/benefitRecommendations"):
			filters["benefits"]++
			fmt.Fprint(
				w,
				`{"value":[{"properties":{"currencyCode":"EUR","costWithoutBenefit":600,"commitmentGranularity":"Hourly",
				"recommendationDetails":{"commitmentAmount":0.5,"savingsAmount":60,"savingsPercentage":10,"coveragePercentage":80,"averageUtilizationPercentage":95}}}]}`,
			)
		case strings.HasSuffix(r.URL.Path, "/Microsoft.Capacity/reservations"):
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"error":{"code":"AuthorizationFailed","message":"no access"}}`)
		case strings.HasSuffix(r.URL.Path, "/Microsoft.ResourceGraph/resources"):
			fmt.Fprint(w, `{"data":[
				{"id":"/subscriptions/sub-1/resourcegroups/ci/providers/microsoft.compute/disks/old-data","name":"old-data",
				 "type":"microsoft.compute/disks","location":"westeurope","subscriptionId":"sub-1","sku":"Premium_LRS","size":"256"},
				{"id":"/subscriptions/sub-1/resourcegroups/ci/providers/microsoft.network/publicipaddresses/spare","name":"spare",
				 "type":"microsoft.network/publicipaddresses","location":"westeurope","subscriptionId":"sub-1","sku":"Standard"}]}`)
		default:
			t.Errorf("unexpected call %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer management.Close()
	connection := spec.CloudConnection{ID: "conn-az", Name: "Fabrikam", Provider: spec.ProviderAzure,
		Scope: "subscription", ScopeID: "sub-1", Access: spec.AccessProbe,
		TenantID: "11111111-1111-1111-1111-111111111111", Uses: []string{spec.UseCost, spec.UseInventory},
		Cost: &spec.CostSettings{Source: spec.CostSourceAPI, Scope: spec.CostScopeAccount}}

	result := CollectAdvice(context.Background(), connection,
		Options{AzureIMDS: imds.URL, AzureManagement: management.URL}, costNow)

	sections := map[string]spec.AdviceSection{}
	for _, section := range result.Batch.Sections {
		sections[section.Section] = section
	}
	assert.Equal(t, spec.AdviceOK, sections[spec.AdviceRightsizing].State, sections[spec.AdviceRightsizing].Reason)
	assert.Equal(t, spec.AdviceOK, sections[spec.AdviceChecks].State, sections[spec.AdviceChecks].Reason)
	assert.Equal(t, spec.AdviceOK, sections[spec.AdvicePlanner].State, sections[spec.AdvicePlanner].Reason)
	assert.Equal(t, spec.AdviceNeedsPermission, sections[spec.AdviceCommitments].State)
	assert.Contains(t, sections[spec.AdviceCommitments].Reason, "Reservations Reader")

	require.Len(t, result.Batch.Recommendations, 3, "the reservation advice is left to the planner")
	byKind := map[string][]spec.CostRecommendation{}
	for _, rec := range result.Batch.Recommendations {
		byKind[rec.Kind] = append(byKind[rec.Kind], rec)
	}
	vm := byKind[spec.RecommendRightsizing][0]
	assert.Equal(t, "/subscriptions/sub-1/resourcegroups/ci/providers/microsoft.compute/virtualmachines/build-01",
		vm.ResourceKey)
	assert.Equal(t, "Change Standard_D8s_v5 to Standard_D4s_v5", vm.Action)
	assert.Equal(t, int64(100_000_000), vm.MonthlySavingsMicros, "annual savings / 12")
	assert.Equal(t, "EUR", vm.Currency)
	assert.Len(t, byKind[spec.RecommendIdle], 2)
	for _, rec := range byKind[spec.RecommendIdle] {
		assert.Equal(t, 1.0, rec.SavingsShare, "the Site works the amount out from the resource's cost")
	}

	options := map[string]spec.PurchaseOption{}
	for _, option := range result.Batch.Options {
		options[optionKey(option)] = option
	}
	require.Len(t, result.Batch.Options, 24, "2 types × 2 terms × 3 look-backs, monthly and upfront")
	monthly := options["azure_reservation|1y|monthly|030"]
	assert.Equal(t, "2 × Standard_D4s_v5 in westeurope", monthly.Commitment)
	assert.Equal(t, int64(121_750_000), monthly.MonthlySavingsMicros, "120 over 30 days, per month")
	upfront := options["azure_reservation|1y|upfront|030"]
	assert.Equal(t, monthly.MonthlySavingsMicros, upfront.MonthlySavingsMicros, "Azure charges the same either way")
	assert.Equal(t, "No purchase recommended for this usage", options["azure_reservation|3y|monthly|007"].Reason)
	plan := options["azure_sp|3y|monthly|060"]
	assert.Equal(t, "0.5000 an hour", plan.Commitment)
	assert.Equal(t, int64(30_437_500), plan.MonthlySavingsMicros, "60 over 60 days, per month")
	assert.Equal(t, 6, filters["reservations"])
	assert.Equal(t, 6, filters["benefits"])
}

func Test_CollectAdvice_AzureWithoutReader_DoesntRunIdleChecks(t *testing.T) {
	_, ok := advisorRecommendation(azureAdvisorItem{}, "sub-1")
	assert.False(t, ok, "a recommendation without a resource is skipped")
	a := &azureAdvice{connection: spec.CloudConnection{Uses: []string{spec.UseCost}},
		c: &adviceCollector{sections: map[string]spec.AdviceSection{}}}
	a.checks(context.Background())
	assert.Equal(t, spec.AdviceNotEnabled, a.c.sections[spec.AdviceChecks].State,
		"Resource Graph without Reader answers nothing, which would look like nothing idle")
}

func awsAdviceConnection(scope string) spec.CloudConnection {
	connection := awsCostConnection(scope, spec.CostSourceAPI, nil)
	connection.Regions = []string{"eu-west-1"}
	return connection
}

// fakeAdviceAWS answers STS and EC2 (query protocol), Compute Optimizer (JSON 1.0) and Cost
// Explorer (JSON 1.1).
type fakeAdviceAWS struct {
	t        *testing.T
	mu       sync.Mutex
	calls    map[string]int
	enrolled bool
	denyCE   bool
	scope    string
}

func (f *fakeAdviceAWS) count(operation string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[operation]++
}

func (f *fakeAdviceAWS) ceCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for operation, n := range f.calls {
		if strings.HasPrefix(operation, "ce:") {
			total += n
		}
	}
	return total
}

func (f *fakeAdviceAWS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	target := r.Header.Get("X-Amz-Target")
	switch {
	case strings.HasPrefix(target, "AWSInsightsIndexService."):
		f.costExplorer(w, r, strings.TrimPrefix(target, "AWSInsightsIndexService."))
		return
	case strings.Contains(r.URL.Path, "/service/ComputeOptimizerService/operation/"):
		f.optimizer(w, r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:])
		return
	}
	body, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(body))
	action := form.Get("Action")
	f.count(action)
	w.Header().Set("Content-Type", "text/xml")
	switch action {
	case "AssumeRole":
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
	case "DescribeVolumes":
		fmt.Fprint(
			w,
			`<DescribeVolumesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>1</requestId>
			<volumeSet>
			<item><volumeId>vol-0a1</volumeId><size>100</size><status>available</status><volumeType>gp2</volumeType></item>
			<item><volumeId>vol-0b2</volumeId><size>50</size><status>in-use</status><volumeType>gp2</volumeType></item>
			<item><volumeId>vol-0c3</volumeId><size>20</size><status>in-use</status><volumeType>gp3</volumeType></item>
			</volumeSet></DescribeVolumesResponse>`,
		)
	case "DescribeAddresses":
		fmt.Fprint(
			w,
			`<DescribeAddressesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>1</requestId>
			<addressesSet>
			<item><publicIp>198.51.100.7</publicIp><allocationId>eipalloc-0a1</allocationId><domain>vpc</domain></item>
			<item><publicIp>198.51.100.8</publicIp><allocationId>eipalloc-0b2</allocationId><associationId>eipassoc-1</associationId><domain>vpc</domain></item>
			</addressesSet></DescribeAddressesResponse>`,
		)
	default:
		f.t.Errorf("unexpected call %q", action)
		w.WriteHeader(http.StatusBadRequest)
	}
}

// optimizer answers in Smithy RPC v2 CBOR, the protocol the Compute Optimizer SDK speaks.
func (f *fakeAdviceAWS) optimizer(w http.ResponseWriter, operation string) {
	f.count(operation)
	w.Header().Set("Content-Type", "application/cbor")
	w.Header().Set("Smithy-Protocol", "rpc-v2-cbor")
	w = cborWriter{w}
	switch operation {
	case "GetEnrollmentStatus":
		status := "Inactive"
		if f.enrolled {
			status = "Active"
		}
		fmt.Fprintf(w, `{"status":%q}`, status)
	case "GetEC2InstanceRecommendations":
		fmt.Fprint(
			w,
			`{"instanceRecommendations":[{"instanceArn":"arn:aws:ec2:eu-west-1:123456789012:instance/i-0a1b2c3d4e5f60001",
			"accountId":"123456789012","instanceName":"web-1","currentInstanceType":"m5.2xlarge","finding":"Overprovisioned",
			"findingReasonCodes":["CPUOverprovisioned"],"lookBackPeriodInDays":14,
			"utilizationMetrics":[{"name":"CPU","statistic":"MAXIMUM","value":22.5}],
			"recommendationOptions":[
			 {"instanceType":"m5.large","rank":2,"performanceRisk":3,"migrationEffort":"Low",
			  "savingsOpportunity":{"savingsOpportunityPercentage":75,"estimatedMonthlySavings":{"currency":"USD","value":210.24}}},
			 {"instanceType":"m5.xlarge","rank":1,"performanceRisk":1,"migrationEffort":"VeryLow",
			  "savingsOpportunity":{"savingsOpportunityPercentage":50,"estimatedMonthlySavings":{"currency":"USD","value":140.16}}}]}]}`,
		)
	case "GetEBSVolumeRecommendations":
		fmt.Fprint(w, `{"volumeRecommendations":[{"volumeArn":"arn:aws:ec2:eu-west-1:123456789012:volume/vol-0b2",
			"accountId":"123456789012","finding":"NotOptimized","lookBackPeriodInDays":14,
			"currentConfiguration":{"volumeType":"gp2","volumeSize":50},
			"volumeRecommendationOptions":[{"rank":1,"performanceRisk":0,"configuration":{"volumeType":"gp3","volumeSize":50},
			 "savingsOpportunity":{"estimatedMonthlySavings":{"currency":"USD","value":1}}}]}]}`)
	case "GetIdleRecommendations":
		fmt.Fprint(w, `{"idleRecommendations":[{"resourceArn":"arn:aws:ec2:eu-west-1:123456789012:volume/vol-0a1",
			"resourceId":"vol-0a1","resourceType":"EBSVolume","accountId":"123456789012","finding":"Unattached",
			"lookBackPeriodInDays":14,"savingsOpportunity":{"estimatedMonthlySavings":{"currency":"USD","value":10}}}]}`)
	default:
		f.t.Errorf("unexpected Compute Optimizer call %q", operation)
		w.WriteHeader(http.StatusBadRequest)
	}
}

// cborWriter turns the JSON a handler writes into CBOR.
type cborWriter struct{ http.ResponseWriter }

func (c cborWriter) Write(data []byte) (int, error) {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return 0, err
	}
	if _, err := c.ResponseWriter.Write(cbor.Encode(toCBOR(value))); err != nil {
		return 0, err
	}
	return len(data), nil
}

func toCBOR(value any) cbor.Value {
	switch typed := value.(type) {
	case map[string]any:
		out := cbor.Map{}
		for key, item := range typed {
			out[key] = toCBOR(item)
		}
		return out
	case []any:
		out := cbor.List{}
		for _, item := range typed {
			out = append(out, toCBOR(item))
		}
		return out
	case string:
		return cbor.String(typed)
	case bool:
		return cbor.Bool(typed)
	case float64:
		if typed == float64(uint64(typed)) {
			return cbor.Uint(typed)
		}
		return cbor.Float64(typed)
	}
	return &cbor.Nil{}
}

func (f *fakeAdviceAWS) costExplorer(w http.ResponseWriter, r *http.Request, operation string) {
	var input map[string]any
	require.NoError(f.t, json.NewDecoder(r.Body).Decode(&input))
	f.count("ce:" + operation)
	if scope, ok := input["AccountScope"].(string); ok {
		f.mu.Lock()
		f.scope = scope
		f.mu.Unlock()
	}
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	if f.denyCE {
		w.Header().Set("x-amzn-ErrorType", "AccessDeniedException")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"__type":"AccessDeniedException","message":"not authorized to perform ce:`+operation+`"}`)
		return
	}
	ends := costNow.AddDate(0, 0, 20).Format(time.RFC3339)
	switch operation {
	case "GetSavingsPlansUtilizationDetails":
		fmt.Fprintf(
			w,
			`{"SavingsPlansUtilizationDetails":[{"SavingsPlanArn":"arn:aws:savingsplans::123456789012:savingsplan/sp-1",
			"Attributes":{"SavingsPlansType":"ComputeSavingsPlans","PurchaseTerm":"1 year","PaymentOption":"No Upfront",
			"EndDateTime":%q,"RecurringHourlyFee":"0.41"},
			"Utilization":{"UtilizationPercentage":"0","UnusedCommitment":"300"},
			"AmortizedCommitment":{"TotalAmortizedCommitment":"300"}}],
			"TimePeriod":{"Start":"2026-10-10","End":"2026-11-09"}}`,
			ends,
		)
	case "GetReservationUtilization":
		service := input["Filter"].(map[string]any)["Dimensions"].(map[string]any)["Values"].([]any)[0].(string)
		if service == ceServiceRDS {
			w.Header().Set("x-amzn-ErrorType", "DataUnavailableException")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"__type":"DataUnavailableException","message":"no data"}`)
			return
		}
		fmt.Fprint(w, `{"UtilizationsByTime":[{"Groups":[{"Key":"SUBSCRIPTION_ID","Value":"ri-1",
			"Attributes":{"instanceType":"m5.xlarge","numberOfInstances":"4","region":"eu-west-1","endDateTime":"2027-06-01T00:00:00.000Z"},
			"Utilization":{"UtilizationPercentage":"92.5","TotalAmortizedFee":"400","RICostForUnusedHours":"30"}}]}]}`)
	case "GetReservationCoverage":
		fmt.Fprint(w, `{"CoveragesByTime":[],"Total":{"CoverageHours":{"CoverageHoursPercentage":"40"},
			"CoverageCost":{"OnDemandCost":"600"}}}`)
	case "GetSavingsPlansCoverage":
		fmt.Fprint(w, `{"SavingsPlansCoverages":[{"Coverage":{"SpendCoveredBySavingsPlans":"25","TotalCost":"100",
			"OnDemandCost":"75"}}]}`)
	case "GetSavingsPlansPurchaseRecommendation":
		if input["SavingsPlansType"] != "COMPUTE_SP" {
			fmt.Fprint(w, `{"SavingsPlansPurchaseRecommendation":{}}`)
			return
		}
		upfront := "0"
		switch input["PaymentOption"] {
		case "ALL_UPFRONT":
			upfront = "17520"
		case "PARTIAL_UPFRONT":
			upfront = "8760"
		}
		fmt.Fprintf(w, `{"SavingsPlansPurchaseRecommendation":{"SavingsPlansPurchaseRecommendationSummary":{
			"CurrencyCode":"USD","HourlyCommitmentToPurchase":"2.0","EstimatedMonthlySavingsAmount":"500",
			"EstimatedSavingsPercentage":"25","CurrentOnDemandSpend":"2000"},
			"SavingsPlansPurchaseRecommendationDetails":[{"AccountId":"123456789012","UpfrontCost":%q,
			"HourlyCommitmentToPurchase":"2.0","EstimatedMonthlySavingsAmount":"500","EstimatedAverageUtilization":"97"}]}}`,
			upfront)
	case "GetReservationPurchaseRecommendation":
		fmt.Fprint(w, `{"Recommendations":[{"RecommendationDetails":[{"AccountId":"123456789012",
			"InstanceDetails":{"EC2InstanceDetails":{"InstanceType":"m5.large","Region":"eu-west-1"}},
			"RecommendedNumberOfInstancesToPurchase":"6","UpfrontCost":"0","RecurringStandardMonthlyCost":"300",
			"EstimatedMonthlySavingsAmount":"120","EstimatedMonthlyOnDemandCost":"420","CurrencyCode":"USD"}]}]}`)
	default:
		f.t.Errorf("unexpected Cost Explorer call %q", operation)
		w.WriteHeader(http.StatusBadRequest)
	}
}
