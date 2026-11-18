package system_upgradestatus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	latestReleaseURL = "https://api.github.com/repos/obzly/obzly/releases/latest"
	releasePageURL   = "https://github.com/obzly/obzly/releases/"
	// The request names no user, organization, host or version: only that it's an update check.
	releaseUserAgent = "obzly-update-check"
	releaseTimeout   = 5 * time.Second
	releaseCacheFor  = 6 * time.Hour
	// A failed check is retried after this, not on every page load.
	releaseRetryAfter = 15 * time.Minute
	// Check again can't ask GitHub more often than this.
	releaseRefreshFloor = time.Minute
	maxReleaseBytes     = 1 << 20
)

// Release check states. Unknown and disabled never mean "up to date".
const (
	ReleaseKnown    = "known"
	ReleaseUnknown  = "unknown"
	ReleaseDisabled = "disabled"
)

var releaseTag = regexp.MustCompile(`^v?(\d+\.\d+\.\d+)$`)

// LatestReleaseDTO is the newest published Obzly release, when it could be read.
type LatestReleaseDTO struct {
	Status string `json:"status"`
	// Version without the leading v, e.g. "1.2.0".
	Version     string     `json:"version,omitempty"`
	PublishedAt *time.Time `json:"publishedAt,omitempty"`
	URL         string     `json:"url,omitempty"`
	CheckedAt   *time.Time `json:"checkedAt,omitempty"`
	// Error says why the check failed (unknown only).
	Error string `json:"error,omitempty"`
}

// ReleaseChecker asks GitHub for the latest release, at most every releaseCacheFor.
type ReleaseChecker struct {
	url     string
	client  *http.Client
	enabled func() bool
	now     func() time.Time

	mu        sync.Mutex
	cached    *LatestReleaseDTO
	fetchedAt time.Time
}

// NewReleaseChecker reads releases from url (tests point it at a fake server).
func NewReleaseChecker(url string, enabled func() bool, now func() time.Time) *ReleaseChecker {
	return &ReleaseChecker{url: url, client: &http.Client{Timeout: releaseTimeout}, enabled: enabled, now: now}
}

// Latest is the cached answer, or a fresh one once it's old (refresh: older than a minute).
func (c *ReleaseChecker) Latest(ctx context.Context, refresh bool) LatestReleaseDTO {
	if !c.enabled() {
		return LatestReleaseDTO{Status: ReleaseDisabled}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.cached != nil {
		age, ttl := now.Sub(c.fetchedAt), releaseCacheFor
		if c.cached.Status != ReleaseKnown {
			ttl = releaseRetryAfter
		}
		if age < ttl && (!refresh || age < releaseRefreshFloor) {
			return *c.cached
		}
	}
	result := c.fetch(ctx)
	result.CheckedAt = &now
	c.cached, c.fetchedAt = &result, now
	return result
}

func (c *ReleaseChecker) fetch(ctx context.Context) LatestReleaseDTO {
	ctx, cancel := context.WithTimeout(ctx, releaseTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return unknownRelease("the release check is misconfigured")
	}
	request.Header.Set("User-Agent", releaseUserAgent)
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := c.client.Do(request)
	if err != nil {
		return unknownRelease("GitHub couldn't be reached")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return unknownRelease(fmt.Sprintf("GitHub answered %d", response.StatusCode))
	}
	var body struct {
		TagName     string    `json:"tag_name"`
		HTMLURL     string    `json:"html_url"`
		PublishedAt time.Time `json:"published_at"`
		Draft       bool      `json:"draft"`
		Prerelease  bool      `json:"prerelease"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxReleaseBytes)).Decode(&body); err != nil {
		return unknownRelease("GitHub's answer couldn't be read")
	}
	match := releaseTag.FindStringSubmatch(body.TagName)
	if match == nil || body.Draft || body.Prerelease {
		return unknownRelease("GitHub's latest release has no version number")
	}
	result := LatestReleaseDTO{Status: ReleaseKnown, Version: match[1], URL: releasePageURL + "tag/" + body.TagName}
	// Only ever link to the project's own release pages.
	if strings.HasPrefix(body.HTMLURL, releasePageURL) {
		result.URL = body.HTMLURL
	}
	if !body.PublishedAt.IsZero() {
		published := body.PublishedAt.UTC()
		result.PublishedAt = &published
	}
	return result
}

func unknownRelease(reason string) LatestReleaseDTO {
	return LatestReleaseDTO{Status: ReleaseUnknown, Error: reason}
}
