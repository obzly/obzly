package discovery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/securityhub"
	shtypes "github.com/aws/aws-sdk-go-v2/service/securityhub/types"
	"github.com/aws/smithy-go"

	"obzly/internal/posture/baseline"
	"obzly/internal/synthetics/spec"
)

// The Security Hub standards Obzly maps, by a part of their ARN or ID.
var securityHubStandards = map[string]string{
	"aws-foundational-security-best-practices": "aws-fsbp",
	"cis-aws-foundations-benchmark":            "cis-aws",
}

var (
	awsRegionalFactTypes = []string{"aws_security_group", "aws_ebs_settings", "aws_rds_instance", "aws_ec2_instance"}
	awsGlobalFactTypes   = []string{"aws_account", "aws_cloudtrail", "aws_s3_bucket"}
	awsNativeStandards   = []string{"aws-fsbp", "cis-aws"}
)

func collectAWSPosture(
	ctx context.Context,
	connection spec.CloudConnection,
	options Options,
	t *tracker,
	c *postureCollector,
	source string,
) {
	eachAWSAccount(ctx, connection, options, t, nil, func(ctx context.Context, account awsAccount) {
		regions := connection.Regions
		if len(regions) == 0 {
			regions = awsEnabledRegions(ctx, account, t)
		}
		if options.Sample {
			regions = regions[:min(len(regions), sampleRegions)]
		}
		var mu sync.Mutex
		nativeAnywhere := false
		inParallel(regions, maxRegionsInParallel, func(region string) {
			if source != spec.PostureSourceBaseline {
				runs, findings, problem := awsSecurityHub(ctx, account, region, t)
				if problem == nil {
					c.add(runs, findings)
					c.cover(account.id, region, spec.PostureSourceNative)
					mu.Lock()
					nativeAnywhere = true
					mu.Unlock()
					return
				}
				// Kept for Coverage: why this region falls back, or isn't covered.
				c.add(unreadRuns(account.id, region, spec.PostureSourceNative, awsNativeStandards, *problem), nil)
				if source == spec.PostureSourceNative {
					c.cover(account.id, region, "")
					return
				}
			}
			facts, problem := awsRegionalFacts(ctx, account, region, t)
			runs, findings := baselineScope(spec.ProviderAWS, account.id, region, awsRegionalFactTypes, facts, problem)
			c.add(runs, findings)
			c.cover(account.id, region, spec.PostureSourceBaseline)
		})
		// Account-wide controls (IAM, CloudTrail, S3) are in Security Hub's findings wherever it
		// runs; the baseline reads them only when no region has it.
		switch {
		case nativeAnywhere:
			c.cover(account.id, spec.GlobalRegion, spec.PostureSourceNative)
		case source == spec.PostureSourceNative:
			c.cover(account.id, spec.GlobalRegion, "")
		default:
			facts, problem := awsGlobalFacts(ctx, account, t, options.AWSEndpoint != "")
			runs, findings := baselineScope(spec.ProviderAWS, account.id, spec.GlobalRegion, awsGlobalFactTypes, facts,
				problem)
			c.add(runs, findings)
			c.cover(account.id, spec.GlobalRegion, spec.PostureSourceBaseline)
		}
	})
}

