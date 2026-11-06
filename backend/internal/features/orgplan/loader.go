package orgplan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"obzly/internal/config"
	"obzly/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	// DefaultOrgSlug is the organization whose plan a standalone install's
	// own projects follow.
	DefaultOrgSlug = "default"

	siteRefreshInterval  = 5 * time.Minute
	localRefreshInterval = time.Minute
	staleAfter           = time.Hour
	fetchTimeout         = 10 * time.Second
	maxDocumentBytes     = 64 << 10
	jwksSuffix           = "/.well-known/jwks.json"
)

// sitePlanRow is a Site's copy of its last verified plan document.
type sitePlanRow struct {
	ID        bool       `gorm:"column:id;primaryKey"`
	OrgID     *uuid.UUID `gorm:"column:org_id"`
	Limits    string     `gorm:"column:limits"`
	IssuedAt  time.Time  `gorm:"column:issued_at"`
	FetchedAt time.Time  `gorm:"column:fetched_at"`
}

var (
	// state is the newest document this process accepted, and when.
	stateMu      sync.Mutex
	lastIssued   time.Time
	lastFetched  time.Time
	lastStaleLog time.Time
	startedAt    = time.Now().UTC()
	notPublished sync.Once

	// ErrNotPublished: the control plane serves no plan for this Site (one
	// older than plans, or not knowing it).
	ErrNotPublished = errors.New("the control plane publishes no plan for this Site")

	httpClient = &http.Client{Timeout: fetchTimeout}
)

func (sitePlanRow) TableName() string { return "site_org_plan" }

// Start keeps Current up to date: on a Site enrolled in a control plane
// from the plan document it publishes (cached in site_org_plan, so the
// plan holds through restarts and outages), on a standalone install from
// the Default organization's plan. A control plane has no projects.
func Start(ctx context.Context, log *slog.Logger) {
	env := config.GetEnv()
	switch {
	case env.ServiceMode.IsControlPlane():
		return
	case env.SiteExchangeEnabled():
		if err := loadCached(); err != nil {
			log.Warn("Failed to load the cached organization plan", "error", err)
		}
		go every(ctx, siteRefreshInterval, func() {
			if err := RefreshFromControlPlane(ctx, env, time.Now().UTC()); err != nil {
				logStale(log, err)
			}
		})
	default:
		go every(ctx, localRefreshInterval, func() {
			if err := RefreshLocal(); err != nil {
				log.Warn("Failed to load the organization plan", "error", err)
			}
		})
	}
}

// RefreshLocal loads the Default organization's plan (standalone).
func RefreshLocal() error {
	var plans []OrgPlan
	err := storage.GetDb().Table("org_plans p").Select("p.*").
		Joins("JOIN organizations o ON o.id = p.org_id").
		Where("o.slug = ?", DefaultOrgSlug).Scan(&plans).Error
	if err != nil {
		return err
	}
	if len(plans) == 0 {
		Set(Limits{})
		return nil
	}
	Set(plans[0].Limits)
	return nil
}

// RefreshFromControlPlane fetches, verifies and applies this Site's plan
// document if it is newer than the one held.
func RefreshFromControlPlane(ctx context.Context, env config.EnvVariables, now time.Time) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, DocumentURL(env), nil)
	if err != nil {
		return err
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("failed to fetch the organization plan: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound {
		// A control plane without this endpoint, or not knowing this Site.
		return ErrNotPublished
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("organization plan: control plane answered %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxDocumentBytes+1))
	if err != nil || len(body) > maxDocumentBytes {
		return errors.New("organization plan: response too large or unreadable")
	}
	doc, err := VerifyDocument(ctx, env, strings.TrimSpace(string(body)), now)
	if err != nil {
		return fmt.Errorf("organization plan: %w", err)
	}
	return apply(doc, now)
}

// DocumentURL is where a Site fetches its plan: next to the control
// plane's JWKS (CONTROL_PLANE_JWKS_URL may point at an internal address),
// else under its issuer.
func DocumentURL(env config.EnvVariables) string {
	base, found := strings.CutSuffix(env.ControlPlaneJWKSURL, jwksSuffix)
	if !found {
		base = env.ControlPlaneIssuer
	}
	return strings.TrimRight(base, "/") + "/api/v1/site-plans/" + url.PathEscape(env.SiteID)
}

func apply(doc *Document, now time.Time) error {
	issued := time.Unix(doc.IssuedAt, 0).UTC()
	stateMu.Lock()
	defer stateMu.Unlock()
	lastFetched = now
	if !issued.After(lastIssued) {
		return nil
	}
	limits, err := json.Marshal(doc.Limits)
	if err != nil {
		return err
	}
	var orgID *uuid.UUID
	if parsed, err := uuid.Parse(doc.OrgID); err == nil {
		orgID = &parsed
	}
	// Replicas race to store; an older document never replaces a newer one.
	if err := storage.GetDb().Exec(`INSERT INTO site_org_plan (id, org_id, limits, issued_at, fetched_at)
		VALUES (TRUE, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET org_id = EXCLUDED.org_id, limits = EXCLUDED.limits,
			issued_at = EXCLUDED.issued_at, fetched_at = EXCLUDED.fetched_at
		WHERE site_org_plan.issued_at < EXCLUDED.issued_at`,
		orgID, string(limits), issued, now).Error; err != nil {
		return err
	}
	lastIssued = issued
	Set(*doc.Limits)
	return nil
}

func loadCached() error {
	var row sitePlanRow
	err := storage.GetDb().First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var limits Limits
	if err := json.Unmarshal([]byte(row.Limits), &limits); err != nil {
		return err
	}
	stateMu.Lock()
	lastIssued, lastFetched = row.IssuedAt, row.FetchedAt
	stateMu.Unlock()
	Set(limits)
	return nil
}

// logStale reports failed refreshes once they have lasted an hour (since
// the last success, or since start), then hourly; the held plan stays in
// force. A control plane without plans is noted once.
func logStale(log *slog.Logger, err error) {
	if errors.Is(err, ErrNotPublished) {
		notPublished.Do(func() { log.Info("The control plane publishes no organization plan for this Site") })
		return
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	now := time.Now().UTC()
	since := lastFetched
	if startedAt.After(since) {
		since = startedAt
	}
	if now.Sub(since) < staleAfter || now.Sub(lastStaleLog) < staleAfter {
		return
	}
	lastStaleLog = now
	log.Warn("The organization plan hasn't been refreshed for over an hour; keeping the last one",
		"lastFetched", lastFetched, "error", err)
}

func every(ctx context.Context, interval time.Duration, run func()) {
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
