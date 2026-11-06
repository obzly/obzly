package checks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"obzly/internal/synthetics/report"
	"obzly/internal/synthetics/spec"
)

func Test_RunMulti_ExtractsEachKind_AndSubstitutesThemInLaterSteps(t *testing.T) {
	var seen atomic.Pointer[http.Request]
	var seenBody atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			w.Header().Set("X-Request-Id", "req-77")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"accessToken":"tok-abc","items":[{"id":42}],"note":"order=ord_8f2c1a;"}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		seen.Store(r)
		seenBody.Store(string(body))
		_, _ = w.Write([]byte(`{"state":"pending"}`))
	}))
	defer server.Close()
	check := multiCheck(
		spec.Step{
			Name:    "Sign in",
			Request: spec.HTTPRequest{Method: "POST", URL: server.URL + "/login", Body: `{"user":"{{user}}"}`},
			Extract: []spec.Extract{
				{Name: "token", From: spec.ExtractJSON, Path: "$.accessToken"},
				{Name: "id", From: spec.ExtractJSON, Path: "$.items[0].id"},
				{Name: "requestId", From: spec.ExtractHeader, Path: "x-request-id"},
				{Name: "code", From: spec.ExtractStatus},
				{Name: "order", From: spec.ExtractRegex, Path: `order=(\w+);`},
				{Name: "whole", From: spec.ExtractBody},
			},
		},
		spec.Step{
			Name: "Use them",
			Request: spec.HTTPRequest{
				Method:  "PUT",
				URL:     server.URL + "/orders/{{id}}?order={{order}}&code={{code}}&u={{$uuid}}&t={{$timestamp}}&r={{$randomInt}}",
				Headers: map[string]string{"Authorization": "Bearer {{token}}", "X-Req": "{{requestId}}", "X-At": "{{$isoTimestamp}}"},
				Body:    "{{whole}}",
			},
			Assertions: []spec.Assertion{
				{Kind: spec.AssertJSON, Op: spec.OpEquals, Path: "$.state", Value: "{{state}}", Severity: spec.SeverityFail},
			},
		},
	)
	check.Definition.Variables = map[string]string{"user": "synthetic@example.com", "state": "pending"}

	result := testRunner().Run(context.Background(), check)

	require.Equal(t, spec.StatusUp, result.Status, result.Error)
	require.Len(t, result.Steps, 2)
	request := seen.Load()
	require.NotNil(t, request)
	require.Equal(t, "/orders/42", request.URL.Path)
	require.Equal(t, "ord_8f2c1a", request.URL.Query().Get("order"))
	require.Equal(t, "201", request.URL.Query().Get("code"))
	require.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, request.URL.Query().Get("u"))
	require.Regexp(t, `^\d{10}$`, request.URL.Query().Get("t"))
	require.Regexp(t, `^\d{1,4}$`, request.URL.Query().Get("r"))
	require.Equal(t, "Bearer tok-abc", request.Header.Get("Authorization"))
	require.Equal(t, "req-77", request.Header.Get("X-Req"))
	_, err := time.Parse(time.RFC3339, request.Header.Get("X-At"))
	require.NoError(t, err)
	require.Contains(t, seenBody.Load(), `"accessToken":"tok-abc"`)
	require.Equal(t, "Use them", result.Steps[1].Name)
	require.Equal(t, `$.state equals "{{state}}"`, result.Steps[1].Assertions[0].Description)
	extracted := map[string]string{}
	for _, value := range result.Steps[0].Extracted {
		extracted[value.Name] = value.Value
	}
	require.Equal(t, "tok-abc", extracted["token"])
	require.Equal(t, "42", extracted["id"])
	require.Contains(t, result.Steps[0].PhasesMs, "connect")
	require.Equal(t, server.URL+"/login", result.Target)
}

func Test_RunMulti_Cookies_CarriedBetweenSteps_UnlessTurnedOff(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "s1", Path: "/"})
			return
		}
		if cookie, err := r.Cookie("session"); err != nil || cookie.Value != "s1" {
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer server.Close()
	steps := []spec.Step{
		{Name: "Sign in", Request: spec.HTTPRequest{Method: "POST", URL: server.URL + "/login"}},
		{Name: "Account", Request: spec.HTTPRequest{Method: "GET", URL: server.URL + "/account"}, Assertions: []spec.Assertion{
			{Kind: spec.AssertStatus, Op: spec.OpEquals, Value: "200", Severity: spec.SeverityFail},
		}},
	}

	withCookies := testRunner().Run(context.Background(), multiCheck(steps...))
	off := false
	check := multiCheck(steps...)
	check.Definition.Cookies = &off
	withoutCookies := testRunner().Run(context.Background(), check)

	require.Equal(t, spec.StatusUp, withCookies.Status, withCookies.Error)
	require.Equal(t, spec.StatusDown, withoutCookies.Status)
	require.Equal(t, 401, withoutCookies.Steps[1].HTTPStatus)
}

