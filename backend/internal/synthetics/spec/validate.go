package spec

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Monitor is a check as people manage it: a Definition plus its schedule,
// where it runs and where its results go (a destination by name).
type Monitor struct {
	Name            string   `json:"name"`
	Enabled         bool     `json:"enabled"`
	IntervalSeconds int      `json:"intervalSeconds"`
	Locations       []string `json:"locations"`
	Destination     string   `json:"destination,omitempty"`
	// DownLocations: red once at least this many locations fail; 0 means
	// half of them, at least one.
	DownLocations int        `json:"downLocations"`
	Definition    Definition `json:"definition"`
	// Folder is a path such as "Payments/EU"; "" for none.
	Folder string `json:"folder,omitempty"`
	// Parents are the monitors this one depends on, by name (YAML); the
	// control plane stores their ids.
	Parents []string       `json:"parents,omitempty"`
	Alerts  *MonitorAlerts `json:"alerts,omitempty"`
}

// MonitorAlerts is when a monitor notifies and where (channels by name in
// YAML, by id over the API).
type MonitorAlerts struct {
	Channels []string `json:"channels,omitempty"`
	// Notify is NotifyDown or NotifyDegraded (Down or Degraded).
	Notify string `json:"notify,omitempty"`
	// CertDays alerts when the certificate expires within this many days; 0 off.
	CertDays      int `json:"certDays,omitempty"`
	RepeatSeconds int `json:"repeatSeconds,omitempty"`
}

const (
	NotifyDown     = "down"
	NotifyDegraded = "degraded"
	maxFolderDepth = 5
)

var (
	httpMethods = map[string]bool{
		"GET":     true,
		"HEAD":    true,
		"POST":    true,
		"PUT":     true,
		"PATCH":   true,
		"DELETE":  true,
		"OPTIONS": true,
	}
	dnsRecords   = map[string]bool{"A": true, "AAAA": true, "CNAME": true, "MX": true, "TXT": true, "NS": true}
	tlsVersions  = map[string]bool{"TLS1.0": true, "TLS1.1": true, "TLS1.2": true, "TLS1.3": true}
	jsonPathExpr = regexp.MustCompile(`^\$(\.[A-Za-z_][A-Za-z0-9_-]*|\[\d+\])*$`)
	variableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	builtins     = map[string]bool{
		BuiltinUUID: true, BuiltinTimestamp: true, BuiltinISOTimestamp: true, BuiltinRandomInt: true,
	}
)

// Normalize fills defaults in m and reports the first problem found.
func (m *Monitor) Normalize() error {
	m.Name = strings.TrimSpace(m.Name)
	if m.Name == "" {
		return errors.New("name is required")
	}
	if len(m.Name) > 200 {
		return errors.New("name is longer than 200 characters")
	}
	if m.IntervalSeconds == 0 {
		m.IntervalSeconds = DefaultIntervalSeconds
	}
	if m.IntervalSeconds < MinIntervalSeconds {
		return fmt.Errorf("interval must be at least %ds", MinIntervalSeconds)
	}
	if m.DownLocations < 0 {
		return errors.New("down_locations can't be negative")
	}
	var locations []string
	for _, location := range m.Locations {
		location = strings.TrimSpace(location)
		if location != "" && location != "all" {
			locations = append(locations, location)
		}
	}
	// nil, not empty: no locations listed means every location.
	m.Locations = locations
	if err := m.Definition.Normalize(); err != nil {
		return err
	}
	if m.Definition.TimeoutSeconds > m.IntervalSeconds {
		return errors.New("timeout must not be longer than the interval")
	}
	folder, err := normalizeFolder(m.Folder)
	if err != nil {
		return err
	}
	m.Folder = folder
	var parents []string
	for _, parent := range m.Parents {
		if parent = strings.TrimSpace(parent); parent != "" && !slices.Contains(parents, parent) {
			if strings.EqualFold(parent, m.Name) {
				return errors.New("a monitor can't depend on itself")
			}
			parents = append(parents, parent)
		}
	}
	m.Parents = parents
	if m.Alerts != nil {
		return m.Alerts.normalize()
	}
	return nil
}