// awsSecurityHub reads one region's Security Hub control findings, failed and passed, for the
// enabled standards Obzly maps. A problem says why the region can't be read natively.
func awsSecurityHub(
	ctx context.Context, account awsAccount, region string, t *tracker,
) ([]spec.PostureRun, []spec.PostureFinding, *postureProblem) {
	client := securityhub.NewFromConfig(awsRegionConfig(account, region))
	if _, err := client.DescribeHub(ctx, &securityhub.DescribeHubInput{}); err != nil {
		return nil, nil, securityHubProblem("securityhub:DescribeHub", err, t)
	}
	enabled := map[string]bool{}
	standards := securityhub.NewGetEnabledStandardsPaginator(client, &securityhub.GetEnabledStandardsInput{})
	for standards.HasMorePages() {
		page, err := standards.NextPage(ctx)
		if err != nil {
			return nil, nil, securityHubProblem("securityhub:GetEnabledStandards", err, t)
		}
		for _, subscription := range page.StandardsSubscriptions {
			if subscription.StandardsStatus == shtypes.StandardsStatusReady {
				if id := mappedStandard(aws.ToString(subscription.StandardsArn)); id != "" {
					enabled[id] = true
				}
			}
		}
	}
	if len(enabled) == 0 {
		return nil, nil, &postureProblem{result: spec.PostureRunNotEnabled,
			reason: "Security Hub has neither AWS Foundational Security Best Practices nor CIS enabled"}
	}
	equals := func(value string) []shtypes.StringFilter {
		return []shtypes.StringFilter{{Value: aws.String(value), Comparison: shtypes.StringFilterComparisonEquals}}
	}
	input := &securityhub.GetFindingsInput{MaxResults: aws.Int32(100), Filters: &shtypes.AwsSecurityFindingFilters{
		ProductName: equals("Security Hub"), RecordState: equals("ACTIVE"),
		ComplianceStatus: append(equals("FAILED"), equals("PASSED")...),
	}}
	var found []shtypes.AwsSecurityFinding
	findings := securityhub.NewGetFindingsPaginator(client, input)
	for findings.HasMorePages() && len(found) < postureMaxFindings {
		page, err := findings.NextPage(ctx)
		if err != nil {
			return nil, nil, securityHubProblem("securityhub:GetFindings", err, t)
		}
		found = append(found, page.Findings...)
	}
	runs, mapped := securityHubResults(account.id, region, enabled, found)
	return runs, mapped, nil
}

// securityHubResults turns control findings into a run per enabled standard and a finding per
// failed control and resource.
func securityHubResults(
	account, region string, enabled map[string]bool, found []shtypes.AwsSecurityFinding,
) ([]spec.PostureRun, []spec.PostureFinding) {
	passed, failed := map[string]map[string]bool{}, map[string]map[string]bool{}
	for standard := range enabled {
		passed[standard], failed[standard] = map[string]bool{}, map[string]bool{}
	}
	var findings []spec.PostureFinding
	for _, finding := range found {
		compliance := finding.Compliance
		if compliance == nil || aws.ToString(compliance.SecurityControlId) == "" {
			continue
		}
		control := aws.ToString(compliance.SecurityControlId)
		refs := securityHubRefs(control, compliance)
		isFailed := compliance.Status == shtypes.ComplianceStatusFailed
		for _, ref := range refs {
			if !enabled[ref.Standard] {
				continue
			}
			if isFailed {
				failed[ref.Standard][ref.Control] = true
			} else {
				passed[ref.Standard][ref.Control] = true
			}
		}
		if !isFailed || len(finding.Resources) == 0 {
			continue
		}
		evidence := map[string]string{}
		if len(compliance.StatusReasons) > 0 {
			evidence["status_reason"] = aws.ToString(compliance.StatusReasons[0].Description)
		}
		mapped := spec.PostureFinding{
			Source: spec.PostureSourceNative, Account: orDefault(aws.ToString(finding.AwsAccountId), account),
			Region: region, Control: control, Title: aws.ToString(finding.Title),
			Severity:    strings.ToLower(string(severityLabel(finding.Severity))),
			ResourceKey: aws.ToString(finding.Resources[0].Id), ResourceType: aws.ToString(finding.Resources[0].Type),
			Standards: refs, Evidence: evidence,
		}
		if finding.Remediation != nil && finding.Remediation.Recommendation != nil {
			mapped.RemediationURL = aws.ToString(finding.Remediation.Recommendation.Url)
		}
		findings = append(findings, mapped)
	}
	runs := make([]spec.PostureRun, 0, len(enabled))
	for _, standard := range awsNativeStandards {
		if !enabled[standard] {
			continue
		}
		run := spec.PostureRun{Account: account, Region: region, Standard: standard, Source: spec.PostureSourceNative,
			Result: spec.PostureRunOK}
		run.Passed, run.Failed = controlLists(passed[standard], failed[standard])
		runs = append(runs, run)
	}
	return runs, findings
}

