package spec

import "time"

// Cost advice (docs/CLOUD_COST_POSTURE.md "Recommendations"): what the probe reads weekly for a
// connection that reads cost: savings recommendations, existing commitments and the providers'
// purchase recommendations for the planner. Obzly never buys or changes anything.

const (
	// AdviceRefreshDays: purchase recommendations change slowly and Cost Explorer charges per call.
	AdviceRefreshDays = 7

	// Sections of an advice refresh, each with its own state.
	AdviceRightsizing = "rightsizing" // Compute Optimizer, Azure Advisor
	AdviceChecks      = "checks"      // Obzly's own idle and modernization checks
	AdviceCommitments = "commitments" // existing Savings Plans, reservations
	AdvicePlanner     = "planner"     // purchase recommendations

	// Section states: the posture run results, so the UI reads them the same way.
	AdviceOK              = PostureRunOK
	AdviceNotEnabled      = PostureRunNotEnabled
	AdviceNeedsPermission = PostureRunNeedsPermission
	AdviceError           = PostureRunError

	// Recommendation kinds.
	RecommendRightsizing = "rightsizing"
	RecommendIdle        = "idle"
	RecommendModernize   = "modernize"
	RecommendStorage     = "storage"

	// Recommendation sources.
	RecommendFromComputeOptimizer = "compute_optimizer"
	RecommendFromAdvisor          = "advisor"
	RecommendFromObzly            = "obzly"

	// Commitment and purchase types.
	CommitComputeSP    = "compute_sp"
	CommitEC2SP        = "ec2_instance_sp"
	CommitEC2RI        = "ec2_ri"
	CommitRDSRI        = "rds_ri"
	CommitAzureSP      = "azure_sp"
	CommitAzureReserve = "azure_reservation"

	// Terms and payments.
	Term1Year       = "1y"
	Term3Years      = "3y"
	PayNoUpfront    = "no_upfront"
	PayPartial      = "partial_upfront"
	PayAllUpfront   = "all_upfront"
	PayMonthly      = "monthly"
	PayUpfront      = "upfront"
	MaxAdviceChunks = 50
)

// AdviceLookbacks are the look-back windows the planner compares, in days.
var AdviceLookbacks = []int{7, 30, 60}

// AdviceScanInfo describes one advice refresh, reported with the cost refresh it ran in.
type AdviceScanInfo struct {
	Recommendations int             `json:"recommendations"`
	Commitments     int             `json:"commitments"`
	Options         int             `json:"options"`
	Sections        []AdviceSection `json:"sections,omitempty"`
}

// AdviceSection is how one part of a refresh went: ok, not_enabled, needs_permission or error.
// A section that isn't ok keeps its last good data on the Site.
type AdviceSection struct {
	Section string `json:"section"`
	State   string `json:"state"`
	Reason  string `json:"reason,omitempty"`
}

// AdviceBatch is one chunk of a connection's advice refresh. Once every chunk has arrived the
// Site replaces the connection's data for each section that came back ok.
type AdviceBatch struct {
	BatchID         string               `json:"batchId"`
	Seq             int                  `json:"seq"`
	Total           int                  `json:"total"`
	ConnectionID    string               `json:"connectionId"`
	ConnectionName  string               `json:"connectionName"`
	Provider        string               `json:"provider"`
	GeneratedAt     time.Time            `json:"generatedAt"`
	Sections        []AdviceSection      `json:"sections,omitempty"`
	Recommendations []CostRecommendation `json:"recommendations,omitempty"`
	Commitments     []Commitment         `json:"commitments,omitempty"`
	Coverage        []CommitmentCoverage `json:"coverage,omitempty"`
	Options         []PurchaseOption     `json:"options,omitempty"`
}

