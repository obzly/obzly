package logs_querier

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	logs_core "obzly/internal/features/logs/core"
	projects_models "obzly/internal/features/projects/models"
	projects_services "obzly/internal/features/projects/services"
	"obzly/internal/storage/segments"
	"obzly/internal/util/attrnames"

	"github.com/google/uuid"
)

var storedPatternHashSQL = logs_core.StoredPatternHashSQL

// systemFieldColumns maps QueryNode field names to logs schema columns
// (ingester.LogSchema); any other field is custom, read from the `fields` JSON.
var systemFieldColumns = map[string]string{
	"timestamp":  "timestamp",
	"project_id": "project_id",
	"id":         "id",
	"level":      "level",
	"client_ip":  "client_ip",
	"message":    "message",
	"trace_id":   "trace_id",
	"span_id":    "span_id",
	// Promoted OTel resource attributes (logs_core.PromotedResourceColumns).
	"service_name":       "service_name",
	"service_version":    "service_version",
	"k8s_namespace_name": "k8s_namespace_name",
	"k8s_pod_name":       "k8s_pod_name",
	"k8s_container_name": "k8s_container_name",
	"k8s_node_name":      "k8s_node_name",
	"k8s_cluster_name":   "k8s_cluster_name",
	"cloud_provider":     "cloud_provider",
	"cloud_region":       "cloud_region",
	// A SQL expression, not a column (stored at ingest, else computed from
	// message); consumers splice it in as-is, so filtering and grouping work.
	logs_core.PatternHashField: storedPatternHashSQL,
}

// whereBuilder translates the QueryNode AST (logs/core/dto.go) into a
// parameterized DuckDB WHERE fragment.
type whereBuilder struct {
	// promoted: the project's promoted fields by name.
	promoted map[string]projects_models.PromotedField
	// rollup: the conditions run on logs_1m, whose pattern_hash is always stored
	// (rollupAnswerable has checked they only use its columns).
	rollup bool
}

func (b *whereBuilder) systemColumn(field string) (string, bool) {
	if b.rollup && field == logs_core.PatternHashField {
		return "pattern_hash", true
	}
	column, isSystem := systemFieldColumns[field]
	return column, isSystem
}

func newWhereBuilder(projectID uuid.UUID) *whereBuilder {
	builder := &whereBuilder{promoted: map[string]projects_models.PromotedField{}}
	for _, field := range projects_services.GetPromotedFieldService().ActiveFields(projectID) {
		builder.promoted[field.Field] = field
	}
	return builder
}

// fieldText is a custom field's value as text: a promoted string field as a
// token each fragment resolves to its column or the fields JSON per the row's
// mask (segments.ResolvePromotedFields); any other field from the fields JSON.
func (b *whereBuilder) fieldText(fieldName string) (string, []any) {
	path := jsonPathFor(fieldName)
	if field, ok := b.promoted[fieldName]; ok && field.Type() == projects_models.PromotedTypeString {
		return segments.PromotedToken(field.Slot, field.Bit(), path), nil
	}
	return "json_extract_string(fields, ?)", []any{path}
}

// fieldNumber is a promoted number field's value as a DOUBLE token (NULL when
// not a number); ok false for any other field.
func (b *whereBuilder) fieldNumber(fieldName string) (string, []any, bool) {
	field, ok := b.promoted[fieldName]
	if !ok || field.Type() != projects_models.PromotedTypeNumber {
		return "", nil, false
	}
	return segments.PromotedToken(field.Slot, field.Bit(), jsonPathFor(fieldName)), nil, true
}

// Build returns a SQL boolean expression (safe to AND onto a WHERE clause)
// and its bind arguments in order. An empty node produces ("", nil).
func (b *whereBuilder) Build(node *logs_core.QueryNode) (string, []any) {
	if node == nil {
		return "", nil
	}

	switch node.Type {
	case logs_core.QueryNodeTypeCondition:
		if node.Condition == nil {
			return "", nil
		}
		return b.buildCondition(node.Condition)

	case logs_core.QueryNodeTypeLogical:
		if node.Logic == nil || len(node.Logic.Children) == 0 {
			return "", nil
		}
		return b.buildLogical(node.Logic)

	default:
		return "", nil
	}
}

