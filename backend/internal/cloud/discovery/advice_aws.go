package discovery

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/computeoptimizer"
	cotypes "github.com/aws/aws-sdk-go-v2/service/computeoptimizer/types"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"

	"obzly/internal/synthetics/spec"
)

const (
	ceServiceEC2 = "Amazon Elastic Compute Cloud - Compute"
	ceServiceRDS = "Amazon Relational Database Service"
)

var (
	awsTerms = []struct {
		term string
		ce   cetypes.TermInYears
	}{{spec.Term1Year, cetypes.TermInYearsOneYear}, {spec.Term3Years, cetypes.TermInYearsThreeYears}}
	awsPayments = []struct {
		payment string
		ce      cetypes.PaymentOption
	}{
		{spec.PayNoUpfront, cetypes.PaymentOptionNoUpfront},
		{spec.PayPartial, cetypes.PaymentOptionPartialUpfront},
		{spec.PayAllUpfront, cetypes.PaymentOptionAllUpfront},
	}
	awsLookbacks = map[int]cetypes.LookbackPeriodInDays{
		7: cetypes.LookbackPeriodInDaysSevenDays, 30: cetypes.LookbackPeriodInDaysThirtyDays,
		60: cetypes.LookbackPeriodInDaysSixtyDays,
	}
	awsMigrationEffort = map[cotypes.MigrationEffort]string{
		cotypes.MigrationEffortVeryLow: "low", cotypes.MigrationEffortLow: "low",
		cotypes.MigrationEffortMedium: "medium", cotypes.MigrationEffortHigh: "high",
	}
)

// awsCE is the Cost Explorer side of an AWS advice refresh, run once from the connection's own
// account (the management account for the billing scope, which sees every member).
type awsCE struct {
	client *costexplorer.Client
	scope  cetypes.AccountScope
	c      *adviceCollector
}

func collectAWSAdvice(ctx context.Context, connection spec.CloudConnection, options Options, c *adviceCollector) {
	base, err := awsBaseConfig(ctx, connection, options)
	if err == nil {
		var account awsAccount
		account, err = awsSignIn(ctx, connection, base, connection.Identity, options)
		if err == nil {
			ce := &awsCE{client: costexplorer.NewFromConfig(awsRegionConfig(account, awsDefaultRegion)),
				scope: cetypes.AccountScopeLinked, c: c}
			if connection.Cost != nil && connection.Cost.Scope == spec.CostScopeBilling {
				ce.scope = cetypes.AccountScopePayer
			}
			ce.commitments(ctx)
			ce.planner(ctx)
		}
	}
	if err != nil {
		state := spec.AdviceError
		if isAWSDenied(err) {
			state = spec.AdviceNeedsPermission
		}
		for _, section := range []string{spec.AdviceRightsizing, spec.AdviceChecks, spec.AdviceCommitments,
			spec.AdvicePlanner} {
			c.section(section, state, "couldn't sign in: "+err.Error())
		}
		return
	}
	t := newTracker()
	eachAWSAccount(ctx, connection, options, t, nil, func(ctx context.Context, account awsAccount) {
		regions := connection.Regions
		if len(regions) == 0 {
			regions = awsEnabledRegions(ctx, account, t)
		}
		optimizer := awsOptimizerEnrolled(ctx, account, c)
		inParallel(regions, maxRegionsInParallel, func(region string) {
			if optimizer {
				awsOptimizerRegion(ctx, account, region, c)
			}
			awsChecksRegion(ctx, account, region, c)
		})
	})
	if result := t.result(); result.State != spec.UseOK {
		state := spec.AdviceError
		if result.State == spec.UseNeedsPermission {
			state = spec.AdviceNeedsPermission
		}
		reason := result.Message
		if len(result.Missing) > 0 {
			reason = strings.Join(result.Missing, ", ") + " denied"
		}
		c.section(spec.AdviceRightsizing, state, reason)
		c.section(spec.AdviceChecks, state, reason)
	}
	c.section(spec.AdviceRightsizing, spec.AdviceOK, "")
	c.section(spec.AdviceChecks, spec.AdviceOK, "")
}

// awsOptimizerEnrolled: Compute Optimizer gives rightsizing and idle recommendations only to an
// account that opted in (free).
func awsOptimizerEnrolled(ctx context.Context, account awsAccount, c *adviceCollector) bool {
	client := computeoptimizer.NewFromConfig(awsRegionConfig(account, awsDefaultRegion))
	output, err := client.GetEnrollmentStatus(ctx, &computeoptimizer.GetEnrollmentStatusInput{})
	switch {
	case isAWSDenied(err):
		c.section(spec.AdviceRightsizing, spec.AdviceNeedsPermission,
			"compute-optimizer:GetEnrollmentStatus denied in account "+account.id)
		return false
	case err != nil:
		c.section(spec.AdviceRightsizing, spec.AdviceError, "Compute Optimizer: "+err.Error())
		return false
	case output.Status != cotypes.StatusActive:
		c.section(spec.AdviceRightsizing, spec.AdviceNotEnabled,
			"Compute Optimizer isn't opted in for account "+account.id+
				" (opting in is free, in the Compute Optimizer console)")
		return false
	}
	return true
}

