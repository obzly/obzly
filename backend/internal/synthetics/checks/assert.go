package checks

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"obzly/internal/synthetics/spec"
)

var jsonPathStep = regexp.MustCompile(`\.([A-Za-z_][A-Za-z0-9_-]*)|\[(\d+)\]`)

func evaluate(
	assertions []spec.Assertion,
	status int,
	durationMs float64,
	header http.Header,
	body []byte,
) []spec.AssertionResult {
	var parsed any
	parsedOK := false
	parseJSON := func() (any, bool) {
		if !parsedOK {
			parsedOK = json.Unmarshal(body, &parsed) == nil
		}
		return parsed, parsedOK
	}
	results := make([]spec.AssertionResult, 0, len(assertions))
	for _, assertion := range assertions {
		result := spec.AssertionResult{Description: describe(assertion), Severity: assertion.Severity}
		if result.Severity == "" {
			result.Severity = spec.SeverityFail
		}
		switch assertion.Kind {
		case spec.AssertStatus:
			result.Actual = strconv.Itoa(status)
			if assertion.Op == spec.OpIn {
				result.Passed = slices.Contains(assertion.Values, status)
			} else {
				result.Passed = assertion.Value == strconv.Itoa(status)
			}
		case spec.AssertResponseTime:
			result.Actual = fmt.Sprintf("%.0f ms", durationMs)
			result.Passed = durationMs < assertion.Number
		case spec.AssertHeader:
			value := header.Get(assertion.Name)
			_, present := header[http.CanonicalHeaderKey(assertion.Name)]
			result.Actual = value
			result.Passed = compare(assertion.Op, value, present, assertion.Value)
		case spec.AssertBody:
			result.Actual = fmt.Sprintf("%d bytes", len(body))
			if assertion.Op == spec.OpMatches {
				pattern, err := regexp.Compile(assertion.Value)
				result.Passed = err == nil && pattern.Match(body)
			} else {
				result.Passed = strings.Contains(string(body), assertion.Value)
			}
		case spec.AssertJSON:
			document, ok := parseJSON()
			if !ok {
				result.Actual = "the body isn't JSON"
				break
			}
			value, found := lookupJSONPath(document, assertion.Path)
			text := jsonText(value)
			if found {
				result.Actual = text
			} else {
				result.Actual = "not found"
			}
			result.Passed = compare(assertion.Op, text, found, assertion.Value)
		}
		results = append(results, result)
	}
	return results
}

func compare(op, actual string, present bool, expected string) bool {
	switch op {
	case spec.OpExists:
		return present
	case spec.OpContains:
		return present && strings.Contains(actual, expected)
	default:
		return present && actual == expected
	}
}

func describe(a spec.Assertion) string {
	switch a.Kind {
	case spec.AssertStatus:
		if a.Op == spec.OpIn {
			codes := make([]string, len(a.Values))
			for i, code := range a.Values {
				codes[i] = strconv.Itoa(code)
			}
			return "Status is one of " + strings.Join(codes, ", ")
		}
		return "Status equals " + a.Value
	case spec.AssertResponseTime:
		return fmt.Sprintf("Response time below %.0f ms", a.Number)
	case spec.AssertHeader:
		if a.Op == spec.OpExists {
			return fmt.Sprintf("Header %s is present", a.Name)
		}
		return fmt.Sprintf("Header %s %s %q", a.Name, a.Op, a.Value)
	case spec.AssertBody:
		if a.Op == spec.OpMatches {
			return fmt.Sprintf("Body matches /%s/", a.Value)
		}
		return fmt.Sprintf("Body contains %q", a.Value)
	case spec.AssertJSON:
		if a.Op == spec.OpExists {
			return a.Path + " exists"
		}
		return fmt.Sprintf("%s %s %q", a.Path, a.Op, a.Value)
	}
	return a.Kind
}

func lookupJSONPath(document any, path string) (any, bool) {
	current := document
	for _, step := range jsonPathStep.FindAllStringSubmatch(strings.TrimPrefix(path, "$"), -1) {
		if step[1] != "" {
			object, ok := current.(map[string]any)
			if !ok {
				return nil, false
			}
			if current, ok = object[step[1]]; !ok {
				return nil, false
			}
			continue
		}
		index, _ := strconv.Atoi(step[2])
		list, ok := current.([]any)
		if !ok || index >= len(list) {
			return nil, false
		}
		current = list[index]
	}
	return current, true
}

// jsonText is how a JSON value compares against an assertion's text:
// strings bare, numbers and booleans as written, the rest as JSON.
func jsonText(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	default:
		encoded, _ := json.Marshal(typed)
		return string(encoded)
	}
}
