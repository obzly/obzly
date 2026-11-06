package spec

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// The YAML bulk-provisioning format (docs/examples/synthetics-checks.yaml).
const (
	YAMLAPIVersion = "obzly.com/synthetics/v1"
	YAMLKind       = "MonitorList"
)

// MonitorList is a parsed YAML file, defaults already applied.
type MonitorList struct {
	// Prune deletes monitors that aren't in the file.
	Prune    bool
	Monitors []Monitor
	// Maintenance windows, when the file has a maintenance list
	// (HasMaintenance); prune then deletes the windows it leaves out.
	Maintenance    []Maintenance
	HasMaintenance bool
}

// ImportError is one problem in a file, with the line it starts on.
type ImportError struct {
	Line    int    `json:"line,omitempty"`
	Monitor string `json:"monitor,omitempty"`
	Window  string `json:"window,omitempty"`
	Message string `json:"message"`
}

func (e ImportError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("line %d: %s", e.Line, e.Message)
	}
	return e.Message
}

type yamlFile struct {
	APIVersion string       `yaml:"apiVersion"`
	Kind       string       `yaml:"kind"`
	Prune      bool         `yaml:"prune"`
	Defaults   yamlDefaults `yaml:"defaults"`
	Monitors   []yaml.Node  `yaml:"monitors"`
	// Maintenance is a pointer so an absent list differs from an empty one.
	Maintenance *[]yaml.Node `yaml:"maintenance"`
}

type yamlDefaults struct {
	Interval      string   `yaml:"interval"`
	Timeout       string   `yaml:"timeout"`
	Locations     []string `yaml:"locations"`
	Destination   string   `yaml:"destination"`
	DownLocations int      `yaml:"down_locations"`
	Enabled       *bool    `yaml:"enabled"`
}

type yamlMonitor struct {
	Name          string       `yaml:"name"`
	Type          string       `yaml:"type"`
	Enabled       *bool        `yaml:"enabled,omitempty"`
	Interval      string       `yaml:"interval,omitempty"`
	Timeout       string       `yaml:"timeout,omitempty"`
	Locations     []string     `yaml:"locations,omitempty"`
	Destination   string       `yaml:"destination,omitempty"`
	DownLocations *int         `yaml:"down_locations,omitempty"`
	Target        string       `yaml:"target,omitempty"`
	Request       *yamlRequest `yaml:"request,omitempty"`
	Assertions    []yaml.Node  `yaml:"assertions,omitempty"`
	TLS           *yamlTLS     `yaml:"tls,omitempty"`
	DNS           *yamlDNS     `yaml:"dns,omitempty"`
	ICMP          *yamlICMP    `yaml:"icmp,omitempty"`
	Folder        string       `yaml:"folder,omitempty"`
	Parents       []string     `yaml:"depends_on,omitempty"`
	Alerts        *yamlAlerts  `yaml:"alerts,omitempty"`
	// Multi-step checks.
	Variables map[string]string `yaml:"variables,omitempty"`
	Cookies   *bool             `yaml:"cookies,omitempty"`
	Steps     []yamlStep        `yaml:"steps,omitempty"`
}

type yamlStep struct {
	Name              string        `yaml:"name"`
	Request           *yamlRequest  `yaml:"request"`
	Assertions        []yaml.Node   `yaml:"assertions,omitempty"`
	Extract           []yamlExtract `yaml:"extract,omitempty"`
	ContinueOnFailure bool          `yaml:"continue_on_failure,omitempty"`
	Timeout           string        `yaml:"timeout,omitempty"`
}

type yamlExtract struct {
	Name   string `yaml:"name"`
	From   string `yaml:"from"`
	Path   string `yaml:"path,omitempty"`
	Secret bool   `yaml:"secret,omitempty"`
}

type yamlAlerts struct {
	Channels []string `yaml:"channels,omitempty"`
	Notify   string   `yaml:"notify,omitempty"`
	CertDays int      `yaml:"cert_days,omitempty"`
	Repeat   string   `yaml:"repeat,omitempty"`
}

type yamlRequest struct {
	Method          string            `yaml:"method,omitempty"`
	URL             string            `yaml:"url"`
	Headers         map[string]string `yaml:"headers,omitempty"`
	Body            string            `yaml:"body,omitempty"`
	FollowRedirects *bool             `yaml:"follow_redirects,omitempty"`
}

