package checks

import (
	"net/url"
	"strings"

	"obzly/internal/synthetics/spec"
)

// SecretEnvPrefix: probe environment variables with this prefix fill
// ${env:NAME} placeholders without being listed in OBZLY_PROBE_ENV_ALLOW.
const SecretEnvPrefix = "OBZLY_SECRET_"

// minRedactLength: shorter values aren't secrets worth garbling results for.
const minRedactLength = 3

// Never read from a monitor, even when listed: the probe's own token and
// settings, and cloud credentials (a monitor could send them anywhere).
var deniedEnvPrefixes = []string{"OBZLY_PROBE_", "AWS_", "AZURE_", "GOOGLE_"}

// EnvPolicy decides which ${env:NAME} placeholders a probe fills from its environment: OBZLY_SECRET_*
// names and those the allow list names (an entry ending in * is a prefix).
type EnvPolicy struct {
	allow  []string
	lookup func(name string) (string, bool)
}

// NewEnvPolicy parses allow, OBZLY_PROBE_ENV_ALLOW's comma-separated list.
func NewEnvPolicy(allow string, lookup func(name string) (string, bool)) *EnvPolicy {
	policy := &EnvPolicy{lookup: lookup}
	for _, entry := range strings.Split(allow, ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			policy.allow = append(policy.allow, entry)
		}
	}
	return policy
}

// Allowed reports whether name may be read from the environment.
func (p *EnvPolicy) Allowed(name string) bool {
	upper := strings.ToUpper(name)
	for _, prefix := range deniedEnvPrefixes {
		if strings.HasPrefix(upper, prefix) {
			return false
		}
	}
	if strings.HasPrefix(name, SecretEnvPrefix) {
		return true
	}
	for _, entry := range p.allow {
		if prefix, ok := strings.CutSuffix(entry, "*"); ok {
			if strings.HasPrefix(name, prefix) {
				return true
			}
		} else if entry == name {
			return true
		}
	}
	return false
}

// Lookup is the value of an allowed name.
func (p *EnvPolicy) Lookup(name string) (string, bool) {
	if p == nil || p.lookup == nil || !p.Allowed(name) {
		return "", false
	}
	return p.lookup(name)
}

// redact replaces each secret value, also URL-escaped, wherever a result
// could echo it.
func redact(result *spec.RunResult, secrets []string) {
	var pairs []string
	for _, secret := range secrets {
		if len(secret) < minRedactLength {
			continue
		}
		pairs = append(pairs, secret, "[redacted]")
		for _, escaped := range []string{url.QueryEscape(secret), url.PathEscape(secret)} {
			if escaped != secret {
				pairs = append(pairs, escaped, "[redacted]")
			}
		}
	}
	if len(pairs) == 0 {
		return
	}
	replacer := strings.NewReplacer(pairs...)
	result.Target = replacer.Replace(result.Target)
	result.Error = replacer.Replace(result.Error)
	result.BodySnippet = replacer.Replace(result.BodySnippet)
	for name, value := range result.Headers {
		result.Headers[name] = replacer.Replace(value)
	}
	for i := range result.Assertions {
		result.Assertions[i].Actual = replacer.Replace(result.Assertions[i].Actual)
		result.Assertions[i].Description = replacer.Replace(result.Assertions[i].Description)
	}
	for i := range result.DNSAnswers {
		result.DNSAnswers[i] = replacer.Replace(result.DNSAnswers[i])
	}
	if result.TLS != nil {
		result.TLS.VerifyError = replacer.Replace(result.TLS.VerifyError)
	}
	for i := range result.Steps {
		step := &result.Steps[i]
		step.URL = replacer.Replace(step.URL)
		step.Error = replacer.Replace(step.Error)
		step.BodySnippet = replacer.Replace(step.BodySnippet)
		for j := range step.Assertions {
			step.Assertions[j].Actual = replacer.Replace(step.Assertions[j].Actual)
			step.Assertions[j].Description = replacer.Replace(step.Assertions[j].Description)
		}
		for j := range step.Extracted {
			step.Extracted[j].Value = replacer.Replace(step.Extracted[j].Value)
		}
	}
}