func awsOptimizerRegion(ctx context.Context, account awsAccount, region string, c *adviceCollector) {
	client := computeoptimizer.NewFromConfig(awsRegionConfig(account, region))
	fail := func(action string, err error) bool {
		if err == nil {
			return false
		}
		var apiErr smithy.APIError
		switch {
		case isAWSDenied(err):
			c.section(spec.AdviceRightsizing, spec.AdviceNeedsPermission, action+" denied in "+region)
		case errors.As(err, &apiErr) && apiErr.ErrorCode() == "OptInRequiredException":
			c.section(spec.AdviceRightsizing, spec.AdviceNotEnabled,
				"Compute Optimizer isn't opted in for account "+account.id)
		default:
			c.section(spec.AdviceRightsizing, spec.AdviceError, action+" in "+region+": "+err.Error())
		}
		return true
	}

	instances := &computeoptimizer.GetEC2InstanceRecommendationsInput{
		Filters: []cotypes.Filter{{Name: cotypes.FilterNameFinding, Values: []string{"Overprovisioned"}}},
	}
	for found := 0; found < adviceMaxPerRegion; {
		output, err := client.GetEC2InstanceRecommendations(ctx, instances)
		if fail("compute-optimizer:GetEC2InstanceRecommendations", err) {
			return
		}
		for _, rec := range output.InstanceRecommendations {
			if converted, ok := awsInstanceRecommendation(rec, region); ok {
				c.addRecs(converted)
				found++
			}
		}
		if aws.ToString(output.NextToken) == "" {
			break
		}
		instances.NextToken = output.NextToken
	}

	volumes := &computeoptimizer.GetEBSVolumeRecommendationsInput{
		Filters: []cotypes.EBSFilter{{Name: cotypes.EBSFilterNameFinding, Values: []string{"NotOptimized"}}},
	}
	for found := 0; found < adviceMaxPerRegion; {
		output, err := client.GetEBSVolumeRecommendations(ctx, volumes)
		if fail("compute-optimizer:GetEBSVolumeRecommendations", err) {
			return
		}
		for _, rec := range output.VolumeRecommendations {
			if converted, ok := awsVolumeRecommendation(rec, region); ok {
				c.addRecs(converted)
				found++
			}
		}
		if aws.ToString(output.NextToken) == "" {
			break
		}
		volumes.NextToken = output.NextToken
	}

	idle := &computeoptimizer.GetIdleRecommendationsInput{}
	for found := 0; found < adviceMaxPerRegion; {
		output, err := client.GetIdleRecommendations(ctx, idle)
		if fail("compute-optimizer:GetIdleRecommendations", err) {
			return
		}
		for _, rec := range output.IdleRecommendations {
			if converted, ok := awsIdleRecommendation(rec, region); ok {
				c.addRecs(converted)
				found++
			}
		}
		if aws.ToString(output.NextToken) == "" {
			break
		}
		idle.NextToken = output.NextToken
	}
}

func awsInstanceRecommendation(rec cotypes.InstanceRecommendation, region string) (spec.CostRecommendation, bool) {
	var best *cotypes.InstanceRecommendationOption
	for i := range rec.RecommendationOptions {
		option := &rec.RecommendationOptions[i]
		if best == nil || option.Rank < best.Rank {
			best = option
		}
	}
	if best == nil {
		return spec.CostRecommendation{}, false
	}
	currency, savings := instanceSavings(best)
	if savings <= 0 {
		return spec.CostRecommendation{}, false
	}
	current, target := aws.ToString(rec.CurrentInstanceType), aws.ToString(best.InstanceType)
	evidence := map[string]string{
		"finding":        string(rec.Finding),
		"look_back_days": strconv.FormatFloat(rec.LookBackPeriodInDays, 'f', 0, 64),
		"current_type":   current,
		"recommended":    target,
		"instance_state": string(rec.InstanceState),
	}
	reasons := make([]string, 0, len(rec.FindingReasonCodes))
	for _, code := range rec.FindingReasonCodes {
		reasons = append(reasons, string(code))
	}
	if len(reasons) > 0 {
		evidence["reasons"] = strings.Join(reasons, ", ")
	}
	for _, metric := range rec.UtilizationMetrics {
		evidence[strings.ToLower(string(metric.Name))+"_"+strings.ToLower(string(metric.Statistic))] =
			strconv.FormatFloat(metric.Value, 'f', 1, 64)
	}
	arn := aws.ToString(rec.InstanceArn)
	return spec.CostRecommendation{
		Key: "co:ec2:" + arn, Source: spec.RecommendFromComputeOptimizer, Kind: spec.RecommendRightsizing,
		Account: aws.ToString(rec.AccountId), Region: region, ResourceKey: arn,
		ResourceName: firstNonEmptyString(aws.ToString(rec.InstanceName), lastSegment(arn)),
		ResourceType: "aws_ec2_instance", Action: fmt.Sprintf("Change %s to %s", current, target),
		Current: current, Target: target, Currency: currency, MonthlySavingsMicros: savings,
		Effort: firstNonEmptyString(awsMigrationEffort[best.MigrationEffort], "medium"),
		Risk:   riskLevel(best.PerformanceRisk), Evidence: evidence,
	}, true
}