type yamlTLS struct {
	Verify         *bool  `yaml:"verify,omitempty"`
	WarnDays       int    `yaml:"warn_days,omitempty"`
	FailDays       int    `yaml:"fail_days,omitempty"`
	MinProtocol    string `yaml:"min_protocol,omitempty"`
	VerifyHostname *bool  `yaml:"verify_hostname,omitempty"`
}

type yamlDNS struct {
	RecordType  string   `yaml:"record_type,omitempty"`
	Resolver    string   `yaml:"resolver,omitempty"`
	ExpectAnyOf []string `yaml:"expect_any_of,omitempty"`
}

type yamlICMP struct {
	Count        int     `yaml:"count,omitempty"`
	MaxLossRatio float64 `yaml:"max_loss_ratio,omitempty"`
	RTTWarnMs    int     `yaml:"rtt_warn_ms,omitempty"`
}

// ParseMonitorList reads a YAML file into normalized monitors, or every
// problem found (so the preview can show them all at once).
func ParseMonitorList(data []byte) (*MonitorList, []ImportError) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var file yamlFile
	if err := decoder.Decode(&file); err != nil {
		return nil, []ImportError{{Message: "invalid YAML: " + err.Error()}}
	}
	if file.APIVersion != YAMLAPIVersion || file.Kind != YAMLKind {
		return nil, []ImportError{{Line: 1, Message: fmt.Sprintf(
			"expected apiVersion %s and kind %s", YAMLAPIVersion, YAMLKind)}}
	}

	defaults, err := file.Defaults.resolve()
	if err != nil {
		return nil, []ImportError{{Message: "defaults: " + err.Error()}}
	}
	list := &MonitorList{Prune: file.Prune}
	var problems []ImportError
	seen := map[string]int{}
	for i := range file.Monitors {
		node := &file.Monitors[i]
		var raw yamlMonitor
		if err := node.Decode(&raw); err != nil {
			problems = append(problems, ImportError{Line: node.Line, Message: err.Error()})
			continue
		}
		monitor, err := raw.toMonitor(defaults)
		if err == nil {
			err = monitor.Normalize()
		}
		if err != nil {
			problems = append(problems, ImportError{Line: node.Line, Monitor: raw.Name, Message: err.Error()})
			continue
		}
		key := strings.ToLower(monitor.Name)
		if first, ok := seen[key]; ok {
			problems = append(problems, ImportError{Line: node.Line, Monitor: monitor.Name,
				Message: fmt.Sprintf("duplicate name (first on line %d)", first)})
			continue
		}
		seen[key] = node.Line
		list.Monitors = append(list.Monitors, *monitor)
	}
	if file.Maintenance != nil {
		windows, windowProblems := parseMaintenance(*file.Maintenance)
		list.Maintenance, list.HasMaintenance = windows, true
		problems = append(problems, windowProblems...)
	}
	if len(problems) > 0 {
		return nil, problems
	}
	return list, nil
}

// MarshalMonitorList writes monitors in the import format. Placeholders
// are written as they are; secret values never appear in a Monitor.
func MarshalMonitorList(monitors []Monitor) ([]byte, error) {
	return MarshalMonitorFile(monitors, nil)
}

// MarshalMonitorFile writes monitors and, when there are any, maintenance
// windows.
func MarshalMonitorFile(monitors []Monitor, maintenance []Maintenance) ([]byte, error) {
	out := struct {
		APIVersion  string            `yaml:"apiVersion"`
		Kind        string            `yaml:"kind"`
		Monitors    []yamlMonitor     `yaml:"monitors"`
		Maintenance []yamlMaintenance `yaml:"maintenance,omitempty"`
	}{APIVersion: YAMLAPIVersion, Kind: YAMLKind}
	for _, window := range sortedMaintenance(maintenance) {
		out.Maintenance = append(out.Maintenance, fromMaintenance(window))
	}
	sorted := append([]Monitor(nil), monitors...)
	sort.SliceStable(
		sorted,
		func(i, j int) bool { return strings.ToLower(sorted[i].Name) < strings.ToLower(sorted[j].Name) },
	)
	for _, monitor := range sorted {
		raw, err := fromMonitor(monitor)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", monitor.Name, err)
		}
		out.Monitors = append(out.Monitors, raw)
	}
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(out); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

