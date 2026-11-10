// Package discovery lists the resources of one cloud connection - an AWS
// account or Organization, an Azure subscription or management group -
// for Infrastructure (docs/INFRASTRUCTURE.md "Cloud connections"). It runs
// in obzly-probe with read-only credentials it never stores.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"obzly/internal/synthetics/report"
	"obzly/internal/synthetics/spec"
)

// Resource is one discovered resource. Type is provider-prefixed, e.g.
// aws_ec2_instance or azure_virtual_machine.
type Resource struct {
	Provider  string
	AccountID string
	Region    string
	Type      string
	ID        string
	Name      string
	State     string
	// Health from a health scan: healthy, degraded, unhealthy or unknown;
	// empty when the cloud has no health for it.
	Health string `json:",omitempty"`
	// Attributes become labels: host_id, k8s_cluster_name, instance_type...
	Attributes map[string]string
	// Targets: a load balancer's targets by health (healthy, unhealthy...).
	Targets map[string]int
}

// TokenSource fetches the control plane's federated token.
type TokenSource func(ctx context.Context) (string, error)

type Options struct {
	HTTP  *http.Client
	Token TokenSource
	// Throttle paces calls; share one across a connection's scans.
	Throttle *Throttle
	// AWS keeps sign-ins between a connection's scans; nil signs in each
	// time (Test access).
	AWS *AWSCache
	// Sample is Test access: the first account and at most two regions,
	// so it answers within the wizard's wait.
	Sample bool
	// AWSEndpoint and the Azure ones point at a fake in tests.
	AWSEndpoint     string
	AzureLogin      string
	AzureManagement string
	AzureIMDS       string
	// AzureBlob replaces https://{account}.blob.core.windows.net ({account} is kept).
	AzureBlob        string
	FederatedTokenFS func(path string) ([]byte, error)
	Getenv           func(string) string
}

// Result is what one scan found and how each use went.
type Result struct {
	Resources []Resource
	Scan      spec.CloudScan
}

// A resource's health from a health scan.
const (
	HealthHealthy   = "healthy"
	HealthDegraded  = "degraded"
	HealthUnhealthy = "unhealthy"
	HealthUnknown   = "unknown"
)

const (
	// maxRegionsInParallel and maxAccountsInParallel bound API calls in
	// flight per scan (up to 16 at once for an Organization).
	maxRegionsInParallel  = 4
	maxAccountsInParallel = 4
	sampleRegions         = 2
)

// Discover lists everything connection can see, once.
func Discover(ctx context.Context, connection spec.CloudConnection, options Options) Result {
	started := time.Now().UTC()
	ctx, stats := withStats(ctx)
	options = options.prepared()
	tracker := newTracker()
	var resources []Resource
	switch connection.Provider {
	case spec.ProviderAWS:
		resources = discoverAWS(ctx, connection, options, tracker)
	case spec.ProviderAzure:
		resources = discoverAzure(ctx, connection, options, tracker)
	default:
		tracker.fail(fmt.Errorf("unknown provider %q", connection.Provider))
	}
	return finish(ctx, spec.ScanInventory, connection, filterTypes(connection, resources), tracker, stats, started)
}

// CheckHealth re-reads the state and health of known, the resources the
// last discovery found; resources it doesn't know wait for the next one.
// Result.Resources are the known resources still there, updated; those
// whose type couldn't be read come back as they were.
func CheckHealth(ctx context.Context, connection spec.CloudConnection, known []Resource, options Options) Result {
	started := time.Now().UTC()
	ctx, stats := withStats(ctx)
	options = options.prepared()
	tracker := newTracker()
	var resources []Resource
	switch connection.Provider {
	case spec.ProviderAWS:
		resources = healthAWS(ctx, connection, known, options, tracker)
	case spec.ProviderAzure:
		resources = healthAzure(ctx, connection, known, options, tracker)
	default:
		tracker.fail(fmt.Errorf("unknown provider %q", connection.Provider))
	}
	return finish(ctx, spec.ScanHealth, connection, resources, tracker, stats, started)
}

