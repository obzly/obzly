package spec

import "time"

// TokenPrefix marks a probe token (obz_prb_...).
const TokenPrefix = "obz_prb_"

// Probe group headers: an instance names itself and says how many monitors
// it runs; the control plane answers with the group's live members.
const (
	HeaderInstance = "X-Obzly-Probe-Instance"
	HeaderChecks   = "X-Obzly-Probe-Checks"
	HeaderMembers  = "X-Obzly-Probe-Members"
)

// ProbeConfig is what GET /api/v1/synthetics/probe/config returns: this
// probe's checks with secrets filled in, and where to send results.
type ProbeConfig struct {
	Probe               ProbeInfo     `json:"probe"`
	PollIntervalSeconds int           `json:"pollIntervalSeconds"`
	Destinations        []Destination `json:"destinations"`
	Checks              []ProbeCheck  `json:"checks"`
	// Cloud: the cloud connections this probe group scans (older probes
	// ignore it).
	Cloud []CloudConnection `json:"cloud,omitempty"`
}

type ProbeInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Location string `json:"location"`
}

// Destination is a Site project's OTLP endpoint and ingest key.
type Destination struct {
	ID      string `json:"id"`
	OTLPURL string `json:"otlpUrl"`
	APIKey  string `json:"apiKey"`
}

type ProbeCheck struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	IntervalSeconds int        `json:"intervalSeconds"`
	DestinationID   string     `json:"destinationId,omitempty"`
	Definition      Definition `json:"definition"`
	// Target labels results: the target before the control plane filled in
	// secrets, so no value reaches a label (empty from older control planes).
	Target string `json:"target,omitempty"`
	// Redact: the secret values the control plane filled in, which the
	// probe keeps out of what it reports.
	Redact []string `json:"redact,omitempty"`
}

// Task is an interactive Test from the editor: run once, report back to
// the control plane, send nothing to a Site.
type Task struct {
	ID    string     `json:"id"`
	Check ProbeCheck `json:"check"`
	// Cloud, when set, is a Test access of a cloud connection instead.
	Cloud *CloudConnection `json:"cloud,omitempty"`
}

// RunResult is one run of one check from one place.
type RunResult struct {
	CheckID   string    `json:"checkId,omitempty"`
	Check     string    `json:"check"`
	Type      string    `json:"type"`
	Target    string    `json:"target"`
	Location  string    `json:"location"`
	Probe     string    `json:"probe"`
	StartedAt time.Time `json:"startedAt"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	// Unsupported: the probe can't run this type here (ICMP without
	// CAP_NET_RAW or ping sockets).
	Unsupported bool `json:"unsupported,omitempty"`

	DurationMs float64            `json:"durationMs"`
	PhasesMs   map[string]float64 `json:"phasesMs,omitempty"`

	HTTPStatus    int               `json:"httpStatus,omitempty"`
	ResponseBytes int64             `json:"responseBytes,omitempty"`
	Redirects     int               `json:"redirects,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
	BodySnippet   string            `json:"bodySnippet,omitempty"`
	ResolvedIPs   []string          `json:"resolvedIps,omitempty"`

	TLS        *TLSInfo  `json:"tls,omitempty"`
	DNSAnswers []string  `json:"dnsAnswers,omitempty"`
	ICMP       *ICMPInfo `json:"icmp,omitempty"`

	Assertions []AssertionResult `json:"assertions,omitempty"`
	// FromControlPlane: an editor Test the control plane ran itself
	// because no probe could.
	FromControlPlane bool `json:"fromControlPlane,omitempty"`
	// Steps: a multi-step run, every step in order (skipped ones too).
	Steps []StepResult `json:"steps,omitempty"`
}

// StepResult is one step of a multi-step run.
type StepResult struct {
	// Index counts from 1.
	Index int    `json:"index"`
	Name  string `json:"name"`
	// Status is up, warn, down or skipped.
	Status string `json:"status"`
	Method string `json:"method"`
	// URL is the request's, filled in, with secret values redacted.
	URL           string             `json:"url"`
	HTTPStatus    int                `json:"httpStatus,omitempty"`
	DurationMs    float64            `json:"durationMs"`
	PhasesMs      map[string]float64 `json:"phasesMs,omitempty"`
	ResponseBytes int64              `json:"responseBytes,omitempty"`
	Redirects     int                `json:"redirects,omitempty"`
	Error         string             `json:"error,omitempty"`
	// BodySnippet is kept for steps that didn't pass only.
	BodySnippet string            `json:"bodySnippet,omitempty"`
	Assertions  []AssertionResult `json:"assertions,omitempty"`
	Extracted   []ExtractedValue  `json:"extracted,omitempty"`
	// AfterFailure: ran after an earlier step failed (continue_on_failure).
	AfterFailure bool `json:"afterFailure,omitempty"`
}

// ExtractedValue is a value a step kept for later steps; a secret one is
// listed by name only.
type ExtractedValue struct {
	Name   string `json:"name"`
	Value  string `json:"value,omitempty"`
	Secret bool   `json:"secret,omitempty"`
}

