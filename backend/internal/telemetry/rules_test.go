package telemetry_test

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/model/rulefmt"
	"github.com/prometheus/prometheus/promql/parser"
	"github.com/stretchr/testify/require"
)

const shippedRulesFile = "../../../deploy/observability/prometheus/obzly-rules.yaml"

// Guards the shipped alert rules against PromQL typos and against a metric
// being renamed here without the rules following.
func Test_ShippedPrometheusRules_ParseAndReferenceOnlyDefinedMetrics(t *testing.T) {
	groups, errs := rulefmt.ParseFile(
		shippedRulesFile, false, model.UTF8Validation, parser.NewParser(parser.Options{}),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	require.Empty(t, errs)
	require.NotEmpty(t, groups.Groups)

	defined := definedMetricNames(t)
	for _, group := range groups.Groups {
		for _, rule := range group.Rules {
			if rule.Record != "" {
				defined[rule.Record] = true
			}
		}
	}

	for _, group := range groups.Groups {
		for _, rule := range group.Rules {
			requireDefinedMetrics(t, "rule "+rule.Alert+rule.Record, rule.Expr, defined)
		}
	}
}

// definedMetricNames reads the obzly_* metric names this package declares,
// in either style: promauto opts (Name: "...") or custom-collector
// descriptors (namespace+"_...").
func definedMetricNames(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)

	names := map[string]bool{}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`Name:\s+"([a-z_]+)"`),
		regexp.MustCompile(`namespace\+"_([a-z_]+)"`),
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		source, err := os.ReadFile(file)
		require.NoError(t, err)
		for _, pattern := range patterns {
			for _, match := range pattern.FindAllStringSubmatch(string(source), -1) {
				names["obzly_"+match[1]] = true
			}
		}
	}
	require.NotEmpty(t, names)
	return names
}

// requireDefinedMetrics fails if expr references an obzly metric that is
// neither defined in this package nor a recording rule in defined.
func requireDefinedMetrics(t *testing.T, where, expr string, defined map[string]bool) {
	t.Helper()
	parsed, err := parser.NewParser(parser.Options{}).ParseExpr(expr)
	require.NoError(t, err, where)
	for _, selector := range parser.ExtractSelectors(parsed) {
		for _, matcher := range selector {
			if matcher.Name != labels.MetricName || matcher.Type != labels.MatchEqual {
				continue
			}
			name := matcher.Value
			if !strings.HasPrefix(name, "obzly") {
				continue // go_*, process_*, up
			}
			require.Truef(t, defined[histogramBase(name)] || defined[name],
				"%s references %q, which telemetry does not define", where, name)
		}
	}
}

func histogramBase(name string) string {
	for _, suffix := range []string{"_bucket", "_count", "_sum"} {
		if trimmed, ok := strings.CutSuffix(name, suffix); ok {
			return trimmed
		}
	}
	return name
}
