package discovery

import (
	"context"
	"fmt"
	"io"
	"math/big"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"obzly/internal/synthetics/spec"
)

// Cost reading (docs/CLOUD_COST_POSTURE.md "Cost"): a connection's FOCUS export and its billing
// API, aggregated to daily rows in millionths of the currency, sent to a Site in batches.

// CostState is what the probe keeps between one connection's cost refreshes.
type CostState struct {
	// Periods: per billing period (YYYY-MM), a fingerprint of the export files last sent.
	Periods map[string]string `json:"periods,omitempty"`
	// ExportThrough: the last day the export had cost for (YYYY-MM-DD).
	ExportThrough string `json:"exportThrough,omitempty"`
	// APIBackfilled: the billing API's first, longer read has been sent.
	APIBackfilled bool `json:"apiBackfilled,omitempty"`
	// AdviceAt: when recommendations were last read and sent (weekly).
	AdviceAt time.Time `json:"adviceAt,omitzero"`
}

// CostDelivery is one batch to send; once sent, Period's Fingerprint (export) or the API
// backfill is recorded in the state.
type CostDelivery struct {
	Batch       spec.CostBatch
	Period      string
	Fingerprint string
}

// CostResult is one refresh: what to send, and the report for the control plane.
type CostResult struct {
	Deliveries    []CostDelivery
	ExportThrough string
	Scan          spec.CloudScan
}

const (
	// costAPIBackfillDays: the billing API's first read; later reads redo only
	// costAPIRecentDays, where the provider still changes figures.
	costAPIBackfillDays = 90
	costAPIRecentDays   = 5
	// costMaxPeriods: export billing periods read on the first refresh.
	costMaxPeriods = 13
	// costEstimatedDays: the last days of cost the provider hasn't finalised.
	costEstimatedDays = 2
	// costMaxFileBytes bounds one export file the probe reads.
	costMaxFileBytes = 2 << 30
	dayLayout        = "2006-01-02"
)

// costFile is one export file: Period is its billing period (YYYY-MM) and Dir the delivery it
// belongs to (a period's newest delivery replaces the older ones).
type costFile struct {
	Key      string
	Dir      string
	Period   string
	Size     int64
	Modified time.Time
	ETag     string
}

// costReader is one provider's access to billing data, signed in.
type costReader interface {
	exportFiles(ctx context.Context, export *spec.CostExport) ([]costFile, error)
	openFile(ctx context.Context, export *spec.CostExport, file costFile) (io.ReadCloser, error)
	// api adds daily cost for from..to (inclusive) to a.
	api(ctx context.Context, from, to time.Time, a *costAggregator) error
	forecast(ctx context.Context, today time.Time) ([]spec.CostForecast, error)
	billingAccount() string
}

// CollectCost reads what changed since state and returns the batches to send. now is the time
// of the refresh (its UTC day is today).
func CollectCost(
	ctx context.Context, connection spec.CloudConnection, state CostState, options Options, now time.Time,
) CostResult {
	started := time.Now().UTC()
	ctx, stats := withStats(ctx)
	options = options.prepared()
	t := newTracker()
	info := &spec.CostScanInfo{}
	result := CostResult{ExportThrough: state.ExportThrough}
	settings := connection.Cost
	switch {
	case settings == nil:
		t.fail(fmt.Errorf("the cost use has no settings"))
	case settings.Scope == spec.CostScopeVia:
		result.Scan = costScan(ctx, started, stats, info,
			spec.UseResult{State: spec.UseOK, Message: "cost is read by another connection"})
		return result
	default:
		reader := newCostReader(ctx, connection, options, t, info)
		if reader != nil {
			result = collectCost(ctx, connection, *settings, state, reader, t, info, now)
		}
	}
	result.Scan = costScan(ctx, started, stats, info, t.result())
	result.Scan.Errors = t.errorList()
	return result
}

