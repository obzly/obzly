package inventory

import (
	"testing"
	"time"

	metrics_core "obzly/internal/features/metrics/core"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_Observe_CloudResources_JoinAgentAssets_OrStandAlone_WithHealth(t *testing.T) {
	snap := clusterSnapshot()
	hostID := "i-0build01"
	for i, s := range snap.series {
		if s.Labels["host_name"] == "build-01" {
			snap.series[i].Labels = with(s.Labels, "host_id", hostID)
		}
	}
	cloud := func(typ, id, name, state string, extra map[string]string) metrics_core.SeriesLifetimeDTO {
		labels := map[string]string{
			"cloud_provider": "aws", "cloud_account_id": "123456789012", "cloud_region": "ap-south-1",
			"cloud_resource_type": typ, "cloud_resource_id": id, "cloud_resource_name": name,
			"cloud_resource_state": state, "cloud_connection_id": "conn-1",
		}
		for key, value := range extra {
			labels[key] = value
		}
		return series(cloudResourceMetric, testNow.Add(-2*time.Hour), testNow.Add(-5*time.Minute), labels)
	}
	snap.series = append(snap.series,
		cloud("aws_ec2_instance", hostID, "build-01", "running", map[string]string{"host_id": hostID,
			"instance_type": "c7i.xlarge"}),
		func() metrics_core.SeriesLifetimeDTO {
			// Last scanned half an hour ago: between scans, not missing.
			spare := cloud(
				"aws_ec2_instance",
				"i-0idle",
				"batch-spare",
				"stopped",
				map[string]string{"host_id": "i-0idle"},
			)
			spare.LastSeen = testNow.Add(-30 * time.Minute)
			return spare
		}(),
		cloud("aws_eks_cluster", "arn:aws:eks:ap-south-1:123456789012:cluster/prd", "prd", "active",
			map[string]string{"k8s_cluster_name": "prd"}),
		cloud("aws_load_balancer", "arn:lb/web", "web-alb", "active", map[string]string{"lb_type": "application"}),
		cloud("aws_s3_bucket", "arn:aws:s3:::logs", "logs", "available", nil),
	)
	snap.values["cloud_lb_targets"] = []sample{
		{labels: map[string]string{"cloud_resource_id": "arn:lb/web", "health": "healthy"}, value: 1},
		{labels: map[string]string{"cloud_resource_id": "arn:lb/web", "health": "unhealthy"}, value: 2},
	}

	assets := observe(snap)

	host := assets["host:build-01"]
	require.NotNil(t, host)
	assert.ElementsMatch(t, []string{"metrics", "cloud"}, []string(host.Sources), "the agent's host, now also from AWS")
	assert.Equal(t, "c7i.xlarge", host.Attributes["instance_type"])
	assert.Nil(t, assets["host:cloud/"+hostID], "no second asset for the same instance")
	assert.Contains(t, []string(assets["cluster:prd"].Sources), "cloud")

	spare := assets["host:cloud/i-0idle"]
	require.NotNil(t, spare, "an instance with no agent is still inventoried")
	assert.Equal(t, []string{"cloud"}, []string(spare.Sources))
	lb := assets["cloud:aws_load_balancer/arn:lb/web"]
	require.NotNil(t, lb)
	assert.Equal(t, KindLoadBalancer, lb.Kind)
	assert.Equal(t, "Application Load Balancer", lb.Subtype)
	require.NotNil(t, assets["cloud:aws_s3_bucket/arn:aws:s3:::logs"])

	saved, _, _ := reconcile(uuid.New(), nil, assets, nil, testNow, 30*24*time.Hour)
	byKey := indexByKey(saved)
	assert.Equal(t, HealthInactive, byKey["host:cloud/i-0idle"].Health)
	assert.Equal(t, StateNew, byKey["host:cloud/i-0idle"].State, "a cloud-only asset isn't missing between scans")
	assert.Equal(t, HealthWarning, byKey["cloud:aws_load_balancer/arn:lb/web"].Health)
	assert.Contains(t, texts(byKey["cloud:aws_load_balancer/arn:lb/web"]), "2 of 3 targets unhealthy")
	assert.Equal(t, HealthHealthy, byKey["cloud:aws_s3_bucket/arn:aws:s3:::logs"].Health)
}

func Test_Reconcile_CloudOnlyAssets_FollowTheirRefreshInterval_AndCloudHealth(t *testing.T) {
	resource := func(id, refresh string, lastSeen time.Time, extra map[string]string) metrics_core.SeriesLifetimeDTO {
		labels := map[string]string{
			"cloud_provider": "aws", "cloud_account_id": "123456789012", "cloud_region": "us-east-1",
			"cloud_resource_type": "aws_rds_instance", "cloud_resource_id": id, "cloud_resource_name": id,
			"cloud_resource_state": "available", "cloud_connection_id": "conn-1",
		}
		if refresh != "" {
			labels["cloud_refresh_seconds"] = refresh
		}
		for key, value := range extra {
			labels[key] = value
		}
		return series(cloudResourceMetric, testNow.Add(-48*time.Hour), lastSeen, labels)
	}
	snap := &snapshot{now: testNow, values: map[string][]sample{}, series: []metrics_core.SeriesLifetimeDTO{
		// Hourly health scans: seen 2 hours ago is between scans.
		resource("hourly", "3600", testNow.Add(-2*time.Hour), nil),
		// The default 15 minutes: an hour unseen is missing.
		resource("quarterly", "900", testNow.Add(-time.Hour), nil),
		resource("impaired", "900", testNow.Add(-20*time.Minute), map[string]string{"cloud_health": "unhealthy"}),
		resource("recovered", "900", testNow.Add(-40*time.Minute), map[string]string{"cloud_health": "unhealthy"}),
		resource("recovered", "900", testNow.Add(-10*time.Minute), nil),
	}}

	saved, _, _ := reconcile(uuid.New(), nil, observe(snap), nil, testNow, 30*24*time.Hour)
	byKey := indexByKey(saved)
	key := func(id string) string { return "cloud:aws_rds_instance/" + id }
	assert.Equal(t, StateActive, byKey[key("hourly")].State)
	assert.Equal(t, StateNotReporting, byKey[key("quarterly")].State)
	assert.Equal(t, HealthCritical, byKey[key("impaired")].Health)
	assert.Contains(t, texts(byKey[key("impaired")])[0], "unhealthy")
	assert.Equal(t, HealthHealthy, byKey[key("recovered")].Health, "only the latest scan's health counts")
}
