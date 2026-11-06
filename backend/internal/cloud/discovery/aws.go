package discovery

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/ratelimit"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"

	"obzly/internal/synthetics/spec"
)

const (
	awsSessionName = "obzly-probe"
	// awsDefaultRegion answers the global calls (STS, Organizations, S3).
	awsDefaultRegion = "us-east-1"
	maxOrgAccounts   = 200
	maxTargetGroups  = 200
	awsMaxAttempts   = 8
	awsMaxBackoff    = 30 * time.Second
	// maxAWSResources bounds one connection's inventory, as for Azure.
	maxAWSResources = 50000
	// A health scan reads only its known instances: by filter up to
	// maxFilteredInstances (in batches), else the whole region is cheaper.
	maxFilteredInstances = 1000
	instanceFilterBatch  = 200
	// DescribeInstanceStatus takes at most 100 instance IDs a call.
	instanceStatusBatch = 100
	// targetHealthEvery: health scans read load balancers' target health
	// every this many scans (discovery reads it too), unless one is
	// unhealthy.
	targetHealthEvery = 3
	// credentialsExpiryWindow refreshes kept credentials before they expire.
	credentialsExpiryWindow = time.Minute
	tokenFetchTimeout       = 30 * time.Second
)

// AWSCache keeps one connection's AWS sign-ins between its scans:
// discovery signs in afresh, health scans reuse the accounts (whose
// credentials refresh themselves), so a health scan of an Organization
// doesn't call AssumeRole and GetCallerIdentity for every account again.
type AWSCache struct {
	mu          sync.Mutex
	base        *aws.Config
	accounts    map[string]awsAccount
	healthScans int
}

// awsFilter narrows a region listing for a health scan: the known
// instances only (nil: all), and whether to skip target health.
type awsFilter struct {
	instanceIDs []string
	skipTargets bool
}

type awsKey struct{}

func NewAWSCache() *AWSCache { return &AWSCache{accounts: map[string]awsAccount{}} }

// awsDenied are the error codes AWS answers a missing permission with.
var awsDenied = map[string]bool{
	"AccessDenied": true, "AccessDeniedException": true, "UnauthorizedOperation": true,
	"UnauthorizedAccess": true, "AuthorizationError": true, "AuthorizationErrorException": true,
}

type awsAccount struct {
	id     string
	config aws.Config
}

func discoverAWS(ctx context.Context, connection spec.CloudConnection, options Options, t *tracker) []Resource {
	var mu sync.Mutex
	var resources []Resource
	eachAWSAccount(ctx, connection, options, t, nil, func(ctx context.Context, account awsAccount) {
		regions := connection.Regions
		if len(regions) == 0 {
			regions = awsEnabledRegions(ctx, account, t)
		}
		if options.Sample {
			regions = regions[:min(len(regions), sampleRegions)]
		}
		var found []Resource
		if wants(connection, "s3_bucket") {
			found = awsBuckets(ctx, account, t)
		}
		var regionMu sync.Mutex
		inParallel(regions, maxRegionsInParallel, func(region string) {
			inRegion := awsRegion(ctx, connection, account, region, t, awsFilter{})
			regionMu.Lock()
			found = append(found, inRegion...)
			regionMu.Unlock()
		})
		mu.Lock()
		resources = appendCapped(resources, found, maxAWSResources, t)
		mu.Unlock()
	})
	return resources
}

// appendCapped adds found to resources up to limit in all, recording that
// it stopped there.
func appendCapped(resources, found []Resource, limit int, t *tracker) []Resource {
	if room := limit - len(resources); len(found) > room {
		found = found[:max(room, 0)]
		t.call("listing", fmt.Errorf("stopped at %d resources", limit), false)
	}
	return append(resources, found...)
}

