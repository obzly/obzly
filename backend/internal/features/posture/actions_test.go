package posture

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"obzly/internal/synthetics/spec"
	test_utils "obzly/internal/util/testing"
)

const (
	instanceB = "arn:aws:ec2:eu-west-1:111122223333:instance/i-0a1b2c3d4e5f60002"
	database  = "arn:aws:rds:eu-west-1:111122223333:db:orders"
	regional  = "arn:aws:ec2:eu-west-1:111122223333:ebs-default-encryption"
)

func Test_PostureActions_GroupFindings_RankCoverageGapsFirst_AndOfferGuardrails(t *testing.T) {
	f := newFixture(t, "Posture actions")
	f.send(t, f.batch(testNow.Add(-time.Hour), actionRuns(),
		finding("S3.8", bucketA, spec.SeverityHigh), finding("S3.9", bucketA, spec.SeverityMedium),
		finding("EC2.8", instance, spec.SeverityHigh), finding("EC2.8", instanceB, spec.SeverityHigh),
		finding("EC2.7", regional, spec.SeverityMedium), finding("RDS.2", database, spec.SeverityCritical)))

	actions := f.actions(t)
	kinds := make([]string, 0, len(actions.Actions))
	for _, action := range actions.Actions {
		kinds = append(kinds, action.Kind)
	}
	require.GreaterOrEqual(t, len(kinds), 3)
	gap := actions.Actions[0]
	assert.Equal(t, ActionCoverage, gap.Kind, "a scope nothing read comes first")
	assert.Contains(t, gap.Title, "Add the posture permissions")
	assert.Equal(t, []string{"111122223333/ap-south-1"}, gap.Scopes)
	assert.Equal(t, ActionCoverage, actions.Actions[1].Kind)
	assert.Equal(t, "Turn on Security Hub", actions.Actions[1].Title, "a fallback to the baseline is a smaller gap")

	bucket := action(t, actions, "fix:resource:"+f.connection.String()+":"+bucketA)
	assert.Equal(t, "Fix 2 findings on contoso-assets", bucket.Title)
	assert.Len(t, bucket.Findings, 2)
	assert.Equal(t, spec.SeverityHigh, bucket.Severity)
	assert.Equal(t, "low", bucket.Effort)
	instances := action(t, actions, "fix:control:"+f.connection.String()+":111122223333:EC2.8")
	assert.Equal(t, "EC2.8 fails (2 resources)", instances.Title)
	rds := action(t, actions, "fix:finding:"+f.connection.String()+":RDS.2:"+database)
	assert.Equal(t, "medium", rds.Effort)

	imds := action(t, actions, "guardrail:aws-imdsv2-default:"+f.connection.String()+":111122223333:eu-west-1")
	assert.NotNil(t, imds.Findings, "an empty list, not null: the UI counts it")
	assert.Empty(t, imds.Findings, "a default for new instances fixes none of today's")
	assert.Contains(t, imds.Steps[1], "--http-tokens required")
	ebs := action(t, actions, "guardrail:aws-ebs-encryption-default:"+f.connection.String()+":111122223333:eu-west-1")
	require.Len(t, ebs.Findings, 1, "turning it on closes its own control")
	assert.Equal(t, "EC2.7", ebs.Findings[0].Control)
	for _, item := range actions.Actions {
		assert.NotEqual(t, "fix:finding:"+f.connection.String()+":EC2.7:"+regional, item.Key,
			"the guardrail is the fix: the finding isn't offered twice")
	}

	fsbp := simulated(actions, "aws-fsbp")
	assert.InDelta(t, 37.5, fsbp.Current, 0.1, "IAM.4, CloudTrail.1 and EC2.13 pass; five of eight fail")
	assert.Equal(t, fsbp.Current, fsbp.WithPlan, "nothing planned yet")
	assert.Equal(t, 100.0, fsbp.WithAll)

	f.patchAction(t, ActionUpdate{ProjectID: f.project, Key: bucket.Key, Dismissed: ptr(true)}, http.StatusBadRequest)
	planned := f.patchAction(t, ActionUpdate{ProjectID: f.project, Key: bucket.Key, Planned: ptr(true)}, http.StatusOK)
	assert.True(t, planned.Planned)
	fsbp = simulated(f.actions(t), "aws-fsbp")
	assert.InDelta(t, 62.5, fsbp.WithPlan, 0.1, "S3.8 and S3.9 pass with the plan done")
}

