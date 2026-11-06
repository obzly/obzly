package metrics_promql

import (
	"strings"

	"obzly/internal/util/attrnames"

	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
)

// NormalizePromQL rewrites metric and label names in a query to the
// attrnames rule (system.cpu.time -> system_cpu_time), so queries written
// against OTel's dotted names still match stored data. A query that doesn't
// parse, or needs no change, is returned unchanged.
func NormalizePromQL(query string) string {
	expr, err := GetParser().ParseExpr(query)
	if err != nil {
		return query
	}

	changed := false
	rename := func(names []string, normalize func(string) string) []string {
		for i, name := range names {
			if normal := normalize(name); normal != name {
				names[i] = normal
				changed = true
			}
		}
		return names
	}
	renameLiteral := func(node parser.Expr) {
		if literal, ok := node.(*parser.StringLiteral); ok {
			if normal := attrnames.Normalize(literal.Val); normal != literal.Val {
				literal.Val = normal
				changed = true
			}
		}
	}

	parser.Inspect(expr, func(node parser.Node, _ []parser.Node) error {
		switch n := node.(type) {
		case *parser.VectorSelector:
			if normal := attrnames.NormalizeMetricName(n.Name); normal != n.Name {
				n.Name = normal
				changed = true
			}
			for i, matcher := range n.LabelMatchers {
				if normal := normalizeMatcher(matcher); normal != nil {
					n.LabelMatchers[i] = normal
					changed = true
				}
			}
			// {"__name__"="x"} prints as plain x once x is a valid name.
			for _, matcher := range n.LabelMatchers {
				if n.Name == "" && matcher.Name == model.MetricNameLabel && matcher.Type == labels.MatchEqual &&
					changed {
					n.Name = matcher.Value
				}
			}
		case *parser.AggregateExpr:
			n.Grouping = rename(n.Grouping, attrnames.Normalize)
			if n.Op == parser.COUNT_VALUES {
				renameLiteral(n.Param)
			}
		case *parser.BinaryExpr:
			if n.VectorMatching != nil {
				n.VectorMatching.MatchingLabels = rename(n.VectorMatching.MatchingLabels, attrnames.Normalize)
				n.VectorMatching.Include = rename(n.VectorMatching.Include, attrnames.Normalize)
			}
		case *parser.Call:
			switch n.Func.Name {
			case "label_replace": // (v, dst, replacement, src, regex)
				if len(n.Args) == 5 {
					renameLiteral(n.Args[1])
					renameLiteral(n.Args[3])
				}
			case "label_join": // (v, dst, separator, src...)
				if len(n.Args) >= 3 {
					renameLiteral(n.Args[1])
					for _, arg := range n.Args[3:] {
						renameLiteral(arg)
					}
				}
			}
		}
		return nil
	})

	if !changed {
		return query
	}
	return expr.String()
}

// normalizeMatcher returns a renamed matcher, or nil when none is needed.
func normalizeMatcher(matcher *labels.Matcher) *labels.Matcher {
	name, value := matcher.Name, matcher.Value
	if name == model.MetricNameLabel {
		switch matcher.Type {
		case labels.MatchEqual, labels.MatchNotEqual:
			value = attrnames.NormalizeMetricName(value)
		default: // regex: an escaped dot becomes an underscore
			value = strings.ReplaceAll(value, `\.`, "_")
		}
	} else {
		name = attrnames.Normalize(name)
	}
	if name == matcher.Name && value == matcher.Value {
		return nil
	}
	normal, err := labels.NewMatcher(matcher.Type, name, value)
	if err != nil {
		return nil
	}
	return normal
}

// NormalizeMatchers renames matcher names and metric-name values (for API
// calls that pass matchers instead of a query, e.g. series/label values).
func NormalizeMatchers(matchers []*labels.Matcher) []*labels.Matcher {
	out := make([]*labels.Matcher, len(matchers))
	for i, matcher := range matchers {
		if normal := normalizeMatcher(matcher); normal != nil {
			out[i] = normal
		} else {
			out[i] = matcher
		}
	}
	return out
}