// eachAWSAccount signs in to the account, or to each active account of the
// Organization, and runs scan for each, several at once. Each account's
// calls are paced apart: AWS throttles per account. A health scan (only:
// the accounts discovery found) reuses options.AWS's sign-ins and doesn't
// list the Organization again.
func eachAWSAccount(
	ctx context.Context,
	connection spec.CloudConnection,
	options Options,
	t *tracker,
	only map[string]bool,
	scan func(ctx context.Context, account awsAccount),
) {
	ctx = context.WithValue(ctx, awsKey{}, true)
	cache, reuse := options.AWS, options.AWS != nil && only != nil
	base, err := cache.baseConfig(ctx, connection, options, reuse)
	if err != nil {
		t.fail(fmt.Errorf("couldn't set up AWS: %w", err))
		return
	}
	signIn := func(ctx context.Context, role string) (awsAccount, error) {
		if account, ok := cache.account(role, reuse); ok {
			return account, nil
		}
		account, err := awsSignIn(ctx, connection, base, role, options)
		if err == nil {
			cache.keep(role, account)
		}
		return account, err
	}
	first, err := signIn(ctx, connection.Identity)
	if err == nil && connection.Scope == "organization" && !options.Sample {
		var members []string
		if only != nil {
			members = slices.Sorted(maps.Keys(only))
		} else {
			members, err = awsMemberAccounts(ctx, first)
		}
		if err == nil {
			inParallel(members, maxAccountsInParallel, func(id string) {
				if only != nil && !only[id] {
					return
				}
				account := first
				if id != first.id {
					var signInErr error
					account, signInErr = signIn(withScope(ctx, id), memberRoleARN(connection.Identity, id))
					// One account without the role doesn't stop the rest.
					if !t.call("sts:AssumeRole into account "+id, signInErr, false) {
						return
					}
				}
				scan(withScope(ctx, account.id), account)
			})
			return
		}
	}
	if err != nil {
		if isAWSDenied(err) {
			t.failPermission(fmt.Errorf("couldn't sign in: %w", err), signInAction(connection))
		} else {
			t.fail(fmt.Errorf("couldn't sign in: %w", err))
		}
		return
	}
	scan(withScope(ctx, first.id), first)
}

func awsBaseConfig(ctx context.Context, connection spec.CloudConnection, options Options) (aws.Config, error) {
	region := awsDefaultRegion
	if len(connection.Regions) > 0 {
		region = connection.Regions[0]
	}
	loaders := []func(*config.LoadOptions) error{config.WithRegion(region), config.WithRetryer(awsRetryer)}
	if options.HTTP != nil {
		loaders = append(loaders, config.WithHTTPClient(options.HTTP))
	}
	if options.AWSEndpoint != "" {
		loaders = append(loaders, config.WithBaseEndpoint(options.AWSEndpoint))
	}
	if connection.Access == spec.AccessFederated {
		// No identity of its own: only the web identity exchange signs in.
		loaders = append(loaders, config.WithCredentialsProvider(aws.AnonymousCredentials{}))
	}
	return config.LoadDefaultConfig(ctx, loaders...)
}

// awsMemberAccounts lists the Organization's active accounts (the role has
// the same name in each, from the StackSet).
func awsMemberAccounts(ctx context.Context, first awsAccount) ([]string, error) {
	var accounts []string
	paginator := organizations.NewListAccountsPaginator(organizations.NewFromConfig(first.config),
		&organizations.ListAccountsInput{})
	for paginator.HasMorePages() && len(accounts) < maxOrgAccounts {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing the Organization's accounts: %w", err)
		}
		for _, member := range page.Accounts {
			if member.Status == orgtypes.AccountStatusActive && aws.ToString(member.Id) != "" &&
				len(accounts) < maxOrgAccounts {
				accounts = append(accounts, aws.ToString(member.Id))
			}
		}
	}
	return accounts, nil
}

func awsSignIn(
	ctx context.Context,
	connection spec.CloudConnection,
	base aws.Config,
	role string,
	options Options,
) (awsAccount, error) {
	cfg := base.Copy()
	window := func(o *aws.CredentialsCacheOptions) { o.ExpiryWindow = credentialsExpiryWindow }
	switch connection.Access {
	case spec.AccessRole:
		cfg.Credentials = aws.NewCredentialsCache(stscreds.NewAssumeRoleProvider(sts.NewFromConfig(base), role,
			func(o *stscreds.AssumeRoleOptions) {
				o.ExternalID, o.RoleSessionName = aws.String(connection.ExternalID), awsSessionName
			}), window)
	case spec.AccessFederated:
		if options.Token == nil {
			return awsAccount{}, errors.New("no control plane token source")
		}
		cfg.Credentials = aws.NewCredentialsCache(stscreds.NewWebIdentityRoleProvider(sts.NewFromConfig(base), role,
			tokenRetriever{source: options.Token}, func(o *stscreds.WebIdentityRoleOptions) {
				o.RoleSessionName = awsSessionName
			}), window)
	}
	identity, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return awsAccount{}, err
	}
	return awsAccount{id: aws.ToString(identity.Account), config: cfg}, nil
}

