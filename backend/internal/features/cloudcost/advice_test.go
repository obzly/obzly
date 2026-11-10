package cloudcost

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"obzly/internal/features/alerting"
	"obzly/internal/synthetics/spec"
	test_utils "obzly/internal/util/testing"
)

const (
	instanceARN = "arn:aws:ec2:eu-west-1:111122223333:instance/i-0a1b2c3d4e5f60001"
	volumeARN   = "arn:aws:ec2:eu-west-1:111122223333:volume/vol-0a1"
	eipARN      = "arn:aws:ec2:eu-west-1:111122223333:elastic-ip/eipalloc-0a1"
)

func Test_Advice_RecommendationsKeepTheirStatus_DoneIsDetected_AndRealizedSavingsAreMeasured(t *testing.T) {
	f := newFixture(t, "Cost advice")
	connection := uuid.New()
	first, second := testNow.AddDate(0, 0, -25), testNow.AddDate(0, 0, -20)
	var rows []spec.CostRow
	for day := testNow.AddDate(0, 0, -45); day.Before(testNow); day = day.AddDate(0, 0, 1) {
		rows = append(rows, resourceRow(day, "vol-0a1", 1_000_000))
		if !day.Before(second.AddDate(0, 0, -14)) {
			amount := int64(10_000_000)
			if !day.Before(utcDay(second)) {
				amount = 5_000_000
			}
			rows = append(rows, resourceRow(day, "i-0a1b2c3d4e5f60001", amount))
		}
	}
	f.send(t, f.apiKey, batch(connection, spec.CostSourceExport, rows[0].Day, rows[len(rows)-1].Day, rows...),
		http.StatusOK)

	f.sendAdvice(t, adviceBatch(connection, first, allOK(), instanceRec(), volumeRec(), eipRec()), http.StatusOK)
	listed := f.recommendations(t)
	require.Len(t, listed.Items, 3)
	assert.Equal(t, instanceARN, listed.Items[0].ResourceKey, "largest savings first")
	volume := item(t, listed, "obzly:ebs-unattached:"+volumeARN)
	require.NotNil(t, volume.MonthlySavings, "worked out from the volume's own cost")
	assert.InDelta(t, 30.44, *volume.MonthlySavings, 0.01)
	assert.Equal(t, "resource_cost", volume.SavingsBasis)
	assert.Equal(t, "list_price", item(t, listed, "obzly:eip:"+eipARN).SavingsBasis)
	require.Len(t, listed.Sections, 4)
	for _, section := range listed.Sections {
		assert.Equal(t, spec.AdviceOK, section.State, section.Section)
		assert.Equal(t, "Contoso-Payer", section.ConnectionName)
	}

	f.update(t, RecommendationUpdate{ProjectID: f.project, Key: "obzly:eip:" + eipARN, Status: statusDismissed},
		http.StatusBadRequest)
	f.update(t, RecommendationUpdate{ProjectID: f.project, Key: "obzly:eip:" + eipARN, Status: statusDismissed,
		Reason: "Kept for a DNS cutover"}, http.StatusOK)
	planned := f.update(t, RecommendationUpdate{ProjectID: f.project, Key: "co:ec2:" + instanceARN,
		Status: statusPlanned, Owner: "pat@example.com", DueDate: "2026-11-30"}, http.StatusOK)
	assert.Equal(t, statusPlanned, planned.Status)
	assert.Equal(t, "2026-11-30", planned.DueDate)
	require.NotNil(t, planned.ExpectedMonthly)
	assert.InDelta(t, 140.16, *planned.ExpectedMonthly, 0.001)

	// The next refresh no longer has the instance: it was resized. The checks couldn't run, so the
	// volume stays as it was, and the dismissed Elastic IP stays dismissed.
	sections := allOK()
	sections[1] = spec.AdviceSection{Section: spec.AdviceChecks, State: spec.AdviceNeedsPermission,
		Reason: "ec2:DescribeVolumes denied in eu-west-1"}
	f.sendAdvice(t, adviceBatch(connection, second, sections), http.StatusOK)
	listed = f.recommendations(t)
	done := item(t, listed, "co:ec2:"+instanceARN)
	assert.Equal(t, statusDone, done.Status)
	assert.Equal(t, doneByDetected, done.DoneBy)
	assert.Nil(t, done.RealizedMonthly, "measured once the 14 days after have cost")
	assert.Equal(t, statusOpen, item(t, listed, "obzly:ebs-unattached:"+volumeARN).Status)
	assert.Equal(t, statusDismissed, item(t, listed, "obzly:eip:"+eipARN).Status)

	require.NoError(t, f.service.realizeSavings(context.Background(), testNow))
	done = item(t, f.recommendations(t), "co:ec2:"+instanceARN)
	require.NotNil(t, done.RealizedMonthly)
	assert.InDelta(t, 152.19, *done.RealizedMonthly, 0.01, "(10 - 5) a day, per month")

	// It comes back: the change didn't stick.
	f.sendAdvice(t, adviceBatch(connection, testNow, allOK(), instanceRec(), volumeRec()), http.StatusOK)
	back := item(t, f.recommendations(t), "co:ec2:"+instanceARN)
	assert.Equal(t, statusOpen, back.Status)
	assert.Nil(t, back.RealizedMonthly)
}

