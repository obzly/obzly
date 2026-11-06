package checks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	mathrand "math/rand/v2"
	"net/http"
	"net/http/cookiejar"
	"regexp"
	"slices"
	"strconv"
	"time"

	"obzly/internal/synthetics/spec"
	"obzly/internal/util/egress"
)

// runMulti runs the steps in order on one connection pool and cookie jar; after a failure only
// continue-on-failure (clean-up) steps run. addSecret gets each extracted secret, to redact it.
func runMulti(
	ctx context.Context,
	policy egress.Policy,
	definition spec.Definition,
	result *spec.RunResult,
	addSecret func(string),
) {
	var jar http.CookieJar
	if definition.KeepsCookies() {
		jar, _ = cookiejar.New(nil)
	}
	session := newHTTPSession(policy, definition.VerifiesTLS(), true, jar)
	defer session.close()

	values := make(map[string]string, len(definition.Variables))
	for name, value := range definition.Variables {
		values[name] = value
	}
	started := time.Now()
	failed := false
	for i, step := range definition.Steps {
		out := spec.StepResult{Index: i + 1, Name: step.Name, Method: step.Request.Method, URL: step.Request.URL}
		switch {
		case failed && !step.ContinueOnFailure:
			out.Status = spec.StatusSkipped
		case ctx.Err() != nil:
			out.Status, out.Error = spec.StatusDown, "the check's total timeout ran out before this step"
			out.AfterFailure = failed
		default:
			out.AfterFailure = failed
			runStep(ctx, session, step, values, &out, addSecret)
		}
		if out.Status == spec.StatusDown {
			failed = true
		}
		result.Steps = append(result.Steps, out)
	}
	result.DurationMs = millis(time.Since(started))
	finishMulti(result)
}

func runStep(
	ctx context.Context,
	session *httpSession,
	step spec.Step,
	values map[string]string,
	out *spec.StepResult,
	addSecret func(string),
) {
	var missing []string
	request := step.Request
	request = request.MapTexts(func(text string) string { return substitute(text, values, &missing) })
	assertions := slices.Clone(step.Assertions)
	for i := range assertions {
		assertions[i].Value = substitute(assertions[i].Value, values, &missing)
	}
	out.URL = request.URL
	if len(missing) > 0 {
		out.Status = spec.StatusDown
		out.Error = fmt.Sprintf("{{%s}} has no value: the step that sets it failed or didn't run", missing[0])
		return
	}

	timeout := time.Duration(step.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = spec.DefaultStepTimeoutSeconds * time.Second
	}
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	outcome := session.do(stepCtx, request)
	out.DurationMs, out.PhasesMs, out.Redirects = outcome.durationMs, outcome.phases, outcome.redirects
	out.HTTPStatus, out.ResponseBytes = outcome.status, outcome.bytes
	if outcome.err != "" {
		out.Status, out.Error = spec.StatusDown, outcome.err
		if errors.Is(stepCtx.Err(), context.DeadlineExceeded) {
			if ctx.Err() != nil {
				out.Error = "the check's total timeout ran out"
			} else {
				out.Error = fmt.Sprintf("timed out after %s", timeout)
			}
		}
		out.BodySnippet = snippet(outcome.body)
		return
	}

	out.Assertions = evaluate(assertions, outcome.status, outcome.durationMs, outcome.header, outcome.body)
	// Described from the template, so a value filled in never shows there.
	for i := range out.Assertions {
		out.Assertions[i].Description = describe(step.Assertions[i])
	}
	out.Status = assertionStatus(out.Assertions)
	if out.Status != spec.StatusDown {
		for _, extract := range step.Extract {
			value, err := extractValue(extract, outcome)
			if err != nil {
				out.Status, out.Error = spec.StatusDown, fmt.Sprintf("extract %s: %s", extract.Name, err)
				break
			}
			values[extract.Name] = value
			kept := spec.ExtractedValue{Name: extract.Name, Secret: extract.Secret}
			if extract.Secret {
				addSecret(value)
			} else {
				kept.Value = value
			}
			out.Extracted = append(out.Extracted, kept)
		}
	}
	if out.Status != spec.StatusUp {
		out.BodySnippet = snippet(outcome.body)
	}
}

