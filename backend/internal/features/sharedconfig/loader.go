package sharedconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"obzly/internal/config"
	"obzly/internal/storage"
)

const (
	refreshInterval  = 5 * time.Minute
	fetchTimeout     = 10 * time.Second
	maxDocumentBytes = 1 << 20
	maxClockSkew     = 2 * time.Minute
	jwksSuffix       = "/.well-known/jwks.json"
)

// ErrNotPublished: the control plane serves no shared config (one older
// than this feature).
var ErrNotPublished = errors.New("the control plane publishes no shared settings")

// State is what a Site last applied.
type State struct {
	ID        bool      `json:"-"         gorm:"column:id;primaryKey"`
	Version   string    `json:"version"   gorm:"column:version"`
	IssuedAt  time.Time `json:"issuedAt"  gorm:"column:issued_at"`
	FetchedAt time.Time `json:"fetchedAt" gorm:"column:fetched_at"`
}

func (State) TableName() string { return "shared_config_state" }

type document struct {
	TokenUse string `json:"token_use"`
	Version  string `json:"version"`
	Sealed   string `json:"sealed"`
	IssuedAt int64  `json:"iat"`
	Expiry   int64  `json:"exp"`
}

// controlPlaneKeyMaxAge: how long a Site uses the control plane's public
// key before reading it again (and at once after a 401).
const controlPlaneKeyMaxAge = time.Hour

var (
	httpClient = &http.Client{Timeout: fetchTimeout}
	verifiers  sync.Map

	controlPlaneKeyCache struct {
		sync.Mutex
		key string
		at  time.Time
	}
)