func instanceSavings(option *cotypes.InstanceRecommendationOption) (string, int64) {
	if after := option.SavingsOpportunityAfterDiscounts; after != nil && after.EstimatedMonthlySavings != nil {
		return string(after.EstimatedMonthlySavings.Currency), microsOf(after.EstimatedMonthlySavings.Value)
	}
	if before := option.SavingsOpportunity; before != nil && before.EstimatedMonthlySavings != nil {
		return string(before.EstimatedMonthlySavings.Currency), microsOf(before.EstimatedMonthlySavings.Value)
	}
	return "", 0
}

func awsVolumeRecommendation(rec cotypes.VolumeRecommendation, region string) (spec.CostRecommendation, bool) {
	var best *cotypes.VolumeRecommendationOption
	for i := range rec.VolumeRecommendationOptions {
		option := &rec.VolumeRecommendationOptions[i]
		if best == nil || option.Rank < best.Rank {
			best = option
		}
	}
	if best == nil || best.Configuration == nil || rec.CurrentConfiguration == nil {
		return spec.CostRecommendation{}, false
	}
	currency, savings := "", int64(0)
	if after := best.SavingsOpportunityAfterDiscounts; after != nil && after.EstimatedMonthlySavings != nil {
		currency, savings = string(
			after.EstimatedMonthlySavings.Currency,
		), microsOf(
			after.EstimatedMonthlySavings.Value,
		)
	} else if before := best.SavingsOpportunity; before != nil && before.EstimatedMonthlySavings != nil {
		currency, savings = string(before.EstimatedMonthlySavings.Currency),
			microsOf(before.EstimatedMonthlySavings.Value)
	}
	if savings <= 0 {
		return spec.CostRecommendation{}, false
	}
	current := volumeConfig(rec.CurrentConfiguration)
	target := volumeConfig(best.Configuration)
	kind := spec.RecommendStorage
	if aws.ToString(rec.CurrentConfiguration.VolumeType) != aws.ToString(best.Configuration.VolumeType) {
		kind = spec.RecommendModernize
	}
	arn := aws.ToString(rec.VolumeArn)
	return spec.CostRecommendation{
		Key: "co:ebs:" + arn, Source: spec.RecommendFromComputeOptimizer, Kind: kind,
		Account: aws.ToString(rec.AccountId), Region: region, ResourceKey: arn, ResourceName: lastSegment(arn),
		ResourceType: "aws_ebs_volume", Action: fmt.Sprintf("Change %s to %s", current, target),
		Current: current, Target: target, Currency: currency, MonthlySavingsMicros: savings, Effort: "low",
		Risk: riskLevel(best.PerformanceRisk),
		Evidence: map[string]string{"finding": string(rec.Finding),
			"look_back_days": strconv.FormatFloat(rec.LookBackPeriodInDays, 'f', 0, 64)},
	}, true
}

func volumeConfig(config *cotypes.VolumeConfiguration) string {
	return fmt.Sprintf("%s %d GiB", aws.ToString(config.VolumeType), config.VolumeSize)
}

func awsIdleRecommendation(rec cotypes.IdleRecommendation, region string) (spec.CostRecommendation, bool) {
	currency, savings := "", int64(0)
	if after := rec.SavingsOpportunityAfterDiscounts; after != nil && after.EstimatedMonthlySavings != nil {
		currency, savings = string(
			after.EstimatedMonthlySavings.Currency,
		), microsOf(
			after.EstimatedMonthlySavings.Value,
		)
	} else if before := rec.SavingsOpportunity; before != nil && before.EstimatedMonthlySavings != nil {
		currency, savings = string(before.EstimatedMonthlySavings.Currency),
			microsOf(before.EstimatedMonthlySavings.Value)
	}
	arn := aws.ToString(rec.ResourceArn)
	if arn == "" || savings <= 0 {
		return spec.CostRecommendation{}, false
	}
	what, typ := awsIdleNames(string(rec.ResourceType))
	action := "Stop or delete the idle " + what
	if rec.Finding == cotypes.IdleFindingUnattached {
		action = "Delete the unattached " + what + " (snapshot it first if you need the data)"
	}
	evidence := map[string]string{"finding": string(rec.Finding),
		"look_back_days": strconv.FormatFloat(rec.LookBackPeriodInDays, 'f', 0, 64)}
	if description := aws.ToString(rec.FindingDescription); description != "" {
		evidence["description"] = clip(description, 300)
	}
	for _, metric := range rec.UtilizationMetrics {
		evidence[strings.ToLower(string(metric.Name))+"_"+strings.ToLower(string(metric.Statistic))] =
			strconv.FormatFloat(metric.Value, 'f', 1, 64)
	}
	return spec.CostRecommendation{
		Key: "co:idle:" + arn, Source: spec.RecommendFromComputeOptimizer, Kind: spec.RecommendIdle,
		Account: aws.ToString(rec.AccountId), Region: region, ResourceKey: arn,
		ResourceName: firstNonEmptyString(aws.ToString(rec.ResourceId), lastSegment(arn)), ResourceType: typ,
		Action: action, Currency: currency, MonthlySavingsMicros: savings, Effort: "low", Risk: "low",
		Evidence: evidence,
	}, true
}

