package discovery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"obzly/internal/synthetics/spec"
)

// awsBillingPeriod finds a Data Exports file's billing period in its key.
var awsBillingPeriod = regexp.MustCompile(`BILLING_PERIOD=(\d{4}-\d{2})`)

// awsCostReader reads the connection's own account: for the billing scope, the management
// account, whose Cost Explorer sees every member.
type awsCostReader struct {
	connection spec.CloudConnection
	account    awsAccount
	t          *tracker
	info       *spec.CostScanInfo
	// pathStyle: a custom endpoint (tests) can't take the bucket in its host name.
	pathStyle bool
}

func newAWSCostReader(
	ctx context.Context, connection spec.CloudConnection, options Options, t *tracker, info *spec.CostScanInfo,
) costReader {
	base, err := awsBaseConfig(ctx, connection, options)
	if err != nil {
		t.fail(fmt.Errorf("couldn't set up AWS: %w", err))
		return nil
	}
	account, err := awsSignIn(ctx, connection, base, connection.Identity, options)
	if err != nil {
		if isAWSDenied(err) {
			t.failPermission(fmt.Errorf("couldn't sign in: %w", err), signInAction(connection))
		} else {
			t.fail(fmt.Errorf("couldn't sign in: %w", err))
		}
		return nil
	}
	return &awsCostReader{connection: connection, account: account, t: t, info: info,
		pathStyle: options.AWSEndpoint != ""}
}

func (r *awsCostReader) billingAccount() string { return r.account.id }

func (r *awsCostReader) s3(export *spec.CostExport) *s3.Client {
	return s3.NewFromConfig(awsRegionConfig(r.account, export.Region), func(o *s3.Options) {
		o.UsePathStyle = r.pathStyle
	})
}

