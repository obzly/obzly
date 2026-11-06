package spec

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_ParseMonitorList_TheSampleFile_ParsesEveryMonitorWithDefaults(t *testing.T) {
	data, err := os.ReadFile("../../../../docs/examples/synthetics-checks.yaml")
	require.NoError(t, err)

	list, problems := ParseMonitorList(data)

	require.Empty(t, problems)
	require.False(t, list.Prune)
	require.Len(t, list.Monitors, 9)
	byName := map[string]Monitor{}
	for _, monitor := range list.Monitors {
		byName[monitor.Name] = monitor
	}

	health := byName["Public API health"]
	require.Equal(t, TypeHTTP, health.Definition.Type)
	require.Equal(t, 180, health.IntervalSeconds)
	require.Equal(t, 10, health.Definition.TimeoutSeconds)
	require.Equal(t, "synthetics", health.Destination)
	require.Equal(t, 2, health.DownLocations)
	require.Equal(t, []string{"aws-ap-south-1", "aws-eu-west-1", "office-blr"}, health.Locations)
	require.Equal(t, "GET", health.Definition.Request.Method)
	require.Len(t, health.Definition.Assertions, 6)
	require.Equal(
		t,
		Assertion{Kind: AssertStatus, Op: OpEquals, Value: "200", Severity: SeverityFail},
		health.Definition.Assertions[0],
	)
	require.Equal(
		t,
		Assertion{Kind: AssertResponseTime, Op: OpBelow, Number: 400, Severity: SeverityWarn},
		health.Definition.Assertions[2],
	)
	require.Equal(
		t,
		Assertion{Kind: AssertJSON, Op: OpExists, Path: "$.checks.database", Severity: SeverityFail},
		health.Definition.Assertions[5],
	)
	require.Equal(t, 21, health.Definition.TLS.WarnDays)

	quote := byName["Orders API - create quote"]
	require.Equal(t, 300, quote.IntervalSeconds)
	require.Empty(t, quote.Locations, "[all] means every location")
	require.Equal(t, []int{200, 201}, quote.Definition.Assertions[0].Values)
	require.Equal(t, []string{"ORDERS_API_TOKEN"}, quote.Definition.Placeholders())

	flow := byName["Checkout API flow"].Definition
	require.Len(t, flow.Steps, 3)
	require.Equal(t, 60, flow.TimeoutSeconds)
	require.True(t, flow.Steps[2].ContinueOnFailure)

	require.False(t, byName["Internal admin portal"].Definition.VerifiesTLS())
	require.Equal(t, "TLS1.2", byName["Mail server certificate"].Definition.TLS.MinProtocol)
	require.Equal(t, "1.1.1.1:53", byName["Apex A records"].Definition.DNS.Resolver)
	require.Equal(t, 5, byName["Branch office gateway"].Definition.ICMP.Count)
	require.False(t, byName["Legacy status page"].Enabled)
	require.True(t, health.Enabled)
}

// The UI offers the same file as a download; it ships with the frontend
// build, which can't read docs/.
func Test_SampleFile_TheFrontendCopy_MatchesTheDocs(t *testing.T) {
	docs, err := os.ReadFile("../../../../docs/examples/synthetics-checks.yaml")
	require.NoError(t, err)
	frontend, err := os.ReadFile("../../../../frontend/src/features/synthetics/lib/synthetics-checks.sample.yaml")
	require.NoError(t, err)

	require.Equal(
		t,
		string(docs),
		string(frontend),
		"copy docs/examples/synthetics-checks.yaml over the frontend's copy",
	)
}

func Test_MarshalMonitorList_ThenParse_RoundTrips(t *testing.T) {
	data, err := os.ReadFile("../../../../docs/examples/synthetics-checks.yaml")
	require.NoError(t, err)
	original, problems := ParseMonitorList(data)
	require.Empty(t, problems)

	exported, err := MarshalMonitorList(original.Monitors)
	require.NoError(t, err)
	again, problems := ParseMonitorList(exported)

	require.Empty(t, problems, string(exported))
	require.ElementsMatch(t, original.Monitors, again.Monitors)
}

func Test_ParseMonitorList_InvalidMonitors_ReportsEveryProblemWithItsLine(t *testing.T) {
	data := []byte(`apiVersion: obzly.com/synthetics/v1
kind: MonitorList
monitors:
  - name: Too often
    type: http
    interval: 10s
    request: {url: https://example.com}
  - name: No url
    type: http
  - name: Fine
    type: tcp
    target: db:5432
  - name: fine
    type: tcp
    target: db:5433
`)

	list, problems := ParseMonitorList(data)

	require.Nil(t, list)
	require.Len(t, problems, 3)
	require.Equal(t, 4, problems[0].Line)
	require.Contains(t, problems[0].Message, "at least 30s")
	require.Equal(t, 8, problems[1].Line)
	require.Contains(t, problems[1].Message, "request.url is required")
	require.Equal(t, 13, problems[2].Line)
	require.Contains(t, problems[2].Message, "duplicate name")
}

func Test_ParseMonitorList_UnknownField_IsRefused(t *testing.T) {
	data := []byte("apiVersion: obzly.com/synthetics/v1\nkind: MonitorList\nmonitorz: []\n")

	_, problems := ParseMonitorList(data)

	require.Len(t, problems, 1)
	require.True(t, strings.Contains(problems[0].Message, "monitorz"))
}

func Test_Resolve_FillsKnownPlaceholdersOnly(t *testing.T) {
	definition := Definition{Type: TypeHTTP, Request: &HTTPRequest{
		URL:     "https://api.example.com/${env:PATH_PART}",
		Headers: map[string]string{"Authorization": "Bearer ${env:TOKEN}"},
	}}

	resolved := definition.Resolve(func(name string) (string, bool) {
		if name == "TOKEN" {
			return "s3cret", true
		}
		return "", false
	})

	require.Equal(t, "Bearer s3cret", resolved.Request.Headers["Authorization"])
	require.Equal(t, []string{"PATH_PART"}, resolved.Placeholders())
	require.Equal(t, "Bearer ${env:TOKEN}", definition.Request.Headers["Authorization"], "the original is unchanged")
}