func Test_Advice_CommitmentsAreFlagged_TheyAlert_AndThePlannerCountsRightsizingFirst(t *testing.T) {
	f := newFixture(t, "Cost planner")
	connection := uuid.New()
	ends, later := testNow.AddDate(0, 0, 20), testNow.AddDate(1, 0, 0)
	unused, used := 0.0, 92.5
	advice := adviceBatch(connection, testNow, allOK(), instanceRec())
	advice.Commitments = []spec.Commitment{
		{ID: "sp-1", Type: spec.CommitComputeSP, Description: "Compute Savings Plan, 0.41 an hour", End: &ends,
			Currency: "USD", MonthlyCostMicros: 300_000_000, UtilizationPercent: &unused,
			UnusedMonthlyMicros: 300_000_000},
		{ID: "ri-1", Type: spec.CommitEC2RI, Description: "EC2 reservation: 4 × m5.xlarge", End: &later,
			UtilizationPercent: &used},
	}
	advice.Coverage = []spec.CommitmentCoverage{{Type: spec.CommitComputeSP, Currency: "USD", CoveredPercent: 25}}
	advice.Options = []spec.PurchaseOption{
		option(spec.Term1Year, spec.PayNoUpfront, 500, 0), option(spec.Term1Year, spec.PayAllUpfront, 600, 6000),
		option(spec.Term3Years, spec.PayAllUpfront, 900, 15000),
		{Type: spec.CommitRDSRI, Term: spec.Term3Years, Payment: spec.PayNoUpfront, LookbackDays: 30,
			Reason: "AWS doesn't sell 3-year RDS reservations without upfront"},
	}
	f.sendAdvice(t, advice, http.StatusOK)

	var commitments CommitmentsDTO
	test_utils.MakeGetRequestAndUnmarshal(t, f.router, "/api/v1/cloud-cost/commitments", f.auth, http.StatusOK,
		&commitments)
	require.Len(t, commitments.Commitments, 2)
	plan := commitments.Commitments[0]
	assert.True(t, plan.Unused)
	assert.True(t, plan.EndingSoon)
	assert.Equal(t, 20, *plan.EndsInDays)
	assert.False(t, commitments.Commitments[1].Unused)
	assert.False(t, commitments.Commitments[1].EndingSoon)
	require.Len(t, commitments.Coverage, 1)
	assert.Equal(t, 25.0, commitments.Coverage[0].CoveredPercent)

	samples, err := f.service.EvaluateRule(context.Background(),
		&alerting.AlertRule{ProjectID: f.project, Query: alerting.RuleQuery{CostCommitments: true}}, testNow)
	require.NoError(t, err)
	require.Len(t, samples, 1)
	assert.Equal(t, 20.0, samples[0].Value)
	assert.Equal(t, "Compute Savings Plan, 0.41 an hour", samples[0].Labels["commitment"])

	planner := f.planner(t, "")
	require.Len(t, planner.Plans, 2)
	compute := planner.Plans[0]
	assert.Equal(t, spec.CommitComputeSP, compute.Type)
	assert.InDelta(t, 140.16, compute.RightsizingMonthly, 0.001)
	assert.InDelta(t, 1-140.16/2000, compute.Factor, 0.0001)
	byOption := map[string]PurchaseOptionDTO{}
	for _, item := range compute.Options {
		byOption[item.Term+"|"+item.Payment] = item
	}
	assert.InDelta(t, 464.96, byOption["1y|no_upfront"].MonthlySavings, 0.01,
		"the plan shrinks by what rightsizing saves, so neither counts it twice")
	assert.Equal(t, []string{"lowest_risk"}, byOption["1y|no_upfront"].Labels)
	assert.Equal(t, []string{"most_savings"}, byOption["3y|all_upfront"].Labels)
	assert.False(t, planner.Plans[1].Options[0].Offered)

	unadjusted := f.planner(t, "&rightsizingFirst=false")
	for _, item := range unadjusted.Plans[0].Options {
		if item.Term == spec.Term1Year && item.Payment == spec.PayNoUpfront {
			assert.Equal(t, 500.0, item.MonthlySavings)
		}
	}
	test_utils.MakeGetRequest(t, f.router, "/api/v1/cloud-cost/planner?lookbackDays=14", f.auth,
		http.StatusBadRequest)
}

