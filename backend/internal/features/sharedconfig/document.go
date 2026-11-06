package sharedconfig

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	control_plane_models "obzly/internal/features/control_plane/models"
	control_plane_services "obzly/internal/features/control_plane/services"
	"obzly/internal/storage"
	"obzly/internal/util/logger"
)

const (
	// TokenUse marks a shared-config document, so a Site never takes it
	// for a sign-in assertion or a plan.
	TokenUse         = "shared_config"
	documentLifetime = time.Hour
	// siteKeyCheckInterval: the least time between two reads of a Site's
	// key, which unauthenticated requests can trigger.
	siteKeyCheckInterval = time.Minute
	// requestMaxSkew: how far a Site's signed request time may be off.
	requestMaxSkew = 5 * time.Minute

	// The headers a Site signs its document request with.
	HeaderSiteTime       = "X-Obzly-Site-Time"
	HeaderAppliedVersion = "X-Obzly-Applied-Version"
	HeaderSiteAuth       = "X-Obzly-Site-Auth"
)

var (
	ErrUnknownSite = errors.New("unknown site")
	// ErrSiteUnauthenticated: the request isn't signed with the Site's
	// pinned key (an old Site, a forged request, or the Site's key changed).
	ErrSiteUnauthenticated = errors.New("the request isn't signed with this Site's key")
	// ErrSiteKeyUnavailable: no key is pinned yet and the Site's could not
	// be read.
	ErrSiteKeyUnavailable = errors.New("the Site's key could not be read")
	ErrNoPendingKey       = errors.New("this Site has no changed key to accept")
	ErrKeyMismatch        = errors.New("the Site's changed key is not the one shown; reload and check again")

	appliedVersionPattern = regexp.MustCompile(`^[0-9a-f]{1,64}$`)
)

// SiteAuth is what a Site sends with its document request.
type SiteAuth struct {
	Time    string
	Applied string
	MAC     string
}