func awsIdleNames(resourceType string) (string, string) {
	switch resourceType {
	case "EC2Instance":
		return "EC2 instance", "aws_ec2_instance"
	case "EBSVolume":
		return "EBS volume", "aws_ebs_volume"
	case "AutoScalingGroup":
		return "Auto Scaling group", "aws_autoscaling_group"
	case "ECSService":
		return "ECS service", "aws_ecs_service"
	case "RDSDBInstance":
		return "RDS instance", "aws_rds_instance"
	}
	return resourceType, "aws_" + strings.ToLower(resourceType)
}

// awsChecksRegion runs Obzly's own checks: unattached volumes, gp2 volumes and unused Elastic
// IPs. Compute Optimizer's recommendation wins where both have one.
func awsChecksRegion(ctx context.Context, account awsAccount, region string, c *adviceCollector) {
	client := ec2.NewFromConfig(awsRegionConfig(account, region))
	fail := func(action string, err error) bool {
		switch {
		case err == nil:
			return false
		case isAWSDenied(err):
			c.section(spec.AdviceChecks, spec.AdviceNeedsPermission, action+" denied in "+region)
		default:
			c.section(spec.AdviceChecks, spec.AdviceError, action+" in "+region+": "+err.Error())
		}
		return true
	}
	paginator := ec2.NewDescribeVolumesPaginator(client, &ec2.DescribeVolumesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("status"), Values: []string{"available", "in-use"}},
		},
	})
	for checked := 0; paginator.HasMorePages() && checked < adviceMaxPerRegion; {
		page, err := paginator.NextPage(ctx)
		if fail("ec2:DescribeVolumes", err) {
			return
		}
		for _, volume := range page.Volumes {
			checked++
			if rec, ok := awsVolumeCheck(volume, account.id, region); ok {
				c.addRecs(rec)
			}
		}
	}
	addresses, err := client.DescribeAddresses(ctx, &ec2.DescribeAddressesInput{})
	if fail("ec2:DescribeAddresses", err) {
		return
	}
	for _, address := range addresses.Addresses {
		if aws.ToString(address.AssociationId) != "" || aws.ToString(address.AllocationId) == "" {
			continue
		}
		id := aws.ToString(address.AllocationId)
		arn := fmt.Sprintf("arn:aws:ec2:%s:%s:elastic-ip/%s", region, account.id, id)
		c.addRecs(spec.CostRecommendation{
			Key: "obzly:eip:" + arn, Source: spec.RecommendFromObzly, Kind: spec.RecommendIdle, Account: account.id,
			Region: region, ResourceKey: arn, ResourceName: aws.ToString(address.PublicIp),
			ResourceType: "aws_elastic_ip", Action: "Release the unused Elastic IP",
			Currency: "USD", MonthlySavingsMicros: awsPublicIPv4MonthlyMicros, Effort: "low", Risk: "low",
			Evidence: map[string]string{"public_ip": aws.ToString(address.PublicIp), "associated": "false",
				"price": "public IPv4: $0.005 an hour"},
		})
	}
}

func awsVolumeCheck(volume ec2types.Volume, account, region string) (spec.CostRecommendation, bool) {
	id := aws.ToString(volume.VolumeId)
	arn := fmt.Sprintf("arn:aws:ec2:%s:%s:volume/%s", region, account, id)
	size := aws.ToInt32(volume.Size)
	evidence := map[string]string{"volume_type": string(volume.VolumeType), "size_gib": strconv.Itoa(int(size)),
		"state": string(volume.State)}
	if volume.CreateTime != nil {
		evidence["created"] = volume.CreateTime.UTC().Format(time.RFC3339)
	}
	name := id
	for _, tag := range volume.Tags {
		if aws.ToString(tag.Key) == "Name" && aws.ToString(tag.Value) != "" {
			name = aws.ToString(tag.Value)
		}
	}
	rec := spec.CostRecommendation{Source: spec.RecommendFromObzly, Account: account, Region: region,
		ResourceKey: arn, ResourceName: name, ResourceType: "aws_ebs_volume", Effort: "low", Risk: "low",
		Evidence: evidence}
	switch {
	case volume.State == ec2types.VolumeStateAvailable:
		rec.Key, rec.Kind, rec.SavingsShare = "obzly:ebs-unattached:"+arn, spec.RecommendIdle, 1
		rec.Action = "Delete the unattached EBS volume (snapshot it first if you need the data)"
	case volume.VolumeType == ec2types.VolumeTypeGp2:
		rec.Key, rec.Kind, rec.SavingsShare = "obzly:ebs-gp3:"+arn, spec.RecommendModernize, gp3SavingsShare
		rec.Action = fmt.Sprintf("Change gp2 %d GiB to gp3 %d GiB", size, size)
		rec.Current, rec.Target = fmt.Sprintf("gp2 %d GiB", size), fmt.Sprintf("gp3 %d GiB", size)
	default:
		return spec.CostRecommendation{}, false
	}
	return rec, true
}

