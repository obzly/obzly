package discovery

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"obzly/internal/synthetics/spec"
)

const (
	azureCostAPIVersion  = "2023-11-01"
	azureBlobAPIVersion  = "2023-11-03"
	azureBlobHost        = "https://%s.blob.core.windows.net"
	azureCostQueryAction = "Microsoft.CostManagement/query/action (Cost Management Reader)"
	azureBlobReadAction  = "Microsoft.Storage/storageAccounts/blobServices/containers/blobs/read " +
		"(Storage Blob Data Reader on the export container)"
)

// azureExportRun finds a cost export file's billing period: exports write under a
// YYYYMMDD-YYYYMMDD folder per period.
var azureExportRun = regexp.MustCompile(`/(\d{4})(\d{2})\d{2}-\d{8}/`)

type azureCostReader struct {
	connection spec.CloudConnection
	options    Options
	client     *http.Client
	token      string
	storage    string
	t          *tracker
	info       *spec.CostScanInfo
}

func newAzureCostReader(
	ctx context.Context, connection spec.CloudConnection, options Options, t *tracker, info *spec.CostScanInfo,
) costReader {
	token, err := azureToken(ctx, connection, options, options.HTTP)
	if err != nil {
		t.fail(fmt.Errorf("couldn't sign in to Azure: %w", err))
		return nil
	}
	return &azureCostReader{connection: connection, options: options, client: options.HTTP, token: token, t: t,
		info: info}
}

func (r *azureCostReader) billingAccount() string { return r.connection.ScopeID }

// scope is the connection's Cost Management scope.
func (r *azureCostReader) scope() string {
	if r.connection.Scope == "management_group" {
		return "/providers/Microsoft.Management/managementGroups/" + url.PathEscape(r.connection.ScopeID)
	}
	return "/subscriptions/" + url.PathEscape(r.connection.ScopeID)
}

type azureCostQuery struct {
	Type       string            `json:"type"`
	Timeframe  string            `json:"timeframe"`
	TimePeriod map[string]string `json:"timePeriod"`
	Dataset    azureCostDataset  `json:"dataset"`
	// Forecast only.
	IncludeActualCost       *bool `json:"includeActualCost,omitempty"`
	IncludeFreshPartialCost *bool `json:"includeFreshPartialCost,omitempty"`
}

type azureCostDataset struct {
	Granularity string                       `json:"granularity"`
	Aggregation map[string]map[string]string `json:"aggregation"`
	Grouping    []map[string]string          `json:"grouping,omitempty"`
}

type azureCostAnswer struct {
	Properties struct {
		NextLink string `json:"nextLink"`
		Columns  []struct {
			Name string `json:"name"`
		} `json:"columns"`
		Rows [][]any `json:"rows"`
	} `json:"properties"`
}

// api reads actual cost (billed) and amortized cost (effective), daily, by service and region
// (by service and subscription for a management group).
func (r *azureCostReader) api(ctx context.Context, from, to time.Time, a *costAggregator) error {
	grouping := []map[string]string{{"type": "Dimension", "name": "ServiceName"}}
	if r.connection.Scope == "management_group" {
		grouping = append(grouping, map[string]string{"type": "Dimension", "name": "SubscriptionId"})
	} else {
		grouping = append(grouping, map[string]string{"type": "Dimension", "name": "ResourceLocation"})
	}
	for _, kind := range []string{"ActualCost", "AmortizedCost"} {
		query := azureCostQuery{
			Type: kind, Timeframe: "Custom",
			TimePeriod: map[string]string{
				"from": from.Format(dayLayout) + "T00:00:00Z", "to": to.Format(dayLayout) + "T23:59:59Z",
			},
			Dataset: azureCostDataset{
				Granularity: "Daily",
				Aggregation: map[string]map[string]string{"totalCost": {"name": "Cost", "function": "Sum"}},
				Grouping:    grouping,
			},
		}
		endpoint := orDefault(r.options.AzureManagement, azureManagement) + r.scope() +
			"/providers/Microsoft.CostManagement/query?api-version=" + azureCostAPIVersion
		for endpoint != "" {
			var answer azureCostAnswer
			r.info.Requests++
			status, err := azureJSON(ctx, r.client, http.MethodPost, endpoint, r.token, query, &answer)
			if !r.t.call(
				azureCostQueryAction,
				err,
				status == http.StatusForbidden || status == http.StatusUnauthorized,
			) {
				return err
			}
			if err := r.addRows(answer, kind == "AmortizedCost", a); err != nil {
				return err
			}
			endpoint = answer.Properties.NextLink
		}
	}
	return nil
}