func (r *awsCostReader) exportFiles(ctx context.Context, export *spec.CostExport) ([]costFile, error) {
	client := r.s3(export)
	prefix := export.Prefix
	if prefix != "" {
		prefix += "/"
	}
	var files []costFile
	paginator := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{
		Bucket: aws.String(export.Bucket), Prefix: aws.String(prefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if !r.t.call("s3:ListBucket on "+export.Bucket, err, isAWSDenied(err)) {
			return nil, err
		}
		for _, object := range page.Contents {
			key := aws.ToString(object.Key)
			match := awsBillingPeriod.FindStringSubmatch(key)
			if match == nil || !isExportData(key) {
				continue
			}
			files = append(files, costFile{
				Key: key, Dir: path.Dir(key), Period: match[1], Size: aws.ToInt64(object.Size),
				Modified: aws.ToTime(object.LastModified), ETag: strings.Trim(aws.ToString(object.ETag), `"`),
			})
		}
	}
	return files, nil
}

func (r *awsCostReader) openFile(ctx context.Context, export *spec.CostExport, file costFile) (io.ReadCloser, error) {
	client := r.s3(export)
	output, err := client.GetObject(
		ctx,
		&s3.GetObjectInput{Bucket: aws.String(export.Bucket), Key: aws.String(file.Key)},
	)
	if !r.t.call("s3:GetObject on "+export.Bucket, err, isAWSDenied(err)) {
		return nil, err
	}
	return output.Body, nil
}

// api reads Cost Explorer: per member account for the billing scope (each grouped by service
// and region), else the connection's account only. A management account without the filter
// would answer for the whole Organization.
func (r *awsCostReader) api(ctx context.Context, from, to time.Time, a *costAggregator) error {
	client := costexplorer.NewFromConfig(awsRegionConfig(r.account, awsDefaultRegion))
	accounts := map[string]string{r.account.id: ""}
	if r.connection.Cost != nil && r.connection.Cost.Scope == spec.CostScopeBilling {
		members, err := r.linkedAccounts(ctx, client, from, to)
		if err != nil {
			return err
		}
		accounts = members
	}
	for account, name := range accounts {
		if err := r.accountCost(ctx, client, account, name, from, to, a); err != nil {
			return err
		}
	}
	return nil
}

func (r *awsCostReader) linkedAccounts(
	ctx context.Context, client *costexplorer.Client, from, to time.Time,
) (map[string]string, error) {
	accounts := map[string]string{}
	input := &costexplorer.GetDimensionValuesInput{
		Dimension:  cetypes.DimensionLinkedAccount,
		TimePeriod: ceRange(from, to),
	}
	for {
		r.info.Requests++
		output, err := client.GetDimensionValues(ctx, input)
		if !r.t.call("ce:GetDimensionValues", err, isAWSDenied(err)) {
			return nil, err
		}
		for _, value := range output.DimensionValues {
			accounts[aws.ToString(value.Value)] = value.Attributes["description"]
		}
		if aws.ToString(output.NextPageToken) == "" {
			return accounts, nil
		}
		input.NextPageToken = output.NextPageToken
	}
}

func (r *awsCostReader) accountCost(
	ctx context.Context, client *costexplorer.Client, account, name string, from, to time.Time, a *costAggregator,
) error {
	input := &costexplorer.GetCostAndUsageInput{
		TimePeriod:  ceRange(from, to),
		Granularity: cetypes.GranularityDaily,
		Metrics:     []string{"UnblendedCost", "AmortizedCost"},
		GroupBy: []cetypes.GroupDefinition{
			{Type: cetypes.GroupDefinitionTypeDimension, Key: aws.String("SERVICE")},
			{Type: cetypes.GroupDefinitionTypeDimension, Key: aws.String("REGION")},
		},
		Filter: &cetypes.Expression{Dimensions: &cetypes.DimensionValues{
			Key: cetypes.DimensionLinkedAccount, Values: []string{account},
		}},
	}
	for {
		r.info.Requests++
		output, err := client.GetCostAndUsage(ctx, input)
		if !r.t.call("ce:GetCostAndUsage", err, isAWSDenied(err)) {
			return err
		}
		for _, period := range output.ResultsByTime {
			day := aws.ToString(period.TimePeriod.Start)
			for _, group := range period.Groups {
				service, region := "", ""
				if len(group.Keys) > 0 {
					service = group.Keys[0]
				}
				if len(group.Keys) > 1 && group.Keys[1] != "NoRegion" && group.Keys[1] != "global" {
					region = group.Keys[1]
				}
				unblended, amortized := group.Metrics["UnblendedCost"], group.Metrics["AmortizedCost"]
				billed, err := parseMicros(aws.ToString(unblended.Amount))
				if err != nil {
					return err
				}
				effective, err := parseMicros(aws.ToString(amortized.Amount))
				if err != nil {
					return err
				}
				a.add(costRecord{
					day: day, billingAccount: r.account.id, subAccount: account, subAccountName: name,
					service: service, region: region, currency: firstNonEmpty(aws.ToString(unblended.Unit),
						aws.ToString(amortized.Unit)),
					billed: billed, effective: effective,
				})
			}
		}
		if aws.ToString(output.NextPageToken) == "" {
			return nil
		}
		input.NextPageToken = output.NextPageToken
	}
}

// forecast is Cost Explorer's amortized forecast for the rest of the month.
func (r *awsCostReader) forecast(ctx context.Context, today time.Time) ([]spec.CostForecast, error) {
	client := costexplorer.NewFromConfig(awsRegionConfig(r.account, awsDefaultRegion))
	end := time.Date(today.Year(), today.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	input := &costexplorer.GetCostForecastInput{
		TimePeriod: &cetypes.DateInterval{
			Start: aws.String(today.Format(dayLayout)),
			End:   aws.String(end.Format(dayLayout)),
		},
		Metric:      cetypes.MetricAmortizedCost,
		Granularity: cetypes.GranularityMonthly,
	}
	if r.connection.Cost == nil || r.connection.Cost.Scope != spec.CostScopeBilling {
		input.Filter = &cetypes.Expression{Dimensions: &cetypes.DimensionValues{
			Key: cetypes.DimensionLinkedAccount, Values: []string{r.account.id},
		}}
	}
	r.info.Requests++
	output, err := client.GetCostForecast(ctx, input)
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode() == "DataUnavailableException" {
		// A new account has too little history to forecast.
		return nil, nil
	}
	if !r.t.call("ce:GetCostForecast", err, isAWSDenied(err)) {
		return nil, err
	}
	if output.Total == nil {
		return nil, nil
	}
	remaining, err := parseMicros(aws.ToString(output.Total.Amount))
	if err != nil {
		return nil, err
	}
	return []spec.CostForecast{{
		Month: today.Format("2006-01"), Currency: aws.ToString(output.Total.Unit), RemainingMicros: remaining,
	}}, nil
}

// ceRange is from..to (inclusive) in Cost Explorer's terms, whose end is exclusive.
func ceRange(from, to time.Time) *cetypes.DateInterval {
	return &cetypes.DateInterval{
		Start: aws.String(from.Format(dayLayout)), End: aws.String(to.AddDate(0, 0, 1).Format(dayLayout)),
	}
}

// isExportData: the export's data files, not its manifests or metadata.
func isExportData(key string) bool {
	lower := strings.ToLower(key)
	return strings.HasSuffix(lower, ".parquet") || strings.HasSuffix(lower, ".csv") ||
		strings.HasSuffix(lower, ".csv.gz")
}