// call runs one Cost Explorer request; false stops the section (a denial won't change on the
// next call, and each call costs money).
func (ce *awsCE) call(section, action string, err error) bool {
	ce.c.request()
	switch {
	case err == nil:
		return true
	case isDataUnavailable(err):
		// Nothing for the period (e.g. no Savings Plans bought); the caller carries on.
	case isAWSDenied(err):
		ce.c.section(section, spec.AdviceNeedsPermission, action+" denied")
	default:
		ce.c.section(section, spec.AdviceError, action+": "+err.Error())
	}
	return false
}

// commitments reads existing Savings Plans and reservations with their use over the last 30
// days, and how much eligible usage they covered.
func (ce *awsCE) commitments(ctx context.Context) {
	today := utcDay(time.Now())
	period := ceRange(today.AddDate(0, 0, -30), today.AddDate(0, 0, -1))
	section := spec.AdviceCommitments
	input := &costexplorer.GetSavingsPlansUtilizationDetailsInput{TimePeriod: period}
	for {
		output, err := ce.client.GetSavingsPlansUtilizationDetails(ctx, input)
		if !ce.call(section, "ce:GetSavingsPlansUtilizationDetails", err) {
			if isDataUnavailable(err) {
				break
			}
			return
		}
		for _, detail := range output.SavingsPlansUtilizationDetails {
			ce.c.addCommitments(savingsPlanCommitment(detail))
		}
		if aws.ToString(output.NextToken) == "" {
			break
		}
		input.NextToken = output.NextToken
	}
	for _, service := range []struct{ name, kind string }{{ceServiceEC2, spec.CommitEC2RI},
		{ceServiceRDS, spec.CommitRDSRI}} {
		output, err := ce.client.GetReservationUtilization(ctx, &costexplorer.GetReservationUtilizationInput{
			TimePeriod: period,
			GroupBy: []cetypes.GroupDefinition{
				{Type: cetypes.GroupDefinitionTypeDimension, Key: aws.String("SUBSCRIPTION_ID")},
			},
			Filter: serviceFilter(service.name),
		})
		if !ce.call(section, "ce:GetReservationUtilization", err) {
			if isDataUnavailable(err) {
				continue
			}
			return
		}
		for _, byTime := range output.UtilizationsByTime {
			for _, group := range byTime.Groups {
				ce.c.addCommitments(reservationCommitment(group, service.kind))
			}
		}
		coverage, err := ce.client.GetReservationCoverage(ctx, &costexplorer.GetReservationCoverageInput{
			TimePeriod: period, Filter: serviceFilter(service.name),
		})
		if !ce.call(section, "ce:GetReservationCoverage", err) {
			if isDataUnavailable(err) {
				continue
			}
			return
		}
		if total := coverage.Total; total != nil && total.CoverageHours != nil {
			percent, _ := strconv.ParseFloat(aws.ToString(total.CoverageHours.CoverageHoursPercentage), 64)
			onDemand := int64(0)
			if total.CoverageCost != nil {
				onDemand = monthlyFrom(textMicros(aws.ToString(total.CoverageCost.OnDemandCost)), 30)
			}
			ce.c.addCoverage(spec.CommitmentCoverage{Type: service.kind, Currency: "USD", CoveredPercent: percent,
				OnDemandMonthlyMicros: onDemand})
		}
	}
	output, err := ce.client.GetSavingsPlansCoverage(ctx, &costexplorer.GetSavingsPlansCoverageInput{
		TimePeriod: period, Granularity: cetypes.GranularityMonthly,
	})
	if !ce.call(section, "ce:GetSavingsPlansCoverage", err) {
		if !isDataUnavailable(err) {
			return
		}
	} else {
		var covered, total, onDemand int64
		for _, row := range output.SavingsPlansCoverages {
			if row.Coverage == nil {
				continue
			}
			covered += textMicros(aws.ToString(row.Coverage.SpendCoveredBySavingsPlans))
			total += textMicros(aws.ToString(row.Coverage.TotalCost))
			onDemand += textMicros(aws.ToString(row.Coverage.OnDemandCost))
		}
		if total > 0 {
			ce.c.addCoverage(spec.CommitmentCoverage{
				Type:     spec.CommitComputeSP,
				Currency: "USD",
				CoveredPercent: float64(
					covered,
				) * 100 / float64(
					total,
				),
				OnDemandMonthlyMicros: monthlyFrom(onDemand, 30),
			})
		}
	}
	ce.c.section(section, spec.AdviceOK, "")
}