// Start keeps an enrolled Site's channels and SMTP in step with its control
// plane, and imports retired ALERT_SMTP_* variables once.
func Start(ctx context.Context, log *slog.Logger) {
	env := config.GetEnv()
	switch {
	case env.ServiceMode.IsControlPlane():
		ImportEnvSMTP(SourceCentral, log)
	case env.SiteExchangeEnabled():
		ImportEnvSMTP(SourceLocal, log)
		go func() {
			ticker := time.NewTicker(refreshInterval)
			defer ticker.Stop()
			for {
				if err := Refresh(ctx, env); err != nil && !errors.Is(err, ErrNotPublished) {
					log.Warn(
						"Failed to refresh notification channels and email settings from the control plane; keeping the last ones",
						"error",
						err,
					)
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	default:
		ImportEnvSMTP(SourceLocal, log)
	}
}

// Refresh fetches, verifies, opens and applies the Site's document if its
// version differs from the one applied. Each request reports the applied
// version, so after applying a new one it asks once more to report it.
func Refresh(ctx context.Context, env config.EnvVariables) error {
	applied := ""
	if state, err := AppliedState(); err != nil {
		return err
	} else if state != nil {
		applied = state.Version
	}
	version, err := fetchAndApply(ctx, env, applied)
	if err != nil || version == applied {
		return err
	}
	_, err = fetchAndApply(ctx, env, version)
	return err
}

// DocumentURL sits next to the control plane's JWKS (CONTROL_PLANE_JWKS_URL
// may be an internal address), else under its issuer.
func DocumentURL(env config.EnvVariables) string {
	base, found := strings.CutSuffix(env.ControlPlaneJWKSURL, jwksSuffix)
	if !found {
		base = env.ControlPlaneIssuer
	}
	return strings.TrimRight(base, "/") + "/api/v1/shared-config/" + url.PathEscape(env.SiteID)
}

// KeyURL is where a Site reads its control plane's public key.
func KeyURL(env config.EnvVariables) string {
	return strings.TrimSuffix(DocumentURL(env), "/"+url.PathEscape(env.SiteID)) + "-key"
}

// AppliedState is the Site's last applied document, nil before the first.
func AppliedState() (*State, error) {
	var state State
	err := storage.GetDb().First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &state, err
}

func fetchAndApply(ctx context.Context, env config.EnvVariables, applied string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, DocumentURL(env), nil)
	if err != nil {
		return "", err
	}
	if err := signRequest(ctx, env, request, applied); err != nil {
		return "", err
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return "", ErrNotPublished
	case http.StatusUnauthorized:
		// The control plane's key may have changed (a restore): read it again.
		forgetControlPlaneKey()
		return "", fmt.Errorf("control plane refused this Site: %s", errorText(response.Body))
	default:
		return "", fmt.Errorf("control plane answered %d: %s", response.StatusCode, errorText(response.Body))
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxDocumentBytes+1))
	if err != nil || len(body) > maxDocumentBytes {
		return "", errors.New("response too large or unreadable")
	}
	doc, err := verify(ctx, env, strings.TrimSpace(string(body)), time.Now().UTC())
	if err != nil {
		return "", err
	}
	return apply(doc, time.Now().UTC())
}

// signRequest proves the request comes from this Site: an HMAC under the
// key its X25519 pair and the control plane's share.
func signRequest(ctx context.Context, env config.EnvVariables, request *http.Request, applied string) error {
	controlPlane, err := cachedControlPlaneKey(ctx, env)
	if err != nil {
		return err
	}
	key, err := requestKey(controlPlane)
	if err != nil {
		return err
	}
	timestamp := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	request.Header.Set(HeaderSiteTime, timestamp)
	request.Header.Set(HeaderAppliedVersion, applied)
	request.Header.Set(HeaderSiteAuth, requestMAC(key, env.SiteID, timestamp, applied))
	return nil
}

func cachedControlPlaneKey(ctx context.Context, env config.EnvVariables) (string, error) {
	controlPlaneKeyCache.Lock()
	defer controlPlaneKeyCache.Unlock()
	if controlPlaneKeyCache.key != "" && time.Since(controlPlaneKeyCache.at) < controlPlaneKeyMaxAge {
		return controlPlaneKeyCache.key, nil
	}
	key, err := controlPlaneKey(ctx, env)
	if err != nil {
		return "", err
	}
	controlPlaneKeyCache.key, controlPlaneKeyCache.at = key, time.Now()
	return key, nil
}

func forgetControlPlaneKey() {
	controlPlaneKeyCache.Lock()
	controlPlaneKeyCache.key = ""
	controlPlaneKeyCache.Unlock()
}

// errorText is the control plane's {"error": ...} message, if short.
func errorText(body io.Reader) string {
	var parsed struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(body, 4096)).Decode(&parsed); err != nil || parsed.Error == "" {
		return "no reason given"
	}
	return parsed.Error
}

func verify(ctx context.Context, env config.EnvVariables, raw string, now time.Time) (*document, error) {
	token, err := verifier(env).Verify(ctx, raw)
	if err != nil {
		return nil, err
	}
	var doc document
	if err := token.Claims(&doc); err != nil {
		return nil, err
	}
	switch {
	case doc.TokenUse != TokenUse:
		return nil, errors.New("not a shared-config document")
	case doc.IssuedAt == 0 || time.Unix(doc.IssuedAt, 0).After(now.Add(maxClockSkew)):
		return nil, errors.New("shared-config document has no valid iat")
	case doc.Expiry-doc.IssuedAt > int64((documentLifetime + maxClockSkew).Seconds()):
		return nil, errors.New("shared-config document lives too long")
	case doc.Version == "" || doc.Sealed == "":
		return nil, errors.New("shared-config document is empty")
	}
	return &doc, nil
}

func verifier(env config.EnvVariables) *oidc.IDTokenVerifier {
	key := env.ControlPlaneIssuer + "|" + env.ControlPlaneJWKSURL + "|" + env.SiteID
	if cached, ok := verifiers.Load(key); ok {
		v, _ := cached.(*oidc.IDTokenVerifier)
		return v
	}
	keySet := oidc.NewRemoteKeySet(context.Background(), env.ControlPlaneJWKSURL)
	v := oidc.NewVerifier(env.ControlPlaneIssuer, keySet, &oidc.Config{
		ClientID:             env.SiteID,
		SupportedSigningAlgs: []string{oidc.ES256},
	})
	actual, _ := verifiers.LoadOrStore(key, v)
	loaded, _ := actual.(*oidc.IDTokenVerifier)
	return loaded
}