func Test_PostureActions_PlannedActionsAreDoneWhenTheirFindingsResolve_OrAnAcceptedRiskWhenSuppressed(t *testing.T) {
	f := newFixture(t, "Posture plan")
	f.send(t, f.batch(testNow.Add(-2*time.Hour), actionRuns(),
		finding("S3.8", bucketA, spec.SeverityHigh), finding("S3.9", bucketA, spec.SeverityMedium),
		finding("EC2.8", instance, spec.SeverityHigh), finding("EC2.8", instanceB, spec.SeverityHigh)))
	bucketKey := "fix:resource:" + f.connection.String() + ":" + bucketA
	instancesKey := "fix:control:" + f.connection.String() + ":111122223333:EC2.8"
	f.patchAction(t, ActionUpdate{ProjectID: f.project, Key: bucketKey, Planned: ptr(true)}, http.StatusOK)
	f.patchAction(t, ActionUpdate{ProjectID: f.project, Key: instancesKey, Planned: ptr(true)}, http.StatusOK)

	// The bucket is fixed; the instances' findings are suppressed instead.
	f.send(t, f.batch(testNow.Add(-time.Hour), actionRuns(),
		finding("EC2.8", instance, spec.SeverityHigh), finding("EC2.8", instanceB, spec.SeverityHigh)))
	for _, id := range []string{f.findOn(t, "EC2.8", instance), f.findOn(t, "EC2.8", instanceB)} {
		f.patch(t, "/api/v1/posture/findings/"+id, f.auth,
			FindingUpdate{Suppressed: true, Reason: "Legacy AMI, replaced in Q1"}, http.StatusOK)
	}

	actions := f.actions(t)
	assert.Equal(t, ActionDone, action(t, actions, bucketKey).Status)
	assert.Equal(t, ActionAcceptedRisk, action(t, actions, instancesKey).Status, "suppressed isn't fixed")
	fsbp := simulated(actions, "aws-fsbp")
	assert.Equal(t, fsbp.Current, fsbp.WithPlan, "nothing open is left in the plan")

	other := newFixture(t, "Posture someone else")
	test_utils.MakeRequest(t, f.router, test_utils.RequestOptions{Method: http.MethodPatch,
		URL: "/api/v1/posture/actions", AuthToken: other.auth, ExpectedStatus: http.StatusForbidden,
		Body: ActionUpdate{ProjectID: f.project, Key: bucketKey, Planned: ptr(false)}})
}

func (f *fixture) actions(t *testing.T) ActionsDTO {
	t.Helper()
	var actions ActionsDTO
	test_utils.MakeGetRequestAndUnmarshal(t, f.router, "/api/v1/posture/actions?projectId="+f.project.String(),
		f.auth, http.StatusOK, &actions)
	return actions
}

func (f *fixture) patchAction(t *testing.T, update ActionUpdate, status int) Action {
	t.Helper()
	response := f.patch(t, "/api/v1/posture/actions", f.auth, update, status)
	var updated Action
	if status == http.StatusOK {
		require.NoError(t, json.Unmarshal(response.Body, &updated))
	}
	return updated
}

func (f *fixture) findOn(t *testing.T, control, resource string) string {
	t.Helper()
	var listed FindingsResponse
	test_utils.MakePostRequestAndUnmarshal(t, f.router, "/api/v1/posture/findings", f.auth,
		FindingsRequest{Filters: Filters{Status: "all", Control: []string{control}, Resource: []string{resource}}},
		http.StatusOK, &listed)
	require.Len(t, listed.Findings, 1)
	return listed.Findings[0].ID.String()
}

func actionRuns() []spec.PostureRun {
	return []spec.PostureRun{
		nativeRun("eu-west-1", []string{"IAM.4", "CloudTrail.1"}, []string{"S3.8", "S3.9", "EC2.8", "EC2.7", "RDS.2"}),
		{Account: "111122223333", Region: "us-east-1", Standard: "aws-fsbp", Source: spec.PostureSourceNative,
			Result: spec.PostureRunNotEnabled, Reason: "Security Hub not enabled"},
		{Account: "111122223333", Region: "us-east-1", Standard: "aws-fsbp", Source: spec.PostureSourceBaseline,
			Result: spec.PostureRunOK, Passed: []string{"EC2.13"}},
		{Account: "111122223333", Region: "ap-south-1", Standard: "aws-fsbp", Source: spec.PostureSourceNative,
			Result: spec.PostureRunNeedsPermission, Reason: "securityhub:GetFindings denied"},
	}
}

func action(t *testing.T, actions ActionsDTO, key string) Action {
	t.Helper()
	for _, item := range actions.Actions {
		if item.Key == key {
			return item
		}
	}
	require.Failf(t, "no action", "key %s", key)
	return Action{}
}

func simulated(actions ActionsDTO, standard string) SimulatedScore {
	for _, score := range actions.Standards {
		if score.Standard == standard {
			return score
		}
	}
	return SimulatedScore{}
}

func ptr[T any](value T) *T { return &value }
