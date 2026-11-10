package discovery

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal128"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
	"github.com/stretchr/testify/require"

	"obzly/internal/synthetics/spec"
)

var costNow = time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)

func Test_ParseMicros_ReadsDecimalsExactly(t *testing.T) {
	cases := map[string]int64{
		"":                0,
		"0":               0,
		"12.5":            12_500_000,
		"-0.5":            -500_000,
		"0.0000004":       0,
		"0.0000005":       1,
		"123.4567894":     123_456_789,
		"123.4567895":     123_456_790,
		"1.2E-3":          1_200,
		"-1.0000005":      -1_000_001,
		"9876543210.1234": 9_876_543_210_123_400,
	}
	for value, want := range cases {
		got, err := parseMicros(value)
		require.NoError(t, err, value)
		require.Equal(t, want, got, value)
	}
	_, err := parseMicros("twelve")
	require.Error(t, err)
}

func Test_CostAggregator_KeepsTrackedTagsOnly_LowercasesAzureIDs_AndMarksRecentDaysEstimated(t *testing.T) {
	a := newCostAggregator([]string{"team"}, spec.ProviderAzure)
	record := costRecord{
		day: "2026-11-08", subAccount: "AAAA-Sub", service: "Virtual Machines", currency: "eur",
		resource: "/subscriptions/AAAA/resourceGroups/RG/providers/Microsoft.Compute/virtualMachines/VM1",
		billed:   1_000_000, effective: 900_000, tags: map[string]string{"Team": "analytics", "owner": "pat"},
	}
	a.add(record)
	a.add(record)
	record.day = "2026-11-01"
	a.add(record)

	rows := a.rows(costNow)
	require.Len(t, rows, 2)
	first, last := rows[0], rows[1]
	require.Equal(t, "2026-11-01", first.Day)
	require.False(t, first.Estimated)
	require.Equal(t, "2026-11-08", last.Day)
	require.True(t, last.Estimated)
	require.Equal(t, int64(2_000_000), last.BilledMicros)
	require.Equal(t, int64(1_800_000), last.EffectiveMicros)
	require.Equal(t, "EUR", last.Currency)
	require.Equal(t, "aaaa-sub", last.SubAccount)
	require.Equal(
		t,
		"/subscriptions/aaaa/resourcegroups/rg/providers/microsoft.compute/virtualmachines/vm1",
		last.Resource,
	)
	require.Equal(t, map[string]string{"team": "analytics"}, last.Tags)
}

func Test_ParseFocusCSV_SumsByDayAndDimensions_GzippedOrNot(t *testing.T) {
	csv := "ChargePeriodStart,SubAccountId,ServiceName,RegionId,ResourceId,ChargeCategory,BillingCurrency," +
		"BilledCost,EffectiveCost,Tags\n" +
		`2026-11-01T00:00:00Z,111111111111,Amazon EC2,us-east-1,arn:aws:ec2:i-1,Usage,USD,1.25,1.00,"{""user:team"":""checkout""}"` + "\n" +
		`2026-11-01T05:00:00Z,111111111111,Amazon EC2,us-east-1,arn:aws:ec2:i-1,Usage,USD,0.75,0.50,"{""user:team"":""checkout""}"` + "\n" +
		`2026-11-02T00:00:00Z,111111111111,Amazon S3,us-east-1,,Usage,USD,0.10,0.10,` + "\n"
	for _, name := range []string{"part-1.csv", "part-1.csv.gz"} {
		body := io.Reader(strings.NewReader(csv))
		if strings.HasSuffix(name, ".gz") {
			var zipped bytes.Buffer
			writer := gzip.NewWriter(&zipped)
			_, _ = writer.Write([]byte(csv))
			require.NoError(t, writer.Close())
			body = &zipped
		}
		a := newCostAggregator([]string{"team"}, spec.ProviderAWS)
		require.NoError(t, parseFocus(context.Background(), body, name, a), name)
		rows := a.rows(costNow)
		require.Len(t, rows, 2, name)
		require.Equal(t, "Amazon EC2", rows[0].Service)
		require.Equal(t, int64(2_000_000), rows[0].BilledMicros)
		require.Equal(t, int64(1_500_000), rows[0].EffectiveMicros)
		require.Equal(t, map[string]string{"team": "checkout"}, rows[0].Tags)
		require.Equal(t, "Amazon S3", rows[1].Service)
		require.Nil(t, rows[1].Tags)
	}
}