// securityHubRefs: FSBP cites the control by its own ID; CIS by its number, in the related
// requirements ("CIS AWS Foundations Benchmark v3.0.0/2.1.4").
func securityHubRefs(control string, compliance *shtypes.Compliance) []spec.StandardRef {
	var refs []spec.StandardRef
	seen := map[string]bool{}
	for _, associated := range compliance.AssociatedStandards {
		standard := mappedStandard(aws.ToString(associated.StandardsId))
		if standard == "" || seen[standard] {
			continue
		}
		seen[standard] = true
		number := control
		if standard == "cis-aws" {
			for _, requirement := range compliance.RelatedRequirements {
				if strings.HasPrefix(requirement, "CIS AWS Foundations Benchmark") {
					number = requirement[strings.LastIndex(requirement, "/")+1:]
				}
			}
		}
		refs = append(refs, spec.StandardRef{Standard: standard, Control: number})
	}
	return refs
}

func mappedStandard(arn string) string {
	for part, id := range securityHubStandards {
		if strings.Contains(arn, part) {
			return id
		}
	}
	return ""
}

func severityLabel(severity *shtypes.Severity) shtypes.SeverityLabel {
	if severity == nil || severity.Label == "" {
		return shtypes.SeverityLabelMedium
	}
	return severity.Label
}

// securityHubProblem: not subscribed is "not enabled", a denial needs permission, and a failed
// call is recorded with the scan's errors.
func securityHubProblem(action string, err error, t *tracker) *postureProblem {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "InvalidAccessException", "ResourceNotFoundException":
			return &postureProblem{result: spec.PostureRunNotEnabled, reason: "Security Hub not enabled"}
		}
	}
	if isAWSDenied(err) {
		t.failPermission(fmt.Errorf("%s denied: %w", action, err), action)
		return &postureProblem{result: spec.PostureRunNeedsPermission, reason: action + " denied"}
	}
	t.call(action, err, false)
	return &postureProblem{result: spec.PostureRunError, reason: firstLine(err.Error())}
}

// awsRegionalFacts reads what the region's baseline checks need. A read that fails makes the
// region's runs not ok, so they resolve nothing, while what was read is still checked.
func awsRegionalFacts(
	ctx context.Context,
	account awsAccount,
	region string,
	t *tracker,
) ([]baseline.Fact, *postureProblem) {
	cfg := awsRegionConfig(account, region)
	client := ec2.NewFromConfig(cfg)
	var facts []baseline.Fact
	var problem *postureProblem
	read := func(action string, err error) bool {
		denied := isAWSDenied(err)
		if t.call(action, err, denied) {
			return true
		}
		if problem == nil {
			problem = &postureProblem{result: spec.PostureRunError, reason: action + " failed"}
			if denied {
				problem = &postureProblem{result: spec.PostureRunNeedsPermission, reason: action + " denied"}
			}
		}
		return false
	}
	fact := func(kind, key, name string, values map[string]any) {
		values["name"] = name
		facts = append(facts, baseline.Fact{Type: kind, Key: key, Account: account.id, Region: region, Values: values})
	}

	groups := ec2.NewDescribeSecurityGroupsPaginator(client, &ec2.DescribeSecurityGroupsInput{})
	for groups.HasMorePages() {
		page, err := groups.NextPage(ctx)
		if !read("ec2:DescribeSecurityGroups", err) {
			break
		}
		for _, group := range page.SecurityGroups {
			ports, all := securityGroupWorldPorts(group.IpPermissions)
			id := aws.ToString(group.GroupId)
			fact("aws_security_group", fmt.Sprintf("arn:aws:ec2:%s:%s:security-group/%s", region, account.id, id), id,
				map[string]any{"world_ports": ports, "world_all_traffic": all})
		}
	}
	if output, err := client.GetEbsEncryptionByDefault(ctx, &ec2.GetEbsEncryptionByDefaultInput{}); read(
		"ec2:GetEbsEncryptionByDefault", err) {
		fact("aws_ebs_settings", fmt.Sprintf("arn:aws:ec2:%s:%s:ebs-default-encryption", region, account.id), region,
			map[string]any{"default_encryption": aws.ToBool(output.EbsEncryptionByDefault)})
	}
	instances := ec2.NewDescribeInstancesPaginator(client, &ec2.DescribeInstancesInput{})
	for instances.HasMorePages() {
		page, err := instances.NextPage(ctx)
		if !read("ec2:DescribeInstances", err) {
			break
		}
		for _, reservation := range page.Reservations {
			for _, instance := range reservation.Instances {
				id := aws.ToString(instance.InstanceId)
				tokens, state := "optional", ""
				if instance.MetadataOptions != nil {
					tokens = string(instance.MetadataOptions.HttpTokens)
				}
				if instance.State != nil {
					state = string(instance.State.Name)
				}
				fact("aws_ec2_instance", fmt.Sprintf("arn:aws:ec2:%s:%s:instance/%s", region, account.id, id), id,
					map[string]any{"http_tokens": tokens, "state": state})
			}
		}
	}
	databases := rds.NewDescribeDBInstancesPaginator(rds.NewFromConfig(cfg), &rds.DescribeDBInstancesInput{})
	for databases.HasMorePages() {
		page, err := databases.NextPage(ctx)
		if !read("rds:DescribeDBInstances", err) {
			break
		}
		for _, db := range page.DBInstances {
			fact("aws_rds_instance", aws.ToString(db.DBInstanceArn), aws.ToString(db.DBInstanceIdentifier),
				map[string]any{"publicly_accessible": aws.ToBool(db.PubliclyAccessible),
					"storage_encrypted": aws.ToBool(db.StorageEncrypted)})
		}
	}
	return facts, problem
}