// CostRecommendation is one change that saves money on one resource. Key is stable across
// refreshes, so its state (planned, dismissed...) survives them.
type CostRecommendation struct {
	Key          string `json:"key"`
	Source       string `json:"source"`
	Kind         string `json:"kind"`
	Account      string `json:"account"`
	Region       string `json:"region,omitempty"`
	ResourceKey  string `json:"resourceKey"`
	ResourceName string `json:"resourceName,omitempty"`
	ResourceType string `json:"resourceType,omitempty"`
	// Action says what to do, e.g. "Change m5.2xlarge to m5.xlarge".
	Action  string `json:"action"`
	Current string `json:"current,omitempty"`
	Target  string `json:"target,omitempty"`
	// Currency and MonthlySavingsMicros: the provider's estimate. Without one (Obzly checks),
	// SavingsShare is the part of the resource's own cost the change saves (1 for idle), and the
	// Site works the amount out from cost data.
	Currency             string  `json:"currency,omitempty"`
	MonthlySavingsMicros int64   `json:"monthlySavings,omitempty"`
	SavingsShare         float64 `json:"savingsShare,omitempty"`
	// Effort and Risk: low, medium or high.
	Effort   string            `json:"effort,omitempty"`
	Risk     string            `json:"risk,omitempty"`
	Evidence map[string]string `json:"evidence,omitempty"`
}

// Commitment is an existing Savings Plan or reservation with how much of it was used over the
// last 30 days.
type Commitment struct {
	ID          string     `json:"id"`
	Type        string     `json:"type"`
	Account     string     `json:"account,omitempty"`
	Region      string     `json:"region,omitempty"`
	Description string     `json:"description"`
	Term        string     `json:"term,omitempty"`
	Payment     string     `json:"payment,omitempty"`
	Start       *time.Time `json:"start,omitempty"`
	End         *time.Time `json:"end,omitempty"`
	Currency    string     `json:"currency,omitempty"`
	// MonthlyCostMicros: the commitment's amortized cost per month.
	MonthlyCostMicros int64 `json:"monthlyCost,omitempty"`
	// UtilizationPercent over the last 30 days; nil when the provider didn't say.
	UtilizationPercent *float64 `json:"utilization,omitempty"`
	// UnusedMonthlyMicros: the cost of the unused part per month.
	UnusedMonthlyMicros int64 `json:"unusedMonthly,omitempty"`
}

// CommitmentCoverage: how much of the eligible usage commitments covered over the last 30 days.
type CommitmentCoverage struct {
	Type                  string  `json:"type"`
	Currency              string  `json:"currency,omitempty"`
	CoveredPercent        float64 `json:"coveredPercent"`
	OnDemandMonthlyMicros int64   `json:"onDemandMonthly,omitempty"`
}

// PurchaseOption is the provider's purchase recommendation for one type, term, payment and
// look-back. Offered false is a combination the provider doesn't sell ("Not offered").
type PurchaseOption struct {
	Type         string `json:"type"`
	Term         string `json:"term"`
	Payment      string `json:"payment"`
	LookbackDays int    `json:"lookbackDays"`
	Offered      bool   `json:"offered"`
	Reason       string `json:"reason,omitempty"`
	Currency     string `json:"currency,omitempty"`
	// Commitment says what to buy, e.g. "$4.20 an hour" or "6 × m5.large in eu-west-1".
	Commitment             string         `json:"commitment,omitempty"`
	HourlyCommitmentMicros int64          `json:"hourlyCommitment,omitempty"`
	UpfrontMicros          int64          `json:"upfront,omitempty"`
	MonthlyRecurringMicros int64          `json:"monthlyRecurring,omitempty"`
	MonthlySavingsMicros   int64          `json:"monthlySavings,omitempty"`
	OnDemandMonthlyMicros  int64          `json:"onDemandMonthly,omitempty"`
	SavingsPercent         float64        `json:"savingsPercent,omitempty"`
	BreakEvenMonths        float64        `json:"breakEvenMonths,omitempty"`
	AverageUtilization     float64        `json:"averageUtilization,omitempty"`
	CoveragePercent        float64        `json:"coveragePercent,omitempty"`
	Lines                  []PurchaseLine `json:"lines,omitempty"`
}

// PurchaseLine is one item of a purchase option, e.g. one instance family in one region.
type PurchaseLine struct {
	Description          string `json:"description"`
	Account              string `json:"account,omitempty"`
	Region               string `json:"region,omitempty"`
	Quantity             string `json:"quantity,omitempty"`
	MonthlySavingsMicros int64  `json:"monthlySavings,omitempty"`
	UpfrontMicros        int64  `json:"upfront,omitempty"`
}
