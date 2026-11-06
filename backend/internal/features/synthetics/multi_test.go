package synthetics_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"obzly/internal/features/synthetics"
	"obzly/internal/synthetics/spec"
	test_utils "obzly/internal/util/testing"
)

func Test_MultiStepMonitor_CreateUpdate_StoresSecrets_AndProbesGetTheSteps(t *testing.T) {
	router, admin, member := setup(t)
	destination := createDestination(t, router, admin, "results")
	request := multiCheckRequest("Checkout API flow", destination.ID)
	request.Secrets = map[string]string{"CHECKOUT_PASSWORD": "hunter2-stored"}

	created := createCheck(t, router, admin, request)

	require.Equal(t, spec.TypeMulti, created.Type)
	require.Len(t, created.Definition.Steps, 2)
	require.Equal(t, 60, created.Definition.TimeoutSeconds)
	require.Equal(t, 10, created.Definition.Steps[0].TimeoutSeconds)
	require.Equal(t, []synthetics.SecretState{{Name: "CHECKOUT_PASSWORD", Stored: true}}, created.Secrets)

	update := multiCheckRequest("Checkout API flow", destination.ID)
	update.Version = created.Version
	update.Definition.Steps = append(update.Definition.Steps, spec.Step{
		Name: "Cancel order", ContinueOnFailure: true,
		Request: spec.HTTPRequest{Method: "DELETE", URL: "https://api.example.com/orders/{{orderId}}"},
	})
	var updated synthetics.CheckView
	test_utils.MakePutRequestAndUnmarshal(t, router, "/api/v1/synthetics/checks/"+created.ID.String(),
		bearer(admin), update, http.StatusOK, &updated)
	require.Equal(t, 2, updated.Version)
	require.Len(t, updated.Definition.Steps, 3)
	require.True(t, updated.Definition.Steps[2].ContinueOnFailure)
	require.Equal(t, []synthetics.SecretState{{Name: "CHECKOUT_PASSWORD", Stored: true}}, updated.Secrets,
		"left out of the update, the stored value stays")

	// Members see the steps' names and URLs, not variables, headers or bodies.
	response := test_utils.MakeGetRequest(t, router, "/api/v1/synthetics/checks/"+created.ID.String(),
		bearer(member), http.StatusOK)
	require.Contains(t, string(response.Body), "Create order")
	require.NotContains(t, string(response.Body), "CHECKOUT_PASSWORD")
	require.NotContains(t, string(response.Body), "synthetic@example.com")
	require.NotContains(t, string(response.Body), "Bearer")

	var clone synthetics.CheckView
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/synthetics/checks/"+created.ID.String()+"/clone",
		bearer(admin), nil, http.StatusOK, &clone)
	require.Len(t, clone.Definition.Steps, 3)

	probe := createProbe(t, router, admin, "probe-blr-01", "office-blr")
	served := probeRequest(t, router, probe.Token, "GET", "/api/v1/synthetics/probe/config", nil, nil, http.StatusOK)
	var config spec.ProbeConfig
	require.NoError(t, json.Unmarshal(served.Body, &config))
	require.Len(t, config.Checks, 1, "the clone is disabled")
	sent := config.Checks[0]
	require.Len(t, sent.Definition.Steps, 3)
	require.Equal(t, "hunter2-stored", sent.Definition.Variables["password"])
	require.Equal(t, "Bearer {{token}}", sent.Definition.Steps[1].Request.Headers["Authorization"])
	require.Equal(t, []string{"hunter2-stored"}, sent.Redact)
	require.Equal(t, "https://api.example.com/auth/login", sent.Target)
}

func Test_MultiStepMonitor_UndefinedVariable_Returns400NamingIt(t *testing.T) {
	router, admin, _ := setup(t)
	destination := createDestination(t, router, admin, "results")
	request := multiCheckRequest("Broken flow", destination.ID)
	request.Definition.Steps[1].Request.URL = "https://api.example.com/carts/{{cartId}}"

	response := test_utils.MakePostRequest(t, router, "/api/v1/synthetics/checks", bearer(admin), request,
		http.StatusBadRequest)

	require.Contains(t, string(response.Body), "step 2: uses {{cartId}}, which no earlier step or variable defines")
}