// awsGlobalFacts reads the account-wide settings and the buckets.
func awsGlobalFacts(
	ctx context.Context, account awsAccount, t *tracker, pathStyle bool,
) ([]baseline.Fact, *postureProblem) {
	cfg := account.config.Copy()
	cfg.Region = awsDefaultRegion
	var facts []baseline.Fact
	var problem *postureProblem
	var mu sync.Mutex
	read := func(action string, err error) bool {
		denied := isAWSDenied(err)
		if t.call(action, err, denied) {
			return true
		}
		mu.Lock()
		defer mu.Unlock()
		if problem == nil {
			problem = &postureProblem{result: spec.PostureRunError, reason: action + " failed"}
			if denied {
				problem = &postureProblem{result: spec.PostureRunNeedsPermission, reason: action + " denied"}
			}
		}
		return false
	}
	add := func(fact baseline.Fact) {
		mu.Lock()
		defer mu.Unlock()
		facts = append(facts, fact)
	}

	iamClient := iam.NewFromConfig(cfg)
	summary, err := iamClient.GetAccountSummary(ctx, &iam.GetAccountSummaryInput{})
	if read("iam:GetAccountSummary", err) {
		minLength := 0
		policy, err := iamClient.GetAccountPasswordPolicy(ctx, &iam.GetAccountPasswordPolicyInput{})
		var apiErr smithy.APIError
		switch {
		case err == nil && policy.PasswordPolicy != nil:
			minLength = int(aws.ToInt32(policy.PasswordPolicy.MinimumPasswordLength))
		case errors.As(err, &apiErr) && apiErr.ErrorCode() == "NoSuchEntity":
			// No password policy at all: IAM's default is 8 characters.
			minLength = 8
		default:
			read("iam:GetAccountPasswordPolicy", err)
		}
		add(baseline.Fact{Type: "aws_account", Key: fmt.Sprintf("arn:aws:iam::%s:root", account.id),
			Account: account.id, Region: spec.GlobalRegion, Values: map[string]any{
				"name":     account.id,
				"root_mfa": summary.SummaryMap["AccountMFAEnabled"] == 1,
				"root_access_keys": int(
					summary.SummaryMap["AccountAccessKeysPresent"],
				),
				"password_min_length": minLength,
			}})
	}

	trails, err := cloudtrail.NewFromConfig(cfg).DescribeTrails(ctx, &cloudtrail.DescribeTrailsInput{
		IncludeShadowTrails: aws.Bool(true)})
	if read("cloudtrail:DescribeTrails", err) {
		logging := false
		for _, trail := range trails.TrailList {
			if !aws.ToBool(trail.IsMultiRegionTrail) {
				continue
			}
			trailCfg := cfg.Copy()
			trailCfg.Region = orDefault(aws.ToString(trail.HomeRegion), awsDefaultRegion)
			status, err := cloudtrail.NewFromConfig(trailCfg).GetTrailStatus(ctx,
				&cloudtrail.GetTrailStatusInput{Name: trail.TrailARN})
			if read("cloudtrail:GetTrailStatus", err) && aws.ToBool(status.IsLogging) {
				logging = true
			}
		}
		add(baseline.Fact{Type: "aws_cloudtrail", Key: fmt.Sprintf("arn:aws:cloudtrail::%s:trails", account.id),
			Account: account.id, Region: spec.GlobalRegion, Values: map[string]any{
				"name": account.id, "multi_region_logging": logging, "trails": len(trails.TrailList),
			}})
	}

	buckets := awsBuckets(ctx, account, t)
	if len(buckets) > postureMaxBuckets {
		buckets = buckets[:postureMaxBuckets]
	}
	inParallel(buckets, maxRegionsInParallel, func(bucket Resource) {
		values, ok := awsBucketFacts(ctx, account, bucket, pathStyle, read)
		if ok {
			add(baseline.Fact{Type: "aws_s3_bucket", Key: bucket.ID, Account: account.id, Region: spec.GlobalRegion,
				Values: values})
		}
	})
	return facts, problem
}