// apply stores what the control plane sent: its channels (same ids, kept
// in notification_channels so rules and queued notifications keep pointing
// at them) and its SMTP relay. Channels it no longer sends are removed.
// The applied state is written last, so a failure part-way is retried on
// the next refresh.
func apply(doc *document, now time.Time) (string, error) {
	current, err := AppliedState()
	if err != nil {
		return "", err
	}
	issued := time.Unix(doc.IssuedAt, 0).UTC()
	if current != nil && current.Version == doc.Version {
		return current.Version, storage.GetDb().Model(&State{}).Where("id").Update("fetched_at", now).Error
	}
	if current != nil && issued.Before(current.IssuedAt) {
		return current.Version, nil
	}
	plain, err := Open(doc.Sealed)
	if err != nil {
		return "", err
	}
	var content payload
	if err := json.Unmarshal(plain, &content); err != nil {
		return "", fmt.Errorf("invalid shared-config payload: %w", err)
	}
	err = storage.GetDb().Transaction(func(tx *gorm.DB) error {
		keep := make([]uuid.UUID, 0, len(content.Channels))
		for _, channel := range content.Channels {
			encrypted, err := secrets.Encrypt(string(channel.Config))
			if err != nil {
				return err
			}
			projectIDs, _ := json.Marshal(nonNil(channel.ProjectIDs))
			if err := tx.Exec(
				`INSERT INTO notification_channels
				(id, project_id, name, kind, config_encrypted, source, all_projects, project_ids, created_at, updated_at)
				VALUES (?, NULL, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (id) DO UPDATE SET project_id = NULL, name = EXCLUDED.name, kind = EXCLUDED.kind,
					config_encrypted = EXCLUDED.config_encrypted, source = EXCLUDED.source,
					all_projects = EXCLUDED.all_projects, project_ids = EXCLUDED.project_ids, updated_at = EXCLUDED.updated_at`,
				channel.ID,
				channel.Name,
				channel.Kind,
				encrypted,
				SourceCentral,
				channel.AllProjects,
				string(projectIDs),
				now,
				now,
			).Error; err != nil {
				return err
			}
			keep = append(keep, channel.ID)
		}
		remove := tx.Where("source = ?", SourceCentral)
		if len(keep) > 0 {
			remove = remove.Where("id NOT IN ?", keep)
		}
		return remove.Delete(&channelRow{}).Error
	})
	if err != nil {
		return "", err
	}
	if applyMonitorAlerts != nil {
		if err := applyMonitorAlerts(content.MonitorAlerts); err != nil {
			return "", fmt.Errorf("failed to apply monitor alerts: %w", err)
		}
	}
	if applyMaintenance != nil {
		if err := applyMaintenance(content.Maintenance); err != nil {
			return "", fmt.Errorf("failed to apply maintenance silences: %w", err)
		}
	}
	if content.SMTP != nil {
		err = SaveSMTP(SourceCentral, *content.SMTP)
	} else {
		err = DeleteSMTP(SourceCentral)
	}
	if err != nil {
		return "", err
	}
	return doc.Version, storage.GetDb().Exec(
		`INSERT INTO shared_config_state (id, version, issued_at, fetched_at) VALUES (TRUE, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET version = EXCLUDED.version, issued_at = EXCLUDED.issued_at, fetched_at = EXCLUDED.fetched_at`,
		doc.Version, issued, now).Error
}

// channelRow is notification_channels as far as this package touches it.
type channelRow struct {
	ID uuid.UUID `gorm:"column:id;primaryKey"`
}

func (channelRow) TableName() string { return "notification_channels" }

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
