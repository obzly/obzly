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
	UseCost      = "cost"

	UseOK              = "ok"
	UseNeedsPermission = "needs_permission"
	UseError           = "error"

	// ScanInventory is a full discovery; ScanHealth refreshes the state of
	// what the last discovery found (new resources wait for the next one).
	ScanInventory = "inventory"
	ScanHealth    = "health"
	// ScanCost reads billing data (docs/CLOUD_COST_POSTURE.md "Cost").
	ScanCost = "cost"

	// Cloud APIs throttle per account and region, so full discoveries are
	// rare and health scans only re-read known resources.
	DefaultCloudInventorySeconds = 4 * 3600
	MinCloudInventorySeconds     = 1800
	MaxCloudInventorySeconds     = 24 * 3600
	DefaultCloudHealthSeconds    = 900
	MinCloudHealthSeconds        = 300
	MaxCloudHealthSeconds        = 3600

	// Cost sources: auto reads the FOCUS export once it has data and the
	// billing API until then; export and api use only one of them.
	CostSourceAuto   = "auto"
	CostSourceExport = "export"
	CostSourceAPI    = "api"
	// Cost scopes: account reads this account or subscription only;
	// billing reads the management account or billing scope for every
	// member; via means another connection reads it, so it is never
	// counted twice.
	CostScopeAccount = "account"
	CostScopeBilling = "billing"
	CostScopeVia     = "via"

	// Billing data changes a few times a day at most.
	DefaultCostRefreshHours = 12
	MinCostRefreshHours     = 6
	MaxCostRefreshHours     = 24
	MaxCostTrackedTags      = 10
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
	// Cost is set when Uses has UseCost.
	Cost *CostSettings `json:"cost,omitempty"`
	// Posture is set when Uses has UsePosture.
	Posture *PostureSettings `json:"posture,omitempty"`
}

// CostSettings are the cost use's choices for one connection.
type CostSettings struct {
	Source string `json:"source"`
	Scope  string `json:"scope"`
	// ViaConnectionID: with CostScopeVia, the connection that reads this
	// one's cost.
	ViaConnectionID string      `json:"viaConnectionId,omitempty"`
	Export          *CostExport `json:"export,omitempty"`
	RefreshHours    int         `json:"refreshHours,omitempty"`
	// TrackedTags: tag keys kept on cost rows (all others are dropped).
	TrackedTags []string `json:"trackedTags,omitempty"`
}

// CostExport is where a FOCUS export lands: an S3 bucket (AWS) or a storage
// account container (Azure), with the prefix the export writes under.
type CostExport struct {
	Bucket         string `json:"bucket,omitempty"`
	Region         string `json:"region,omitempty"`
	StorageAccount string `json:"storageAccount,omitempty"`
	Container      string `json:"container,omitempty"`
	Prefix         string `json:"prefix,omitempty"`
}

// CostScanInfo describes one cost refresh, reported to the control plane.
type CostScanInfo struct {
	// Source is what this refresh read: export or api.
	Source string `json:"source"`
	// FinalThrough: the last day the provider has finalised (YYYY-MM-DD).
	FinalThrough string `json:"finalThrough,omitempty"`
	From         string `json:"from,omitempty"`
	To           string `json:"to,omitempty"`
	Rows         int    `json:"rows"`
	// LastFile and LastFileAt: the newest export file read.
	LastFile   string     `json:"lastFile,omitempty"`
	LastFileAt *time.Time `json:"lastFileAt,omitempty"`
	// Requests counts billing API calls (Cost Explorer charges per call).
	Requests int `json:"requests,omitempty"`
	// Advice is set when this refresh also read recommendations (weekly).
	Advice *AdviceScanInfo `json:"advice,omitempty"`
}

// CostBatch is one chunk of daily cost rows sent to a Site. A batch replaces
// the rows of its connection and source for the days From..To (inclusive)
// once all Total chunks have arrived, so a half-sent batch changes nothing.
type CostBatch struct {
	BatchID        string    `json:"batchId"`
	Seq            int       `json:"seq"`
	Total          int       `json:"total"`
	ConnectionID   string    `json:"connectionId"`
	ConnectionName string    `json:"connectionName"`
	Provider       string    `json:"provider"`
	Source         string    `json:"source"`
	From           string    `json:"from"`
	To             string    `json:"to"`
	FinalThrough   string    `json:"finalThrough,omitempty"`
	Rows           []CostRow `json:"rows"`
	// Forecast is sent with the last chunk only.
	Forecast []CostForecast `json:"forecast,omitempty"`
}

// CostRow is one day of cost for one combination of dimensions, in FOCUS
// terms. Amounts are millionths of the currency unit, summed exactly.
type CostRow struct {
	Day             string            `json:"d"`
	BillingAccount  string            `json:"ba,omitempty"`
	SubAccount      string            `json:"sa,omitempty"`
	SubAccountName  string            `json:"san,omitempty"`
	Service         string            `json:"svc,omitempty"`
	Region          string            `json:"rg,omitempty"`
	Resource        string            `json:"res,omitempty"`
	ChargeCategory  string            `json:"cc,omitempty"`
	PricingCategory string            `json:"pc,omitempty"`
	Currency        string            `json:"cur"`
	BilledMicros    int64             `json:"b"`
	EffectiveMicros int64             `json:"e"`
	ListMicros      int64             `json:"l,omitempty"`
	Tags            map[string]string `json:"t,omitempty"`
	Estimated       bool              `json:"est,omitempty"`
}

// CostForecast is the provider's forecast of effective cost for the rest of
// a month, per currency (the Site adds the month's actual cost so far).
type CostForecast struct {
	Month           string `json:"month"`
	Currency        string `json:"cur"`
	RemainingMicros int64  `json:"remaining"`
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
	// Cost is set on ScanCost reports.
	Cost *CostScanInfo `json:"cost,omitempty"`
	// Posture is set on ScanPosture reports.
	Posture *PostureScanInfo `json:"posture,omitempty"`
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