func awsBucketFacts(
	ctx context.Context, account awsAccount, bucket Resource, pathStyle bool, read func(string, error) bool,
) (map[string]any, bool) {
	client := s3.NewFromConfig(awsRegionConfig(account, orDefault(bucket.Region, awsDefaultRegion)),
		func(o *s3.Options) { o.UsePathStyle = pathStyle })
	values := map[string]any{"name": bucket.Name, "block_public_acls": false, "ignore_public_acls": false,
		"block_public_policy": false, "restrict_public_buckets": false}
	block, err := client.GetPublicAccessBlock(ctx, &s3.GetPublicAccessBlockInput{Bucket: aws.String(bucket.Name)})
	var apiErr smithy.APIError
	switch {
	case err == nil && block.PublicAccessBlockConfiguration != nil:
		settings := block.PublicAccessBlockConfiguration
		values["block_public_acls"] = aws.ToBool(settings.BlockPublicAcls)
		values["ignore_public_acls"] = aws.ToBool(settings.IgnorePublicAcls)
		values["block_public_policy"] = aws.ToBool(settings.BlockPublicPolicy)
		values["restrict_public_buckets"] = aws.ToBool(settings.RestrictPublicBuckets)
	case errors.As(err, &apiErr) && apiErr.ErrorCode() == "NoSuchPublicAccessBlockConfiguration":
	default:
		if !read("s3:GetBucketPublicAccessBlock", err) {
			return nil, false
		}
	}
	logging, err := client.GetBucketLogging(ctx, &s3.GetBucketLoggingInput{Bucket: aws.String(bucket.Name)})
	if !read("s3:GetBucketLogging", err) {
		return nil, false
	}
	values["logging_enabled"] = logging.LoggingEnabled != nil
	return values, true
}

// securityGroupWorldPorts: the admin ports a group opens to 0.0.0.0/0 or ::/0, and whether it
// opens everything.
func securityGroupWorldPorts(permissions []ec2types.IpPermission) ([]int, bool) {
	ports := map[int]bool{}
	all := false
	for _, permission := range permissions {
		world := false
		for _, cidr := range permission.IpRanges {
			world = world || aws.ToString(cidr.CidrIp) == "0.0.0.0/0"
		}
		for _, cidr := range permission.Ipv6Ranges {
			world = world || aws.ToString(cidr.CidrIpv6) == "::/0"
		}
		if !world {
			continue
		}
		switch protocol := aws.ToString(permission.IpProtocol); protocol {
		case "-1":
			all = true
		case "tcp", "6":
			for _, port := range portsOpen(int(aws.ToInt32(permission.FromPort)), int(aws.ToInt32(permission.ToPort))) {
				ports[port] = true
			}
		}
	}
	open := []int{}
	for _, port := range adminPorts {
		if ports[port] {
			open = append(open, port)
		}
	}
	return open, all
}