// isDataUnavailable: Cost Explorer has nothing for the period (e.g. no Savings Plans bought).
func isDataUnavailable(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode() == "DataUnavailableException"
}

func serviceFilter(service string) *cetypes.Expression {
	return &cetypes.Expression{Dimensions: &cetypes.DimensionValues{Key: cetypes.DimensionService,
		Values: []string{service}}}
}

func savingsPlanCommitment(detail cetypes.SavingsPlansUtilizationDetail) spec.Commitment {
	attributes := detail.Attributes
	arn := aws.ToString(detail.SavingsPlanArn)
	kind := spec.CommitComputeSP
	if strings.Contains(strings.ToLower(attr(attributes, "SavingsPlansType")), "instance") {
		kind = spec.CommitEC2SP
	}
	description := "Compute Savings Plan"
	if kind == spec.CommitEC2SP {
		description = strings.TrimSpace("EC2 Instance Savings Plan " + attr(attributes, "InstanceFamily") + " " +
			attr(attributes, "Region"))
	}
	if hourly := attr(attributes, "RecurringHourlyFee"); hourly != "" {
		description += ", " + hourly + " USD an hour"
	}
	commitment := spec.Commitment{
		ID: arn, Type: kind, Account: attr(attributes, "AccountId"), Region: attr(attributes, "Region"),
		Description: description, Term: ceTerm(attr(attributes, "PurchaseTerm")),
		Payment: cePayment(attr(attributes, "PaymentOption")), Start: ceTime(attr(attributes, "StartDateTime")),
		End: ceTime(attr(attributes, "EndDateTime")), Currency: "USD",
	}
	if detail.AmortizedCommitment != nil {
		commitment.MonthlyCostMicros = monthlyFrom(
			textMicros(aws.ToString(detail.AmortizedCommitment.TotalAmortizedCommitment)), 30)
	}
	if detail.Utilization != nil {
		if percent, err := strconv.ParseFloat(aws.ToString(detail.Utilization.UtilizationPercentage), 64); err == nil {
			commitment.UtilizationPercent = &percent
		}
		commitment.UnusedMonthlyMicros = monthlyFrom(textMicros(aws.ToString(detail.Utilization.UnusedCommitment)), 30)
	}
	return commitment
}

func reservationCommitment(group cetypes.ReservationUtilizationGroup, kind string) spec.Commitment {
	attributes := group.Attributes
	what := "EC2 reservation"
	if kind == spec.CommitRDSRI {
		what = "RDS reservation"
	}
	description := what
	if instance := attr(attributes, "instanceType"); instance != "" {
		count := attr(attributes, "numberOfInstances")
		if count == "" {
			count = "1"
		}
		description = fmt.Sprintf("%s: %s × %s", what, count, instance)
	}
	commitment := spec.Commitment{
		ID: firstNonEmptyString(aws.ToString(group.Value), attr(attributes, "leaseId")), Type: kind,
		Account: attr(attributes, "accountId"), Region: attr(attributes, "region"), Description: description,
		Start: ceTime(attr(attributes, "startDateTime")), End: ceTime(attr(attributes, "endDateTime")),
		Currency: "USD",
	}
	if aggregates := group.Utilization; aggregates != nil {
		if percent, err := strconv.ParseFloat(aws.ToString(aggregates.UtilizationPercentage), 64); err == nil {
			commitment.UtilizationPercent = &percent
		}
		commitment.MonthlyCostMicros = monthlyFrom(textMicros(aws.ToString(aggregates.TotalAmortizedFee)), 30)
		commitment.UnusedMonthlyMicros = monthlyFrom(textMicros(aws.ToString(aggregates.RICostForUnusedHours)), 30)
	}
	return commitment
}

