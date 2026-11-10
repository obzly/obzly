package baseline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every check's fixtures: each pass fixture passes (or doesn't apply), each fail one fails.
func Test_Catalog_EveryCheckPassesAndFailsItsFixtures(t *testing.T) {
	checks, err := Checks()
	require.NoError(t, err)
	require.NotEmpty(t, ForProvider("aws"))
	require.NotEmpty(t, ForProvider("azure"))
	for _, check := range checks {
		t.Run(check.ID, func(t *testing.T) {
			require.NotEmpty(t, check.Fixtures.Pass, "a check needs a passing fixture")
			require.NotEmpty(t, check.Fixtures.Fail, "a check needs a failing fixture")
			assert.NotEmpty(t, check.Remediation.Console)
			for _, values := range check.Fixtures.Pass {
				applies, passed, err := check.evaluate(values)
				require.NoError(t, err, "%v", values)
				assert.True(t, !applies || passed, "should pass: %v", values)
			}
			for _, values := range check.Fixtures.Fail {
				applies, passed, err := check.evaluate(values)
				require.NoError(t, err, "%v", values)
				assert.True(t, applies && !passed, "should fail: %v", values)
			}
		})
	}
}

func Test_Evaluate_MissingFieldsFail_AndChecksOnlyReadTheirResourceType(t *testing.T) {
	results := Evaluate("aws", []Fact{
		{Type: "aws_rds_instance", Key: "arn:aws:rds:eu-west-1:111122223333:db:orders", Region: "eu-west-1",
			Values: map[string]any{"publicly_accessible": false}},
		{Type: "aws_unknown", Key: "x", Values: map[string]any{}},
	})
	byCheck := map[string]Result{}
	for _, result := range results {
		byCheck[result.Check.ID] = result
	}
	require.Len(t, results, 2, "the two RDS checks, nothing for an unknown type")
	assert.True(t, byCheck["aws-rds-not-public"].Passed)
	encrypted := byCheck["aws-rds-encrypted"]
	assert.False(t, encrypted.Passed, "a field the probe couldn't read is never a pass")
	assert.Contains(t, encrypted.Check.EvidenceOf(encrypted.Fact.Values)["evaluation_error"], "storage_encrypted")
	assert.Equal(t, "S3.8", Lookup("aws-s3-block-public-access").Control)
	assert.Nil(t, Lookup("gone"))
}