func Test_Advice_BadBatchesAreRefused_AndOtherUsersCantChangeAProjectsRecommendations(t *testing.T) {
	f := newFixture(t, "Cost advice refused")
	connection := uuid.New()
	bad := adviceBatch(connection, testNow, allOK(), instanceRec())
	bad.Recommendations[0].Kind = "buy-more"
	f.sendAdvice(t, bad, http.StatusBadRequest)
	f.sendAdvice(t, adviceBatch(connection, testNow, allOK(), instanceRec()), http.StatusOK)

	other := newFixture(t, "Someone else's")
	test_utils.MakeRequest(t, f.router, test_utils.RequestOptions{Method: http.MethodPatch,
		URL: "/api/v1/cloud-cost/recommendations", AuthToken: other.auth, ExpectedStatus: http.StatusForbidden,
		Body: RecommendationUpdate{ProjectID: f.project, Key: "co:ec2:" + instanceARN, Status: statusPlanned}})
	var listed RecommendationsDTO
	test_utils.MakeGetRequestAndUnmarshal(t, f.router, "/api/v1/cloud-cost/recommendations?projectId="+
		f.project.String(), other.auth, http.StatusForbidden, &listed)
}

func (f *fixture) sendAdvice(t *testing.T, advice spec.AdviceBatch, status int) {
	t.Helper()
	var body bytes.Buffer
	writer := gzip.NewWriter(&body)
	require.NoError(t, json.NewEncoder(writer).Encode(advice))
	require.NoError(t, writer.Close())
	request := httptest.NewRequest(http.MethodPost, "/v1/cloud/advice", &body)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Encoding", "gzip")
	request.Header.Set("X-API-Key", f.apiKey)
	recorder := httptest.NewRecorder()
	f.router.ServeHTTP(recorder, request)
	require.Equal(t, status, recorder.Code, recorder.Body.String())
}

func (f *fixture) recommendations(t *testing.T) RecommendationsDTO {
	t.Helper()
	var listed RecommendationsDTO
	test_utils.MakeGetRequestAndUnmarshal(t, f.router, "/api/v1/cloud-cost/recommendations?projectId="+
		f.project.String(), f.auth, http.StatusOK, &listed)
	return listed
}

func (f *fixture) update(t *testing.T, update RecommendationUpdate, status int) RecommendationDTO {
	t.Helper()
	response := test_utils.MakeRequest(t, f.router, test_utils.RequestOptions{Method: http.MethodPatch,
		URL: "/api/v1/cloud-cost/recommendations", Body: update, AuthToken: f.auth, ExpectedStatus: status})
	var item RecommendationDTO
	if status == http.StatusOK {
		require.NoError(t, json.Unmarshal(response.Body, &item))
	}
	return item
}