func Test_ParseFocusParquet_ReadsTimestampsDecimalsAndTagMaps(t *testing.T) {
	a := newCostAggregator([]string{"team"}, spec.ProviderAWS)
	require.NoError(t, parseFocus(context.Background(), bytes.NewReader(focusParquet(t)), "x.snappy.parquet", a))
	rows := a.rows(costNow)
	require.Len(t, rows, 1)
	row := rows[0]
	require.Equal(t, "2026-11-03", row.Day)
	require.Equal(t, "Amazon RDS", row.Service)
	require.Equal(t, int64(24_691_358), row.BilledMicros) // 2 x 12.3456789
	require.Equal(t, int64(20_500_000), row.EffectiveMicros)
	require.Equal(t, map[string]string{"team": "checkout"}, row.Tags)
}

func Test_CollectCost_AWSBillingScope_ReadsEachLinkedAccount_AndForecasts(t *testing.T) {
	isolateAWS(t)
	fake := newFakeCostExplorer(t)
	server := httptest.NewServer(fake)
	defer server.Close()

	result := CollectCost(context.Background(), awsCostConnection(spec.CostScopeBilling, spec.CostSourceAPI, nil),
		CostState{}, Options{AWSEndpoint: server.URL}, costNow)

	require.Equal(t, spec.UseOK, result.Scan.Uses[spec.UseCost].State, result.Scan.Errors)
	require.Equal(t, spec.ScanCost, result.Scan.Kind)
	require.Equal(t, []string{"111111111111", "222222222222"}, fake.filteredAccounts())
	require.Len(t, result.Deliveries, 1)
	batch := result.Deliveries[0].Batch
	require.Equal(t, spec.CostSourceAPI, batch.Source)
	require.Equal(t, "2026-08-11", batch.From, "a first read backfills 90 days")
	require.Equal(t, "2026-11-09", batch.To)
	require.Len(t, batch.Rows, 2)
	byAccount := map[string]spec.CostRow{}
	for _, row := range batch.Rows {
		byAccount[row.SubAccount] = row
	}
	require.Equal(t, "Prod", byAccount["111111111111"].SubAccountName)
	require.Equal(t, int64(12_500_000), byAccount["111111111111"].BilledMicros)
	require.Equal(t, int64(10_250_000), byAccount["111111111111"].EffectiveMicros)
	require.Equal(t, "us-east-1", byAccount["111111111111"].Region)
	require.Equal(t, "", byAccount["222222222222"].Region, "NoRegion is no region")
	require.Equal(
		t,
		[]spec.CostForecast{{Month: "2026-11", Currency: "USD", RemainingMicros: 345_678_000}},
		batch.Forecast,
	)
	require.Equal(t, 4, result.Scan.Cost.Requests, "the accounts, one read per account, the forecast")

	// The next refresh rereads only the last days.
	result = CollectCost(context.Background(), awsCostConnection(spec.CostScopeBilling, spec.CostSourceAPI, nil),
		CostState{APIBackfilled: true}, Options{AWSEndpoint: server.URL}, costNow)
	require.Equal(t, "2026-11-04", result.Deliveries[0].Batch.From)
}

func Test_CollectCost_AccountScope_FiltersToItsOwnAccount(t *testing.T) {
	isolateAWS(t)
	fake := newFakeCostExplorer(t)
	server := httptest.NewServer(fake)
	defer server.Close()

	CollectCost(context.Background(), awsCostConnection(spec.CostScopeAccount, spec.CostSourceAPI, nil),
		CostState{}, Options{AWSEndpoint: server.URL}, costNow)

	require.Equal(t, []string{"123456789012"}, fake.filteredAccounts(),
		"a management account read without the filter would count the whole Organization")
	require.Zero(t, fake.calls["GetDimensionValues"])
}

