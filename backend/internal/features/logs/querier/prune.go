package logs_querier

import (
	"strings"

	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/storage/segments"
	"obzly/internal/util/attrnames"
)

// hintsFor turns the query conditions every matching row must satisfy - the
// query root, or children of top-level ANDs - into catalog pruning hints:
// service_name / level equals and IN, turned into strings exactly as
// buildCondition does. Anything under OR or NOT never prunes.
func hintsFor(query *logs_core.QueryNode) segments.PruningHints {
	var hints segments.PruningHints
	collectHints(query, &hints)
	return hints
}

func collectHints(node *logs_core.QueryNode, hints *segments.PruningHints) {
	if node == nil {
		return
	}
	switch node.Type {
	case logs_core.QueryNodeTypeCondition:
		addConditionHint(node.Condition, hints)
	case logs_core.QueryNodeTypeLogical:
		if node.Logic == nil || node.Logic.Operator != logs_core.LogicalOperatorAnd {
			return
		}
		for i := range node.Logic.Children {
			collectHints(&node.Logic.Children[i], hints)
		}
	}
}

func addConditionHint(cond *logs_core.ConditionNode, hints *segments.PruningHints) {
	if cond == nil {
		return
	}

	fieldName := attrnames.Normalize(strings.TrimSpace(cond.Field))
	var list *[]string
	switch fieldName {
	case "service_name":
		list = &hints.Services
	case "level":
		list = &hints.Levels
		cond = logs_core.LowercaseCondition(cond)
	default:
		return
	}

	var values []string
	switch cond.Operator {
	case logs_core.ConditionOperatorEquals:
		value, ok := coerceValue(fieldName, cond.Value)
		text, isString := value.(string)
		if !ok || !isString {
			return
		}
		values = []string{text}
	case logs_core.ConditionOperatorIn:
		values = asStringSlice(cond.Value)
	default:
		return
	}
	if len(values) == 0 {
		return // buildInCondition's own "IN ()" handling decides the result
	}
	*list = segments.Intersect(*list, values)
}