func (a *MonitorAlerts) normalize() error {
	a.Notify = strings.ToLower(strings.TrimSpace(a.Notify))
	if a.Notify == "" {
		a.Notify = NotifyDown
	}
	if a.Notify != NotifyDown && a.Notify != NotifyDegraded {
		return fmt.Errorf("alerts.notify must be %q or %q", NotifyDown, NotifyDegraded)
	}
	if a.CertDays < 0 || a.CertDays > 365 {
		return errors.New("alerts.cert_days must be between 0 and 365")
	}
	if a.RepeatSeconds < 0 {
		return errors.New("alerts.repeat can't be negative")
	}
	var channels []string
	for _, channel := range a.Channels {
		if channel = strings.TrimSpace(channel); channel != "" && !slices.Contains(channels, channel) {
			channels = append(channels, channel)
		}
	}
	a.Channels = channels
	return nil
}

// normalizeFolder trims each part of a folder path ("Payments / EU" is
// "Payments/EU").
func normalizeFolder(folder string) (string, error) {
	var parts []string
	for _, part := range strings.Split(folder, "/") {
		if part = strings.TrimSpace(part); part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) > maxFolderDepth {
		return "", fmt.Errorf("a folder can be at most %d levels deep", maxFolderDepth)
	}
	joined := strings.Join(parts, "/")
	if len(joined) > 200 {
		return "", errors.New("folder is longer than 200 characters")
	}
	return joined, nil
}

