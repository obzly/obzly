package discovery

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"obzly/internal/posture/baseline"
	"obzly/internal/synthetics/spec"
)

// Posture reading (docs/CLOUD_COST_POSTURE.md "Posture"): native findings where the provider's
// service is on and readable, Obzly's baseline checks elsewhere, one source per scope.

// PostureResult is one refresh: the batch to send (the reporter splits it) and the report.
type PostureResult struct {
	Batch spec.PostureBatch
	Scan  spec.CloudScan
}

const (
	// postureMaxFindings bounds one scope's native findings, postureMaxBuckets the buckets
	// the baseline reads.
	postureMaxFindings = 20_000
	postureMaxBuckets  = 1000
	// postureNativeStandard is what a run without enabled standards is reported under.
	postureBaselineStandard = "obzly-baseline"
)

// adminPorts are the remote admin ports the baseline looks for open to the world.
var adminPorts = []int{22, 3389}

// postureProblem is why a scope couldn't be read from a source.
type postureProblem struct {
	result string
	reason string
}

// postureCollector gathers runs and findings from scopes read in parallel.
type postureCollector struct {
	mu       sync.Mutex
	runs     []spec.PostureRun
	findings []spec.PostureFinding
	covered  map[string]map[string]bool
}

// CollectPosture reads the connection's posture once. now stamps the batch.
func CollectPosture(
	ctx context.Context,
	connection spec.CloudConnection,
	options Options,
	now time.Time,
) PostureResult {
	started := time.Now().UTC()
	ctx, stats := withStats(ctx)
	options = options.prepared()
	t := newTracker()
	source := spec.PostureSourceAuto
	if connection.Posture != nil && connection.Posture.Source != "" {
		source = connection.Posture.Source
	}
	c := &postureCollector{covered: map[string]map[string]bool{}}
	switch connection.Provider {
	case spec.ProviderAWS:
		collectAWSPosture(ctx, connection, options, t, c, source)
	case spec.ProviderAzure:
		collectAzurePosture(ctx, connection, options, t, c, source)
	default:
		t.fail(fmt.Errorf("unknown provider %q", connection.Provider))
	}
	info := c.info()
	scan := spec.CloudScan{
		Kind: spec.ScanPosture, Uses: map[string]spec.UseResult{spec.UsePosture: postureUse(t, info, source)},
		StartedAt: started, Duration: time.Since(started).Seconds(), Throttled: int(stats.throttled.Load()),
		StoodDown: time.Duration(stats.stoodDown.Load()).Seconds(), Posture: info, Partial: ctx.Err() != nil,
		Errors: t.errorList(),
	}
	sort.Slice(c.runs, func(i, j int) bool { return runKey(c.runs[i]) < runKey(c.runs[j]) })
	return PostureResult{
		Batch: spec.PostureBatch{
			BatchID: fmt.Sprintf("%s-posture-%d", connection.ID, now.UnixNano()), ConnectionID: connection.ID,
			ConnectionName: connection.Name, Provider: connection.Provider, ScannedAt: now.UTC(),
			Runs: c.runs, Findings: c.findings,
		},
		Scan: scan,
	}
}

func (c *postureCollector) add(runs []spec.PostureRun, findings []spec.PostureFinding) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.runs = append(c.runs, runs...)
	c.findings = append(c.findings, findings...)
}

// cover records how a scope was covered: native, baseline or none.
func (c *postureCollector) cover(account, region, how string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.covered[how] == nil {
		c.covered[how] = map[string]bool{}
	}
	c.covered[how][scopeName(account, region)] = true
}

func (c *postureCollector) info() *spec.PostureScanInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return &spec.PostureScanInfo{
		Findings:   len(c.findings),
		Native:     slices.Sorted(maps.Keys(c.covered[spec.PostureSourceNative])),
		Baseline:   slices.Sorted(maps.Keys(c.covered[spec.PostureSourceBaseline])),
		NotCovered: slices.Sorted(maps.Keys(c.covered[""])),
	}
}

// postureUse is the posture use's state: the calls' result, or a note when native found
// nothing to read under Native only.
func postureUse(t *tracker, info *spec.PostureScanInfo, source string) spec.UseResult {
	use := t.result()
	if use.State != spec.UseOK {
		return use
	}
	switch {
	case source == spec.PostureSourceNative && len(info.Native) == 0:
		use.Message = "the native service isn't on anywhere in scope, so nothing is covered"
	case len(info.NotCovered) > 0:
		use.Message = fmt.Sprintf("not covered: %s", strings.Join(info.NotCovered, ", "))
	default:
		use.Message = fmt.Sprintf("%d findings; native in %d scopes, baseline in %d", info.Findings,
			len(info.Native), len(info.Baseline))
	}
	return use
}