func (b *whereBuilder) buildLogical(logic *logs_core.LogicalNode) (string, []any) {
	var parts []string
	var args []any

	for _, child := range logic.Children {
		childSQL, childArgs := b.Build(&child)
		if childSQL == "" {
			continue
		}
		parts = append(parts, childSQL)
		args = append(args, childArgs...)
	}

	if len(parts) == 0 {
		return "", nil
	}

	switch logic.Operator {
	case logs_core.LogicalOperatorAnd:
		return "(" + strings.Join(parts, " AND ") + ")", args
	case logs_core.LogicalOperatorOr:
		return "(" + strings.Join(parts, " OR ") + ")", args
	case logs_core.LogicalOperatorNot:
		// A row matches NOT(children) only if it matches none of them.
		return "NOT (" + strings.Join(parts, " OR ") + ")", args
	default:
		return "", nil
	}
}

func (b *whereBuilder) buildCondition(cond *logs_core.ConditionNode) (string, []any) {
	// Old names (resource.k8s.pod.name, pattern.hash) still work.
	fieldName := attrnames.Normalize(strings.TrimSpace(cond.Field))
	if fieldName == "" {
		return "1=0", nil
	}

	if fieldName == "level" {
		return b.buildSystemCondition("lower(level)", fieldName, logs_core.LowercaseCondition(cond))
	}
	if column, isSystem := b.systemColumn(fieldName); isSystem {
		return b.buildSystemCondition(column, fieldName, cond)
	}
	return b.buildCustomFieldCondition(fieldName, cond)
}

// nullableSystemColumns can be absent: trace_id/span_id are "" without trace
// context and NULL in older segments (union_by_name). Other system columns are
// always populated, so their Exists/Not* forms stay simple.
var nullableSystemColumns = map[string]bool{
	"trace_id": true,
	"span_id":  true,
	// Also NULL in older segments, and "" for logs without k8s/service context.
	"service_name":       true,
	"service_version":    true,
	"k8s_namespace_name": true,
	"k8s_pod_name":       true,
	"k8s_container_name": true,
	"k8s_node_name":      true,
	"k8s_cluster_name":   true,
	"cloud_provider":     true,
	"cloud_region":       true,
}

func (b *whereBuilder) buildSystemCondition(column, fieldName string, cond *logs_core.ConditionNode) (string, []any) {
	nullable := nullableSystemColumns[fieldName]

	switch cond.Operator {
	case logs_core.ConditionOperatorEquals:
		value, ok := coerceValue(fieldName, cond.Value)
		if !ok {
			return "1=0", nil
		}
		return column + " = ?", []any{value}

	case logs_core.ConditionOperatorNotEquals:
		value, ok := coerceValue(fieldName, cond.Value)
		if !ok {
			return "1=0", nil
		}
		if nullable {
			return "(" + column + " IS NULL OR " + column + " <> ?)", []any{value}
		}
		return column + " <> ?", []any{value}

	// contains(), not LIKE '%v%' ESCAPE '\': the ESCAPE clause turns off
	// DuckDB's substring fast path, which made full-text scans 3.5x slower.
	case logs_core.ConditionOperatorContains:
		value := fmt.Sprintf("%v", cond.Value)
		if fieldName == "message" && segments.IsIDToken(value) {
			return tokenMatch(column, value)
		}
		return "contains(" + column + ", ?)", []any{value}

	case logs_core.ConditionOperatorNotContains:
		value := fmt.Sprintf("%v", cond.Value)
		if fieldName == "message" && segments.IsIDToken(value) {
			match, args := tokenMatch(column, value)
			return "NOT " + match, args
		}
		if nullable {
			return "(" + column + " IS NULL OR NOT contains(" + column + ", ?))", []any{value}
		}
		return "NOT contains(" + column + ", ?)", []any{value}

	case logs_core.ConditionOperatorIn:
		return b.buildInCondition(column, fieldName, cond.Value, false)

	case logs_core.ConditionOperatorNotIn:
		return b.buildInCondition(column, fieldName, cond.Value, true)

	case logs_core.ConditionOperatorExists:
		if nullable {
			return "(" + column + " IS NOT NULL AND " + column + " <> '')", nil
		}
		return "1=1", nil // non-nullable system columns are always populated

	case logs_core.ConditionOperatorNotExists:
		if nullable {
			return "(" + column + " IS NULL OR " + column + " = '')", nil
		}
		return "1=0", nil

	case logs_core.ConditionOperatorGreaterThan, logs_core.ConditionOperatorGreaterOrEqual,
		logs_core.ConditionOperatorLessThan, logs_core.ConditionOperatorLessOrEqual:
		value, ok := coerceValue(fieldName, cond.Value)
		if !ok {
			return "1=0", nil
		}
		return column + " " + rangeOperatorSQL(cond.Operator) + " ?", []any{value}

	default:
		return "1=0", nil
	}
}