func awsEnabledRegions(ctx context.Context, account awsAccount, t *tracker) []string {
	output, err := ec2.NewFromConfig(account.config).DescribeRegions(ctx, &ec2.DescribeRegionsInput{})
	if !t.call("ec2:DescribeRegions", err, isAWSDenied(err)) {
		return []string{account.config.Region}
	}
	regions := make([]string, 0, len(output.Regions))
	for _, region := range output.Regions {
		regions = append(regions, aws.ToString(region.RegionName))
	}
	return regions
}

func awsRegion(
	ctx context.Context,
	connection spec.CloudConnection,
	account awsAccount,
	region string,
	t *tracker,
	filter awsFilter,
) []Resource {
	cfg := awsRegionConfig(account, region)
	resource := func(kind, id, name, state string, attributes map[string]string) Resource {
		return Resource{
			Provider: spec.ProviderAWS, AccountID: account.id, Region: region, Type: "aws_" + kind, ID: id, Name: name,
			State: strings.ToLower(state), Attributes: attributes,
		}
	}
	var found []Resource
	if wants(connection, "ec2_instance") {
		client := ec2.NewFromConfig(cfg)
	listing:
		for _, input := range describeInstancesInputs(filter.instanceIDs) {
			paginator := ec2.NewDescribeInstancesPaginator(client, input)
			for paginator.HasMorePages() {
				page, err := paginator.NextPage(ctx)
				if !t.call("ec2:DescribeInstances", err, isAWSDenied(err)) {
					break listing
				}
				found = append(found, awsInstances(page, resource)...)
			}
		}
	}
	if wants(connection, "load_balancer") {
		found = append(found, awsLoadBalancers(ctx, cfg, resource, t, !filter.skipTargets)...)
	}
	if wants(connection, "eks_cluster") {
		client := eks.NewFromConfig(cfg)
		paginator := eks.NewListClustersPaginator(client, &eks.ListClustersInput{})
		for paginator.HasMorePages() {
			page, err := paginator.NextPage(ctx)
			if !t.call("eks:ListClusters", err, isAWSDenied(err)) {
				break
			}
			for _, name := range page.Clusters {
				described, err := client.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String(name)})
				if !t.call("eks:DescribeCluster", err, isAWSDenied(err)) || described.Cluster == nil {
					found = append(
						found,
						resource("eks_cluster", name, name, "", map[string]string{"k8s_cluster_name": name}),
					)
					continue
				}
				cluster := described.Cluster
				found = append(found, resource("eks_cluster", aws.ToString(cluster.Arn), name, string(cluster.Status),
					map[string]string{"k8s_cluster_name": name, "k8s_version": aws.ToString(cluster.Version)}))
			}
		}
	}
	if wants(connection, "rds_instance") {
		paginator := rds.NewDescribeDBInstancesPaginator(rds.NewFromConfig(cfg), &rds.DescribeDBInstancesInput{})
		for paginator.HasMorePages() {
			page, err := paginator.NextPage(ctx)
			if !t.call("rds:DescribeDBInstances", err, isAWSDenied(err)) {
				break
			}
			for _, db := range page.DBInstances {
				found = append(found, resource("rds_instance", aws.ToString(db.DBInstanceArn),
					aws.ToString(db.DBInstanceIdentifier), aws.ToString(db.DBInstanceStatus), map[string]string{
						"db_engine": aws.ToString(db.Engine), "db_engine_version": aws.ToString(db.EngineVersion),
						"instance_type": aws.ToString(db.DBInstanceClass),
					}))
			}
		}
	}
	if wants(connection, "autoscaling_group") {
		paginator := autoscaling.NewDescribeAutoScalingGroupsPaginator(autoscaling.NewFromConfig(cfg),
			&autoscaling.DescribeAutoScalingGroupsInput{})
		for paginator.HasMorePages() {
			page, err := paginator.NextPage(ctx)
			if !t.call("autoscaling:DescribeAutoScalingGroups", err, isAWSDenied(err)) {
				break
			}
			for _, group := range page.AutoScalingGroups {
				healthy := 0
				for _, instance := range group.Instances {
					if aws.ToString(instance.HealthStatus) == "Healthy" {
						healthy++
					}
				}
				found = append(found, resource("autoscaling_group", aws.ToString(group.AutoScalingGroupARN),
					aws.ToString(group.AutoScalingGroupName), "", map[string]string{
						"desired": fmt.Sprint(aws.ToInt32(group.DesiredCapacity)),
						"healthy": fmt.Sprint(healthy),
					}))
			}
		}
	}
	if wants(connection, "lambda_function") {
		paginator := lambda.NewListFunctionsPaginator(lambda.NewFromConfig(cfg), &lambda.ListFunctionsInput{})
		for paginator.HasMorePages() {
			page, err := paginator.NextPage(ctx)
			if !t.call("lambda:ListFunctions", err, isAWSDenied(err)) {
				break
			}
			for _, function := range page.Functions {
				found = append(found, resource("lambda_function", aws.ToString(function.FunctionArn),
					aws.ToString(function.FunctionName), string(function.State),
					map[string]string{"runtime": string(function.Runtime)}))
			}
		}
	}
	return found
}