func Test_CollectCost_WithoutCostExplorer_NeedsPermission(t *testing.T) {
	isolateAWS(t)
	fake := newFakeCostExplorer(t)
	fake.deny = true
	server := httptest.NewServer(fake)
	defer server.Close()

	result := CollectCost(context.Background(), awsCostConnection(spec.CostScopeAccount, spec.CostSourceAPI, nil),
		CostState{}, Options{AWSEndpoint: server.URL}, costNow)

	use := result.Scan.Uses[spec.UseCost]
	require.Equal(t, spec.UseNeedsPermission, use.State)
	require.Contains(t, use.Missing, "ce:GetCostAndUsage")
	require.Empty(t, result.Deliveries, "a refresh that read nothing must not clear the days")
}

func Test_CollectCost_Auto_ReadsChangedExportPeriods_AndTheAPIOnlyAfterTheExport(t *testing.T) {
	isolateAWS(t)
	fake := newFakeCostExplorer(t)
	fake.objects = map[string]string{
		"focus/obzly/data/BILLING_PERIOD=2026-10/obzly-00001.csv": "ChargePeriodStart,SubAccountId,ServiceName," +
			"BillingCurrency,BilledCost,EffectiveCost\n2026-10-31T00:00:00Z,123456789012,Amazon EC2,USD,4,3\n",
		"focus/obzly/data/BILLING_PERIOD=2026-11/obzly-00001.csv": "ChargePeriodStart,SubAccountId,ServiceName," +
			"BillingCurrency,BilledCost,EffectiveCost\n2026-11-08T00:00:00Z,123456789012,Amazon EC2,USD,2,1\n",
		"focus/obzly/metadata/BILLING_PERIOD=2026-11/obzly-Manifest.json": "{}",
	}
	server := httptest.NewServer(fake)
	defer server.Close()
	export := &spec.CostExport{Bucket: "contoso-billing-exports", Region: "us-east-1", Prefix: "focus"}

	result := CollectCost(context.Background(), awsCostConnection(spec.CostScopeAccount, spec.CostSourceAuto, export),
		CostState{}, Options{AWSEndpoint: server.URL}, costNow)

	require.Equal(t, spec.UseOK, result.Scan.Uses[spec.UseCost].State, result.Scan.Errors)
	require.Len(t, result.Deliveries, 2, "two export periods; the export reaches yesterday, so no API read")
	november := result.Deliveries[0]
	require.Equal(t, "2026-11", november.Period)
	require.Equal(t, spec.CostSourceExport, november.Batch.Source)
	require.Equal(t, "2026-11-01", november.Batch.From)
	require.Equal(t, "2026-11-09", november.Batch.To)
	require.Equal(t, int64(2_000_000), november.Batch.Rows[0].BilledMicros)
	require.Equal(t, "2026-10", result.Deliveries[1].Period)
	require.Equal(t, "2026-10-31", result.Deliveries[1].Batch.To)
	require.Equal(t, "2026-11-08", result.ExportThrough)
	require.Zero(t, fake.calls["GetCostAndUsage"])
	require.Nil(t, november.Batch.Forecast)
	require.NotNil(t, result.Deliveries[1].Batch.Forecast, "the forecast rides on the last batch")

	// Unchanged files aren't read again; the forecast still goes out.
	state := CostState{Periods: map[string]string{}, ExportThrough: result.ExportThrough}
	for _, delivery := range result.Deliveries {
		state.Periods[delivery.Period] = delivery.Fingerprint
	}
	result = CollectCost(context.Background(), awsCostConnection(spec.CostScopeAccount, spec.CostSourceAuto, export),
		state, Options{AWSEndpoint: server.URL}, costNow)
	require.Len(t, result.Deliveries, 1)
	require.Equal(t, "", result.Deliveries[0].Batch.From, "a forecast-only batch clears no days")
	require.NotNil(t, result.Deliveries[0].Batch.Forecast)
}