type resolvedDefaults struct {
	interval, timeout int
	locations         []string
	destination       string
	downLocations     int
	enabled           bool
}

func (d yamlDefaults) resolve() (resolvedDefaults, error) {
	resolved := resolvedDefaults{
		locations: d.Locations, destination: d.Destination, downLocations: d.DownLocations, enabled: true,
	}
	if d.Enabled != nil {
		resolved.enabled = *d.Enabled
	}
	var err error
	if resolved.interval, err = parseSeconds(d.Interval); err != nil {
		return resolved, fmt.Errorf("interval: %w", err)
	}
	if resolved.timeout, err = parseSeconds(d.Timeout); err != nil {
		return resolved, fmt.Errorf("timeout: %w", err)
	}
	return resolved, nil
}

func (raw yamlMonitor) toMonitor(defaults resolvedDefaults) (*Monitor, error) {
	monitor := &Monitor{
		Name: raw.Name, Enabled: defaults.enabled, IntervalSeconds: defaults.interval,
		Locations: append([]string(nil), defaults.locations...), Destination: defaults.destination,
		DownLocations: defaults.downLocations,
		Definition:    Definition{Type: raw.Type, Target: raw.Target, TimeoutSeconds: defaults.timeout},
	}
	if raw.Enabled != nil {
		monitor.Enabled = *raw.Enabled
	}
	if raw.Interval != "" {
		seconds, err := parseSeconds(raw.Interval)
		if err != nil {
			return nil, fmt.Errorf("interval: %w", err)
		}
		monitor.IntervalSeconds = seconds
	}
	if raw.Timeout != "" {
		seconds, err := parseSeconds(raw.Timeout)
		if err != nil {
			return nil, fmt.Errorf("timeout: %w", err)
		}
		monitor.Definition.TimeoutSeconds = seconds
	}
	if raw.Locations != nil {
		monitor.Locations = raw.Locations
	}
	if raw.Destination != "" {
		monitor.Destination = raw.Destination
	}
	if raw.DownLocations != nil {
		monitor.DownLocations = *raw.DownLocations
	}
	monitor.Folder, monitor.Parents = raw.Folder, raw.Parents
	if raw.Alerts != nil {
		monitor.Alerts = &MonitorAlerts{
			Channels: raw.Alerts.Channels, Notify: raw.Alerts.Notify, CertDays: raw.Alerts.CertDays,
		}
		if raw.Alerts.Repeat != "" {
			seconds, err := parseSeconds(raw.Alerts.Repeat)
			if err != nil {
				return nil, fmt.Errorf("alerts.repeat: %w", err)
			}
			monitor.Alerts.RepeatSeconds = seconds
		}
	}
	if raw.Request != nil {
		request := raw.Request.toRequest()
		monitor.Definition.Request = &request
	}
	if raw.TLS != nil {
		monitor.Definition.TLS = &TLSOptions{
			Verify: raw.TLS.Verify, WarnDays: raw.TLS.WarnDays, FailDays: raw.TLS.FailDays,
			MinProtocol: raw.TLS.MinProtocol, VerifyHostname: raw.TLS.VerifyHostname,
		}
	}
	if raw.DNS != nil {
		monitor.Definition.DNS = &DNSOptions{
			RecordType:  raw.DNS.RecordType,
			Resolver:    raw.DNS.Resolver,
			ExpectAnyOf: raw.DNS.ExpectAnyOf,
		}
	}
	if raw.ICMP != nil {
		monitor.Definition.ICMP = &ICMPOptions{
			Count:        raw.ICMP.Count,
			MaxLossRatio: raw.ICMP.MaxLossRatio,
			RTTWarnMs:    raw.ICMP.RTTWarnMs,
		}
	}
	assertions, err := parseAssertions(raw.Assertions)
	if err != nil {
		return nil, err
	}
	monitor.Definition.Assertions = assertions
	monitor.Definition.Variables, monitor.Definition.Cookies = raw.Variables, raw.Cookies
	for i, rawStep := range raw.Steps {
		step, err := rawStep.toStep()
		if err != nil {
			return nil, fmt.Errorf("step %d: %w", i+1, err)
		}
		monitor.Definition.Steps = append(monitor.Definition.Steps, step)
	}
	return monitor, nil
}

