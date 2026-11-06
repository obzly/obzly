package sharedconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"obzly/internal/config"
	control_plane_models "obzly/internal/features/control_plane/models"
	"obzly/internal/storage"
)

// AdoptResult is what "Move to central" took over from a Site.
type AdoptResult struct {
	Channels []string `json:"channels"`
	// SMTP: the Site's relay became the central one (there was none yet).
	SMTP bool `json:"smtp"`
	// SMTPSkipped: the Site had a relay but the control plane already has one.
	SMTPSkipped bool `json:"smtpSkipped"`
}

// ErrChannelIDTaken: an exported channel has the id of a central channel
// used beyond that Site.
var ErrChannelIDTaken = errors.New("a channel can't be moved")

// localChannel is a Site's own channel row.
type localChannel struct {
	ID              uuid.UUID `gorm:"column:id;primaryKey"`
	Name            string    `gorm:"column:name"`
	Kind            string    `gorm:"column:kind"`
	ConfigEncrypted string    `gorm:"column:config_encrypted"`
	AllProjects     bool      `gorm:"column:all_projects"`
	ProjectIDs      string    `gorm:"column:project_ids"`
}

func (localChannel) TableName() string { return "notification_channels" }

// ExportLocal seals this Site's own channels and SMTP relay to its control
// plane's key, for "Move to central": whoever carries it (the admin's
// browser) can't read the secrets in it.
func ExportLocal(ctx context.Context) (string, error) {
	env := config.GetEnv()
	if !env.SiteExchangeEnabled() {
		return "", errors.New("this Site isn't enrolled in a control plane")
	}
	key, err := controlPlaneKey(ctx, env)
	if err != nil {
		return "", err
	}
	var rows []localChannel
	if err := storage.GetDb().Where("source = ?", SourceLocal).Find(&rows).Error; err != nil {
		return "", err
	}
	content := payload{Channels: []SyncedChannel{}}
	for _, row := range rows {
		plain, err := secrets.Decrypt(row.ConfigEncrypted)
		if err != nil {
			return "", fmt.Errorf("failed to decrypt channel %q: %w", row.Name, err)
		}
		var projectIDs []string
		_ = json.Unmarshal([]byte(row.ProjectIDs), &projectIDs)
		content.Channels = append(content.Channels, SyncedChannel{
			ID: row.ID, Name: row.Name, Kind: row.Kind, Config: json.RawMessage(plain),
			AllProjects: row.AllProjects, ProjectIDs: projectIDs,
		})
	}
	if stored, err := StoredSMTP(SourceLocal); err != nil {
		return "", err
	} else if stored != nil {
		if content.SMTP, err = decryptSMTP(stored); err != nil {
			return "", err
		}
	}
	plain, err := json.Marshal(content)
	if err != nil {
		return "", err
	}
	return Seal(key, plain)
}

