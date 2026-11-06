package spec

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const multiYAML = `apiVersion: obzly.com/synthetics/v1
kind: MonitorList
defaults:
  destination: synthetics
monitors:
  - name: Checkout API flow
    type: multi
    interval: 5m
    cookies: false
    variables:
      user: synthetic@example.com
      password: ${env:OBZLY_SECRET_CHECKOUT_PASSWORD}
    steps:
      - name: Sign in
        request:
          method: POST
          url: https://api.example.com/auth/login
          headers: { Content-Type: application/json }
          body: '{"email":"{{user}}","password":"{{password}}"}'
        assertions:
          - status: 200
          - response_time_ms: {below: 800, severity: warn}
        extract:
          - { name: token, from: json, path: $.accessToken, secret: true }
          - { name: session, from: header, path: X-Session }
      - name: Create order
        timeout: 20s
        request:
          method: POST
          url: https://api.example.com/orders?at={{$timestamp}}
          headers: { Authorization: "Bearer {{token}}" }
          body: '{"sku":"TEST-1","qty":1}'
        assertions:
          - status: 201
          - json: { path: $.state, equals: pending }
        extract:
          - { name: orderId, from: regex, path: '"id":"(\w+)"' }
      - name: Cancel order
        continue_on_failure: true
        request:
          method: DELETE
          url: https://api.example.com/orders/{{orderId}}
          headers: { Authorization: "Bearer {{token}}" }
        assertions:
          - status: 204
`

func Test_ParseMonitorList_AMultiStepMonitor_RoundTripsThroughExport(t *testing.T) {
	list, problems := ParseMonitorList([]byte(multiYAML))
	require.Empty(t, problems)
	monitor := list.Monitors[0]
	definition := monitor.Definition

	require.Equal(t, TypeMulti, definition.Type)
	require.Equal(t, DefaultMultiTimeoutSeconds, definition.TimeoutSeconds)
	require.False(t, definition.KeepsCookies())
	require.Len(t, definition.Steps, 3)
	require.Equal(t, 10, definition.Steps[0].TimeoutSeconds)
	require.Equal(t, 20, definition.Steps[1].TimeoutSeconds)
	require.Equal(t, Extract{Name: "token", From: ExtractJSON, Path: "$.accessToken", Secret: true},
		definition.Steps[0].Extract[0])
	require.True(t, definition.Steps[2].ContinueOnFailure)
	require.Equal(t, []string{"OBZLY_SECRET_CHECKOUT_PASSWORD"}, definition.Placeholders())
	require.Equal(t, map[string]bool{"password": true}, definition.SecretVariables())
	require.Equal(t, "https://api.example.com/auth/login", definition.TargetLabel())

	exported, err := MarshalMonitorList(list.Monitors)
	require.NoError(t, err)
	again, problems := ParseMonitorList(exported)
	require.Empty(t, problems, string(exported))
	require.Equal(t, list.Monitors, again.Monitors)
	require.Contains(t, string(exported), "continue_on_failure: true")
	require.Contains(t, string(exported), "cookies: false")
	require.Contains(t, string(exported), "${env:OBZLY_SECRET_CHECKOUT_PASSWORD}")
}

func Test_NormalizeMulti_AnUndefinedVariable_IsRejectedNamingTheStep(t *testing.T) {
	for _, test := range []struct {
		name, yaml, problem string
	}{
		{"defined later", "{{orderId}}", "step 1: uses {{orderId}}, which no earlier step or variable defines"},
		{"unknown built-in", "{{$guid}}", "step 1: uses {{$guid}}, which no earlier step or variable defines"},
	} {
		t.Run(test.name, func(t *testing.T) {
			definition := Definition{Type: TypeMulti, Steps: []Step{
				{Name: "Cancel", Request: HTTPRequest{Method: "DELETE", URL: "https://api.example.com/orders/" + test.yaml}},
				{Name: "Create", Request: HTTPRequest{URL: "https://api.example.com/orders"},
					Extract: []Extract{{Name: "orderId", From: ExtractJSON, Path: "$.id"}}},
			}}

			require.EqualError(t, definition.Normalize(), test.problem)
		})
	}

	used := Definition{Type: TypeMulti, Steps: []Step{
		{Name: "A", Request: HTTPRequest{URL: "https://api.example.com/a"}, Assertions: []Assertion{
			{Kind: AssertHeader, Op: OpEquals, Name: "X-Id", Value: "{{missing}}"},
		}},
	}}
	require.ErrorContains(t, used.Normalize(), "{{missing}}", "assertion values count too")
}

func Test_NormalizeMulti_Limits(t *testing.T) {
	step := Step{Name: "A", Request: HTTPRequest{URL: "https://api.example.com/"}}
	many := make([]Step, MaxSteps+1)
	for i := range many {
		many[i] = step
	}
	for _, test := range []struct {
		definition Definition
		problem    string
	}{
		{Definition{Type: TypeMulti}, "add at least one step"},
		{Definition{Type: TypeMulti, Steps: many}, "at most 10 steps"},
		{Definition{Type: TypeMulti, Steps: []Step{step}, TimeoutSeconds: 121}, "between 1s and 120s"},
		{Definition{Type: TypeMulti, TimeoutSeconds: 30, Steps: []Step{{Name: "A", TimeoutSeconds: 31,
			Request: HTTPRequest{URL: "https://api.example.com/"}}}}, "total timeout (30s)"},
		{Definition{Type: TypeMulti, Steps: []Step{step}, Variables: map[string]string{"1bad": "x"}}, "variable \"1bad\""},
		{Definition{Type: TypeMulti, Steps: []Step{step}, Variables: map[string]string{
			"big": strings.Repeat("x", MaxValueBytes+1)}}, "longer than 4 KB"},
		{Definition{Type: TypeMulti, Steps: []Step{{Name: "A", Request: step.Request,
			Extract: []Extract{{Name: "x", From: "cookie"}}}}}, "from must be"},
		{Definition{Type: TypeMulti, Steps: []Step{{Name: "A", Request: step.Request,
			Extract: []Extract{{Name: "x", From: ExtractRegex, Path: "("}}}}}, "invalid regular expression"},
		{Definition{Type: TypeMulti, Steps: []Step{step}, Request: &HTTPRequest{URL: "https://api.example.com/"}},
			"in steps"},
		{Definition{Type: TypeHTTP, Request: &HTTPRequest{URL: "https://api.example.com/"}, Steps: []Step{step}},
			"multi-step checks only"},
	} {
		require.ErrorContains(t, test.definition.Normalize(), test.problem)
	}
}