type TLSInfo struct {
	Subject     string    `json:"subject"`
	Issuer      string    `json:"issuer"`
	SANs        []string  `json:"sans,omitempty"`
	NotAfter    time.Time `json:"notAfter"`
	DaysLeft    float64   `json:"daysLeft"`
	Protocol    string    `json:"protocol"`
	Cipher      string    `json:"cipher"`
	Verified    bool      `json:"verified"`
	VerifyError string    `json:"verifyError,omitempty"`
	SelfSigned  bool      `json:"selfSigned,omitempty"`
}

type ICMPInfo struct {
	Sent      int     `json:"sent"`
	Received  int     `json:"received"`
	LossRatio float64 `json:"lossRatio"`
	RTTAvgMs  float64 `json:"rttAvgMs"`
	JitterMs  float64 `json:"jitterMs"`
}

type AssertionResult struct {
	Description string `json:"description"`
	Severity    string `json:"severity"`
	Passed      bool   `json:"passed"`
	Actual      string `json:"actual,omitempty"`
}

// Cloud connection providers, access methods and uses (docs/INFRASTRUCTURE.md
// "Cloud connections").
const (
	ProviderAWS   = "aws"
	ProviderAzure = "azure"

	// AccessProbe: the probe's own identity (instance profile, IRSA, managed
	// identity). AccessRole: an AWS role assumed with an external ID.
	// AccessWorkload: an Azure identity trusting the probe's Kubernetes
	// service account. AccessFederated: a token from the control plane.
	AccessProbe     = "probe"
	AccessRole      = "role"
	AccessWorkload  = "workload"
	AccessFederated = "federated"

	UseInventory = "inventory"

	UseOK              = "ok"
	UseNeedsPermission = "needs_permission"
	UseError           = "error"

	// ScanInventory is a full discovery; ScanHealth refreshes the state of
	// what the last discovery found (new resources wait for the next one).
	ScanInventory = "inventory"
	ScanHealth    = "health"

	// Cloud APIs throttle per account and region, so full discoveries are
	// rare and health scans only re-read known resources.
	DefaultCloudInventorySeconds = 4 * 3600
	MinCloudInventorySeconds     = 1800
	MaxCloudInventorySeconds     = 24 * 3600
	DefaultCloudHealthSeconds    = 900
	MinCloudHealthSeconds        = 300
	MaxCloudHealthSeconds        = 3600
)

// CloudConnection is what a probe needs to scan one account or
// subscription: identifiers only, never keys.
type CloudConnection struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	// Scope: account or organization (AWS), subscription or
	// management_group (Azure); ScopeID is its ID.
	Scope   string `json:"scope"`
	ScopeID string `json:"scopeId"`
	Access  string `json:"access"`
	// Identity: the role ARN (AWS) or client ID (Azure) to sign in as.
	Identity      string   `json:"identity,omitempty"`
	ExternalID    string   `json:"externalId,omitempty"`
	TenantID      string   `json:"tenantId,omitempty"`
	Uses          []string `json:"uses"`
	Regions       []string `json:"regions,omitempty"`
	ResourceTypes []string `json:"resourceTypes,omitempty"`
	// IntervalSeconds between full discoveries; HealthIntervalSeconds
	// between health scans (0 from an older control plane: the default).
	IntervalSeconds       int    `json:"intervalSeconds"`
	HealthIntervalSeconds int    `json:"healthIntervalSeconds,omitempty"`
	DestinationID         string `json:"destinationId"`
}

// CloudScan is the outcome of one scan or Test access: per use whether it
// worked, and what was found.
type CloudScan struct {
	// Kind: ScanInventory (also Test access) or ScanHealth.
	Kind      string               `json:"kind,omitempty"`
	Uses      map[string]UseResult `json:"uses"`
	Resources int                  `json:"resources"`
	// Types counts resources by type, e.g. aws_ec2_instance: 12.
	Types     map[string]int `json:"types,omitempty"`
	Regions   []string       `json:"regions,omitempty"`
	Errors    []string       `json:"errors,omitempty"`
	Probe     string         `json:"probe,omitempty"`
	StartedAt time.Time      `json:"startedAt"`
	Duration  float64        `json:"durationSeconds"`
	// Throttled: calls the cloud answered "slow down"; StoodDown: seconds
	// spent waiting as it asked.
	Throttled int     `json:"throttled,omitempty"`
	StoodDown float64 `json:"stoodDownSeconds,omitempty"`
	// Partial: the scan hit its time limit; what it found was still sent.
	Partial bool `json:"partial,omitempty"`
}

type UseResult struct {
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
	// Missing: the actions or roles a needs_permission use lacks.
	Missing []string `json:"missing,omitempty"`
}

// CloudToken is a short-lived token the control plane signs for a
// federated connection, which the probe exchanges for cloud credentials.
type CloudToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}
