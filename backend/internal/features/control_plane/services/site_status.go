package control_plane_services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	control_plane_models "obzly/internal/features/control_plane/models"

	"golang.org/x/sync/singleflight"
)

// SiteStatus is the Sites page's traffic light.
type SiteStatus string

const (
	SiteStatusUp       SiteStatus = "up"       // reachable, right Site ID, healthy
	SiteStatusDegraded SiteStatus = "degraded" // answers, but unhealthy, slow or the wrong Site
	SiteStatusDown     SiteStatus = "down"     // unreachable, or an error such as an ingress 503
	SiteStatusDisabled SiteStatus = "disabled"
)

type SiteStatusResult struct {
	SiteID    string     `json:"siteId"`
	Status    SiteStatus `json:"status"`
	Message   string     `json:"message,omitempty"`
	Version   string     `json:"version,omitempty"`
	LatencyMs int64      `json:"latencyMs,omitempty"`
	CheckedAt time.Time  `json:"checkedAt"`
}

const (
	siteStatusTimeout  = 5 * time.Second
	siteStatusSlow     = 2 * time.Second
	siteStatusCacheTTL = 15 * time.Second
)

type cachedSiteStatus struct {
	key     string
	result  SiteStatusResult
	expires time.Time
}

// siteStatusConcurrency caps how many Sites one call checks at once.
const siteStatusConcurrency = 8

// Results are cached briefly so a polling Sites page (or several admins)
// doesn't probe every Site on every request.
var (
	siteStatusCache  sync.Map // site ID -> cachedSiteStatus
	siteStatusFlight singleflight.Group
)

// CheckSiteStatuses checks every Site, a few at a time.
func CheckSiteStatuses(ctx context.Context, sites []*control_plane_models.Site) []SiteStatusResult {
	results := make([]SiteStatusResult, len(sites))
	slots := make(chan struct{}, siteStatusConcurrency)
	var wg sync.WaitGroup
	for i, site := range sites {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer func() { <-slots; wg.Done() }()
			results[i] = cachedSiteStatusFor(ctx, site)
		}()
	}
	wg.Wait()
	return results
}

func cachedSiteStatusFor(ctx context.Context, site *control_plane_models.Site) SiteStatusResult {
	// Keyed on what the check depends on, so editing a Site re-checks it.
	key := fmt.Sprintf("%s|%t", site.ApiURL, site.Enabled)
	if result, ok := freshSiteStatus(site.ID, key); ok {
		return result
	}
	shared, _, _ := siteStatusFlight.Do(site.ID+"|"+key, func() (any, error) {
		if result, ok := freshSiteStatus(site.ID, key); ok {
			return result, nil
		}
		// Not the caller's context: a closed tab must not cache "down" for everyone.
		result := checkSiteStatus(context.WithoutCancel(ctx), site)
		siteStatusCache.Store(
			site.ID,
			cachedSiteStatus{key: key, result: result, expires: time.Now().Add(siteStatusCacheTTL)},
		)
		return result, nil
	})
	result, _ := shared.(SiteStatusResult)
	return result
}

func freshSiteStatus(siteID, key string) (SiteStatusResult, bool) {
	if cached, ok := siteStatusCache.Load(siteID); ok {
		if entry, ok := cached.(cachedSiteStatus); ok && entry.key == key && time.Now().Before(entry.expires) {
			return entry.result, true
		}
	}
	return SiteStatusResult{}, false
}

func checkSiteStatus(ctx context.Context, site *control_plane_models.Site) SiteStatusResult {
	result := SiteStatusResult{SiteID: site.ID, CheckedAt: time.Now().UTC()}
	if !site.Enabled {
		result.Status = SiteStatusDisabled
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, siteStatusTimeout)
	defer cancel()
	if err := checkEgressAllowedContext(ctx, site.ApiURL); err != nil {
		result.Status, result.Message = SiteStatusDown, "site address is not allowed"
		return result
	}
	base := strings.TrimRight(site.ApiURL, "/")

	start := time.Now()
	infoResp, err := siteStatusGet(ctx, base+"/api/v1/site/info")
	result.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		result.Status, result.Message = SiteStatusDown, "site unreachable"
		return result
	}
	defer func() { _ = infoResp.Body.Close() }()
	if infoResp.StatusCode != http.StatusOK {
		result.Status = SiteStatusDown
		result.Message = fmt.Sprintf("site answered HTTP %d - it may be restarting or deploying", infoResp.StatusCode)
		return result
	}
	info, err := DecodeSiteInfo(infoResp.Body)
	if err != nil {
		result.Status, result.Message = SiteStatusDegraded, "could not parse the site's info response"
		return result
	}
	result.Version = info.Version
	if info.SiteID != site.ID {
		result.Status = SiteStatusDegraded
		result.Message = fmt.Sprintf("the address answers as site %q, not %q", info.SiteID, site.ID)
		return result
	}

	// The Site's own health: its database works and disk is below 95%.
	healthResp, err := siteStatusGet(ctx, base+"/api/v1/system/health")
	if err != nil {
		result.Status, result.Message = SiteStatusDegraded, "health check did not respond"
		return result
	}
	defer func() { _ = healthResp.Body.Close() }()
	if healthResp.StatusCode != http.StatusOK {
		result.Status = SiteStatusDegraded
		result.Message = "site reports unhealthy" + healthReason(healthResp.Body)
		return result
	}

	if time.Duration(result.LatencyMs)*time.Millisecond > siteStatusSlow {
		result.Status = SiteStatusDegraded
		result.Message = fmt.Sprintf("slow response (%d ms)", result.LatencyMs)
		return result
	}
	result.Status = SiteStatusUp
	return result
}

func siteStatusGet(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return CheckHTTPClient.Do(req)
}

// healthReason pulls the Site's own status text ({"status": "..."}) if it
// is short JSON; anything else (an HTML page) is left out.
func healthReason(body io.Reader) string {
	var payload struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(body, 4096)).
		Decode(&payload); err != nil || payload.Status == "" ||
		len(payload.Status) > 200 {
		return ""
	}
	return ": " + payload.Status
}