func (r *azureCostReader) addRows(answer azureCostAnswer, amortized bool, a *costAggregator) error {
	index := map[string]int{}
	for i, column := range answer.Properties.Columns {
		index[column.Name] = i
	}
	get := func(row []any, name string) string {
		if i, ok := index[name]; ok && i < len(row) && row[i] != nil {
			switch value := row[i].(type) {
			case float64:
				return strconv.FormatFloat(value, 'f', -1, 64)
			case string:
				return value
			default:
				return fmt.Sprint(value)
			}
		}
		return ""
	}
	for _, row := range answer.Properties.Rows {
		amount, err := parseMicros(firstNonEmpty(get(row, "Cost"), get(row, "PreTaxCost")))
		if err != nil {
			return err
		}
		record := costRecord{
			day:            azureUsageDay(get(row, "UsageDate")),
			billingAccount: r.connection.ScopeID,
			subAccount: firstNonEmpty(
				get(row, "SubscriptionId"),
				r.connection.ScopeID,
			),
			service:  get(row, "ServiceName"),
			region:   get(row, "ResourceLocation"),
			currency: get(row, "Currency"),
		}
		if amortized {
			record.effective = amount
		} else {
			record.billed = amount
		}
		a.add(record)
	}
	return nil
}

// forecast is Cost Management's amortized forecast for the rest of the month.
func (r *azureCostReader) forecast(ctx context.Context, today time.Time) ([]spec.CostForecast, error) {
	end := time.Date(today.Year(), today.Month()+1, 0, 0, 0, 0, 0, time.UTC)
	no := false
	query := azureCostQuery{
		Type: "AmortizedCost", Timeframe: "Custom",
		TimePeriod: map[string]string{
			"from": today.Format(dayLayout) + "T00:00:00Z", "to": end.Format(dayLayout) + "T23:59:59Z",
		},
		Dataset: azureCostDataset{
			Granularity: "Daily",
			Aggregation: map[string]map[string]string{"totalCost": {"name": "Cost", "function": "Sum"}},
		},
		IncludeActualCost: &no, IncludeFreshPartialCost: &no,
	}
	endpoint := orDefault(r.options.AzureManagement, azureManagement) + r.scope() +
		"/providers/Microsoft.CostManagement/forecast?api-version=" + azureCostAPIVersion
	var answer azureCostAnswer
	r.info.Requests++
	status, err := azureJSON(ctx, r.client, http.MethodPost, endpoint, r.token, query, &answer)
	if !r.t.call(azureCostQueryAction, err, status == http.StatusForbidden || status == http.StatusUnauthorized) {
		return nil, err
	}
	sums := newCostAggregator(nil, spec.ProviderAzure)
	if err := r.addRows(answer, true, sums); err != nil {
		return nil, err
	}
	byCurrency := map[string]int64{}
	for key, sum := range sums.sums {
		byCurrency[key.currency] += sum.effective
	}
	var forecast []spec.CostForecast
	for currency, remaining := range byCurrency {
		forecast = append(forecast, spec.CostForecast{
			Month: today.Format("2006-01"), Currency: currency, RemainingMicros: remaining,
		})
	}
	return forecast, nil
}

type azureBlobList struct {
	Blobs []struct {
		Name       string `xml:"Name"`
		Properties struct {
			LastModified  string `xml:"Last-Modified"`
			ETag          string `xml:"Etag"`
			ContentLength int64  `xml:"Content-Length"`
		} `xml:"Properties"`
	} `xml:"Blobs>Blob"`
	NextMarker string `xml:"NextMarker"`
}

