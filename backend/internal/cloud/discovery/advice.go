package discovery

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"obzly/internal/synthetics/spec"
)

// Cost advice (docs/CLOUD_COST_POSTURE.md "Recommendations"): savings recommendations from the
// providers and Obzly's own checks, existing commitments and the providers' purchase
// recommendations, read weekly by the connection that reads cost.

// AdviceResult is one advice refresh: the batch to send (the reporter splits it), the report,
// and the billing API calls it made (Cost Explorer charges per call).
type AdviceResult struct {
	Batch    spec.AdviceBatch
	Info     spec.AdviceScanInfo
	Requests int
}

const (
	// adviceMaxPerRegion bounds one region's recommendations and checked volumes.
	adviceMaxPerRegion = 2000
	daysPerMonth       = 30.4375
	hoursPerMonth      = 730
	// awsPublicIPv4Hourly: AWS charges every public IPv4 address $0.005 an hour, in use or not.
	awsPublicIPv4MonthlyMicros = 3_650_000
	// gp3 costs 20% less per GiB-month than gp2 at the same baseline performance.
	gp3SavingsShare = 0.2
)

// sectionRank orders section states: a worse state from any account or region wins, so a
// section is ok only when every part of it was read.
var sectionRank = map[string]int{
	spec.AdviceOK: 0, spec.AdviceNotEnabled: 1, spec.AdviceNeedsPermission: 2, spec.AdviceError: 3,
}

// adviceCollector gathers what accounts and regions read in parallel find.
type adviceCollector struct {
	mu          sync.Mutex
	recs        []spec.CostRecommendation
	commitments []spec.Commitment
	coverage    []spec.CommitmentCoverage
	options     []spec.PurchaseOption
	sections    map[string]spec.AdviceSection
	requests    int
}

// CollectAdvice reads the connection's recommendations, commitments and purchase options once.
func CollectAdvice(ctx context.Context, connection spec.CloudConnection, options Options, now time.Time) AdviceResult {
	options = options.prepared()
	c := &adviceCollector{sections: map[string]spec.AdviceSection{}}
	switch connection.Provider {
	case spec.ProviderAWS:
		collectAWSAdvice(ctx, connection, options, c)
	case spec.ProviderAzure:
		collectAzureAdvice(ctx, connection, options, c)
	default:
		for _, section := range []string{spec.AdviceRightsizing, spec.AdviceChecks, spec.AdviceCommitments,
			spec.AdvicePlanner} {
			c.section(section, spec.AdviceError, fmt.Sprintf("unknown provider %q", connection.Provider))
		}
	}
	if ctx.Err() != nil {
		for section := range c.sections {
			c.section(section, spec.AdviceError, "the refresh ran out of time")
		}
	}
	recs := dedupeRecommendations(c.recs)
	sections := make([]spec.AdviceSection, 0, len(c.sections))
	for _, section := range c.sections {
		sections = append(sections, section)
	}
	sort.Slice(sections, func(i, j int) bool { return sections[i].Section < sections[j].Section })
	sort.Slice(c.options, func(i, j int) bool { return optionKey(c.options[i]) < optionKey(c.options[j]) })
	return AdviceResult{
		Batch: spec.AdviceBatch{
			ConnectionID: connection.ID, ConnectionName: connection.Name, Provider: connection.Provider,
			GeneratedAt: now.UTC(), Sections: sections, Recommendations: recs, Commitments: c.commitments,
			Coverage: c.coverage, Options: c.options,
		},
		Info: spec.AdviceScanInfo{Recommendations: len(recs), Commitments: len(c.commitments),
			Options: len(c.options), Sections: sections},
		Requests: c.requests,
	}
}

// section records how a part of a section went; ok only where it hasn't gone worse elsewhere.
func (c *adviceCollector) section(name, state, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	current, seen := c.sections[name]
	if seen && sectionRank[current.State] >= sectionRank[state] {
		return
	}
	c.sections[name] = spec.AdviceSection{Section: name, State: state, Reason: clip(reason, 500)}
}

func (c *adviceCollector) addRecs(recs ...spec.CostRecommendation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recs = append(c.recs, recs...)
}

func (c *adviceCollector) addCommitments(commitments ...spec.Commitment) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.commitments = append(c.commitments, commitments...)
}

func (c *adviceCollector) addOption(option spec.PurchaseOption) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.options = append(c.options, option)
}

func (c *adviceCollector) addCoverage(coverage spec.CommitmentCoverage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.coverage = append(c.coverage, coverage)
}

func (c *adviceCollector) request() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests++
}

// dedupeRecommendations keeps one recommendation per resource and kind, the provider's over
// Obzly's own check (the provider knows the resource's real usage).
func dedupeRecommendations(recs []spec.CostRecommendation) []spec.CostRecommendation {
	best := map[string]int{}
	var out []spec.CostRecommendation
	for _, rec := range recs {
		key := rec.ResourceKey + "\x00" + rec.Kind
		if i, ok := best[key]; ok {
			if out[i].Source == spec.RecommendFromObzly && rec.Source != spec.RecommendFromObzly {
				out[i] = rec
			}
			continue
		}
		best[key] = len(out)
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func optionKey(option spec.PurchaseOption) string {
	return fmt.Sprintf("%s|%s|%s|%03d", option.Type, option.Term, option.Payment, option.LookbackDays)
}

// microsOf converts a provider's decimal amount (a number or numeric text) to millionths.
func microsOf(value float64) int64 { return int64(math.Round(value * 1_000_000)) }

func textMicros(value string) int64 {
	micros, err := parseMicros(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return micros
}

// monthlyFrom scales an amount over days to a month.
func monthlyFrom(micros int64, days int) int64 {
	if days <= 0 {
		return 0
	}
	return int64(math.Round(float64(micros) * daysPerMonth / float64(days)))
}

// riskLevel maps a 0-4 performance risk to low, medium or high.
func riskLevel(risk float64) string {
	switch {
	case risk < 1.5:
		return "low"
	case risk < 3:
		return "medium"
	}
	return "high"
}

func clip(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) > limit {
		return text[:limit]
	}
	return text
}