func (f *fixture) planner(t *testing.T, extra string) PlannerDTO {
	t.Helper()
	var planner PlannerDTO
	test_utils.MakeGetRequestAndUnmarshal(t, f.router, "/api/v1/cloud-cost/planner?lookbackDays=30"+extra, f.auth,
		http.StatusOK, &planner)
	return planner
}

func item(t *testing.T, listed RecommendationsDTO, key string) RecommendationDTO {
	t.Helper()
	for _, item := range listed.Items {
		if item.Key == key {
			return item
		}
	}
	require.Failf(t, "no recommendation", "key %s", key)
	return RecommendationDTO{}
}

func resourceRow(day time.Time, resource string, effective int64) spec.CostRow {
	return spec.CostRow{Day: day.Format(dayLayout), SubAccount: "111122223333", Service: "Amazon EC2",
		Region: "eu-west-1", Resource: resource, Currency: "USD", BilledMicros: effective, EffectiveMicros: effective,
		PricingCategory: "Standard"}
}

func adviceBatch(connection uuid.UUID, at time.Time, sections []spec.AdviceSection,
	recs ...spec.CostRecommendation,
) spec.AdviceBatch {
	return spec.AdviceBatch{BatchID: uuid.NewString(), Seq: 1, Total: 1, ConnectionID: connection.String(),
		ConnectionName: "Contoso-Payer", Provider: spec.ProviderAWS, GeneratedAt: at, Sections: sections,
		Recommendations: recs}
}

func allOK() []spec.AdviceSection {
	return []spec.AdviceSection{
		{Section: spec.AdviceRightsizing, State: spec.AdviceOK}, {Section: spec.AdviceChecks, State: spec.AdviceOK},
		{Section: spec.AdviceCommitments, State: spec.AdviceOK}, {Section: spec.AdvicePlanner, State: spec.AdviceOK},
	}
}

func instanceRec() spec.CostRecommendation {
	return spec.CostRecommendation{Key: "co:ec2:" + instanceARN, Source: spec.RecommendFromComputeOptimizer,
		Kind: spec.RecommendRightsizing, Account: "111122223333", Region: "eu-west-1", ResourceKey: instanceARN,
		ResourceName: "web-1", ResourceType: "aws_ec2_instance", Action: "Change m5.2xlarge to m5.xlarge",
		Currency: "USD", MonthlySavingsMicros: 140_160_000, Effort: "low", Risk: "low"}
}

func volumeRec() spec.CostRecommendation {
	return spec.CostRecommendation{Key: "obzly:ebs-unattached:" + volumeARN, Source: spec.RecommendFromObzly,
		Kind: spec.RecommendIdle, Account: "111122223333", Region: "eu-west-1", ResourceKey: volumeARN,
		ResourceType: "aws_ebs_volume", Action: "Delete the unattached EBS volume", SavingsShare: 1}
}

func eipRec() spec.CostRecommendation {
	return spec.CostRecommendation{
		Key:                  "obzly:eip:" + eipARN,
		Source:               spec.RecommendFromObzly,
		Kind:                 spec.RecommendIdle,
		Account:              "111122223333",
		Region:               "eu-west-1",
		ResourceKey:          eipARN,
		ResourceType:         "aws_elastic_ip",
		Action:               "Release the unused Elastic IP",
		Currency:             "USD",
		MonthlySavingsMicros: 3_650_000,
	}
}

func option(term, payment string, savings, upfront int64) spec.PurchaseOption {
	return spec.PurchaseOption{Type: spec.CommitComputeSP, Term: term, Payment: payment, LookbackDays: 30,
		Offered: true, Currency: "USD", Commitment: "2.00 an hour", HourlyCommitmentMicros: 2_000_000,
		MonthlySavingsMicros: savings * 1_000_000, UpfrontMicros: upfront * 1_000_000,
		OnDemandMonthlyMicros: 2_000_000_000}
}