func Test_MultiStepTest_QueuedForAProbe_StepResultsReachTheEditor_AndFromTheControlPlaneIsHardened(t *testing.T) {
	router, admin, _ := setup(t)
	destination := createDestination(t, router, admin, "results")
	probe := createProbe(t, router, admin, "probe-blr-01", "office-blr")
	probeRequest(t, router, probe.Token, "GET", "/api/v1/synthetics/probe/config", nil, nil, http.StatusOK)
	draft := multiCheckRequest("Draft flow", destination.ID)
	draft.Secrets = map[string]string{"CHECKOUT_PASSWORD": "unsaved-editor-value"}

	var run synthetics.TestRun
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/synthetics/tests", bearer(admin),
		synthetics.TestRequest{Check: draft, Locations: []string{"office-blr"}}, http.StatusOK, &run)
	polled := probeRequest(t, router, probe.Token, "GET", "/api/v1/synthetics/probe/tasks?wait=2", nil, nil,
		http.StatusOK)
	var task spec.Task
	require.NoError(t, json.Unmarshal(polled.Body, &task))
	require.Len(t, task.Check.Definition.Steps, 2)
	require.Equal(t, "unsaved-editor-value", task.Check.Definition.Variables["password"])
	require.Equal(t, []string{"unsaved-editor-value"}, task.Check.Redact)
	result := spec.RunResult{
		Check: "Draft flow", Type: spec.TypeMulti, Status: spec.StatusDown, DurationMs: 420,
		StartedAt: time.Now().UTC(), Error: "step 2 (Create order): Status equals 201 failed (got 409)",
		Steps: []spec.StepResult{
			{Index: 1, Name: "Sign in", Status: spec.StatusUp, HTTPStatus: 200,
				Extracted: []spec.ExtractedValue{{Name: "token", Secret: true}}},
			{Index: 2, Name: "Create order", Status: spec.StatusDown, HTTPStatus: 409},
		},
	}
	probeRequest(t, router, probe.Token, "POST", "/api/v1/synthetics/probe/tasks/"+task.ID+"/result", result, nil,
		http.StatusOK)
	var done synthetics.TestRun
	test_utils.MakeGetRequestAndUnmarshal(t, router, "/api/v1/synthetics/tests/"+run.ID, bearer(admin),
		http.StatusOK, &done)
	require.Equal(t, synthetics.TaskDone, done.Tasks[0].Status)
	require.Len(t, done.Tasks[0].Result.Steps, 2)
	require.Equal(t, spec.StatusDown, done.Tasks[0].Result.Steps[1].Status)

	internal := multiCheckRequest("Internal flow", destination.ID)
	internal.Definition.Steps[0].Request.URL = "http://10.0.0.5/auth/login"
	internal.Secrets = map[string]string{"CHECKOUT_PASSWORD": "unsaved-editor-value"}
	var local synthetics.TestRun
	test_utils.MakePostRequestAndUnmarshal(t, router, "/api/v1/synthetics/tests", bearer(admin),
		synthetics.TestRequest{Check: internal, FromControlPlane: true}, http.StatusOK, &local)
	require.Len(t, local.Tasks, 1)
	steps := local.Tasks[0].Result.Steps
	require.Len(t, steps, 2)
	require.Equal(t, spec.StatusDown, steps[0].Status)
	require.Contains(t, steps[0].Error, "not public")
	require.Equal(t, spec.StatusSkipped, steps[1].Status)
}

func multiCheckRequest(name string, destinationID uuid.UUID) synthetics.CheckRequest {
	return synthetics.CheckRequest{
		Name: name, Enabled: true, DestinationID: destinationID, IntervalSeconds: 300,
		Definition: spec.Definition{
			Type: spec.TypeMulti,
			Variables: map[string]string{
				"user": "synthetic@example.com", "password": "${env:CHECKOUT_PASSWORD}",
			},
			Steps: []spec.Step{
				{
					Name: "Sign in",
					Request: spec.HTTPRequest{Method: "POST", URL: "https://api.example.com/auth/login",
						Body: `{"email":"{{user}}","password":"{{password}}"}`},
					Assertions: []spec.Assertion{{Kind: spec.AssertStatus, Op: spec.OpEquals, Value: "200"}},
					Extract:    []spec.Extract{{Name: "token", From: spec.ExtractJSON, Path: "$.accessToken", Secret: true}},
				},
				{
					Name: "Create order",
					Request: spec.HTTPRequest{Method: "POST", URL: "https://api.example.com/orders",
						Headers: map[string]string{"Authorization": "Bearer {{token}}"}},
					Extract: []spec.Extract{{Name: "orderId", From: spec.ExtractJSON, Path: "$.id"}},
				},
			},
		},
	}
}