// SharedChannel is a channel defined on the control plane. Config is the
// alerting.ChannelConfig JSON, encrypted.
type SharedChannel struct {
	ID              uuid.UUID  `json:"id"        gorm:"column:id;primaryKey"`
	Name            string     `json:"name"      gorm:"column:name"`
	Kind            string     `json:"kind"      gorm:"column:kind"`
	ConfigEncrypted string     `json:"-"         gorm:"column:config_encrypted"`
	Scope           Scope      `json:"scope"     gorm:"column:scope"`
	CreatedBy       *uuid.UUID `json:"-"         gorm:"column:created_by"`
	CreatedAt       time.Time  `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt       time.Time  `json:"updatedAt" gorm:"column:updated_at"`
}

func (SharedChannel) TableName() string { return "shared_channels" }

// Scope is where a shared channel can be used: every project of every Site,
// every project of some Sites, and/or some projects.
type Scope struct {
	AllSites bool         `json:"allSites"`
	Sites    []string     `json:"sites,omitempty"`
	Projects []ProjectRef `json:"projects,omitempty"`
}

type ProjectRef struct {
	SiteID    string `json:"siteId"`
	ProjectID string `json:"projectId"`
}

func (s Scope) Value() (driver.Value, error) {
	data, err := json.Marshal(s)
	return string(data), err
}

func (s *Scope) Scan(value any) error {
	switch typed := value.(type) {
	case []byte:
		return json.Unmarshal(typed, s)
	case string:
		return json.Unmarshal([]byte(typed), s)
	}
	return nil
}

// ForSite is the part of the scope that applies on siteID: every project,
// or these projects, or (false, nil) nothing.
func (s Scope) ForSite(siteID string) (allProjects bool, projectIDs []string) {
	if s.AllSites || slices.Contains(s.Sites, siteID) {
		return true, nil
	}
	for _, project := range s.Projects {
		if project.SiteID == siteID {
			projectIDs = append(projectIDs, project.ProjectID)
		}
	}
	return false, projectIDs
}

// payload is what a Site receives, sealed: plaintext configs the Site
// re-encrypts with its own key.
type payload struct {
	Channels []SyncedChannel `json:"channels"`
	SMTP     *SMTP           `json:"smtp,omitempty"`
	// MonitorAlerts are the alert rules of Synthetics monitors whose results
	// go to this Site; it keeps them as managed rules.
	MonitorAlerts []MonitorAlert `json:"monitorAlerts,omitempty"`
	// Maintenance is the next 48 hours of Synthetics maintenance for this
	// Site's monitors; it keeps them as managed silences.
	Maintenance []MaintenanceSilence `json:"maintenance,omitempty"`
}

// MonitorAlert is one managed alert rule a Site keeps for a feature on the
// control plane, identified by Key.
type MonitorAlert struct {
	Key             string      `json:"key"`
	ProjectID       string      `json:"projectId"`
	Name            string      `json:"name"`
	PromQL          string      `json:"promql"`
	Comparison      string      `json:"comparison"`
	Threshold       float64     `json:"threshold"`
	IntervalSeconds int         `json:"intervalSeconds"`
	RepeatSeconds   int         `json:"repeatSeconds"`
	Severity        string      `json:"severity"`
	Summary         string      `json:"summary"`
	ChannelIDs      []uuid.UUID `json:"channelIds"`
	// CheckID becomes the rule's check_id label, which maintenance silences match.
	CheckID string `json:"checkId,omitempty"`
}

// MaintenanceSilence is one occurrence of a maintenance window for the
// monitors of one project: a silence matching their check_id from StartsAt
// to EndsAt, identified by Key.
type MaintenanceSilence struct {
	Key       string    `json:"key"`
	ProjectID string    `json:"projectId"`
	Comment   string    `json:"comment"`
	CheckIDs  []string  `json:"checkIds"`
	StartsAt  time.Time `json:"startsAt"`
	EndsAt    time.Time `json:"endsAt"`
}

var (
	// monitorAlertsFor builds a Site's MonitorAlerts on the control plane.
	monitorAlertsFor func(siteID string) ([]MonitorAlert, error)
	// applyMonitorAlerts stores them on a Site (alerting's managed rules).
	applyMonitorAlerts func(alerts []MonitorAlert) error

	maintenanceFor   func(siteID string) ([]MaintenanceSilence, error)
	applyMaintenance func(silences []MaintenanceSilence) error
)

// SetMonitorAlerts registers where the control plane gets monitor alerts
// and how a Site applies them (main.go; avoids an import cycle).
func SetMonitorAlerts(source func(siteID string) ([]MonitorAlert, error), apply func([]MonitorAlert) error) {
	monitorAlertsFor, applyMonitorAlerts = source, apply
}

// SetMaintenance registers where the control plane gets a Site's
// maintenance and how a Site applies it (main.go; avoids an import cycle).
func SetMaintenance(source func(siteID string) ([]MaintenanceSilence, error), apply func([]MaintenanceSilence) error) {
	maintenanceFor, applyMaintenance = source, apply
}

type SyncedChannel struct {
	ID          uuid.UUID       `json:"id"`
	Name        string          `json:"name"`
	Kind        string          `json:"kind"`
	Config      json.RawMessage `json:"config"`
	AllProjects bool            `json:"allProjects"`
	ProjectIDs  []string        `json:"projectIds"`
}

// SiteSync is how up to date a Site is: what it was last served, what it
// reports it applied (Current compares that with what it would get now),
// and the key its documents are sealed to.
type SiteSync struct {
	SiteID         string     `json:"siteId"`
	Version        string     `json:"version"`
	FetchedAt      *time.Time `json:"fetchedAt"`
	AppliedVersion string     `json:"appliedVersion"`
	AppliedAt      *time.Time `json:"appliedAt"`
	Current        bool       `json:"current"`
	KeyFingerprint string     `json:"keyFingerprint,omitempty"`
	// PendingKeyFingerprint: the Site now serves another key; nothing is
	// sealed to it until an admin accepts it.
	PendingKeyFingerprint string     `json:"pendingKeyFingerprint,omitempty"`
	KeyCheckedAt          *time.Time `json:"keyCheckedAt"`
}

// Document is siteID's signed document: version (a hash of what it holds)
// and the payload sealed to the Site's pinned key. Only the Site can ask for
// it (auth is signed with that key).
func Document(ctx context.Context, siteID string, auth SiteAuth) (string, error) {
	var site control_plane_models.Site
	if err := storage.GetDb().Where("id = ?", siteID).First(&site).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrUnknownSite
		}
		return "", err
	}
	if !site.Enabled {
		return "", ErrUnknownSite
	}
	key, err := pinnedKey(ctx, &site)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	if !authentic(key, siteID, auth, now) {
		recheckKey(ctx, &site)
		return "", ErrSiteUnauthenticated
	}
	plain, version, err := (&payloadSource{}).payloadFor(siteID)
	if err != nil {
		return "", err
	}
	sealed, err := Seal(key, plain)
	if err != nil {
		return "", err
	}
	raw, err := control_plane_services.GetSigningKeyService().SignSiteDocument(siteID, TokenUse, documentLifetime,
		map[string]any{"version": version, "sealed": sealed})
	if err != nil {
		return "", err
	}
	// The Site proved it holds the pinned key, so a different key read from
	// its address since is not its own.
	updates := map[string]any{
		"shared_config_version": version, "shared_config_fetched_at": now, "shared_config_pending_key": "",
	}
	if appliedVersionPattern.MatchString(auth.Applied) {
		updates["shared_config_applied_version"] = auth.Applied
		updates["shared_config_applied_reported_at"] = now
	}
	_ = storage.GetDb().Model(&control_plane_models.Site{}).Where("id = ?", siteID).Updates(updates).Error
	return raw, nil
}

// SyncStatus is each Site's applied version against what it would get now.
func SyncStatus() ([]SiteSync, error) {
	var sites []control_plane_models.Site
	if err := storage.GetDb().Order("id").Find(&sites).Error; err != nil {
		return nil, err
	}
	source := &payloadSource{}
	statuses := make([]SiteSync, 0, len(sites))
	for _, site := range sites {
		_, version, err := source.payloadFor(site.ID)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, SiteSync{
			SiteID: site.ID, Version: site.SharedConfigVersion, FetchedAt: site.SharedConfigFetchedAt,
			AppliedVersion: site.SharedConfigAppliedVersion, AppliedAt: site.SharedConfigAppliedReportedAt,
			Current:               site.SharedConfigAppliedVersion == version,
			KeyFingerprint:        Fingerprint(site.SharedConfigKey),
			PendingKeyFingerprint: Fingerprint(site.SharedConfigPendingKey),
			KeyCheckedAt:          site.SharedConfigKeyCheckedAt,
		})
	}
	return statuses, nil
}

// AcceptKey makes the key siteID now serves its pinned key. fingerprint is
// the one the admin was shown, so a key that changed again since is not
// accepted unseen.
func AcceptKey(siteID, fingerprint string) (previous, accepted string, err error) {
	var site control_plane_models.Site
	if err := storage.GetDb().Where("id = ?", siteID).First(&site).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", "", ErrUnknownSite
		}
		return "", "", err
	}
	if site.SharedConfigPendingKey == "" {
		return "", "", ErrNoPendingKey
	}
	if Fingerprint(site.SharedConfigPendingKey) != fingerprint {
		return "", "", ErrKeyMismatch
	}
	result := storage.GetDb().Model(&control_plane_models.Site{}).
		Where("id = ? AND shared_config_pending_key = ?", siteID, site.SharedConfigPendingKey).
		Updates(map[string]any{"shared_config_key": site.SharedConfigPendingKey, "shared_config_pending_key": ""})
	if result.Error != nil {
		return "", "", result.Error
	}
	if result.RowsAffected == 0 {
		return "", "", ErrKeyMismatch
	}
	return Fingerprint(site.SharedConfigKey), fingerprint, nil
}

// SharedChannels lists the control plane's channels.
func SharedChannels() ([]*SharedChannel, error) {
	var channels []*SharedChannel
	err := storage.GetDb().Order("lower(name)").Find(&channels).Error
	return channels, err
}

// payloadSource reads what every Site's payload is built from once, so a
// status over many Sites decrypts each channel and the relay only once.
type payloadSource struct {
	loaded   bool
	channels []*SharedChannel
	configs  map[uuid.UUID]string
	smtp     *SMTP
}

func (p *payloadSource) load() error {
	if p.loaded {
		return nil
	}
	channels, err := SharedChannels()
	if err != nil {
		return err
	}
	stored, err := StoredSMTP(SourceCentral)
	if err != nil {
		return err
	}
	if stored != nil {
		if p.smtp, err = decryptSMTP(stored); err != nil {
			return err
		}
	}
	p.channels, p.configs, p.loaded = channels, map[uuid.UUID]string{}, true
	return nil
}

func (p *payloadSource) config(channel *SharedChannel) (string, error) {
	if config, ok := p.configs[channel.ID]; ok {
		return config, nil
	}
	config, err := secrets.Decrypt(channel.ConfigEncrypted)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt channel %q: %w", channel.Name, err)
	}
	p.configs[channel.ID] = config
	return config, nil
}

func (p *payloadSource) payloadFor(siteID string) ([]byte, string, error) {
	if err := p.load(); err != nil {
		return nil, "", err
	}
	var err error
	content := payload{Channels: []SyncedChannel{}, SMTP: p.smtp}
	if monitorAlertsFor != nil {
		if content.MonitorAlerts, err = monitorAlertsFor(siteID); err != nil {
			return nil, "", err
		}
	}
	if maintenanceFor != nil {
		if content.Maintenance, err = maintenanceFor(siteID); err != nil {
			return nil, "", err
		}
	}
	// A monitor's channels reach its project even when their scope leaves
	// that Site or project out.
	alertProjects := map[uuid.UUID][]string{}
	for _, alert := range content.MonitorAlerts {
		for _, id := range alert.ChannelIDs {
			if !slices.Contains(alertProjects[id], alert.ProjectID) {
				alertProjects[id] = append(alertProjects[id], alert.ProjectID)
			}
		}
	}
	for _, channel := range p.channels {
		allProjects, projectIDs := channel.Scope.ForSite(siteID)
		if !allProjects {
			for _, projectID := range alertProjects[channel.ID] {
				if !slices.Contains(projectIDs, projectID) {
					projectIDs = append(projectIDs, projectID)
				}
			}
		}
		if !allProjects && len(projectIDs) == 0 {
			continue
		}
		config, err := p.config(channel)
		if err != nil {
			return nil, "", err
		}
		content.Channels = append(content.Channels, SyncedChannel{
			ID: channel.ID, Name: channel.Name, Kind: channel.Kind, Config: json.RawMessage(config),
			AllProjects: allProjects, ProjectIDs: projectIDs,
		})
	}
	plain, err := json.Marshal(content)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(plain)
	return plain, hex.EncodeToString(sum[:12]), nil
}

// pinnedKey is the Site key documents are sealed to. The first one is read
// from the Site's /site/info (trust on first use); after that it only
// changes when an admin accepts a new one (AcceptKey).
func pinnedKey(ctx context.Context, site *control_plane_models.Site) (string, error) {
	if site.SharedConfigKey != "" {
		return site.SharedConfigKey, nil
	}
	if !dueForKeyCheck(site) {
		return "", ErrSiteKeyUnavailable
	}
	key, err := readSiteKey(ctx, site)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrSiteKeyUnavailable, err)
	}
	if err := storage.GetDb().Model(&control_plane_models.Site{}).
		Where("id = ? AND shared_config_key = ''", site.ID).
		Update("shared_config_key", key).Error; err != nil {
		return "", err
	}
	var stored control_plane_models.Site
	if err := storage.GetDb().Select("shared_config_key").Where("id = ?", site.ID).First(&stored).Error; err != nil {
		return "", err
	}
	site.SharedConfigKey = stored.SharedConfigKey
	return stored.SharedConfigKey, nil
}

// recheckKey reads the Site's key after a request failed to authenticate,
// at most once per siteKeyCheckInterval: a different key (the Site was
// restored or rebuilt, or something else answers at its address) is kept
// as pending for an admin to accept, never sealed to.
func recheckKey(ctx context.Context, site *control_plane_models.Site) {
	if !dueForKeyCheck(site) {
		return
	}
	key, err := readSiteKey(ctx, site)
	if err != nil || key == site.SharedConfigKey || key == site.SharedConfigPendingKey {
		return
	}
	if err := storage.GetDb().Model(&control_plane_models.Site{}).Where("id = ?", site.ID).
		Update("shared_config_pending_key", key).Error; err == nil {
		logger.GetLogger().Warn("A Site serves a different shared-config key; an admin must accept it",
			"site", site.ID, "pinned", Fingerprint(site.SharedConfigKey), "new", Fingerprint(key))
	}
}

func dueForKeyCheck(site *control_plane_models.Site) bool {
	checked := site.SharedConfigKeyCheckedAt
	return checked == nil || time.Since(*checked) >= siteKeyCheckInterval
}

// readSiteKey reads the Site's public key from its /site/info through the
// control plane's hardened client (never from the request asking for the
// document) and records when it did.
func readSiteKey(ctx context.Context, site *control_plane_models.Site) (string, error) {
	now := time.Now().UTC()
	site.SharedConfigKeyCheckedAt = &now
	if err := storage.GetDb().Model(&control_plane_models.Site{}).Where("id = ?", site.ID).
		Update("shared_config_key_checked_at", now).Error; err != nil {
		return "", err
	}
	if err := control_plane_services.CheckEgressAllowed(site.ApiURL); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(site.ApiURL, "/")+"/api/v1/site/info", nil)
	if err != nil {
		return "", err
	}
	response, err := control_plane_services.CheckHTTPClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("failed to read the Site's key: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	info, err := control_plane_services.DecodeSiteInfo(response.Body)
	if err != nil || info.SiteID != site.ID {
		return "", errors.New("the address does not answer as this Site")
	}
	if raw, err := base64.StdEncoding.DecodeString(info.SharedConfigKey); err != nil || len(raw) != 32 {
		return "", errors.New("the Site publishes no shared-config key (it is older than this control plane)")
	}
	return info.SharedConfigKey, nil
}

// authentic checks the Site signed this request with the key pinned for it.
func authentic(siteKey, siteID string, auth SiteAuth, now time.Time) bool {
	seconds, err := strconv.ParseInt(auth.Time, 10, 64)
	if err != nil || auth.MAC == "" {
		return false
	}
	if skew := now.Sub(time.Unix(seconds, 0)); skew > requestMaxSkew || skew < -requestMaxSkew {
		return false
	}
	key, err := requestKey(siteKey)
	if err != nil {
		return false
	}
	return hmac.Equal([]byte(requestMAC(key, siteID, auth.Time, auth.Applied)), []byte(auth.MAC))
}