// Merge is updates followed by the resources of base not among them.
func Merge(base, updates []Resource) []Resource {
	seen := make(map[string]bool, len(updates))
	merged := make([]Resource, 0, max(len(base), len(updates)))
	for _, resource := range updates {
		seen[resource.ID] = true
		merged = append(merged, resource)
	}
	for _, resource := range base {
		if !seen[resource.ID] {
			merged = append(merged, resource)
		}
	}
	return merged
}

// CarryHealth gives found the health previous had for the same resources,
// until the next health scan reads it again.
func CarryHealth(previous, found []Resource) []Resource {
	health := map[string]string{}
	for _, resource := range previous {
		if resource.Health != "" {
			health[resource.ID] = resource.Health
		}
	}
	for i := range found {
		if found[i].Health == "" {
			found[i].Health = health[found[i].ID]
		}
	}
	return found
}

// finish summarizes a scan: counts, regions, how each use went, and
// whether it stopped at its time limit.
func finish(
	ctx context.Context,
	kind string,
	connection spec.CloudConnection,
	resources []Resource,
	tracker *tracker,
	stats *scanStats,
	started time.Time,
) Result {
	scan := spec.CloudScan{
		Kind: kind, Uses: map[string]spec.UseResult{}, Resources: len(resources), Types: map[string]int{},
		StartedAt: started, Duration: time.Since(started).Seconds(),
		Throttled: int(stats.throttled.Load()), StoodDown: time.Duration(stats.stoodDown.Load()).Seconds(),
		Partial: errors.Is(ctx.Err(), context.DeadlineExceeded),
	}
	regions := map[string]bool{}
	for _, resource := range resources {
		scan.Types[resource.Type]++
		if resource.Region != "" {
			regions[resource.Region] = true
		}
	}
	for region := range regions {
		scan.Regions = append(scan.Regions, region)
	}
	sort.Strings(scan.Regions)
	use := tracker.result()
	scan.Errors = tracker.errorList()
	if scan.Partial {
		use = spec.UseResult{
			State: spec.UseError, Missing: use.Missing,
			Message: fmt.Sprintf("stopped at its %.0f-minute time limit; the %d resources found were still sent "+
				"(scan less often or narrow the regions and types)", scan.Duration/60, len(resources)),
		}
	}
	// Discovery and health scans grade inventory only; cost reports on its own (CollectCost).
	scan.Uses[spec.UseInventory] = use
	return Result{Resources: resources, Scan: scan}
}

// Gauges turns resources into cloud_resource_info (1 per resource, its
// identity, state, health and attributes as labels) and cloud_lb_targets
// (per target health). refresh is how often they're resent, in seconds.
func Gauges(connection spec.CloudConnection, resources []Resource, refresh int) []report.Gauge {
	points := make([]report.Gauge, 0, len(resources))
	for _, resource := range resources {
		labels := map[string]string{}
		for key, value := range resource.Attributes {
			if value != "" {
				labels[key] = value
			}
		}
		identity := map[string]string{
			"cloud_connection_id": connection.ID,
			"cloud_provider":      resource.Provider,
			"cloud_account_id":    resource.AccountID,
			"cloud_region":        resource.Region,
			"cloud_resource_type": resource.Type,
			"cloud_resource_id":   resource.ID,
		}
		for key, value := range identity {
			labels[key] = value
		}
		labels["cloud_resource_name"] = resource.Name
		labels["cloud_resource_state"] = resource.State
		if resource.Health != "" {
			labels["cloud_health"] = resource.Health
		}
		if refresh > 0 {
			labels["cloud_refresh_seconds"] = strconv.Itoa(refresh)
		}
		points = append(points, report.Gauge{Name: "cloud_resource_info", Value: 1, Labels: labels})
		for health, count := range resource.Targets {
			targetLabels := map[string]string{"health": health}
			for key, value := range identity {
				targetLabels[key] = value
			}
			points = append(points, report.Gauge{Name: "cloud_lb_targets", Value: float64(count), Labels: targetLabels})
		}
	}
	return points
}