func Test_CollectCost_Azure_QueriesActualAndAmortizedCost(t *testing.T) {
	imds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"arm-token"}`))
	}))
	defer imds.Close()
	var mu sync.Mutex
	var kinds []string
	management := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer arm-token", r.Header.Get("Authorization"))
		var query azureCostQuery
		require.NoError(t, json.NewDecoder(r.Body).Decode(&query))
		if strings.HasSuffix(r.URL.Path, "/forecast") {
			_, _ = w.Write([]byte(`{"properties":{"columns":[{"name":"Cost"},{"name":"UsageDate"},{"name":"Currency"}],
				"rows":[[10.5,20261110,"EUR"],[11.5,20261111,"EUR"]]}}`))
			return
		}
		require.Equal(t, "/subscriptions/33333333-3333-3333-3333-333333333333/providers/Microsoft.CostManagement/query",
			r.URL.Path)
		mu.Lock()
		kinds = append(kinds, query.Type)
		mu.Unlock()
		cost := 4.0
		if query.Type == "AmortizedCost" {
			cost = 3.5
		}
		fmt.Fprintf(w, `{"properties":{"columns":[{"name":"Cost"},{"name":"UsageDate"},{"name":"ServiceName"},
			{"name":"ResourceLocation"},{"name":"Currency"}],"rows":[[%v,20261108,"Virtual Machines","westeurope","EUR"]]}}`,
			cost)
	}))
	defer management.Close()
	connection := spec.CloudConnection{
		ID: "conn-az", Name: "Fabrikam", Provider: spec.ProviderAzure, Scope: "subscription",
		ScopeID: "33333333-3333-3333-3333-333333333333", Access: spec.AccessProbe,
		TenantID: "11111111-1111-1111-1111-111111111111", Uses: []string{spec.UseCost},
		Cost: &spec.CostSettings{Source: spec.CostSourceAPI, Scope: spec.CostScopeAccount},
	}

	result := CollectCost(context.Background(), connection, CostState{APIBackfilled: true},
		Options{AzureIMDS: imds.URL, AzureManagement: management.URL}, costNow)

	require.Equal(t, spec.UseOK, result.Scan.Uses[spec.UseCost].State, result.Scan.Errors)
	require.ElementsMatch(t, []string{"ActualCost", "AmortizedCost"}, kinds)
	batch := result.Deliveries[0].Batch
	require.Len(t, batch.Rows, 1)
	row := batch.Rows[0]
	require.Equal(t, "2026-11-08", row.Day)
	require.Equal(t, "westeurope", row.Region)
	require.Equal(t, int64(4_000_000), row.BilledMicros)
	require.Equal(t, int64(3_500_000), row.EffectiveMicros)
	require.Equal(
		t,
		[]spec.CostForecast{{Month: "2026-11", Currency: "EUR", RemainingMicros: 22_000_000}},
		batch.Forecast,
	)
}

func Test_CollectCost_Via_ReadsNothing(t *testing.T) {
	result := CollectCost(context.Background(), spec.CloudConnection{
		Provider: spec.ProviderAWS, Uses: []string{spec.UseCost},
		Cost: &spec.CostSettings{Scope: spec.CostScopeVia, ViaConnectionID: "payer"},
	}, CostState{}, Options{}, costNow)
	require.Empty(t, result.Deliveries)
	require.Equal(t, spec.UseOK, result.Scan.Uses[spec.UseCost].State)
}

func awsCostConnection(scope, source string, export *spec.CostExport) spec.CloudConnection {
	return spec.CloudConnection{
		ID: "conn-aws", Name: "Contoso-Payer", Provider: spec.ProviderAWS, Scope: "account", ScopeID: "123456789012",
		Access: spec.AccessRole, Identity: "arn:aws:iam::123456789012:role/obzly-readonly",
		ExternalID: "obz-0123456789abcdef", Uses: []string{spec.UseCost},
		Cost: &spec.CostSettings{Source: source, Scope: scope, Export: export, TrackedTags: []string{"team"}},
	}
}

// fakeCostExplorer answers STS (query protocol), Cost Explorer (JSON 1.1) and S3 (REST, path style).
type fakeCostExplorer struct {
	t        *testing.T
	mu       sync.Mutex
	calls    map[string]int
	accounts []string
	deny     bool
	objects  map[string]string
}

func newFakeCostExplorer(t *testing.T) *fakeCostExplorer {
	return &fakeCostExplorer{t: t, calls: map[string]int{}}
}

func (f *fakeCostExplorer) filteredAccounts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	accounts := append([]string{}, f.accounts...)
	sort.Strings(accounts)
	return accounts
}

func (f *fakeCostExplorer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if target := r.Header.Get("X-Amz-Target"); target != "" {
		f.costExplorer(w, r, strings.TrimPrefix(target, "AWSInsightsIndexService."))
		return
	}
	if r.Method == http.MethodGet {
		f.s3(w, r)
		return
	}
	body, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(body))
	w.Header().Set("Content-Type", "text/xml")
	switch form.Get("Action") {
	case "AssumeRole":
		fmt.Fprint(w, `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult>
			<Credentials><AccessKeyId>ASIATEST</AccessKeyId><SecretAccessKey>assumed</SecretAccessKey>
			<SessionToken>session</SessionToken><Expiration>2099-01-01T00:00:00Z</Expiration></Credentials>
			<AssumedRoleUser><Arn>arn:aws:sts::123456789012:assumed-role/obzly-readonly/obzly-probe</Arn>
			<AssumedRoleId>AROA:obzly-probe</AssumedRoleId></AssumedRoleUser></AssumeRoleResult>
			<ResponseMetadata><RequestId>1</RequestId></ResponseMetadata></AssumeRoleResponse>`)
	case "GetCallerIdentity":
		fmt.Fprint(w, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
			<GetCallerIdentityResult><Arn>arn:aws:sts::123456789012:assumed-role/obzly-readonly/obzly-probe</Arn>
			<UserId>AROA:obzly-probe</UserId><Account>123456789012</Account></GetCallerIdentityResult>
			<ResponseMetadata><RequestId>1</RequestId></ResponseMetadata></GetCallerIdentityResponse>`)
	default:
		f.t.Errorf("unexpected call %q", form.Get("Action"))
		w.WriteHeader(http.StatusBadRequest)
	}
}