func (raw yamlStep) toStep() (Step, error) {
	step := Step{Name: raw.Name, ContinueOnFailure: raw.ContinueOnFailure}
	if raw.Request == nil {
		return step, errors.New("request is required")
	}
	step.Request = raw.Request.toRequest()
	seconds, err := parseSeconds(raw.Timeout)
	if err != nil {
		return step, fmt.Errorf("timeout: %w", err)
	}
	step.TimeoutSeconds = seconds
	if step.Assertions, err = parseAssertions(raw.Assertions); err != nil {
		return step, err
	}
	for _, extract := range raw.Extract {
		step.Extract = append(step.Extract, Extract(extract))
	}
	return step, nil
}

func (r yamlRequest) toRequest() HTTPRequest {
	return HTTPRequest(r)
}

func fromRequest(r HTTPRequest) *yamlRequest {
	raw := yamlRequest(r)
	return &raw
}

func parseAssertions(nodes []yaml.Node) ([]Assertion, error) {
	var assertions []Assertion
	for i := range nodes {
		assertion, err := parseAssertion(&nodes[i])
		if err != nil {
			return nil, fmt.Errorf("assertion %d (line %d): %w", i+1, nodes[i].Line, err)
		}
		assertions = append(assertions, assertion)
	}
	return assertions, nil
}