// TestCost is Test access for the cost use: sign in, list the export's files, and ask the
// billing API for yesterday.
func TestCost(ctx context.Context, connection spec.CloudConnection, options Options, now time.Time) spec.UseResult {
	ctx, _ = withStats(ctx)
	options = options.prepared()
	t := newTracker()
	info := &spec.CostScanInfo{}
	settings := connection.Cost
	if settings == nil || settings.Scope == spec.CostScopeVia {
		return spec.UseResult{State: spec.UseOK, Message: "cost is read by another connection"}
	}
	reader := newCostReader(ctx, connection, options, t, info)
	if reader == nil {
		return t.result()
	}
	var notes []string
	if settings.Source != spec.CostSourceAPI && settings.Export != nil {
		files, err := reader.exportFiles(ctx, settings.Export)
		switch {
		case err != nil:
		case len(files) == 0:
			notes = append(notes, "the export has no files yet")
		default:
			newest := slices.MaxFunc(files, func(a, b costFile) int { return a.Modified.Compare(b.Modified) })
			notes = append(notes, fmt.Sprintf("export readable, newest file %s (%s)", path.Base(newest.Key),
				newest.Modified.Format(dayLayout)))
		}
	}
	if settings.Source != spec.CostSourceExport {
		day := utcDay(now).AddDate(0, 0, -1)
		aggregator := newCostAggregator(nil, connection.Provider)
		if reader.api(ctx, day, day, aggregator) == nil {
			notes = append(notes, "billing API readable")
		}
	}
	use := t.result()
	if use.State == spec.UseOK && len(notes) > 0 {
		use.Message = strings.Join(notes, "; ")
	}
	return use
}

func newCostReader(
	ctx context.Context, connection spec.CloudConnection, options Options, t *tracker, info *spec.CostScanInfo,
) costReader {
	switch connection.Provider {
	case spec.ProviderAWS:
		return newAWSCostReader(ctx, connection, options, t, info)
	case spec.ProviderAzure:
		return newAzureCostReader(ctx, connection, options, t, info)
	}
	t.fail(fmt.Errorf("unknown provider %q", connection.Provider))
	return nil
}

