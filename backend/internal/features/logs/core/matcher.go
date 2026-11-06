package logs_core

import (
	"fmt"
	"strings"
	"time"

	"obzly/internal/util/attrnames"
)

// Matches evaluates a QueryNode against one live LogItem (Live Tail). It must
// stay aligned with logs_querier's whereBuilder so a filter matches the same
// logs live and stored. A nil node always matches.
func Matches(node *QueryNode, log *LogItem) bool {
	if node == nil {
		return true
	}
	return matches(node, normalizedView(log))
}

// normalizedView sees a log as stored, with attrnames field keys (ingest
// usually did this already).
func normalizedView(log *LogItem) *LogItem {
	for key := range log.Fields {
		if !attrnames.IsNormal(key) {
			view := *log
			view.Fields = attrnames.NormalizeMap(log.Fields)
			return &view
		}
	}
	return log
}

func matches(node *QueryNode, log *LogItem) bool {
	if node == nil {
		return true
	}

	switch node.Type {
	case QueryNodeTypeCondition:
		if node.Condition == nil {
			return true
		}
		return matchCondition(node.Condition, log)

	case QueryNodeTypeLogical:
		if node.Logic == nil || len(node.Logic.Children) == 0 {
			return true
		}
		return matchLogical(node.Logic, log)

	default:
		return true
	}
}

func matchLogical(logic *LogicalNode, log *LogItem) bool {
	switch logic.Operator {
	case LogicalOperatorAnd:
		for _, child := range logic.Children {
			if !Matches(&child, log) {
				return false
			}
		}
		return true

	case LogicalOperatorOr:
		for _, child := range logic.Children {
			if Matches(&child, log) {
				return true
			}
		}
		return false

	case LogicalOperatorNot:
		// A log matches NOT(children) only if it matches none of them.
		for _, child := range logic.Children {
			if Matches(&child, log) {
				return false
			}
		}
		return true

	default:
		return true
	}
}

// promotedColumnByName indexes PromotedResourceColumns by column name.
var promotedColumnByName = func() map[string]string {
	m := make(map[string]string, len(PromotedResourceColumns))
	for _, p := range PromotedResourceColumns {
		m[p.Column] = p.OtelKey
	}
	return m
}()

func matchCondition(cond *ConditionNode, log *LogItem) bool {
	fieldName := attrnames.Normalize(strings.TrimSpace(cond.Field))
	if fieldName == "" {
		return false
	}

	if fieldName == "timestamp" {
		return matchTimestamp(log, cond)
	}

	if fieldName == "level" {
		return matchStringCondition(strings.ToLower(string(log.Level)), LowercaseCondition(cond), false)
	}

	if otelKey, isPromoted := promotedColumnByName[fieldName]; isPromoted {
		return matchStringCondition(PromotedColumnValue(log.Fields, otelKey), cond, true)
	}

	if value, isSystem := systemFieldValue(fieldName, log); isSystem {
		return matchStringCondition(value, cond, nullableSystemFields[fieldName])
	}

	return matchCustomFieldCondition(fieldName, cond, log)
}

// systemFieldValue returns a LogItem's struct field for whereBuilder's system
// fields, except timestamp and promoted columns (handled in matchCondition).
func systemFieldValue(fieldName string, log *LogItem) (string, bool) {
	switch fieldName {
	case "id":
		return log.ID.String(), true
	case "project_id":
		return log.ProjectID.String(), true
	case "level":
		return string(log.Level), true
	case "client_ip":
		return log.ClientIP, true
	case "message":
		return log.Message, true
	case "trace_id":
		return log.TraceID, true
	case "span_id":
		return log.SpanID, true
	case PatternHashField:
		return PatternHash(log.Message), true
	default:
		return "", false
	}
}

// nullableSystemFields mirrors where_builder.go's nullableSystemColumns:
// Exists/NotExists test for non-empty rather than always true.
var nullableSystemFields = map[string]bool{
	"trace_id": true, "span_id": true,
	"service_name": true, "service_version": true,
	"k8s_namespace_name": true, "k8s_pod_name": true, "k8s_container_name": true,
	"k8s_node_name": true, "k8s_cluster_name": true,
	"cloud_provider": true, "cloud_region": true,
}