// planner reads the providers' purchase recommendations for every type, term, payment and
// look-back the planner compares: 69 Cost Explorer requests a week.
func (ce *awsCE) planner(ctx context.Context) {
	section := spec.AdvicePlanner
	for _, plan := range []struct {
		kind string
		ce   cetypes.SupportedSavingsPlansType
	}{{spec.CommitComputeSP, cetypes.SupportedSavingsPlansTypeComputeSp},
		{spec.CommitEC2SP, cetypes.SupportedSavingsPlansTypeEc2InstanceSp}} {
		for _, term := range awsTerms {
			for _, payment := range awsPayments {
				for _, days := range spec.AdviceLookbacks {
					output, err := ce.client.GetSavingsPlansPurchaseRecommendation(ctx,
						&costexplorer.GetSavingsPlansPurchaseRecommendationInput{
							SavingsPlansType: plan.ce, TermInYears: term.ce, PaymentOption: payment.ce,
							LookbackPeriodInDays: awsLookbacks[days], AccountScope: ce.scope,
						})
					if !ce.call(section, "ce:GetSavingsPlansPurchaseRecommendation", err) {
						if !isDataUnavailable(err) {
							return
						}
						output = &costexplorer.GetSavingsPlansPurchaseRecommendationOutput{}
					}
					ce.c.addOption(savingsPlanOption(plan.kind, term.term, payment.payment, days, output))
				}
			}
		}
	}
	for _, service := range []struct{ name, kind string }{{ceServiceEC2, spec.CommitEC2RI},
		{ceServiceRDS, spec.CommitRDSRI}} {
		for _, term := range awsTerms {
			for _, payment := range awsPayments {
				for _, days := range spec.AdviceLookbacks {
					option := spec.PurchaseOption{Type: service.kind, Term: term.term, Payment: payment.payment,
						LookbackDays: days, Offered: true}
					if service.kind == spec.CommitRDSRI && term.term == spec.Term3Years &&
						payment.payment == spec.PayNoUpfront {
						option.Offered = false
						option.Reason = "AWS doesn't sell 3-year RDS reservations without upfront"
						ce.c.addOption(option)
						continue
					}
					output, err := ce.client.GetReservationPurchaseRecommendation(ctx,
						&costexplorer.GetReservationPurchaseRecommendationInput{
							Service: aws.String(service.name), TermInYears: term.ce, PaymentOption: payment.ce,
							LookbackPeriodInDays: awsLookbacks[days], AccountScope: ce.scope,
						})
					if !ce.call(section, "ce:GetReservationPurchaseRecommendation", err) {
						if !isDataUnavailable(err) {
							return
						}
						output = &costexplorer.GetReservationPurchaseRecommendationOutput{}
					}
					ce.c.addOption(reservationOption(option, output))
				}
			}
		}
	}
	ce.c.section(section, spec.AdviceOK, "")
}

func savingsPlanOption(
	kind, term, payment string, days int, output *costexplorer.GetSavingsPlansPurchaseRecommendationOutput,
) spec.PurchaseOption {
	option := spec.PurchaseOption{Type: kind, Term: term, Payment: payment, LookbackDays: days, Offered: true}
	rec := output.SavingsPlansPurchaseRecommendation
	if rec == nil || rec.SavingsPlansPurchaseRecommendationSummary == nil {
		option.Reason = "No purchase recommended for this usage"
		return option
	}
	summary := rec.SavingsPlansPurchaseRecommendationSummary
	option.Currency = firstNonEmptyString(aws.ToString(summary.CurrencyCode), "USD")
	option.HourlyCommitmentMicros = textMicros(aws.ToString(summary.HourlyCommitmentToPurchase))
	option.MonthlySavingsMicros = textMicros(aws.ToString(summary.EstimatedMonthlySavingsAmount))
	option.SavingsPercent, _ = strconv.ParseFloat(aws.ToString(summary.EstimatedSavingsPercentage), 64)
	// CurrentOnDemandSpend covers the look-back period.
	option.OnDemandMonthlyMicros = monthlyFrom(textMicros(aws.ToString(summary.CurrentOnDemandSpend)), days)
	if option.HourlyCommitmentMicros <= 0 {
		option.Reason = "No purchase recommended for this usage"
		return option
	}
	option.Commitment = fmt.Sprintf("%s an hour", decimalText(option.HourlyCommitmentMicros))
	var utilization float64
	for _, detail := range rec.SavingsPlansPurchaseRecommendationDetails {
		upfront := textMicros(aws.ToString(detail.UpfrontCost))
		option.UpfrontMicros += upfront
		if value, err := strconv.ParseFloat(aws.ToString(detail.EstimatedAverageUtilization), 64); err == nil {
			utilization = max(utilization, value)
		}
		line := spec.PurchaseLine{Account: aws.ToString(detail.AccountId),
			MonthlySavingsMicros: textMicros(aws.ToString(detail.EstimatedMonthlySavingsAmount)),
			UpfrontMicros:        upfront,
			Quantity:             decimalText(textMicros(aws.ToString(detail.HourlyCommitmentToPurchase))) + " an hour"}
		line.Description = "Compute Savings Plan"
		if plan := detail.SavingsPlansDetails; plan != nil && aws.ToString(plan.InstanceFamily) != "" {
			line.Description = aws.ToString(plan.InstanceFamily) + " family"
			line.Region = aws.ToString(plan.Region)
		}
		if len(option.Lines) < 50 {
			option.Lines = append(option.Lines, line)
		}
	}
	option.AverageUtilization = utilization
	termMonths := 12.0
	if term == spec.Term3Years {
		termMonths = 36
	}
	option.MonthlyRecurringMicros = max(0, option.HourlyCommitmentMicros*hoursPerMonth-
		int64(float64(option.UpfrontMicros)/termMonths))
	option.BreakEvenMonths = breakEven(option.UpfrontMicros, option.MonthlySavingsMicros, termMonths)
	return option
}