func (o Options) prepared() Options {
	if o.Throttle == nil {
		o.Throttle = NewThrottle()
	}
	o.HTTP = o.Throttle.client(o.HTTP)
	return o
}

func filterTypes(connection spec.CloudConnection, resources []Resource) []Resource {
	if len(connection.ResourceTypes) == 0 {
		return resources
	}
	kept := resources[:0]
	for _, resource := range resources {
		if slices.Contains(connection.ResourceTypes, strings.TrimPrefix(resource.Type, connection.Provider+"_")) {
			kept = append(kept, resource)
		}
	}
	return kept
}

func wants(connection spec.CloudConnection, kind string) bool {
	return len(connection.ResourceTypes) == 0 || slices.Contains(connection.ResourceTypes, kind)
}

// tracker collects, across concurrent calls, which were denied and which
// failed, to grade the use: ok, needs_permission (with what), or error.
type tracker struct {
	mu        sync.Mutex
	calls     int
	missing   map[string]bool
	failed    map[string]bool
	errors    []string
	fatal     error
	fatalPerm bool
}

func newTracker() *tracker { return &tracker{missing: map[string]bool{}, failed: map[string]bool{}} }

// call records one API call: denied adds action to the missing ones.
func (t *tracker) call(action string, err error, denied bool) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls++
	if err != nil {
		t.failed[action] = true
	}
	switch {
	case err == nil:
		return true
	case denied:
		t.missing[action] = true
	case len(t.errors) < 20:
		t.errors = append(t.errors, fmt.Sprintf("%s: %v", action, err))
	}
	return false
}

// fail ends the scan: signing in or listing accounts didn't work.
func (t *tracker) fail(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fatal == nil {
		t.fatal = err
	}
}

// failPermission is fail for a denial (e.g. the role doesn't trust the probe).
func (t *tracker) failPermission(err error, missing ...string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fatal == nil {
		t.fatal, t.fatalPerm = err, true
		for _, action := range missing {
			t.missing[action] = true
		}
	}
}

func (t *tracker) result() spec.UseResult {
	t.mu.Lock()
	defer t.mu.Unlock()
	missing := make([]string, 0, len(t.missing))
	for action := range t.missing {
		missing = append(missing, action)
	}
	sort.Strings(missing)
	switch {
	case t.fatal != nil && t.fatalPerm:
		return spec.UseResult{State: spec.UseNeedsPermission, Message: t.fatal.Error(), Missing: missing}
	case t.fatal != nil:
		return spec.UseResult{State: spec.UseError, Message: t.fatal.Error()}
	case len(missing) > 0:
		return spec.UseResult{
			State: spec.UseNeedsPermission, Missing: missing,
			Message: "some calls were denied; everything else was scanned",
		}
	case len(t.errors) > 0 && len(t.errors) == t.calls:
		return spec.UseResult{State: spec.UseError, Message: t.errors[0]}
	case len(t.errors) > 0:
		return spec.UseResult{State: spec.UseOK, Message: fmt.Sprintf("%d calls failed; see errors", len(t.errors))}
	default:
		return spec.UseResult{State: spec.UseOK}
	}
}

// failedAny: some call of these actions failed, so what they list is
// incomplete.
func (t *tracker) failedAny(actions ...string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, action := range actions {
		if t.failed[action] {
			return true
		}
	}
	return false
}

// merge adds a sub-scan's calls to t.
func (t *tracker) merge(sub *tracker) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls += sub.calls
	for action := range sub.missing {
		t.missing[action] = true
	}
	for action := range sub.failed {
		t.failed[action] = true
	}
	for _, message := range sub.errors {
		if len(t.errors) < 20 {
			t.errors = append(t.errors, message)
		}
	}
}

func (t *tracker) errorList() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string{}, t.errors...)
}

// inParallel runs work for each item, at most limit at once.
func inParallel[T any](items []T, limit int, work func(T)) {
	slots := make(chan struct{}, limit)
	var group sync.WaitGroup
	for _, item := range items {
		group.Add(1)
		slots <- struct{}{}
		go func() {
			defer group.Done()
			defer func() { <-slots }()
			work(item)
		}()
	}
	group.Wait()
}