// matchStringCondition implements whereBuilder's string operators against a
// Go string. nullable mirrors whereBuilder: an absent value counts as "not
// equal"/"does not contain", never as a positive match.
func matchStringCondition(value string, cond *ConditionNode, nullable bool) bool {
	switch cond.Operator {
	case ConditionOperatorEquals:
		return value == fmt.Sprintf("%v", cond.Value)

	case ConditionOperatorNotEquals:
		if nullable && value == "" {
			return true
		}
		return value != fmt.Sprintf("%v", cond.Value)

	case ConditionOperatorContains:
		return strings.Contains(value, fmt.Sprintf("%v", cond.Value))

	case ConditionOperatorNotContains:
		if nullable && value == "" {
			return true
		}
		return !strings.Contains(value, fmt.Sprintf("%v", cond.Value))

	case ConditionOperatorIn:
		return containsString(asStringSlice(cond.Value), value)

	case ConditionOperatorNotIn:
		values := asStringSlice(cond.Value)
		if len(values) == 0 {
			return true // NOT IN () matches everything, mirrors whereBuilder
		}
		if nullable && value == "" {
			return true
		}
		return !containsString(values, value)

	case ConditionOperatorExists:
		if nullable {
			return value != ""
		}
		return true // non-nullable fields are always populated

	case ConditionOperatorNotExists:
		if nullable {
			return value == ""
		}
		return false

	case ConditionOperatorGreaterThan, ConditionOperatorGreaterOrEqual,
		ConditionOperatorLessThan, ConditionOperatorLessOrEqual:
		// Range operators on strings match nothing, as in whereBuilder.
		return false

	default:
		return false
	}
}

func matchCustomFieldCondition(fieldName string, cond *ConditionNode, log *LogItem) bool {
	raw, present := log.Fields[fieldName]
	value := ""
	if present {
		value = StringifyFieldValue(raw)
	}

	switch cond.Operator {
	case ConditionOperatorExists:
		return present

	case ConditionOperatorNotExists:
		return !present

	case ConditionOperatorGreaterThan, ConditionOperatorGreaterOrEqual,
		ConditionOperatorLessThan, ConditionOperatorLessOrEqual:
		// Range operators on custom fields match nothing, as in whereBuilder.
		return false

	default:
		// An absent custom field is "", nullable, like whereBuilder's notSafe().
		return matchStringCondition(value, cond, !present)
	}
}

func containsString(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
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

// matchTimestamp compares log.Timestamp to a condition value parsed as
// whereBuilder.coerceValue does; an unparseable value matches nothing.
func matchTimestamp(log *LogItem, cond *ConditionNode) bool {
	// Exists/NotExists don't need cond.Value: timestamp is always populated.
	switch cond.Operator {
	case ConditionOperatorExists:
		return true
	case ConditionOperatorNotExists:
		return false
	}

	target, ok := coerceTimestamp(cond.Value)
	if !ok {
		return false // unparseable value matches nothing, mirrors whereBuilder's "1=0"
	}

	switch cond.Operator {
	case ConditionOperatorEquals:
		return log.Timestamp.Equal(target)
	case ConditionOperatorNotEquals:
		return !log.Timestamp.Equal(target)
	case ConditionOperatorGreaterThan:
		return log.Timestamp.After(target)
	case ConditionOperatorGreaterOrEqual:
		return !log.Timestamp.Before(target)
	case ConditionOperatorLessThan:
		return log.Timestamp.Before(target)
	case ConditionOperatorLessOrEqual:
		return !log.Timestamp.After(target)
	default:
		return false
	}
}

func coerceTimestamp(value any) (time.Time, bool) {
	switch typed := value.(type) {
	case time.Time:
		return typed, true
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, typed)
		if err != nil {
			return time.Time{}, false
		}
		return parsed, true
	default:
		return time.Time{}, false
	}
}