// Normalize fills defaults in d and reports the first problem found.
func (d *Definition) Normalize() error {
	d.Type = strings.ToLower(strings.TrimSpace(d.Type))
	defaultTimeout, maxTimeout := DefaultTimeoutSeconds, MaxTimeoutSeconds
	if d.Type == TypeMulti {
		defaultTimeout, maxTimeout = DefaultMultiTimeoutSeconds, MaxMultiTimeoutSeconds
	}
	if d.TimeoutSeconds == 0 {
		d.TimeoutSeconds = defaultTimeout
	}
	if d.TimeoutSeconds < 1 || d.TimeoutSeconds > maxTimeout {
		return fmt.Errorf("timeout must be between 1s and %ds", maxTimeout)
	}
	if d.Type != TypeMulti && (len(d.Steps) > 0 || len(d.Variables) > 0 || d.Cookies != nil) {
		return errors.New("steps, variables and cookies apply to multi-step checks only")
	}
	switch d.Type {
	case TypeHTTP:
		if err := d.normalizeHTTP(); err != nil {
			return err
		}
	case TypeTLS:
		if err := requireHostPort(d.Target, "target"); err != nil {
			return err
		}
	case TypeTCP:
		if err := requireHostPort(d.Target, "target"); err != nil {
			return err
		}
	case TypeDNS:
		if err := d.normalizeDNS(); err != nil {
			return err
		}
	case TypeICMP:
		if err := d.normalizeICMP(); err != nil {
			return err
		}
	case TypeMulti:
		if err := d.normalizeMulti(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("type must be one of http, multi, tls, tcp, dns, icmp (got %q)", d.Type)
	}
	if d.TLS != nil {
		d.TLS.MinProtocol = strings.ToUpper(strings.ReplaceAll(d.TLS.MinProtocol, " ", ""))
		if d.TLS.MinProtocol != "" && !tlsVersions[d.TLS.MinProtocol] {
			return fmt.Errorf("tls.min_protocol must be TLS1.0 to TLS1.3 (got %q)", d.TLS.MinProtocol)
		}
		if d.TLS.WarnDays < 0 || d.TLS.FailDays < 0 {
			return errors.New("tls warn_days and fail_days can't be negative")
		}
	}
	for i := range d.Assertions {
		if err := d.Assertions[i].normalize(d.Type); err != nil {
			return fmt.Errorf("assertion %d: %w", i+1, err)
		}
	}
	return nil
}

func (d *Definition) normalizeHTTP() error {
	if d.Request == nil {
		return errors.New("request.url is required")
	}
	return d.Request.normalize()
}

func (r *HTTPRequest) normalize() error {
	r.URL = strings.TrimSpace(r.URL)
	if r.URL == "" {
		return errors.New("request.url is required")
	}
	// A placeholder or {{variable}} may stand for the scheme or host; it's
	// checked on the probe once filled in.
	if !placeholderPattern.MatchString(r.URL) && !TemplatePattern.MatchString(r.URL) {
		parsed, err := url.Parse(r.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("request.url must be an http(s) URL (got %q)", r.URL)
		}
	}
	r.Method = strings.ToUpper(strings.TrimSpace(r.Method))
	if r.Method == "" {
		r.Method = "GET"
	}
	if !httpMethods[r.Method] {
		return fmt.Errorf("request.method %q isn't supported", r.Method)
	}
	return nil
}

// normalizeMulti checks the steps, and that every {{name}} a step uses is
// a variable, a built-in, or extracted by an earlier step.
func (d *Definition) normalizeMulti() error {
	if d.Request != nil || len(d.Assertions) > 0 {
		return errors.New("a multi-step check has its requests and assertions in steps")
	}
	d.Target, d.DNS, d.ICMP = "", nil, nil
	if len(d.Steps) == 0 {
		return errors.New("add at least one step")
	}
	if len(d.Steps) > MaxSteps {
		return fmt.Errorf("a multi-step check has at most %d steps", MaxSteps)
	}
	if len(d.Variables) > MaxVariables {
		return fmt.Errorf("at most %d variables", MaxVariables)
	}
	defined := map[string]bool{}
	for name, value := range d.Variables {
		if !variableName.MatchString(name) {
			return fmt.Errorf("variable %q: use letters, digits and _ (not starting with a digit)", name)
		}
		if len(value) > MaxValueBytes {
			return fmt.Errorf("variable %q is longer than 4 KB", name)
		}
		defined[name] = true
	}
	for i := range d.Steps {
		if err := d.Steps[i].normalize(d.TimeoutSeconds, defined); err != nil {
			return fmt.Errorf("step %d: %w", i+1, err)
		}
	}
	return nil
}

func (s *Step) normalize(totalTimeout int, defined map[string]bool) error {
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" {
		return errors.New("name is required")
	}
	if len(s.Name) > 100 {
		return errors.New("name is longer than 100 characters")
	}
	if err := s.Request.normalize(); err != nil {
		return err
	}
	if s.TimeoutSeconds == 0 {
		s.TimeoutSeconds = min(DefaultStepTimeoutSeconds, totalTimeout)
	}
	if s.TimeoutSeconds < 1 || s.TimeoutSeconds > totalTimeout {
		return fmt.Errorf("timeout must be between 1s and the check's total timeout (%ds)", totalTimeout)
	}
	for i := range s.Assertions {
		if err := s.Assertions[i].normalize(TypeHTTP); err != nil {
			return fmt.Errorf("assertion %d: %w", i+1, err)
		}
	}
	for _, name := range s.UsedVariables() {
		if !defined[name] && !builtins[name] {
			return fmt.Errorf("uses {{%s}}, which no earlier step or variable defines", name)
		}
	}
	if len(s.Extract) > MaxExtractsPerStep {
		return fmt.Errorf("at most %d extracted values", MaxExtractsPerStep)
	}
	for i := range s.Extract {
		if err := s.Extract[i].normalize(); err != nil {
			return fmt.Errorf("extract %d: %w", i+1, err)
		}
		defined[s.Extract[i].Name] = true
	}
	return nil
}

// UsedVariables lists the {{name}}s s uses, each once, in order.
func (s Step) UsedVariables() []string {
	texts := s.Request.texts()
	for _, assertion := range s.Assertions {
		texts = append(texts, assertion.Value)
	}
	var names []string
	seen := map[string]bool{}
	for _, text := range texts {
		for _, match := range TemplatePattern.FindAllStringSubmatch(text, -1) {
			if !seen[match[1]] {
				seen[match[1]] = true
				names = append(names, match[1])
			}
		}
	}
	return names
}

func (e *Extract) normalize() error {
	e.Name = strings.TrimSpace(e.Name)
	if !variableName.MatchString(e.Name) {
		return fmt.Errorf("name %q: use letters, digits and _ (not starting with a digit)", e.Name)
	}
	e.From = strings.ToLower(strings.TrimSpace(e.From))
	e.Path = strings.TrimSpace(e.Path)
	switch e.From {
	case ExtractJSON:
		if !jsonPathExpr.MatchString(e.Path) {
			return fmt.Errorf("json path %q isn't supported (use $.a.b or $.items[0].id)", e.Path)
		}
	case ExtractHeader:
		if e.Path == "" {
			return errors.New("header needs the header's name as path")
		}
	case ExtractRegex:
		if e.Path == "" {
			return errors.New("regex needs a regular expression as path")
		}
		if _, err := regexp.Compile(e.Path); err != nil {
			return fmt.Errorf("invalid regular expression: %w", err)
		}
	case ExtractStatus, ExtractBody:
		e.Path = ""
	default:
		return fmt.Errorf("from must be json, header, status, regex or body (got %q)", e.From)
	}
	return nil
}

