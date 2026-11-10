package discovery

import (
	"compress/gzip"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
)

// FOCUS columns read (the same names in AWS Data Exports and Azure FocusCost exports; RegionId is
// Region in early FOCUS files).
var focusColumns = []string{
	"ChargePeriodStart", "BillingAccountId", "SubAccountId", "SubAccountName", "ServiceName", "RegionId", "Region",
	"ResourceId", "ChargeCategory", "PricingCategory", "BillingCurrency", "BilledCost", "EffectiveCost", "ListCost",
	"Tags",
}

const focusBatchRows = 8192

// parseFocus adds every line of one export file to a: Parquet, or CSV (gzipped or not).
func parseFocus(ctx context.Context, body io.Reader, name string, a *costAggregator) error {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".parquet"):
		return parseFocusParquet(ctx, body, a)
	case strings.HasSuffix(lower, ".csv.gz"), strings.HasSuffix(lower, ".gz"):
		gz, err := gzip.NewReader(body)
		if err != nil {
			return err
		}
		defer func() { _ = gz.Close() }()
		return parseFocusCSV(ctx, gz, a)
	case strings.HasSuffix(lower, ".csv"):
		return parseFocusCSV(ctx, body, a)
	}
	return fmt.Errorf("%s isn't a Parquet or CSV file", name)
}

