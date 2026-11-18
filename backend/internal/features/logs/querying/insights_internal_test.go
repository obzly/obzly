package logs_querying

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func Test_WorkloadOfPod_StripsGeneratedNameParts(t *testing.T) {
	cases := map[string]string{
		"payments-api-5c8b6d7f9-m4tqz": "payments-api", // Deployment
		"fluent-bit-x7k2p":             "fluent-bit",   // DaemonSet
		"postgres-0":                   "postgres",     // StatefulSet
		"search-indexer":               "search-indexer",
		"standalone":                   "standalone",
	}
	for pod, workload := range cases {
		assert.Equal(t, workload, WorkloadOfPod(pod), pod)
	}
}

func Test_ReplicaSetOfPod_OnlyForDeploymentPods(t *testing.T) {
	assert.Equal(t, "payments-api-5c8b6d7f9", ReplicaSetOfPod("payments-api-5c8b6d7f9-m4tqz"))
	assert.Equal(t, "", ReplicaSetOfPod("fluent-bit-x7k2p"))
	assert.Equal(t, "", ReplicaSetOfPod("postgres-0"))
}

func Test_TrendBucket_KeepsAtMostSixtyBuckets(t *testing.T) {
	assert.Equal(t, time.Minute, trendBucket(0, time.Hour))
	assert.Equal(t, 5*time.Minute, trendBucket(0, 3*time.Hour))
	assert.Equal(t, 3*time.Hour, trendBucket(0, 7*24*time.Hour))
	assert.Equal(t, 2*time.Minute, trendBucket(90_000, time.Hour), "rounded up to whole minutes")
	assert.Equal(t, time.Minute, trendBucket(1000, 90*time.Second+time.Minute-time.Second))
	assert.Equal(t, 30*time.Minute, trendBucket(60_000, 24*time.Hour), "too many buckets: the auto choice")
}
