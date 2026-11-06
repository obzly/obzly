// Package spec is what the control plane and obzly-probe agree on. Standard
// library only (plus YAML), so the probe binary stays small.
package spec

import (
	"regexp"
	"sort"
	"strings"
)

// Check types.
const (
	TypeHTTP = "http"
	TypeTLS  = "tls"
	TypeTCP  = "tcp"
	TypeDNS  = "dns"
	TypeICMP = "icmp"
	// TypeMulti runs HTTP steps in order, carrying values between them.
	TypeMulti = "multi"
)

// Run outcomes; also the monitor colours (plus "off" for disabled).
const (
	StatusUp   = "up"
	StatusWarn = "warn"
	StatusDown = "down"
	// StatusSkipped: a step that didn't run after an earlier one failed.
	StatusSkipped = "skipped"
)

// Assertion kinds and operators.
const (
	AssertStatus       = "status"
	AssertResponseTime = "response_time_ms"
	AssertHeader       = "header"
	AssertJSON         = "json"
	AssertBody         = "body"

	OpEquals   = "equals"
	OpIn       = "in"
	OpBelow    = "below"
	OpContains = "contains"
	OpMatches  = "matches"
	OpExists   = "exists"

	SeverityFail = "fail"
	SeverityWarn = "warn"
)

const (
	DefaultIntervalSeconds = 180
	MinIntervalSeconds     = 30
	DefaultTimeoutSeconds  = 10
	MaxTimeoutSeconds      = 60
	DefaultICMPCount       = 5
	DefaultTLSWarnDays     = 21

	DefaultMultiTimeoutSeconds = 60
	MaxMultiTimeoutSeconds     = 120
	DefaultStepTimeoutSeconds  = 10
	MaxSteps                   = 10
	MaxExtractsPerStep         = 10
	MaxVariables               = 50
	// MaxValueBytes caps a variable's or an extracted value.
	MaxValueBytes = 4 << 10
)

// Extract sources.
const (
	ExtractJSON   = "json"
	ExtractHeader = "header"
	ExtractStatus = "status"
	ExtractRegex  = "regex"
	ExtractBody   = "body"
)

// Built-in variables; each use gets a new value.
const (
	BuiltinUUID         = "$uuid"
	BuiltinTimestamp    = "$timestamp"
	BuiltinISOTimestamp = "$isoTimestamp"
	BuiltinRandomInt    = "$randomInt"
)

// Definition is everything a probe needs to run one check, apart from where results go. Strings may
// hold ${env:NAME} placeholders: the control plane fills stored secrets, the probe the rest from its
// environment (OBZLY_SECRET_* and OBZLY_PROBE_ENV_ALLOW names).
type Definition struct {
	Type           string       `json:"type"`
	Target         string       `json:"target,omitempty"`
	Request        *HTTPRequest `json:"request,omitempty"`
	Assertions     []Assertion  `json:"assertions,omitempty"`
	TLS            *TLSOptions  `json:"tls,omitempty"`
	DNS            *DNSOptions  `json:"dns,omitempty"`
	ICMP           *ICMPOptions `json:"icmp,omitempty"`
	TimeoutSeconds int          `json:"timeoutSeconds"`
	// Multi-step only: the steps, starting values for {{name}}, and whether
	// cookies carry from step to step (default on).
	Steps     []Step            `json:"steps,omitempty"`
	Variables map[string]string `json:"variables,omitempty"`
	Cookies   *bool             `json:"cookies,omitempty"`
}

type HTTPRequest struct {
	Method          string            `json:"method"`
	URL             string            `json:"url"`
	Headers         map[string]string `json:"headers,omitempty"`
	Body            string            `json:"body,omitempty"`
	FollowRedirects *bool             `json:"followRedirects,omitempty"`
}

// Step is one request of a multi-step check. Its URL, header values, body
// and assertion values may use {{name}}.
type Step struct {
	Name       string      `json:"name"`
	Request    HTTPRequest `json:"request"`
	Assertions []Assertion `json:"assertions,omitempty"`
	Extract    []Extract   `json:"extract,omitempty"`
	// ContinueOnFailure runs the step even after an earlier one failed
	// (clean-up).
	ContinueOnFailure bool `json:"continueOnFailure,omitempty"`
	TimeoutSeconds    int  `json:"timeoutSeconds,omitempty"`
}

// Extract keeps a value from a step's response as {{Name}} for later steps. Path is the JSON
// path, header name or regular expression (its first group), by source.
type Extract struct {
	Name   string `json:"name"`
	From   string `json:"from"`
	Path   string `json:"path,omitempty"`
	Secret bool   `json:"secret,omitempty"`
}

// Assertion is one condition on a run. Severity warn makes a failure amber
// instead of red.
type Assertion struct {
	Kind     string  `json:"kind"`
	Op       string  `json:"op"`
	Name     string  `json:"name,omitempty"`
	Path     string  `json:"path,omitempty"`
	Value    string  `json:"value,omitempty"`
	Values   []int   `json:"values,omitempty"`
	Number   float64 `json:"number,omitempty"`
	Severity string  `json:"severity,omitempty"`
}