func Test_RunMulti_AFailedStep_SkipsTheRest_ButContinueOnFailureStepsRun(t *testing.T) {
	var cleanups, skipped atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/slow":
			w.WriteHeader(http.StatusAccepted)
		case "/orders":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"duplicate sku in cart"}`))
		case "/never":
			skipped.Add(1)
		case "/cleanup":
			cleanups.Add(1)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	status := func(code string, severity string) []spec.Assertion {
		return []spec.Assertion{{Kind: spec.AssertStatus, Op: spec.OpEquals, Value: code, Severity: severity}}
	}

	result := testRunner().Run(context.Background(), multiCheck(
		spec.Step{Name: "Warns", Request: spec.HTTPRequest{Method: "GET", URL: server.URL + "/slow"},
			Assertions: status("200", spec.SeverityWarn)},
		spec.Step{Name: "Create order", Request: spec.HTTPRequest{Method: "POST", URL: server.URL + "/orders"},
			Assertions: status("201", spec.SeverityFail),
			Extract:    []spec.Extract{{Name: "orderId", From: spec.ExtractJSON, Path: "$.id"}}},
		spec.Step{Name: "Check order", Request: spec.HTTPRequest{Method: "GET", URL: server.URL + "/never"}},
		spec.Step{Name: "Cancel order", ContinueOnFailure: true,
			Request:    spec.HTTPRequest{Method: "DELETE", URL: server.URL + "/cleanup"},
			Assertions: status("204", spec.SeverityFail)},
		spec.Step{Name: "Cancel by id", ContinueOnFailure: true,
			Request: spec.HTTPRequest{Method: "DELETE", URL: server.URL + "/cleanup/{{orderId}}"}},
	))

	require.Equal(t, spec.StatusDown, result.Status)
	require.Equal(t, `step 2 (Create order): Status equals 201 failed (got 409)`, result.Error)
	statuses := make([]string, len(result.Steps))
	for i, step := range result.Steps {
		statuses[i] = step.Status
	}
	require.Equal(t, []string{spec.StatusWarn, spec.StatusDown, spec.StatusSkipped, spec.StatusUp, spec.StatusDown}, statuses)
	require.Equal(t, `{"error":"duplicate sku in cart"}`, result.Steps[1].BodySnippet)
	require.Empty(t, result.Steps[3].BodySnippet, "only steps that didn't pass keep the body")
	require.True(t, result.Steps[3].AfterFailure)
	require.Contains(t, result.Steps[4].Error, "{{orderId}} has no value")
	require.Equal(t, int32(0), skipped.Load())
	require.Equal(t, int32(1), cleanups.Load(), "a step missing a value sends nothing")
	summary := report.Summary(result)
	require.Contains(t, summary, "3. Check order: skipped")
	require.Contains(t, summary, "2. Create order: POST "+server.URL+"/orders → 409")
	require.Contains(t, summary, "assertions 0/1 passed: Status equals 201 failed (got 409)")
}

func Test_RunMulti_StepAndTotalTimeouts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(1500 * time.Millisecond):
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	slow := func(name string, timeout int) spec.Step {
		return spec.Step{Name: name, TimeoutSeconds: timeout, Request: spec.HTTPRequest{Method: "GET", URL: server.URL}}
	}

	stepTimeout := testRunner().Run(context.Background(), multiCheck(slow("Slow", 1)))
	check := multiCheck(slow("First", 2), slow("Second", 2), slow("Third", 2))
	check.Definition.TimeoutSeconds = 2
	total := testRunner().Run(context.Background(), check)

	require.Equal(t, spec.StatusDown, stepTimeout.Status)
	require.Equal(t, "timed out after 1s", stepTimeout.Steps[0].Error)
	require.Equal(t, spec.StatusUp, total.Steps[0].Status, total.Steps[0].Error)
	require.Equal(t, "the check's total timeout ran out", total.Steps[1].Error)
	require.Equal(t, spec.StatusSkipped, total.Steps[2].Status)
	require.Less(t, total.DurationMs, 2600.0)
}

func Test_RunMulti_ReadsAtMostOneMBOfAResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", 3<<20) + "END"))
	}))
	defer server.Close()

	result := testRunner().Run(context.Background(), multiCheck(
		spec.Step{Name: "Big", Request: spec.HTTPRequest{Method: "GET", URL: server.URL},
			Assertions: []spec.Assertion{{Kind: spec.AssertBody, Op: spec.OpContains, Value: "END", Severity: spec.SeverityWarn}},
			Extract:    []spec.Extract{{Name: "all", From: spec.ExtractBody}}},
	))

	require.Equal(t, spec.StatusDown, result.Status)
	require.Equal(t, int64(3<<20+3), result.Steps[0].ResponseBytes)
	require.Equal(t, "1048576 bytes", result.Steps[0].Assertions[0].Actual)
	require.False(t, result.Steps[0].Assertions[0].Passed)
	require.Equal(t, "extract all: the value is longer than 4 KB", result.Steps[0].Error)
}

func Test_RunMulti_Secrets_NeverInTheRunLogOrTestResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/login" {
			// An API that echoes the password, then hands out a token.
			_, _ = w.Write([]byte(`{"echo":` + string(body) + `,"token":"tok-very-secret-1"}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("token " + r.URL.Query().Get("t") + " and stored " + r.Header.Get("X-Stored") + " rejected"))
	}))
	defer server.Close()
	check := multiCheck(
		spec.Step{Name: "Sign in", Request: spec.HTTPRequest{Method: "POST", URL: server.URL + "/login",
			Body: `{"password":"{{password}}"}`},
			Assertions: []spec.Assertion{{Kind: spec.AssertBody, Op: spec.OpContains, Value: "nope", Severity: spec.SeverityWarn}},
			Extract:    []spec.Extract{{Name: "token", From: spec.ExtractJSON, Path: "$.token", Secret: true}}},
		spec.Step{Name: "Use", Request: spec.HTTPRequest{Method: "GET", URL: server.URL + "/api?t={{token}}",
			Headers: map[string]string{"X-Stored": "{{apiKey}}"}},
			Assertions: []spec.Assertion{
				{Kind: spec.AssertBody, Op: spec.OpContains, Value: "{{token}}x", Severity: spec.SeverityFail},
			}},
	)
	check.Definition.Variables = map[string]string{
		"password": "${env:OBZLY_SECRET_CHECKOUT_PASSWORD}", "apiKey": "control-plane-stored-key",
	}
	check.Redact = []string{"control-plane-stored-key"}
	runner := testRunner()
	runner.Env = NewEnvPolicy("", func(name string) (string, bool) {
		return "hunter2-probe-env", name == "OBZLY_SECRET_CHECKOUT_PASSWORD"
	})

	result := runner.Run(context.Background(), check)

	require.Equal(t, spec.StatusDown, result.Status)
	require.Equal(t, []spec.ExtractedValue{{Name: "token", Secret: true}}, result.Steps[0].Extracted)
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	logLine := report.Summary(result)
	for _, secret := range []string{"hunter2-probe-env", "tok-very-secret-1", "control-plane-stored-key"} {
		require.NotContains(t, string(encoded), secret)
		require.NotContains(t, logLine, secret)
	}
	require.Contains(t, result.Steps[1].BodySnippet, "token [redacted] and stored [redacted] rejected")
	require.Contains(t, logLine, "extracted token (secret)")
}

func Test_RunMulti_AVariableWithNoValue_FailsTheStepWithoutSendingIt(t *testing.T) {
	result := testRunner().Run(context.Background(), multiCheck(
		spec.Step{Name: "Only", Request: spec.HTTPRequest{Method: "GET", URL: "https://api.example.com/{{nothing}}"}},
	))

	require.Equal(t, spec.StatusDown, result.Status)
	require.Regexp(t, regexp.MustCompile(`^step 1 \(Only\): \{\{nothing\}\} has no value`), result.Error)
}

func multiCheck(steps ...spec.Step) spec.ProbeCheck {
	return spec.ProbeCheck{ID: "m1", Name: "Checkout API flow", Definition: spec.Definition{
		Type: spec.TypeMulti, TimeoutSeconds: 30, Steps: steps,
	}}
}