func collectCost(
	ctx context.Context, connection spec.CloudConnection, settings spec.CostSettings, state CostState,
	reader costReader, t *tracker, info *spec.CostScanInfo, now time.Time,
) CostResult {
	today := utcDay(now)
	result := CostResult{ExportThrough: state.ExportThrough}
	batch := func(source string, from, to time.Time, rows []spec.CostRow) spec.CostBatch {
		return spec.CostBatch{
			ConnectionID: connection.ID, ConnectionName: connection.Name, Provider: connection.Provider,
			Source: source, From: from.Format(dayLayout), To: to.Format(dayLayout), Rows: rows,
			FinalThrough: today.AddDate(0, 0, -costEstimatedDays-1).Format(dayLayout),
		}
	}
	if settings.Source != spec.CostSourceAPI && settings.Export != nil {
		info.Source = spec.CostSourceExport
		for _, period := range exportPeriods(ctx, reader, settings.Export, state, today, info) {
			aggregator := newCostAggregator(settings.TrackedTags, connection.Provider)
			ok := true
			for _, file := range period.files {
				if err := readExportFile(ctx, reader, settings.Export, file, aggregator); err != nil {
					t.call("reading "+path.Base(file.Key), err, false)
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			from, to := periodBounds(period.name, today)
			rows := aggregator.rows(today)
			result.Deliveries = append(result.Deliveries, CostDelivery{
				Batch: batch(
					spec.CostSourceExport,
					from,
					to,
					rows,
				),
				Period:      period.name,
				Fingerprint: period.fingerprint,
			})
			info.Rows += len(rows)
			if last := lastDay(rows); last > result.ExportThrough {
				result.ExportThrough = last
			}
		}
	}
	// The billing API covers what the export doesn't: everything for api, the days after the
	// export's last for auto (all of them until its first file lands).
	yesterday := today.AddDate(0, 0, -1).Format(dayLayout)
	if settings.Source == spec.CostSourceAPI ||
		(settings.Source == spec.CostSourceAuto && result.ExportThrough < yesterday) {
		days := costAPIRecentDays
		if !state.APIBackfilled {
			days = costAPIBackfillDays
		}
		from := today.AddDate(0, 0, -days)
		if settings.Source == spec.CostSourceAuto && result.ExportThrough != "" {
			if through, err := time.Parse(dayLayout, result.ExportThrough); err == nil && !through.Before(from) {
				from = through.AddDate(0, 0, 1)
			}
		}
		aggregator := newCostAggregator(settings.TrackedTags, connection.Provider)
		if err := reader.api(ctx, from, today, aggregator); err == nil {
			rows := aggregator.rows(today)
			result.Deliveries = append(
				result.Deliveries,
				CostDelivery{Batch: batch(spec.CostSourceAPI, from, today, rows)},
			)
			info.Rows += len(rows)
			if info.Source == "" || len(rows) > 0 {
				info.Source = spec.CostSourceAPI
			}
			info.From, info.To = from.Format(dayLayout), today.Format(dayLayout)
		}
	}
	forecast, err := reader.forecast(ctx, today)
	if err == nil && len(forecast) > 0 {
		if len(result.Deliveries) == 0 {
			// Nothing changed: a batch with no days only updates the forecast.
			source := info.Source
			if source == "" {
				source = spec.CostSourceAPI
			}
			result.Deliveries = append(result.Deliveries, CostDelivery{Batch: spec.CostBatch{
				ConnectionID: connection.ID, ConnectionName: connection.Name, Provider: connection.Provider,
				Source: source,
			}})
		}
		last := &result.Deliveries[len(result.Deliveries)-1].Batch
		last.Forecast = forecast
	}
	if result.ExportThrough != "" {
		info.FinalThrough = min(result.ExportThrough, today.AddDate(0, 0, -costEstimatedDays-1).Format(dayLayout))
	}
	return result
}

type exportPeriod struct {
	name        string
	files       []costFile
	fingerprint string
}

// exportPeriods are the billing periods whose newest delivery changed since state, newest first:
// up to costMaxPeriods on the first refresh.
func exportPeriods(
	ctx context.Context, reader costReader, export *spec.CostExport, state CostState, today time.Time,
	info *spec.CostScanInfo,
) []exportPeriod {
	files, err := reader.exportFiles(ctx, export)
	if err != nil || len(files) == 0 {
		return nil
	}
	byPeriod := map[string][]costFile{}
	for _, file := range files {
		if file.Period != "" && file.Period <= today.Format("2006-01") {
			byPeriod[file.Period] = append(byPeriod[file.Period], file)
		}
	}
	names := make([]string, 0, len(byPeriod))
	for name := range byPeriod {
		names = append(names, name)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	if len(names) > costMaxPeriods {
		names = names[:costMaxPeriods]
	}
	var periods []exportPeriod
	for _, name := range names {
		newest := newestDelivery(byPeriod[name])
		fingerprint := filesFingerprint(newest)
		for _, file := range newest {
			if info.LastFileAt == nil || file.Modified.After(*info.LastFileAt) {
				at := file.Modified
				info.LastFile, info.LastFileAt = file.Key, &at
			}
		}
		if state.Periods[name] == fingerprint {
			continue
		}
		periods = append(periods, exportPeriod{name: name, files: newest, fingerprint: fingerprint})
	}
	return periods
}

// newestDelivery keeps the files of the directory with the newest file: an export that writes a
// new delivery per run leaves older ones next to it.
func newestDelivery(files []costFile) []costFile {
	newestDir, newestAt := "", time.Time{}
	for _, file := range files {
		if file.Modified.After(newestAt) {
			newestDir, newestAt = file.Dir, file.Modified
		}
	}
	var kept []costFile
	for _, file := range files {
		if file.Dir == newestDir {
			kept = append(kept, file)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Key < kept[j].Key })
	return kept
}

func filesFingerprint(files []costFile) string {
	var b strings.Builder
	for _, file := range files {
		fmt.Fprintf(&b, "%s|%s|%d|%d;", file.Key, file.ETag, file.Size, file.Modified.Unix())
	}
	return b.String()
}

func readExportFile(
	ctx context.Context, reader costReader, export *spec.CostExport, file costFile, a *costAggregator,
) error {
	if file.Size > costMaxFileBytes {
		return fmt.Errorf("the file is %d MB, more than the probe reads", file.Size>>20)
	}
	body, err := reader.openFile(ctx, export, file)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	return parseFocus(ctx, body, file.Key, a)
}

// periodBounds is a billing period's first and last day, the last at most today.
func periodBounds(period string, today time.Time) (time.Time, time.Time) {
	from, err := time.Parse("2006-01", period)
	if err != nil {
		return today, today
	}
	to := from.AddDate(0, 1, -1)
	if to.After(today) {
		to = today
	}
	return from, to
}

func lastDay(rows []spec.CostRow) string {
	last := ""
	for _, row := range rows {
		last = max(last, row.Day)
	}
	return last
}

func costScan(
	ctx context.Context, started time.Time, stats *scanStats, info *spec.CostScanInfo, use spec.UseResult,
) spec.CloudScan {
	return spec.CloudScan{
		Kind: spec.ScanCost, Uses: map[string]spec.UseResult{spec.UseCost: use}, StartedAt: started,
		Duration: time.Since(started).Seconds(), Throttled: int(stats.throttled.Load()),
		StoodDown: time.Duration(stats.stoodDown.Load()).Seconds(), Cost: info,
		Partial: ctx.Err() != nil,
	}
}

func utcDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// costRecord is one line of billing data, in FOCUS terms.
type costRecord struct {
	day, billingAccount, subAccount, subAccountName, service, region, resource string
	chargeCategory, pricingCategory, currency                                  string
	billed, effective, list                                                    int64
	tags                                                                       map[string]string
}

type costKey struct {
	day, billingAccount, subAccount, service, region, resource, chargeCategory, pricingCategory, currency, tags string
}

type costSums struct {
	billed, effective, list int64
	subAccountName          string
	tags                    map[string]string
}

// costAggregator sums records by day and dimensions, keeping only the tracked tags.
type costAggregator struct {
	tracked  []string
	provider string
	sums     map[costKey]*costSums
}

func newCostAggregator(tracked []string, provider string) *costAggregator {
	return &costAggregator{tracked: tracked, provider: provider, sums: map[costKey]*costSums{}}
}

func (a *costAggregator) add(r costRecord) {
	if r.day == "" || r.currency == "" {
		return
	}
	if a.provider == spec.ProviderAzure {
		// Azure IDs are case-insensitive; inventory and findings use them lowercased.
		r.resource, r.subAccount = strings.ToLower(r.resource), strings.ToLower(r.subAccount)
	}
	var kept map[string]string
	var tagKey strings.Builder
	for _, name := range a.tracked {
		if value, ok := lookupTag(r.tags, name); ok && value != "" {
			if kept == nil {
				kept = map[string]string{}
			}
			kept[name] = value
			fmt.Fprintf(&tagKey, "%s=%s\x00", name, value)
		}
	}
	key := costKey{
		day: r.day, billingAccount: r.billingAccount, subAccount: r.subAccount, service: r.service, region: r.region,
		resource: r.resource, chargeCategory: r.chargeCategory, pricingCategory: r.pricingCategory,
		currency: strings.ToUpper(r.currency), tags: tagKey.String(),
	}
	sums, ok := a.sums[key]
	if !ok {
		sums = &costSums{tags: kept}
		a.sums[key] = sums
	}
	sums.billed += r.billed
	sums.effective += r.effective
	sums.list += r.list
	if sums.subAccountName == "" {
		sums.subAccountName = r.subAccountName
	}
}

// rows are the sums as cost rows; the last costEstimatedDays before today are estimated.
func (a *costAggregator) rows(today time.Time) []spec.CostRow {
	estimatedFrom := today.AddDate(0, 0, -costEstimatedDays).Format(dayLayout)
	rows := make([]spec.CostRow, 0, len(a.sums))
	for key, sums := range a.sums {
		if sums.billed == 0 && sums.effective == 0 && sums.list == 0 {
			continue
		}
		rows = append(rows, spec.CostRow{
			Day: key.day, BillingAccount: key.billingAccount, SubAccount: key.subAccount,
			SubAccountName: sums.subAccountName, Service: key.service, Region: key.region, Resource: key.resource,
			ChargeCategory: key.chargeCategory, PricingCategory: key.pricingCategory, Currency: key.currency,
			BilledMicros: sums.billed, EffectiveMicros: sums.effective, ListMicros: sums.list, Tags: sums.tags,
			Estimated: key.day >= estimatedFrom,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Day < rows[j].Day })
	return rows
}

// lookupTag finds a tag case-insensitively (Azure keeps the case it was given; FOCUS tag keys from
// AWS can carry a user: prefix).
func lookupTag(tags map[string]string, name string) (string, bool) {
	if value, ok := tags[name]; ok {
		return value, true
	}
	for key, value := range tags {
		trimmed := strings.TrimPrefix(strings.TrimPrefix(key, "user:"), "user_")
		if strings.EqualFold(key, name) || strings.EqualFold(trimmed, name) {
			return value, true
		}
	}
	return "", false
}

// parseMicros reads a decimal amount ("12.3456789", "-0.5", "1.2E-7") as millionths, rounded half
// away from zero, without going through a float.
func parseMicros(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	if micros, ok := plainMicros(value); ok {
		return micros, nil
	}
	rat, ok := new(big.Rat).SetString(value)
	if !ok {
		return 0, fmt.Errorf("%q isn't an amount", value)
	}
	rat.Mul(rat, big.NewRat(1_000_000, 1))
	num, den := rat.Num(), rat.Denom()
	quotient, remainder := new(big.Int).QuoRem(num, den, new(big.Int))
	if new(big.Int).Mul(new(big.Int).Abs(remainder), big.NewInt(2)).Cmp(den) >= 0 {
		if num.Sign() < 0 {
			quotient.Sub(quotient, big.NewInt(1))
		} else {
			quotient.Add(quotient, big.NewInt(1))
		}
	}
	if !quotient.IsInt64() {
		return 0, fmt.Errorf("%q is too large", value)
	}
	return quotient.Int64(), nil
}

// plainMicros is parseMicros for the common [-]digits[.digits] form.
func plainMicros(value string) (int64, bool) {
	negative := strings.HasPrefix(value, "-")
	value = strings.TrimPrefix(strings.TrimPrefix(value, "-"), "+")
	whole, fraction, _ := strings.Cut(value, ".")
	if whole == "" && fraction == "" || len(whole) > 12 {
		return 0, false
	}
	var micros int64
	for _, c := range whole {
		if c < '0' || c > '9' {
			return 0, false
		}
		micros = micros*10 + int64(c-'0')
	}
	for i := range 6 {
		micros *= 10
		if i < len(fraction) {
			c := fraction[i]
			if c < '0' || c > '9' {
				return 0, false
			}
			micros += int64(c - '0')
		}
	}
	for _, c := range fraction[min(6, len(fraction)):] {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	if len(fraction) > 6 && fraction[6] >= '5' {
		micros++
	}
	if negative {
		micros = -micros
	}
	return micros, true
}