type TLSOptions struct {
	// Verify false accepts self-signed, expired or untrusted certificates;
	// the run is still reported, labelled tls_verified=false.
	Verify         *bool  `json:"verify,omitempty"`
	WarnDays       int    `json:"warnDays,omitempty"`
	FailDays       int    `json:"failDays,omitempty"`
	MinProtocol    string `json:"minProtocol,omitempty"`
	VerifyHostname *bool  `json:"verifyHostname,omitempty"`
}

type DNSOptions struct {
	RecordType  string   `json:"recordType,omitempty"`
	Resolver    string   `json:"resolver,omitempty"`
	ExpectAnyOf []string `json:"expectAnyOf,omitempty"`
}

type ICMPOptions struct {
	Count        int     `json:"count,omitempty"`
	MaxLossRatio float64 `json:"maxLossRatio,omitempty"`
	RTTWarnMs    int     `json:"rttWarnMs,omitempty"`
}

var (
	placeholderPattern = regexp.MustCompile(`\$\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)
	// TemplatePattern finds {{name}} and {{$builtin}} uses.
	TemplatePattern = regexp.MustCompile(`\{\{\s*(\$?[A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)
)

// Placeholders lists the ${env:NAME} names d uses, each once, in order.
func (d Definition) Placeholders() []string {
	var names []string
	seen := map[string]bool{}
	for _, text := range d.texts() {
		for _, match := range placeholderPattern.FindAllStringSubmatch(text, -1) {
			if !seen[match[1]] {
				seen[match[1]] = true
				names = append(names, match[1])
			}
		}
	}
	return names
}

// Resolve returns a copy of d with each placeholder lookup knows replaced;
// unknown ones stay as they are.
func (d Definition) Resolve(lookup func(name string) (string, bool)) Definition {
	replace := func(text string) string {
		return placeholderPattern.ReplaceAllStringFunc(text, func(match string) string {
			name := placeholderPattern.FindStringSubmatch(match)[1]
			if value, ok := lookup(name); ok {
				return value
			}
			return match
		})
	}
	resolved := d
	resolved.Target = replace(d.Target)
	if d.Request != nil {
		request := d.Request.MapTexts(replace)
		resolved.Request = &request
	}
	if d.Steps != nil {
		resolved.Steps = make([]Step, len(d.Steps))
		for i, step := range d.Steps {
			step.Request = step.Request.MapTexts(replace)
			resolved.Steps[i] = step
		}
	}
	if d.Variables != nil {
		resolved.Variables = make(map[string]string, len(d.Variables))
		for name, value := range d.Variables {
			resolved.Variables[name] = replace(value)
		}
	}
	return resolved
}

// SecretVariables names the variables whose value holds a secret
// (${env:NAME}).
func (d Definition) SecretVariables() map[string]bool {
	names := map[string]bool{}
	for name, value := range d.Variables {
		if placeholderPattern.MatchString(value) {
			names[name] = true
		}
	}
	return names
}

// VerifiesTLS reports whether certificate verification is on (the default).
func (d Definition) VerifiesTLS() bool {
	return d.TLS == nil || d.TLS.Verify == nil || *d.TLS.Verify
}

// KeepsCookies: multi-step runs carry cookies between steps unless turned
// off.
func (d Definition) KeepsCookies() bool {
	return d.Cookies == nil || *d.Cookies
}

// FollowsRedirects defaults to true.
func (r HTTPRequest) FollowsRedirects() bool {
	return r.FollowRedirects == nil || *r.FollowRedirects
}

// TargetLabel is what results are labelled with: the URL for HTTP, the
// first step's URL for multi-step, else the target.
func (d Definition) TargetLabel() string {
	if d.Request != nil {
		return d.Request.URL
	}
	if len(d.Steps) > 0 {
		return d.Steps[0].Request.URL
	}
	return d.Target
}

func (d Definition) texts() []string {
	texts := []string{d.Target}
	if d.Request != nil {
		texts = append(texts, d.Request.texts()...)
	}
	for _, step := range d.Steps {
		texts = append(texts, step.Request.texts()...)
	}
	for _, name := range sortedKeys(d.Variables) {
		texts = append(texts, d.Variables[name])
	}
	return texts
}

func (r HTTPRequest) texts() []string {
	texts := []string{r.URL, r.Body}
	for _, name := range sortedKeys(r.Headers) {
		texts = append(texts, r.Headers[name])
	}
	return texts
}

// MapTexts is a copy of r with replace applied to its URL, body and header
// values.
func (r HTTPRequest) MapTexts(replace func(string) string) HTTPRequest {
	mapped := r
	mapped.URL = replace(r.URL)
	mapped.Body = replace(r.Body)
	if r.Headers != nil {
		mapped.Headers = make(map[string]string, len(r.Headers))
		for name, value := range r.Headers {
			mapped.Headers[name] = replace(value)
		}
	}
	return mapped
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func normalizeSeverity(severity string) string {
	if strings.EqualFold(severity, SeverityWarn) {
		return SeverityWarn
	}
	return SeverityFail
}