func (f *fakeCostExplorer) costExplorer(w http.ResponseWriter, r *http.Request, operation string) {
	var input map[string]any
	require.NoError(f.t, json.NewDecoder(r.Body).Decode(&input))
	f.mu.Lock()
	f.calls[operation]++
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	if f.deny {
		w.Header().Set("x-amzn-ErrorType", "AccessDeniedException")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"__type":"AccessDeniedException","message":"not authorized to perform ce:`+operation+`"}`)
		return
	}
	switch operation {
	case "GetDimensionValues":
		fmt.Fprint(w, `{"DimensionValues":[{"Value":"111111111111","Attributes":{"description":"Prod"}},
			{"Value":"222222222222","Attributes":{"description":"Dev"}}],"ReturnSize":2,"TotalSize":2}`)
	case "GetCostAndUsage":
		filter := input["Filter"].(map[string]any)["Dimensions"].(map[string]any)
		account := filter["Values"].([]any)[0].(string)
		f.mu.Lock()
		f.accounts = append(f.accounts, account)
		f.mu.Unlock()
		region := "us-east-1"
		if account == "222222222222" {
			region = "NoRegion"
		}
		fmt.Fprintf(w, `{"ResultsByTime":[{"TimePeriod":{"Start":"2026-11-07","End":"2026-11-08"},"Estimated":true,
			"Groups":[{"Keys":["Amazon EC2","%s"],"Metrics":{"UnblendedCost":{"Amount":"12.5","Unit":"USD"},
			"AmortizedCost":{"Amount":"10.25","Unit":"USD"}}}]}]}`, region)
	case "GetCostForecast":
		fmt.Fprint(w, `{"Total":{"Amount":"345.678","Unit":"USD"},"ForecastResultsByTime":[]}`)
	default:
		f.t.Errorf("unexpected Cost Explorer call %q", operation)
		w.WriteHeader(http.StatusBadRequest)
	}
}

func (f *fakeCostExplorer) s3(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/contoso-billing-exports")
	if r.URL.Query().Get("list-type") == "2" {
		prefix := r.URL.Query().Get("prefix")
		var keys []string
		for key := range f.objects {
			if strings.HasPrefix(key, prefix) {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteString(
			`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>contoso-billing-exports</Name>`,
		)
		for _, key := range keys {
			fmt.Fprintf(&b, `<Contents><Key>%s</Key><LastModified>2026-11-09T03:12:00.000Z</LastModified>
				<ETag>"etag-%d"</ETag><Size>%d</Size></Contents>`, key, len(f.objects[key]), len(f.objects[key]))
		}
		b.WriteString(`<IsTruncated>false</IsTruncated></ListBucketResult>`)
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, b.String())
		return
	}
	object, ok := f.objects[strings.TrimPrefix(path, "/")]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	fmt.Fprint(w, object)
}

// focusParquet is a two-line FOCUS file: a timestamp, a decimal and a float cost, and a tag map.
func focusParquet(t *testing.T) []byte {
	schema := arrow.NewSchema([]arrow.Field{
		{Name: "ChargePeriodStart", Type: &arrow.TimestampType{Unit: arrow.Microsecond, TimeZone: "UTC"}},
		{Name: "SubAccountId", Type: arrow.BinaryTypes.String},
		{Name: "ServiceName", Type: arrow.BinaryTypes.String},
		{Name: "BillingCurrency", Type: arrow.BinaryTypes.String},
		{Name: "BilledCost", Type: &arrow.Decimal128Type{Precision: 38, Scale: 10}},
		{Name: "EffectiveCost", Type: arrow.PrimitiveTypes.Float64},
		{Name: "Tags", Type: arrow.MapOf(arrow.BinaryTypes.String, arrow.BinaryTypes.String)},
		{Name: "x_Discounts", Type: arrow.BinaryTypes.String},
	}, nil)
	builder := array.NewRecordBuilder(memory.NewGoAllocator(), schema)
	defer builder.Release()
	at := time.Date(2026, 11, 3, 4, 0, 0, 0, time.UTC)
	for range 2 {
		builder.Field(0).(*array.TimestampBuilder).Append(arrow.Timestamp(at.UnixMicro()))
		builder.Field(1).(*array.StringBuilder).Append("123456789012")
		builder.Field(2).(*array.StringBuilder).Append("Amazon RDS")
		builder.Field(3).(*array.StringBuilder).Append("USD")
		builder.Field(4).(*array.Decimal128Builder).Append(decimal128.FromI64(123_456_789_000))
		builder.Field(5).(*array.Float64Builder).Append(10.25)
		tags := builder.Field(6).(*array.MapBuilder)
		tags.Append(true)
		tags.KeyBuilder().(*array.StringBuilder).Append("user:team")
		tags.ItemBuilder().(*array.StringBuilder).Append("checkout")
		builder.Field(7).(*array.StringBuilder).Append("ignored")
	}
	record := builder.NewRecord()
	defer record.Release()
	table := array.NewTableFromRecords(schema, []arrow.Record{record})
	defer table.Release()
	var out bytes.Buffer
	require.NoError(t, pqarrow.WriteTable(table, &out, 1024, nil, pqarrow.DefaultWriterProps()))
	return out.Bytes()
}
