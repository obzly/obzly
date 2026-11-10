package spec

import "time"

// Posture: findings against security standards (docs/CLOUD_COST_POSTURE.md "Posture").
const (
	UsePosture = "posture"
	// ScanPosture reads native findings and runs the baseline checks.
	ScanPosture = "posture"

	// Posture sources: auto uses native findings where the service is on and readable and the
	// Obzly baseline elsewhere; native and baseline use only one of them.
	PostureSourceAuto     = "auto"
	PostureSourceNative   = "native"
	PostureSourceBaseline = "baseline"

	DefaultPostureRefreshHours = 12
	MinPostureRefreshHours     = 4
	MaxPostureRefreshHours     = 24

	// A scope run's result: ok (evaluated), not_enabled (the native service is off there),
	// needs_permission or error. Only ok resolves findings.
	PostureRunOK              = "ok"
	PostureRunNotEnabled      = "not_enabled"
	PostureRunNeedsPermission = "needs_permission"
	PostureRunError           = "error"

	// GlobalRegion holds account-wide checks (IAM, S3 account settings, subscription policies).
	GlobalRegion = "global"

	// Severities, highest first.
	SeverityCritical      = "critical"
	SeverityHigh          = "high"
	SeverityMedium        = "medium"
	SeverityLow           = "low"
	SeverityInformational = "informational"
)

// PostureSettings are the posture use's choices for one connection.
type PostureSettings struct {
	Source       string `json:"source"`
	RefreshHours int    `json:"refreshHours,omitempty"`
}

// PostureScanInfo describes one posture refresh, reported to the control plane.
type PostureScanInfo struct {
	Findings int `json:"findings"`
	// Native and Baseline: the regions each source covered; NotCovered: neither did.
	Native     []string `json:"native,omitempty"`
	Baseline   []string `json:"baseline,omitempty"`
	NotCovered []string `json:"notCovered,omitempty"`
}

// PostureBatch is one chunk of a posture refresh sent to a Site. Once all Total chunks have
// arrived, each ok run replaces the open findings of its scope: a finding of that scope that
// isn't in the batch is resolved.
type PostureBatch struct {
	BatchID        string           `json:"batchId"`
	Seq            int              `json:"seq"`
	Total          int              `json:"total"`
	ConnectionID   string           `json:"connectionId"`
	ConnectionName string           `json:"connectionName"`
	Provider       string           `json:"provider"`
	ScannedAt      time.Time        `json:"scannedAt"`
	Runs           []PostureRun     `json:"runs,omitempty"`
	Findings       []PostureFinding `json:"findings,omitempty"`
}

// PostureRun is one scope (account, region, standard) read from one source.
type PostureRun struct {
	Account  string `json:"account"`
	Region   string `json:"region"`
	Standard string `json:"standard"`
	Source   string `json:"source"`
	Result   string `json:"result"`
	// Reason says why a run isn't ok: "Security Hub not enabled", "securityhub:GetFindings denied".
	Reason string `json:"reason,omitempty"`
	// Passed and Failed: the controls evaluated in this scope, for the standard's score.
	Passed []string `json:"passed,omitempty"`
	Failed []string `json:"failed,omitempty"`
}

// PostureFinding is one failed control on one resource.
type PostureFinding struct {
	Source  string `json:"source"`
	Account string `json:"account"`
	Region  string `json:"region"`
	// Control is the provider's control ID (Security Hub S3.8, a Defender assessment) or the
	// FSBP / MCSB control a baseline check stands in for, so a finding keeps its identity when
	// its source changes.
	Control      string        `json:"control"`
	Check        string        `json:"check,omitempty"`
	Title        string        `json:"title"`
	Severity     string        `json:"severity"`
	ResourceKey  string        `json:"resourceKey"`
	ResourceType string        `json:"resourceType,omitempty"`
	Standards    []StandardRef `json:"standards,omitempty"`
	// Evidence: a few fields that show why it failed, never secrets or raw configuration.
	Evidence       map[string]string `json:"evidence,omitempty"`
	RemediationURL string            `json:"remediationUrl,omitempty"`
}

// StandardRef is a control in a standard: {cis-aws, 2.1.1}.
type StandardRef struct {
	Standard string `json:"standard"`
	Control  string `json:"control"`
}

// PostureStandardName names the standards findings are mapped to.
var PostureStandardName = map[string]string{
	"aws-fsbp":       "AWS Foundational Security Best Practices",
	"cis-aws":        "CIS AWS Foundations Benchmark",
	"mcsb":           "Microsoft cloud security benchmark",
	"cis-azure":      "CIS Microsoft Azure Foundations Benchmark",
	"obzly-baseline": "Obzly baseline",
}