// finishMulti settles the run's colour from its steps: red when any
// failed, amber when any warned. The error names the first failure.
func finishMulti(result *spec.RunResult) {
	result.Status = spec.StatusUp
	for _, step := range result.Steps {
		switch step.Status {
		case spec.StatusDown:
			if result.Status != spec.StatusDown {
				result.Error = fmt.Sprintf("step %d (%s): %s", step.Index, step.Name, stepFailure(step))
			}
			result.Status = spec.StatusDown
		case spec.StatusWarn:
			if result.Status == spec.StatusUp {
				result.Status = spec.StatusWarn
			}
		}
	}
}

func stepFailure(step spec.StepResult) string {
	if step.Error != "" {
		return step.Error
	}
	for _, assertion := range step.Assertions {
		if !assertion.Passed && assertion.Severity != spec.SeverityWarn {
			return fmt.Sprintf("%s failed (got %s)", assertion.Description, assertion.Actual)
		}
	}
	return "failed"
}

func assertionStatus(assertions []spec.AssertionResult) string {
	status := spec.StatusUp
	for _, assertion := range assertions {
		if assertion.Passed {
			continue
		}
		if assertion.Severity == spec.SeverityWarn {
			status = spec.StatusWarn
			continue
		}
		return spec.StatusDown
	}
	return status
}

// substitute fills {{name}} from values and the built-ins; names with no
// value are added to missing and left as they are.
func substitute(text string, values map[string]string, missing *[]string) string {
	return spec.TemplatePattern.ReplaceAllStringFunc(text, func(match string) string {
		name := spec.TemplatePattern.FindStringSubmatch(match)[1]
		switch name {
		case spec.BuiltinUUID:
			return newUUID()
		case spec.BuiltinTimestamp:
			return strconv.FormatInt(time.Now().UTC().Unix(), 10)
		case spec.BuiltinISOTimestamp:
			return time.Now().UTC().Format(time.RFC3339)
		case spec.BuiltinRandomInt:
			return strconv.Itoa(mathrand.IntN(1001)) //nolint:gosec // test data, not a secret
		}
		if value, ok := values[name]; ok {
			return value
		}
		if !slices.Contains(*missing, name) {
			*missing = append(*missing, name)
		}
		return match
	})
}

func extractValue(extract spec.Extract, outcome exchange) (string, error) {
	var value string
	switch extract.From {
	case spec.ExtractStatus:
		value = strconv.Itoa(outcome.status)
	case spec.ExtractBody:
		value = string(outcome.body)
	case spec.ExtractHeader:
		values, ok := outcome.header[http.CanonicalHeaderKey(extract.Path)]
		if !ok || len(values) == 0 {
			return "", fmt.Errorf("header %s isn't in the response", extract.Path)
		}
		value = values[0]
	case spec.ExtractJSON:
		var document any
		if json.Unmarshal(outcome.body, &document) != nil {
			return "", errors.New("the body isn't JSON")
		}
		found, ok := lookupJSONPath(document, extract.Path)
		if !ok {
			return "", fmt.Errorf("%s isn't in the response", extract.Path)
		}
		value = jsonText(found)
	case spec.ExtractRegex:
		pattern, err := regexp.Compile(extract.Path)
		if err != nil {
			return "", err
		}
		match := pattern.FindSubmatch(outcome.body)
		if match == nil {
			return "", fmt.Errorf("/%s/ doesn't match the body", extract.Path)
		}
		if len(match) > 1 {
			value = string(match[1])
		} else {
			value = string(match[0])
		}
	default:
		return "", fmt.Errorf("unknown source %q", extract.From)
	}
	if len(value) > spec.MaxValueBytes {
		return "", errors.New("the value is longer than 4 KB")
	}
	return value, nil
}

func newUUID() string {
	var id [16]byte
	_, _ = rand.Read(id[:])
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	text := hex.EncodeToString(id[:])
	return text[0:8] + "-" + text[8:12] + "-" + text[12:16] + "-" + text[16:20] + "-" + text[20:]
}