// describeInstancesInputs lists ids by instance-id filter in batches (a
// filter, not InstanceIds, so one gone instance doesn't fail the call);
// none, or too many to be worth it, lists the whole region.
func describeInstancesInputs(ids []string) []*ec2.DescribeInstancesInput {
	if len(ids) == 0 || len(ids) > maxFilteredInstances {
		return []*ec2.DescribeInstancesInput{{}}
	}
	var inputs []*ec2.DescribeInstancesInput
	for batch := range slices.Chunk(ids, instanceFilterBatch) {
		inputs = append(inputs, &ec2.DescribeInstancesInput{
			Filters: []ec2types.Filter{{Name: aws.String("instance-id"), Values: batch}},
		})
	}
	return inputs
}

func awsInstances(
	page *ec2.DescribeInstancesOutput,
	resource func(kind, id, name, state string, attributes map[string]string) Resource,
) []Resource {
	var found []Resource
	for _, reservation := range page.Reservations {
		for _, instance := range reservation.Instances {
			id := aws.ToString(instance.InstanceId)
			state := ""
			if instance.State != nil {
				state = string(instance.State.Name)
			}
			zone := ""
			if instance.Placement != nil {
				zone = aws.ToString(instance.Placement.AvailabilityZone)
			}
			found = append(found, resource("ec2_instance", id, nameTag(instance.Tags, id), state,
				map[string]string{
					"host_id": id, "instance_type": string(instance.InstanceType),
					"cloud_availability_zone": zone, "vpc_id": aws.ToString(instance.VpcId),
					"private_dns_name": aws.ToString(instance.PrivateDnsName),
				}))
		}
	}
	return found
}

// awsLoadBalancers lists ALBs, NLBs and gateway LBs, with their targets'
// health when targets.
func awsLoadBalancers(
	ctx context.Context, cfg aws.Config,
	resource func(kind, id, name, state string, attributes map[string]string) Resource, t *tracker, targets bool,
) []Resource {
	client := elbv2.NewFromConfig(cfg)
	byARN := map[string]*Resource{}
	var order []string
	paginator := elbv2.NewDescribeLoadBalancersPaginator(client, &elbv2.DescribeLoadBalancersInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if !t.call("elasticloadbalancing:DescribeLoadBalancers", err, isAWSDenied(err)) {
			break
		}
		for _, lb := range page.LoadBalancers {
			state := ""
			if lb.State != nil {
				state = string(lb.State.Code)
			}
			arn := aws.ToString(lb.LoadBalancerArn)
			found := resource("load_balancer", arn, aws.ToString(lb.LoadBalancerName), state, map[string]string{
				"lb_type": string(lb.Type), "lb_scheme": string(lb.Scheme), "dns_name": aws.ToString(lb.DNSName),
				"vpc_id": aws.ToString(lb.VpcId),
			})
			found.Targets = map[string]int{}
			byARN[arn] = &found
			order = append(order, arn)
		}
	}
	if len(byARN) > 0 && targets {
		groups := elbv2.NewDescribeTargetGroupsPaginator(client, &elbv2.DescribeTargetGroupsInput{})
		checked := 0
		for groups.HasMorePages() && checked < maxTargetGroups {
			page, err := groups.NextPage(ctx)
			if !t.call("elasticloadbalancing:DescribeTargetGroups", err, isAWSDenied(err)) {
				break
			}
			for _, group := range page.TargetGroups {
				if len(group.LoadBalancerArns) == 0 || checked >= maxTargetGroups {
					continue
				}
				checked++
				health, err := client.DescribeTargetHealth(ctx,
					&elbv2.DescribeTargetHealthInput{TargetGroupArn: group.TargetGroupArn})
				if !t.call("elasticloadbalancing:DescribeTargetHealth", err, isAWSDenied(err)) {
					continue
				}
				for _, arn := range group.LoadBalancerArns {
					if lb, ok := byARN[arn]; ok {
						for _, target := range health.TargetHealthDescriptions {
							state := "unknown"
							if target.TargetHealth != nil {
								state = string(target.TargetHealth.State)
							}
							lb.Targets[state]++
						}
					}
				}
			}
		}
	}
	resources := make([]Resource, 0, len(order))
	for _, arn := range order {
		resources = append(resources, *byARN[arn])
	}
	return resources
}

