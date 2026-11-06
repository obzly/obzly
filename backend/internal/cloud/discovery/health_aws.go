package discovery

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"

	"obzly/internal/synthetics/spec"
)

// awsHealthTypes are the types a health scan re-reads, with the call that
// lists them; buckets and functions keep what discovery found.
var awsHealthTypes = map[string]string{
	"aws_ec2_instance":      "ec2:DescribeInstances",
	"aws_load_balancer":     "elasticloadbalancing:DescribeLoadBalancers",
	"aws_eks_cluster":       "eks:ListClusters",
	"aws_rds_instance":      "rds:DescribeDBInstances",
	"aws_autoscaling_group": "autoscaling:DescribeAutoScalingGroups",
}

// healthAWS re-lists, per account and region with known resources, only
// the types known there, and keeps the known resources it finds.
func healthAWS(
	ctx context.Context,
	connection spec.CloudConnection,
	known []Resource,
	options Options,
	t *tracker,
) []Resource {
	var out []Resource
	byAccount := map[string]map[string][]Resource{}
	accounts := map[string]bool{}
	for _, resource := range known {
		if _, ok := awsHealthTypes[resource.Type]; !ok || resource.Region == "" {
			out = append(out, resource)
			continue
		}
		if byAccount[resource.AccountID] == nil {
			byAccount[resource.AccountID] = map[string][]Resource{}
		}
		byAccount[resource.AccountID][resource.Region] = append(byAccount[resource.AccountID][resource.Region],
			resource)
		accounts[resource.AccountID] = true
	}
	var mu sync.Mutex
	add := func(resources ...Resource) {
		mu.Lock()
		out = append(out, resources...)
		mu.Unlock()
	}
	// Target health is slow to list (a call per target group), so it's read
	// on every few health scans, or on each while a load balancer is
	// unhealthy.
	skipTargets := !options.AWS.targetsDue() && !anyUnhealthyLoadBalancer(known)
	scanned := map[string]bool{}
	eachAWSAccount(ctx, connection, options, t, accounts, func(ctx context.Context, account awsAccount) {
		mu.Lock()
		scanned[account.id] = true
		mu.Unlock()
		regions := make([]string, 0, len(byAccount[account.id]))
		for region := range byAccount[account.id] {
			regions = append(regions, region)
		}
		inParallel(regions, maxRegionsInParallel, func(region string) {
			add(awsRegionHealth(ctx, connection, account, region, byAccount[account.id][region], t, skipTargets)...)
		})
	})
	// Accounts it couldn't sign in to keep what discovery found.
	for id, regions := range byAccount {
		if !scanned[id] {
			for _, resources := range regions {
				out = append(out, resources...)
			}
		}
	}
	return out
}

func awsRegionHealth(
	ctx context.Context,
	connection spec.CloudConnection,
	account awsAccount,
	region string,
	known []Resource,
	t *tracker,
	skipTargets bool,
) []Resource {
	types := map[string]bool{}
	filter := awsFilter{skipTargets: skipTargets}
	for _, resource := range known {
		types[strings.TrimPrefix(resource.Type, "aws_")] = true
		if resource.Type == "aws_ec2_instance" {
			filter.instanceIDs = append(filter.instanceIDs, resource.ID)
		}
	}
	limited := connection
	limited.ResourceTypes = make([]string, 0, len(types))
	for kind := range types {
		limited.ResourceTypes = append(limited.ResourceTypes, kind)
	}
	sub := newTracker()
	current := map[string]Resource{}
	var instances []string
	for _, resource := range awsRegion(ctx, limited, account, region, sub, filter) {
		current[resource.ID] = resource
		if resource.Type == "aws_ec2_instance" {
			instances = append(instances, resource.ID)
		}
	}
	var statuses map[string]string
	if len(instances) > 0 {
		statuses = awsInstanceHealth(ctx, awsRegionConfig(account, region), instances, sub)
	}
	t.merge(sub)

	out := make([]Resource, 0, len(known))
	for _, resource := range known {
		if sub.failedAny(awsHealthTypes[resource.Type]) {
			out = append(out, resource)
			continue
		}
		found, ok := current[resource.ID]
		if !ok {
			// Gone: no longer sent, so the Site sees it stop reporting.
			continue
		}
		found.Health = resource.Health
		if found.Type == "aws_ec2_instance" && statuses != nil {
			found.Health = statuses[found.ID]
		}
		if found.Type == "aws_load_balancer" && skipTargets {
			found.Targets = resource.Targets
		}
		out = append(out, found)
	}
	return out
}

// awsInstanceHealth is the health of ids from their system and instance
// status checks; nil when it couldn't be read. Up to maxFilteredInstances
// are asked for by ID; more, or an ID AWS no longer knows, lists the
// region instead.
func awsInstanceHealth(ctx context.Context, cfg aws.Config, ids []string, t *tracker) map[string]string {
	client := ec2.NewFromConfig(cfg)
	health := map[string]string{}
	if len(ids) <= maxFilteredInstances {
		byID := true
		for batch := range slices.Chunk(ids, instanceStatusBatch) {
			output, err := client.DescribeInstanceStatus(ctx, &ec2.DescribeInstanceStatusInput{
				IncludeAllInstances: aws.Bool(true), InstanceIds: batch,
			})
			if isUnknownInstance(err) {
				byID = false
				break
			}
			if !t.call("ec2:DescribeInstanceStatus", err, isAWSDenied(err)) {
				return nil
			}
			for _, status := range output.InstanceStatuses {
				health[aws.ToString(status.InstanceId)] = instanceHealth(status)
			}
		}
		if byID {
			return health
		}
	}
	paginator := ec2.NewDescribeInstanceStatusPaginator(client, &ec2.DescribeInstanceStatusInput{
		IncludeAllInstances: aws.Bool(true), MaxResults: aws.Int32(1000),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if !t.call("ec2:DescribeInstanceStatus", err, isAWSDenied(err)) {
			return nil
		}
		for _, status := range page.InstanceStatuses {
			health[aws.ToString(status.InstanceId)] = instanceHealth(status)
		}
	}
	return health
}

// anyUnhealthyLoadBalancer: a known load balancer has targets that aren't
// healthy (or on their way in or out).
func anyUnhealthyLoadBalancer(known []Resource) bool {
	for _, resource := range known {
		if resource.Type != "aws_load_balancer" {
			continue
		}
		for state, count := range resource.Targets {
			if count > 0 && !slices.Contains([]string{"healthy", "initial", "draining", "unused"}, state) {
				return true
			}
		}
	}
	return false
}

func isUnknownInstance(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && strings.HasPrefix(apiErr.ErrorCode(), "InvalidInstanceID.")
}

func instanceHealth(status ec2types.InstanceStatus) string {
	system, instance := ec2types.SummaryStatus(""), ec2types.SummaryStatus("")
	if status.SystemStatus != nil {
		system = status.SystemStatus.Status
	}
	if status.InstanceStatus != nil {
		instance = status.InstanceStatus.Status
	}
	switch {
	case system == ec2types.SummaryStatusImpaired || instance == ec2types.SummaryStatusImpaired:
		return HealthUnhealthy
	case system == ec2types.SummaryStatusOk && instance == ec2types.SummaryStatusOk:
		return HealthHealthy
	case (system == "" || system == ec2types.SummaryStatusNotApplicable) &&
		(instance == "" || instance == ec2types.SummaryStatusNotApplicable):
		// Stopped instances have no status checks.
		return ""
	default:
		return HealthUnknown
	}
}