func (d *Definition) normalizeDNS() error {
	d.Target = strings.TrimSuffix(strings.TrimSpace(d.Target), ".")
	if d.Target == "" {
		return errors.New("target (the name to resolve) is required")
	}
	if d.DNS == nil {
		d.DNS = &DNSOptions{}
	}
	d.DNS.RecordType = strings.ToUpper(strings.TrimSpace(d.DNS.RecordType))
	if d.DNS.RecordType == "" {
		d.DNS.RecordType = "A"
	}
	if !dnsRecords[d.DNS.RecordType] {
		return fmt.Errorf("dns.record_type must be A, AAAA, CNAME, MX, TXT or NS (got %q)", d.DNS.RecordType)
	}
	if d.DNS.Resolver != "" {
		if _, _, err := net.SplitHostPort(d.DNS.Resolver); err != nil {
			d.DNS.Resolver = net.JoinHostPort(d.DNS.Resolver, "53")
		}
	}
	return nil
}

func (d *Definition) normalizeICMP() error {
	d.Target = strings.TrimSpace(d.Target)
	if d.Target == "" {
		return errors.New("target (a host or IP) is required")
	}
	if d.ICMP == nil {
		d.ICMP = &ICMPOptions{}
	}
	if d.ICMP.Count == 0 {
		d.ICMP.Count = DefaultICMPCount
	}
	if d.ICMP.Count < 1 || d.ICMP.Count > 20 {
		return errors.New("icmp.count must be between 1 and 20")
	}
	if d.ICMP.MaxLossRatio < 0 || d.ICMP.MaxLossRatio > 1 {
		return errors.New("icmp.max_loss_ratio must be between 0 and 1")
	}
	return nil
}

func (a *Assertion) normalize(checkType string) error {
	a.Severity = normalizeSeverity(a.Severity)
	switch a.Kind {
	case AssertStatus:
		switch a.Op {
		case OpEquals:
			if _, err := strconv.Atoi(a.Value); err != nil {
				return fmt.Errorf("status must be a number (got %q)", a.Value)
			}
		case OpIn:
			if len(a.Values) == 0 {
				return errors.New("status in needs at least one code")
			}
		default:
			return fmt.Errorf("status supports equals or in (got %q)", a.Op)
		}
	case AssertResponseTime:
		if a.Op != OpBelow || a.Number <= 0 {
			return errors.New("response_time_ms needs below: <milliseconds>")
		}
	case AssertHeader:
		if strings.TrimSpace(a.Name) == "" {
			return errors.New("header needs a name")
		}
		if a.Op != OpEquals && a.Op != OpContains && a.Op != OpExists {
			return fmt.Errorf("header supports equals, contains or exists (got %q)", a.Op)
		}
	case AssertJSON:
		if !jsonPathExpr.MatchString(a.Path) {
			return fmt.Errorf("json path %q isn't supported (use $.a.b or $.items[0].id)", a.Path)
		}
		if a.Op != OpEquals && a.Op != OpContains && a.Op != OpExists {
			return fmt.Errorf("json supports equals, contains or exists (got %q)", a.Op)
		}
	case AssertBody:
		switch a.Op {
		case OpContains:
		case OpMatches:
			if _, err := regexp.Compile(a.Value); err != nil {
				return fmt.Errorf("body matches: invalid regular expression: %w", err)
			}
		default:
			return fmt.Errorf("body supports contains or matches (got %q)", a.Op)
		}
	default:
		return fmt.Errorf("unknown assertion %q", a.Kind)
	}
	if checkType != TypeHTTP && a.Kind != AssertResponseTime {
		return fmt.Errorf("%s assertions apply to http checks only", a.Kind)
	}
	return nil
}

func requireHostPort(target, field string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(target))
	if err != nil || host == "" || port == "" {
		return fmt.Errorf("%s must be host:port (got %q)", field, target)
	}
	return nil
}