// baselineScope evaluates the provider's checks on a scope's facts. types are the fact types
// the scope read; their checks' standards each get a run even with nothing to evaluate. A
// problem (a read denied or failed) makes the runs not ok, so they resolve nothing.
func baselineScope(
	provider, account, region string, types []string, facts []baseline.Fact, problem *postureProblem,
) ([]spec.PostureRun, []spec.PostureFinding) {
	passed := map[string]map[string]bool{}
	failed := map[string]map[string]bool{}
	ensure := func(standard string) {
		if passed[standard] == nil {
			passed[standard], failed[standard] = map[string]bool{}, map[string]bool{}
		}
	}
	for _, check := range baseline.ForProvider(provider) {
		if slices.Contains(types, check.Resource) {
			for _, ref := range standardsOf(check) {
				ensure(ref.Standard)
			}
		}
	}
	var findings []spec.PostureFinding
	for _, result := range baseline.Evaluate(provider, facts) {
		for _, ref := range standardsOf(result.Check) {
			ensure(ref.Standard)
			if result.Passed {
				passed[ref.Standard][ref.Control] = true
			} else {
				failed[ref.Standard][ref.Control] = true
			}
		}
		if result.Passed {
			continue
		}
		evidence := result.Check.EvidenceOf(result.Fact.Values)
		for _, field := range []string{"name", "resourceGroup", "location"} {
			if value, ok := result.Fact.Values[field].(string); ok && value != "" {
				evidence[field] = value
			}
		}
		findings = append(findings, spec.PostureFinding{
			Source: spec.PostureSourceBaseline, Account: account, Region: region, Control: result.Check.Control,
			Check: result.Check.ID, Title: result.Check.Title, Severity: result.Check.Severity,
			ResourceKey: result.Fact.Key, ResourceType: result.Fact.Type, Standards: result.Check.Standards,
			Evidence: evidence,
		})
	}
	runs := make([]spec.PostureRun, 0, len(passed))
	for _, standard := range slices.Sorted(maps.Keys(passed)) {
		run := spec.PostureRun{Account: account, Region: region, Standard: standard,
			Source: spec.PostureSourceBaseline, Result: spec.PostureRunOK}
		if problem != nil {
			run.Result, run.Reason = problem.result, problem.reason
		}
		run.Passed, run.Failed = controlLists(passed[standard], failed[standard])
		runs = append(runs, run)
	}
	return runs, findings
}

// unreadRuns reports a native scope that couldn't be read, for each standard it would cover.
func unreadRuns(account, region, source string, standards []string, problem postureProblem) []spec.PostureRun {
	runs := make([]spec.PostureRun, 0, len(standards))
	for _, standard := range standards {
		runs = append(runs, spec.PostureRun{Account: account, Region: region, Standard: standard, Source: source,
			Result: problem.result, Reason: problem.reason})
	}
	return runs
}

// controlLists: a control that failed anywhere in the scope isn't also passed there.
func controlLists(passed, failed map[string]bool) ([]string, []string) {
	var passedList []string
	for control := range passed {
		if !failed[control] {
			passedList = append(passedList, control)
		}
	}
	return sortControls(passedList), sortControls(slices.Collect(maps.Keys(failed)))
}

// sortControls orders control IDs naturally: 1.4 before 1.10, EC2.8 before EC2.13.
func sortControls(controls []string) []string {
	sort.Slice(controls, func(i, j int) bool { return naturalLess(controls[i], controls[j]) })
	return controls
}

func naturalLess(a, b string) bool {
	split := func(s string) []string {
		return strings.FieldsFunc(s, func(r rune) bool { return r == '.' || r == '-' || r == '/' })
	}
	pa, pb := split(a), split(b)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		na, errA := strconv.Atoi(pa[i])
		nb, errB := strconv.Atoi(pb[i])
		switch {
		case errA == nil && errB == nil && na != nb:
			return na < nb
		case (errA != nil || errB != nil) && pa[i] != pb[i]:
			return pa[i] < pb[i]
		}
	}
	return len(pa) < len(pb)
}

func standardsOf(check *baseline.Check) []spec.StandardRef {
	if len(check.Standards) == 0 {
		return []spec.StandardRef{{Standard: postureBaselineStandard, Control: check.Control}}
	}
	return check.Standards
}

func scopeName(account, region string) string { return account + "/" + region }

func runKey(run spec.PostureRun) string {
	return strings.Join([]string{run.Account, run.Region, run.Standard, run.Source}, "|")
}

// portsOpen reports which admin ports from..to (inclusive) covers.
func portsOpen(from, to int) []int {
	var open []int
	for _, port := range adminPorts {
		if from <= port && port <= to {
			open = append(open, port)
		}
	}
	return open
}