// Adopt stores a Site's exported channels as central ones under the same
// ids, scoped to that Site, and its relay if the control plane has none.
// The Site replaces its own copies on its next refresh.
func Adopt(siteID, sealed string, createdBy uuid.UUID) (*AdoptResult, error) {
	var sites int64
	if err := storage.GetDb().Model(&control_plane_models.Site{}).Where("id = ?", siteID).
		Count(&sites).Error; err != nil {
		return nil, err
	}
	if sites == 0 {
		return nil, ErrUnknownSite
	}
	plain, err := Open(sealed)
	if err != nil {
		return nil, err
	}
	var content payload
	if err := json.Unmarshal(plain, &content); err != nil {
		return nil, fmt.Errorf("invalid export: %w", err)
	}
	result := &AdoptResult{Channels: []string{}}
	now := time.Now().UTC()
	err = storage.GetDb().Transaction(func(tx *gorm.DB) error {
		for _, channel := range content.Channels {
			encrypted, err := secrets.Encrypt(string(channel.Config))
			if err != nil {
				return err
			}
			var existing SharedChannel
			found := tx.Where("id = ?", channel.ID).First(&existing).Error == nil
			// Only a channel this Site moved here before may be replaced: an
			// export naming another central channel's id must not take it over.
			if found && !onlyForSite(existing.Scope, siteID) {
				return fmt.Errorf("%w: %q has the id of central channel %q, which reaches other Sites",
					ErrChannelIDTaken, channel.Name, existing.Name)
			}
			if found {
				// Monitors pick channels regardless of scope, so a channel
				// scoped to this Site can still alert for another one.
				if elsewhere, err := usedByMonitorsOffSite(tx, channel.ID, siteID); err != nil {
					return err
				} else if elsewhere {
					return fmt.Errorf("%w: %q has the id of central channel %q, which monitors on other Sites use",
						ErrChannelIDTaken, channel.Name, existing.Name)
				}
			}
			scope := existing.Scope
			if !found {
				scope = Scope{}
			}
			if channel.AllProjects {
				if !slices.Contains(scope.Sites, siteID) {
					scope.Sites = append(scope.Sites, siteID)
				}
			} else {
				for _, projectID := range channel.ProjectIDs {
					ref := ProjectRef{SiteID: siteID, ProjectID: projectID}
					if !slices.Contains(scope.Projects, ref) {
						scope.Projects = append(scope.Projects, ref)
					}
				}
			}
			name, err := freeChannelName(tx, channel.Name, channel.ID, siteID)
			if err != nil {
				return err
			}
			adopted := SharedChannel{
				ID: channel.ID, Name: name, Kind: channel.Kind, ConfigEncrypted: encrypted, Scope: scope,
				CreatedBy: &createdBy, CreatedAt: now, UpdatedAt: now,
			}
			if found {
				adopted.CreatedAt, adopted.CreatedBy = existing.CreatedAt, existing.CreatedBy
			}
			if err := tx.Save(&adopted).Error; err != nil {
				return err
			}
			result.Channels = append(result.Channels, name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if content.SMTP != nil {
		central, err := StoredSMTP(SourceCentral)
		if err != nil {
			return nil, err
		}
		if central == nil {
			if err := SaveSMTP(SourceCentral, *content.SMTP); err != nil {
				return nil, err
			}
			result.SMTP = true
		} else {
			result.SMTPSkipped = true
		}
	}
	return result, nil
}

// usedByMonitorsOffSite: a synthetic monitor that sends to a destination
// on another Site alerts through the channel.
func usedByMonitorsOffSite(tx *gorm.DB, channelID uuid.UUID, siteID string) (bool, error) {
	var count int64
	err := tx.Table("synthetic_checks AS c").
		Joins("JOIN synthetic_destinations AS d ON d.id = c.destination_id").
		Where("c.alerts -> 'channelIds' @> ?::jsonb", fmt.Sprintf("[%q]", channelID.String())).
		Where("d.site_id <> ?", siteID).
		Count(&count).Error
	return count > 0, err
}

// onlyForSite is a scope that reaches siteID and no other Site.
func onlyForSite(scope Scope, siteID string) bool {
	if scope.AllSites || len(scope.Sites)+len(scope.Projects) == 0 {
		return false
	}
	for _, site := range scope.Sites {
		if site != siteID {
			return false
		}
	}
	for _, project := range scope.Projects {
		if project.SiteID != siteID {
			return false
		}
	}
	return true
}

// freeChannelName keeps name unless another central channel has it; then
// it is suffixed with the Site.
func freeChannelName(tx *gorm.DB, name string, id uuid.UUID, siteID string) (string, error) {
	var count int64
	if err := tx.Model(&SharedChannel{}).Where("lower(name) = ? AND id <> ?", strings.ToLower(name), id).
		Count(&count).Error; err != nil {
		return "", err
	}
	if count == 0 {
		return name, nil
	}
	return fmt.Sprintf("%s (%s)", name, siteID), nil
}

func controlPlaneKey(ctx context.Context, env config.EnvVariables) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, KeyURL(env), nil)
	if err != nil {
		return "", err
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("failed to reach the control plane: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound {
		return "", ErrNotPublished
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the control plane answered %d for its key", response.StatusCode)
	}
	var body struct {
		PublicKey string `json:"publicKey"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&body); err != nil || body.PublicKey == "" {
		return "", errors.New("the control plane published no key")
	}
	return body.PublicKey, nil
}