func assertionNodes(assertions []Assertion) ([]yaml.Node, error) {
	var nodes []yaml.Node
	for _, assertion := range assertions {
		node, err := assertionNode(assertion)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

// parseAssertion reads one list item: `status: 200`, `status: {in: [...]}`,
// `response_time_ms: {below: 800, severity: warn}`, `header: {name, contains}`,
// `json: {path, equals|exists}`, `body: {contains|matches}`.
func parseAssertion(node *yaml.Node) (Assertion, error) {
	if node.Kind != yaml.MappingNode || len(node.Content) != 2 {
		return Assertion{}, errors.New("each assertion is one `kind: value` entry")
	}
	kind, value := node.Content[0].Value, node.Content[1]
	assertion := Assertion{Kind: kind}
	if value.Kind == yaml.ScalarNode {
		if kind != AssertStatus {
			return Assertion{}, fmt.Errorf("%s needs options, e.g. {below: 800}", kind)
		}
		assertion.Op, assertion.Value = OpEquals, value.Value
		return assertion, nil
	}
	var options map[string]yaml.Node
	if err := value.Decode(&options); err != nil {
		return Assertion{}, err
	}
	for key, option := range options {
		switch key {
		case "severity":
			assertion.Severity = option.Value
		case "name":
			assertion.Name = option.Value
		case "path":
			assertion.Path = option.Value
		case OpIn:
			if err := option.Decode(&assertion.Values); err != nil {
				return Assertion{}, fmt.Errorf("in: %w", err)
			}
			assertion.Op = OpIn
		case OpBelow:
			number, err := strconv.ParseFloat(option.Value, 64)
			if err != nil {
				return Assertion{}, fmt.Errorf("below must be a number (got %q)", option.Value)
			}
			assertion.Op, assertion.Number = OpBelow, number
		case OpEquals, OpContains, OpMatches:
			assertion.Op, assertion.Value = key, option.Value
		case OpExists:
			if option.Value != "true" {
				return Assertion{}, errors.New("exists only supports true")
			}
			assertion.Op = OpExists
		default:
			return Assertion{}, fmt.Errorf("unknown option %q", key)
		}
	}
	if assertion.Op == "" {
		return Assertion{}, fmt.Errorf("%s needs an operator (equals, in, below, contains, matches or exists)", kind)
	}
	return assertion, nil
}

func fromMonitor(monitor Monitor) (yamlMonitor, error) {
	d := monitor.Definition
	raw := yamlMonitor{
		Name: monitor.Name, Type: d.Type, Target: d.Target,
		Interval: fmt.Sprintf("%ds", monitor.IntervalSeconds), Timeout: fmt.Sprintf("%ds", d.TimeoutSeconds),
		Locations: monitor.Locations, Destination: monitor.Destination,
		Folder: monitor.Folder, Parents: monitor.Parents,
	}
	if a := monitor.Alerts; a != nil {
		raw.Alerts = &yamlAlerts{Channels: a.Channels, Notify: a.Notify, CertDays: a.CertDays}
		if a.RepeatSeconds > 0 {
			raw.Alerts.Repeat = fmt.Sprintf("%ds", a.RepeatSeconds)
		}
	}
	if !monitor.Enabled {
		enabled := false
		raw.Enabled = &enabled
	}
	if monitor.DownLocations > 0 {
		down := monitor.DownLocations
		raw.DownLocations = &down
	}
	if d.Request != nil {
		raw.Request = fromRequest(*d.Request)
	}
	raw.Variables = d.Variables
	if !d.KeepsCookies() {
		raw.Cookies = d.Cookies
	}
	for _, step := range d.Steps {
		rawStep := yamlStep{
			Name: step.Name, Request: fromRequest(step.Request), ContinueOnFailure: step.ContinueOnFailure,
		}
		if step.TimeoutSeconds > 0 && step.TimeoutSeconds != DefaultStepTimeoutSeconds {
			rawStep.Timeout = fmt.Sprintf("%ds", step.TimeoutSeconds)
		}
		for _, extract := range step.Extract {
			rawStep.Extract = append(rawStep.Extract, yamlExtract(extract))
		}
		nodes, err := assertionNodes(step.Assertions)
		if err != nil {
			return raw, err
		}
		rawStep.Assertions = nodes
		raw.Steps = append(raw.Steps, rawStep)
	}
	if d.TLS != nil {
		raw.TLS = &yamlTLS{
			Verify: d.TLS.Verify, WarnDays: d.TLS.WarnDays, FailDays: d.TLS.FailDays,
			MinProtocol: d.TLS.MinProtocol, VerifyHostname: d.TLS.VerifyHostname,
		}
	}
	if d.DNS != nil {
		raw.DNS = &yamlDNS{RecordType: d.DNS.RecordType, Resolver: d.DNS.Resolver, ExpectAnyOf: d.DNS.ExpectAnyOf}
	}
	if d.ICMP != nil {
		raw.ICMP = &yamlICMP{Count: d.ICMP.Count, MaxLossRatio: d.ICMP.MaxLossRatio, RTTWarnMs: d.ICMP.RTTWarnMs}
	}
	nodes, err := assertionNodes(d.Assertions)
	if err != nil {
		return raw, err
	}
	raw.Assertions = nodes
	return raw, nil
}

func assertionNode(a Assertion) (yaml.Node, error) {
	var value any
	switch {
	case a.Kind == AssertStatus && a.Op == OpEquals && a.Severity != SeverityWarn:
		code, _ := strconv.Atoi(a.Value)
		value = code
	default:
		options := map[string]any{}
		switch a.Op {
		case OpIn:
			options[OpIn] = a.Values
		case OpBelow:
			options[OpBelow] = a.Number
		case OpExists:
			options[OpExists] = true
		default:
			options[a.Op] = a.Value
		}
		if a.Name != "" {
			options["name"] = a.Name
		}
		if a.Path != "" {
			options["path"] = a.Path
		}
		if a.Severity == SeverityWarn {
			options["severity"] = SeverityWarn
		}
		value = options
	}
	var node yaml.Node
	if err := node.Encode(map[string]any{a.Kind: value}); err != nil {
		return node, err
	}
	return node, nil
}

// parseSeconds reads "180s", "5m", "1h" or a bare number of seconds; ""
// is 0 (use the default).
func parseSeconds(text string) (int, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, nil
	}
	if seconds, err := strconv.Atoi(text); err == nil {
		return seconds, nil
	}
	duration, err := time.ParseDuration(text)
	if err != nil {
		return 0, fmt.Errorf("%q isn't a duration like 180s or 5m", text)
	}
	if duration%time.Second != 0 {
		return 0, fmt.Errorf("%q must be whole seconds", text)
	}
	return int(duration / time.Second), nil
}