func (r *azureCostReader) exportFiles(ctx context.Context, export *spec.CostExport) ([]costFile, error) {
	if err := r.storageToken(ctx); err != nil {
		return nil, err
	}
	prefix := export.Prefix
	if prefix != "" {
		prefix += "/"
	}
	var files []costFile
	marker := ""
	for {
		query := url.Values{"restype": {"container"}, "comp": {"list"}, "prefix": {prefix}, "maxresults": {"5000"}}
		if marker != "" {
			query.Set("marker", marker)
		}
		var list azureBlobList
		status, err := r.blobGet(ctx, r.containerURL(export)+"?"+query.Encode(), func(body io.Reader) error {
			return xml.NewDecoder(body).Decode(&list)
		})
		if !r.t.call(azureBlobReadAction, err, status == http.StatusForbidden || status == http.StatusUnauthorized) {
			return nil, err
		}
		for _, blob := range list.Blobs {
			match := azureExportRun.FindStringSubmatch("/" + blob.Name)
			if match == nil || !isExportData(blob.Name) {
				continue
			}
			modified, _ := http.ParseTime(blob.Properties.LastModified)
			files = append(files, costFile{
				Key: blob.Name, Dir: path.Dir(blob.Name), Period: match[1] + "-" + match[2],
				Size: blob.Properties.ContentLength, Modified: modified.UTC(),
				ETag: strings.Trim(blob.Properties.ETag, `"`),
			})
		}
		if list.NextMarker == "" {
			return files, nil
		}
		marker = list.NextMarker
	}
}

func (r *azureCostReader) openFile(ctx context.Context, export *spec.CostExport, file costFile) (io.ReadCloser, error) {
	if err := r.storageToken(ctx); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		r.containerURL(export)+"/"+escapeBlobPath(file.Key), nil)
	if err != nil {
		return nil, err
	}
	r.blobHeaders(request)
	response, err := r.client.Do(request)
	if err == nil && response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		_ = response.Body.Close()
		err = fmt.Errorf("reading %s answered %d: %s", file.Key, response.StatusCode, azureError(data))
		r.t.call(azureBlobReadAction, err, response.StatusCode == http.StatusForbidden)
		return nil, err
	}
	if !r.t.call(azureBlobReadAction, err, false) {
		return nil, err
	}
	return response.Body, nil
}

// storageToken signs in for Blob storage once per refresh.
func (r *azureCostReader) storageToken(ctx context.Context) error {
	if r.storage != "" {
		return nil
	}
	token, err := azureTokenFor(ctx, r.connection, r.options, r.client, azureStorageResource)
	if !r.t.call("a token for Azure Storage", err, false) {
		return err
	}
	r.storage = token
	return nil
}

func (r *azureCostReader) containerURL(export *spec.CostExport) string {
	host := fmt.Sprintf(azureBlobHost, export.StorageAccount)
	if r.options.AzureBlob != "" {
		host = strings.ReplaceAll(r.options.AzureBlob, "{account}", export.StorageAccount)
	}
	return host + "/" + url.PathEscape(export.Container)
}

func (r *azureCostReader) blobHeaders(request *http.Request) {
	request.Header.Set("Authorization", "Bearer "+r.storage)
	request.Header.Set("x-ms-version", azureBlobAPIVersion)
}

func (r *azureCostReader) blobGet(ctx context.Context, endpoint string, read func(io.Reader) error) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	r.blobHeaders(request)
	response, err := r.client.Do(request)
	if err != nil {
		return 0, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return response.StatusCode, fmt.Errorf("listing the export answered %d: %s", response.StatusCode,
			azureError(data))
	}
	return response.StatusCode, read(io.LimitReader(response.Body, 64<<20))
}

// azureUsageDay reads Cost Management's UsageDate, a number like 20261101.
func azureUsageDay(value string) string {
	value = strings.TrimSuffix(value, ".0")
	if len(value) == 8 {
		return value[:4] + "-" + value[4:6] + "-" + value[6:]
	}
	return dayOf(value)
}

func escapeBlobPath(name string) string {
	parts := strings.Split(name, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}