func (b *whereBuilder) buildCustomFieldCondition(fieldName string, cond *logs_core.ConditionNode) (string, []any) {
	if sqlExpr, args, ok := b.buildNumberCondition(fieldName, cond); ok {
		return sqlExpr, args
	}
	extract, extractArgs := b.fieldText(fieldName)
	with := func(args ...any) []any { return append(append([]any{}, extractArgs...), args...) }

	switch cond.Operator {
	case logs_core.ConditionOperatorEquals:
		return extract + " = ?", with(fmt.Sprintf("%v", cond.Value))

	case logs_core.ConditionOperatorNotEquals:
		return notSafe(extract, extractArgs, extract+" = ?", with(fmt.Sprintf("%v", cond.Value)))

	case logs_core.ConditionOperatorContains:
		return "contains(" + extract + ", ?)", with(fmt.Sprintf("%v", cond.Value))

	case logs_core.ConditionOperatorNotContains:
		return notSafe(extract, extractArgs, "contains("+extract+", ?)", with(fmt.Sprintf("%v", cond.Value)))

	case logs_core.ConditionOperatorIn:
		values := asStringSlice(cond.Value)
		if len(values) == 0 {
			return "1=0", nil
		}
		inSQL, inArgs := inClause(extract, values)
		return inSQL, with(inArgs...)

	case logs_core.ConditionOperatorNotIn:
		values := asStringSlice(cond.Value)
		if len(values) == 0 {
			return "1=1", []any{} // NOT IN () over an empty set matches everything
		}
		inSQL, inArgs := inClause(extract, values)
		return notSafe(extract, extractArgs, inSQL, with(inArgs...))

	case logs_core.ConditionOperatorExists:
		return extract + " IS NOT NULL", extractArgs

	case logs_core.ConditionOperatorNotExists:
		return extract + " IS NULL", extractArgs

	case logs_core.ConditionOperatorGreaterThan, logs_core.ConditionOperatorGreaterOrEqual,
		logs_core.ConditionOperatorLessThan, logs_core.ConditionOperatorLessOrEqual:
		// Range operators need a number field (promote it as a number).
		return "1=0", nil

	default:
		return "1=0", nil
	}
}

// buildNumberCondition compares a promoted number field numerically; ok false
// for anything else, which then compares the text in fields.
func (b *whereBuilder) buildNumberCondition(fieldName string, cond *logs_core.ConditionNode) (string, []any, bool) {
	number, numberArgs, ok := b.fieldNumber(fieldName)
	if !ok {
		return "", nil, false
	}
	with := func(args ...any) []any { return append(append([]any{}, numberArgs...), args...) }
	switch cond.Operator {
	case logs_core.ConditionOperatorEquals, logs_core.ConditionOperatorNotEquals,
		logs_core.ConditionOperatorGreaterThan, logs_core.ConditionOperatorGreaterOrEqual,
		logs_core.ConditionOperatorLessThan, logs_core.ConditionOperatorLessOrEqual:
		value, err := strconv.ParseFloat(fmt.Sprintf("%v", cond.Value), 64)
		if err != nil {
			if cond.Operator == logs_core.ConditionOperatorEquals ||
				cond.Operator == logs_core.ConditionOperatorNotEquals {
				return "", nil, false
			}
			return "1=0", nil, true
		}
		switch cond.Operator {
		case logs_core.ConditionOperatorEquals:
			return number + " = ?", with(value), true
		case logs_core.ConditionOperatorNotEquals:
			sqlExpr, args := notSafe(number, numberArgs, number+" = ?", with(value))
			return sqlExpr, args, true
		default:
			return number + " " + rangeOperatorSQL(cond.Operator) + " ?", with(value), true
		}
	case logs_core.ConditionOperatorIn, logs_core.ConditionOperatorNotIn:
		texts := asStringSlice(cond.Value)
		values := make([]any, 0, len(texts))
		for _, text := range texts {
			value, err := strconv.ParseFloat(text, 64)
			if err != nil {
				return "", nil, false
			}
			values = append(values, value)
		}
		if len(values) == 0 {
			return "", nil, false
		}
		inSQL := number + " IN (" + strings.TrimRight(strings.Repeat("?, ", len(values)), ", ") + ")"
		if cond.Operator == logs_core.ConditionOperatorIn {
			return inSQL, with(values...), true
		}
		sqlExpr, args := notSafe(number, numberArgs, inSQL, with(values...))
		return sqlExpr, args, true
	}
	return "", nil, false
}