func awsBuckets(ctx context.Context, account awsAccount, t *tracker) []Resource {
	cfg := account.config.Copy()
	cfg.Region = awsDefaultRegion
	var found []Resource
	paginator := s3.NewListBucketsPaginator(s3.NewFromConfig(cfg), &s3.ListBucketsInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if !t.call("s3:ListAllMyBuckets", err, isAWSDenied(err)) {
			break
		}
		for _, bucket := range page.Buckets {
			name := aws.ToString(bucket.Name)
			found = append(found, Resource{
				Provider: spec.ProviderAWS, AccountID: account.id, Region: aws.ToString(bucket.BucketRegion),
				Type: "aws_s3_bucket", ID: "arn:aws:s3:::" + name, Name: name, State: "available",
			})
		}
	}
	return found
}

// awsRegionConfig is account's config for region, with one retryer for
// the region so its adaptive rate follows that region's throttling.
func awsRegionConfig(account awsAccount, region string) aws.Config {
	cfg := account.config.Copy()
	cfg.Region = region
	retryer := awsRetryer()
	cfg.Retryer = func() aws.Retryer { return retryer }
	return cfg
}

// awsRetryer backs off and slows its own rate when AWS throttles, with
// more attempts than the default; long scans can afford to wait.
func awsRetryer() aws.Retryer {
	return retry.NewAdaptiveMode(func(o *retry.AdaptiveModeOptions) {
		o.StandardOptions = append(o.StandardOptions, func(o *retry.StandardOptions) {
			o.MaxAttempts = awsMaxAttempts
			o.MaxBackoff = awsMaxBackoff
			o.RateLimiter = ratelimit.None
		})
	})
}

// memberRoleARN is role's name in another account of the Organization.
func memberRoleARN(role, accountID string) string {
	parts := strings.SplitN(role, ":", 6)
	if len(parts) != 6 {
		return role
	}
	parts[4] = accountID
	return strings.Join(parts, ":")
}

func nameTag(tags []ec2types.Tag, fallback string) string {
	for _, tag := range tags {
		if aws.ToString(tag.Key) == "Name" && aws.ToString(tag.Value) != "" {
			return aws.ToString(tag.Value)
		}
	}
	return fallback
}

func signInAction(connection spec.CloudConnection) string {
	switch connection.Access {
	case spec.AccessRole:
		return "sts:AssumeRole (the role must trust the probe's IAM role with this external ID)"
	case spec.AccessFederated:
		return "sts:AssumeRoleWithWebIdentity (the role must trust the control plane's OIDC provider for this probe group)"
	default:
		return "sts:GetCallerIdentity"
	}
}

func isAWSDenied(err error) bool {
	var apiErr smithy.APIError
	return err != nil && errors.As(err, &apiErr) && awsDenied[apiErr.ErrorCode()]
}

// tokenRetriever hands the control plane's token to the web identity
// exchange. Kept credentials refresh after the scan that signed in has
// ended, so it doesn't use that scan's context.
type tokenRetriever struct {
	source TokenSource
}

func (r tokenRetriever) GetIdentityToken() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenFetchTimeout)
	defer cancel()
	token, err := r.source(ctx)
	return []byte(token), err
}

// baseConfig is the probe's own AWS config: kept from discovery for a
// health scan (reuse), else loaded afresh, which also forgets the kept
// sign-ins and starts counting health scans again.
func (c *AWSCache) baseConfig(
	ctx context.Context, connection spec.CloudConnection, options Options, reuse bool,
) (aws.Config, error) {
	if c != nil && reuse {
		c.mu.Lock()
		base := c.base
		c.mu.Unlock()
		if base != nil {
			return *base, nil
		}
	}
	base, err := awsBaseConfig(ctx, connection, options)
	if err != nil || c == nil {
		return base, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.base, c.accounts = &base, map[string]awsAccount{}
	if !reuse {
		c.healthScans = 0
	}
	return base, nil
}

func (c *AWSCache) account(role string, reuse bool) (awsAccount, bool) {
	if c == nil || !reuse {
		return awsAccount{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	account, ok := c.accounts[role]
	return account, ok
}

func (c *AWSCache) keep(role string, account awsAccount) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.accounts[role] = account
}

// targetsDue counts a health scan; true every targetHealthEvery-th since
// the last discovery.
func (c *AWSCache) targetsDue() bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.healthScans++
	return c.healthScans%targetHealthEvery == 0
}