func reservationOption(
	option spec.PurchaseOption, output *costexplorer.GetReservationPurchaseRecommendationOutput,
) spec.PurchaseOption {
	var breakEven float64
	for _, rec := range output.Recommendations {
		for _, detail := range rec.RecommendationDetails {
			option.Currency = firstNonEmptyString(option.Currency, aws.ToString(detail.CurrencyCode), "USD")
			upfront := textMicros(aws.ToString(detail.UpfrontCost))
			savings := textMicros(aws.ToString(detail.EstimatedMonthlySavingsAmount))
			option.UpfrontMicros += upfront
			option.MonthlyRecurringMicros += textMicros(aws.ToString(detail.RecurringStandardMonthlyCost))
			option.MonthlySavingsMicros += savings
			option.OnDemandMonthlyMicros += textMicros(aws.ToString(detail.EstimatedMonthlyOnDemandCost))
			if value, err := strconv.ParseFloat(aws.ToString(detail.EstimatedBreakEvenInMonths), 64); err == nil {
				breakEven = max(breakEven, value)
			}
			if value, err := strconv.ParseFloat(aws.ToString(detail.AverageUtilization), 64); err == nil {
				option.AverageUtilization = max(option.AverageUtilization, value)
			}
			instance, region := reservationInstance(detail.InstanceDetails)
			count := aws.ToString(detail.RecommendedNumberOfInstancesToPurchase)
			if len(option.Lines) < 50 {
				option.Lines = append(option.Lines, spec.PurchaseLine{
					Description: fmt.Sprintf("%s × %s", count, instance), Account: aws.ToString(detail.AccountId),
					Region: region, Quantity: count, MonthlySavingsMicros: savings, UpfrontMicros: upfront,
				})
			}
		}
	}
	if option.MonthlySavingsMicros <= 0 {
		option.Reason = "No purchase recommended for this usage"
		return option
	}
	if option.OnDemandMonthlyMicros > 0 {
		option.SavingsPercent = float64(option.MonthlySavingsMicros) * 100 / float64(option.OnDemandMonthlyMicros)
	}
	option.BreakEvenMonths = breakEven
	if len(option.Lines) == 1 {
		option.Commitment = option.Lines[0].Description + " in " + option.Lines[0].Region
	} else {
		option.Commitment = fmt.Sprintf("%d reservations", len(option.Lines))
	}
	return option
}

func reservationInstance(details *cetypes.InstanceDetails) (string, string) {
	switch {
	case details == nil:
		return "instances", ""
	case details.EC2InstanceDetails != nil:
		return aws.ToString(details.EC2InstanceDetails.InstanceType), aws.ToString(details.EC2InstanceDetails.Region)
	case details.RDSInstanceDetails != nil:
		return strings.TrimSpace(aws.ToString(details.RDSInstanceDetails.InstanceType) + " " +
			aws.ToString(details.RDSInstanceDetails.DatabaseEngine)), aws.ToString(details.RDSInstanceDetails.Region)
	}
	return "instances", ""
}

// breakEven: the month the upfront payment is paid back by the savings (0 without upfront).
func breakEven(upfront, monthlySavings int64, termMonths float64) float64 {
	if upfront <= 0 || monthlySavings <= 0 {
		return 0
	}
	// Monthly savings already count the upfront spread over the term; add it back to compare.
	gross := float64(monthlySavings) + float64(upfront)/termMonths
	return float64(upfront) / gross
}

func attr(attributes map[string]string, name string) string {
	if value, ok := attributes[name]; ok {
		return value
	}
	for key, value := range attributes {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

func ceTerm(value string) string {
	switch {
	case strings.HasPrefix(value, "1"):
		return spec.Term1Year
	case strings.HasPrefix(value, "3"):
		return spec.Term3Years
	}
	return ""
}

func cePayment(value string) string {
	switch strings.ToLower(strings.ReplaceAll(value, " ", "")) {
	case "noupfront":
		return spec.PayNoUpfront
	case "partialupfront":
		return spec.PayPartial
	case "allupfront":
		return spec.PayAllUpfront
	}
	return ""
}

func ceTime(value string) *time.Time {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05.000Z", "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			parsed = parsed.UTC()
			return &parsed
		}
	}
	return nil
}

// decimalText shows millionths as an amount with two decimals (four below one).
func decimalText(micros int64) string {
	value := float64(micros) / 1_000_000
	if value < 1 {
		return strconv.FormatFloat(value, 'f', 4, 64)
	}
	return strconv.FormatFloat(value, 'f', 2, 64)
}

func lastSegment(arn string) string {
	if i := strings.LastIndexAny(arn, "/:"); i >= 0 {
		return arn[i+1:]
	}
	return arn
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