func (b *whereBuilder) buildInCondition(column, fieldName string, value any, negate bool) (string, []any) {
	values := asStringSlice(value)
	if len(values) == 0 {
		if negate {
			return "1=1", nil // NOT IN () matches everything
		}
		return "1=0", nil // IN () matches nothing
	}

	converted := make([]any, len(values))
	for i, v := range values {
		coerced, ok := coerceValue(fieldName, v)
		if !ok {
			return "1=0", nil
		}
		converted[i] = coerced
	}

	placeholders := strings.TrimRight(strings.Repeat("?, ", len(converted)), ", ")
	sqlExpr := column + " IN (" + placeholders + ")"
	if negate {
		if nullableSystemColumns[fieldName] {
			sqlExpr = "(" + column + " IS NULL OR NOT (" + sqlExpr + "))"
		} else {
			sqlExpr = "NOT (" + sqlExpr + ")"
		}
	}
	return sqlExpr, converted
}

// notSafe negates a positive expression so a NULL extraction (field absent)
// counts as a match: plain NOT (x) is NULL there and would drop the row.
func notSafe(valueExpr string, valueArgs []any, positiveSQL string, positiveArgs []any) (string, []any) {
	sqlExpr := "(" + valueExpr + " IS NULL OR NOT (" + positiveSQL + "))"
	return sqlExpr, append(append([]any{}, valueArgs...), positiveArgs...)
}

func inClause(extractExpr string, values []string) (string, []any) {
	placeholders := strings.TrimRight(strings.Repeat("?, ", len(values)), ", ")
	args := make([]any, len(values))
	for i, v := range values {
		args[i] = v
	}
	return extractExpr + " IN (" + placeholders + ")", args
}

func rangeOperatorSQL(op logs_core.ConditionOperator) string {
	switch op {
	case logs_core.ConditionOperatorGreaterThan:
		return ">"
	case logs_core.ConditionOperatorGreaterOrEqual:
		return ">="
	case logs_core.ConditionOperatorLessThan:
		return "<"
	case logs_core.ConditionOperatorLessOrEqual:
		return "<="
	default:
		return "="
	}
}

// coerceValue converts a condition's raw JSON value into the bind type for a
// system column: time.Time for timestamp, else string. ok=false for an
// unparseable timestamp means "matches nothing" rather than a query error.
func coerceValue(fieldName string, value any) (any, bool) {
	if fieldName != "timestamp" {
		return fmt.Sprintf("%v", value), true
	}

	switch typed := value.(type) {
	case time.Time:
		return typed, true
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, typed)
		if err != nil {
			return nil, false
		}
		return parsed, true
	default:
		return nil, false
	}
}

// jsonPathFor builds a DuckDB JSONPath for one top-level key, escaping quotes
// and backslashes so the field name is never interpolated into SQL text.
func jsonPathFor(key string) string {
	escaped := strings.ReplaceAll(key, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `$."` + escaped + `"`
}

func asStringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			result = append(result, fmt.Sprintf("%v", item))
		}
		return result
	case string:
		return []string{typed}
	default:
		return nil
	}
}

// tokenMatch matches an ID-like term as a whole token (segments.IsIDToken):
// not inside a longer run of letters and digits (or dashes, for a UUID).
// That is what the token filters index, so a segment they skip holds no
// match. contains() first, as the regex is far slower.
func tokenMatch(column, token string) (string, []any) {
	boundary := "[^A-Za-z0-9]"
	if segments.IsUUIDToken(token) {
		boundary = "[^A-Za-z0-9-]"
	}
	// The token holds only letters, digits and dashes: nothing to escape.
	pattern := "(^|" + boundary + ")" + token + "($|" + boundary + ")"
	return "(contains(" + column + ", ?) AND regexp_matches(" + column + ", ?))", []any{token, pattern}
}

// requiredTokens are the ID-like message tokens every match of query holds:
// its conditions reached through ANDs alone (an OR or NOT branch can't
// rule a segment out). Searches skip segments whose filters lack one.
func requiredTokens(query *logs_core.QueryNode) []string {
	if query == nil {
		return nil
	}
	switch query.Type {
	case logs_core.QueryNodeTypeCondition:
		cond := query.Condition
		if cond == nil || cond.Operator != logs_core.ConditionOperatorContains ||
			attrnames.Normalize(strings.TrimSpace(cond.Field)) != "message" {
			return nil
		}
		if value := fmt.Sprintf("%v", cond.Value); segments.IsIDToken(value) {
			return []string{value}
		}
	case logs_core.QueryNodeTypeLogical:
		if query.Logic == nil || query.Logic.Operator != logs_core.LogicalOperatorAnd {
			return nil
		}
		var tokens []string
		for i := range query.Logic.Children {
			tokens = append(tokens, requiredTokens(&query.Logic.Children[i])...)
		}
		return tokens
	}
	return nil
}