// parseFocusParquet reads only the FOCUS columns. Parquet needs random access, so the file goes
// to a temporary file first (export files can be large).
func parseFocusParquet(ctx context.Context, body io.Reader, a *costAggregator) error {
	tmp, err := os.CreateTemp("", "obzly-focus-*.parquet")
	if err != nil {
		return err
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	if _, err := io.Copy(tmp, io.LimitReader(body, costMaxFileBytes+1)); err != nil {
		return err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	reader, err := file.NewParquetReader(tmp)
	if err != nil {
		return fmt.Errorf("not a Parquet file: %w", err)
	}
	defer func() { _ = reader.Close() }()
	schema := reader.MetaData().Schema
	var leaves []int
	for i := range schema.NumColumns() {
		top, _, _ := strings.Cut(schema.Column(i).Path(), ".")
		if isFocusColumn(top) {
			leaves = append(leaves, i)
		}
	}
	if len(leaves) == 0 {
		return errors.New("no FOCUS columns: is this a FOCUS export?")
	}
	arrowReader, err := pqarrow.NewFileReader(reader, pqarrow.ArrowReadProperties{BatchSize: focusBatchRows},
		memory.DefaultAllocator)
	if err != nil {
		return err
	}
	records, err := arrowReader.GetRecordReader(ctx, leaves, nil)
	if err != nil {
		return err
	}
	defer records.Release()
	for records.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := addFocusRecord(records.RecordBatch(), a); err != nil {
			return err
		}
	}
	if err := records.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func addFocusRecord(record arrow.RecordBatch, a *costAggregator) error {
	columns := map[string]arrow.Array{}
	for i, field := range record.Schema().Fields() {
		columns[field.Name] = record.Column(i)
	}
	text := func(name string, row int) string {
		if column, ok := columns[name]; ok {
			return arrowText(column, row)
		}
		return ""
	}
	for row := range int(record.NumRows()) {
		r := costRecord{
			day: dayOf(text("ChargePeriodStart", row)), billingAccount: text("BillingAccountId", row),
			subAccount: text("SubAccountId", row), subAccountName: text("SubAccountName", row),
			service: text("ServiceName", row), region: firstNonEmpty(text("RegionId", row), text("Region", row)),
			resource: text("ResourceId", row), chargeCategory: text("ChargeCategory", row),
			pricingCategory: text("PricingCategory", row), currency: text("BillingCurrency", row),
		}
		var err error
		if r.billed, err = parseMicros(text("BilledCost", row)); err != nil {
			return err
		}
		if r.effective, err = parseMicros(text("EffectiveCost", row)); err != nil {
			return err
		}
		if r.list, err = parseMicros(text("ListCost", row)); err != nil {
			return err
		}
		if tags, ok := columns["Tags"]; ok {
			r.tags = arrowTags(tags, row)
		}
		a.add(r)
	}
	return nil
}

func parseFocusCSV(ctx context.Context, body io.Reader, a *costAggregator) error {
	reader := csv.NewReader(body)
	reader.ReuseRecord = true
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return fmt.Errorf("reading the CSV header: %w", err)
	}
	index := map[string]int{}
	for i, name := range header {
		index[strings.TrimPrefix(strings.TrimSpace(name), "\ufeff")] = i
	}
	if _, ok := index["BilledCost"]; !ok {
		return errors.New("no FOCUS columns: is this a FOCUS export?")
	}
	for line := 0; ; line++ {
		if line%10000 == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		fields, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		get := func(name string) string {
			if i, ok := index[name]; ok && i < len(fields) {
				return strings.TrimSpace(fields[i])
			}
			return ""
		}
		r := costRecord{
			day: dayOf(get("ChargePeriodStart")), billingAccount: get("BillingAccountId"),
			subAccount: get("SubAccountId"), subAccountName: get("SubAccountName"), service: get("ServiceName"),
			region: firstNonEmpty(get("RegionId"), get("Region")), resource: get("ResourceId"),
			chargeCategory: get("ChargeCategory"), pricingCategory: get("PricingCategory"),
			currency: get("BillingCurrency"), tags: jsonTags(get("Tags")),
		}
		if r.billed, err = parseMicros(get("BilledCost")); err != nil {
			return err
		}
		if r.effective, err = parseMicros(get("EffectiveCost")); err != nil {
			return err
		}
		if r.list, err = parseMicros(get("ListCost")); err != nil {
			return err
		}
		a.add(r)
	}
}

func isFocusColumn(name string) bool {
	for _, column := range focusColumns {
		if column == name {
			return true
		}
	}
	return false
}

// arrowText is one value as text: timestamps as RFC 3339, decimals at their scale.
func arrowText(column arrow.Array, row int) string {
	if column.IsNull(row) {
		return ""
	}
	switch typed := column.(type) {
	case *array.String:
		return typed.Value(row)
	case *array.LargeString:
		return typed.Value(row)
	case *array.Timestamp:
		unit := arrow.Microsecond
		if timestamp, ok := typed.DataType().(*arrow.TimestampType); ok {
			unit = timestamp.Unit
		}
		return typed.Value(row).ToTime(unit).UTC().Format(time.RFC3339)
	case *array.Date32:
		return typed.Value(row).ToTime().Format(dayLayout)
	case *array.Dictionary:
		return arrowText(typed.Dictionary(), typed.GetValueIndex(row))
	}
	return column.ValueStr(row)
}

// arrowTags reads a map<string,string> column, or a JSON string of tags.
func arrowTags(column arrow.Array, row int) map[string]string {
	if column.IsNull(row) {
		return nil
	}
	tagMap, ok := column.(*array.Map)
	if !ok {
		return jsonTags(arrowText(column, row))
	}
	start, end := tagMap.ValueOffsets(row)
	if start == end {
		return nil
	}
	keys, items := tagMap.Keys(), tagMap.Items()
	tags := make(map[string]string, end-start)
	for i := int(start); i < int(end); i++ {
		tags[arrowText(keys, i)] = arrowText(items, i)
	}
	return tags
}

// jsonTags reads tags written as a JSON object; Azure's CSV exports leave out the braces.
func jsonTags(value string) map[string]string {
	value = strings.TrimSpace(value)
	if value == "" || value == "{}" {
		return nil
	}
	if !strings.HasPrefix(value, "{") {
		value = "{" + value + "}"
	}
	var raw map[string]any
	if json.Unmarshal([]byte(value), &raw) != nil {
		return nil
	}
	tags := make(map[string]string, len(raw))
	for key, v := range raw {
		tags[key] = fmt.Sprint(v)
	}
	return tags
}

// dayOf is the UTC day of a FOCUS timestamp ("2026-11-01T00:00:00Z", "2026-11-01 00:00:00").
func dayOf(value string) string {
	if len(value) < len(dayLayout) {
		return ""
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05", dayLayout} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC().Format(dayLayout)
		}
	}
	return value[:len(dayLayout)]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
