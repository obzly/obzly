package alerting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"obzly/internal/config"
	"obzly/internal/features/sharedconfig"
	users_models "obzly/internal/features/users/models"
	users_repositories "obzly/internal/features/users/repositories"
	"obzly/internal/storage"
)

// ErrManagedCentrally: on a Site enrolled in a control plane, channels and
// email are managed there.
var ErrManagedCentrally = errors.New("this Site's notification channels are managed on its control plane")

// SettingsChannel is a channel as Obzly settings show it. On a control
// plane Scope says where it can be used; on a Site AllProjects/ProjectIDs.
type SettingsChannel struct {
	ID          uuid.UUID           `json:"id"`
	Name        string              `json:"name"`
	Kind        string              `json:"kind"`
	Target      string              `json:"target"`
	Source      string              `json:"source"`
	Scope       *sharedconfig.Scope `json:"scope,omitempty"`
	AllProjects bool                `json:"allProjects"`
	ProjectIDs  []string            `json:"projectIds"`
	UsedBy      int                 `json:"usedBy"`
	UpdatedAt   time.Time           `json:"updatedAt"`
}

// SettingsChannelRequest creates or updates a channel; on update a nil
// Config keeps the stored one.
type SettingsChannelRequest struct {
	Name        string              `json:"name"`
	Kind        string              `json:"kind"`
	Config      *ChannelConfig      `json:"config"`
	Scope       *sharedconfig.Scope `json:"scope"`
	AllProjects bool                `json:"allProjects"`
	ProjectIDs  []string            `json:"projectIds"`
}

// SettingsService manages channels in Obzly settings: the central ones on a
// control plane, a standalone Site's own on a Site.
type SettingsService struct {
	repo    *repository
	sender  *sender
	audit   auditWriter
	secrets *users_repositories.SecretKeyRepository
}

type auditWriter interface {
	WriteAuditLog(message string, userID *uuid.UUID, projectID *uuid.UUID)
}

func (s *SettingsService) List(user *users_models.User) ([]SettingsChannel, error) {
	if !user.CanUpdateSettings() {
		return nil, ErrForbidden
	}
	if config.GetEnv().ServiceMode.IsControlPlane() {
		channels, err := sharedconfig.SharedChannels()
		if err != nil {
			return nil, err
		}
		views := make([]SettingsChannel, 0, len(channels))
		for _, channel := range channels {
			scope := channel.Scope
			views = append(views, SettingsChannel{
				ID:         channel.ID,
				Name:       channel.Name,
				Kind:       channel.Kind,
				Target:     s.describeEncrypted(channel.Kind, channel.ConfigEncrypted),
				Source:     sharedconfig.SourceCentral,
				Scope:      &scope,
				ProjectIDs: []string{},
				UpdatedAt:  channel.UpdatedAt,
			})
		}
		return views, nil
	}
	channels, err := s.repo.allChannels()
	if err != nil {
		return nil, err
	}
	counts, err := s.repo.ruleCounts()
	if err != nil {
		return nil, err
	}
	views := make([]SettingsChannel, 0, len(channels))
	for _, channel := range channels {
		projectIDs := []string(channel.ProjectIDs)
		if projectIDs == nil {
			projectIDs = []string{}
		}
		views = append(views, SettingsChannel{
			ID:          channel.ID,
			Name:        channel.Name,
			Kind:        channel.Kind,
			Target:      s.describeEncrypted(channel.Kind, channel.ConfigEncrypted),
			Source:      channel.Source,
			AllProjects: channel.AllProjects,
			ProjectIDs:  projectIDs,
			UsedBy:      counts[channel.ID],
			UpdatedAt:   channel.UpdatedAt,
		})
	}
	return views, nil
}

func (s *SettingsService) Create(user *users_models.User, request SettingsChannelRequest) (*SettingsChannel, error) {
	if err := s.canWrite(user); err != nil {
		return nil, err
	}
	if request.Config == nil {
		return nil, errors.New("config is required")
	}
	return s.save(user, uuid.New(), request, true)
}

func (s *SettingsService) Update(
	user *users_models.User,
	id uuid.UUID,
	request SettingsChannelRequest,
) (*SettingsChannel, error) {
	if err := s.canWrite(user); err != nil {
		return nil, err
	}
	return s.save(user, id, request, false)
}

func (s *SettingsService) Delete(user *users_models.User, id uuid.UUID) error {
	if err := s.canWrite(user); err != nil {
		return err
	}
	var deleted bool
	var err error
	if config.GetEnv().ServiceMode.IsControlPlane() {
		result := storage.GetDb().Where("id = ?", id).Delete(&sharedconfig.SharedChannel{})
		deleted, err = result.RowsAffected > 0, result.Error
	} else {
		deleted, err = s.repo.deleteChannel(id)
	}
	if err != nil {
		return err
	}
	if !deleted {
		return ErrNotFound
	}
	s.audit.WriteAuditLog("Notification channel deleted: "+id.String(), &user.ID, nil)
	return nil
}

// Test sends a test notification through a stored channel now.
func (s *SettingsService) Test(ctx context.Context, user *users_models.User, id uuid.UUID) error {
	if !user.CanUpdateSettings() {
		return ErrForbidden
	}
	kind, encrypted, name, err := s.stored(id)
	if err != nil {
		return err
	}
	cfg, err := decryptConfig(s.secrets, encrypted)
	if err != nil {
		return err
	}
	return s.sender.send(ctx, kind, cfg, Notification{
		Status: StatusTest, RuleName: "Test notification from Obzly",
		Summary: "If you can read this, the channel " + name + " works.",
		Labels:  Labels{"channel": name}, StartsAt: time.Now().UTC(),
	})
}

func (s *SettingsService) save(
	user *users_models.User,
	id uuid.UUID,
	request SettingsChannelRequest,
	create bool,
) (*SettingsChannel, error) {
	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" || len(request.Name) > 200 {
		return nil, errors.New("a name of up to 200 characters is required")
	}
	kind, encrypted := request.Kind, ""
	if !create {
		storedKind, storedConfig, _, err := s.stored(id)
		if err != nil {
			return nil, err
		}
		if request.Config == nil {
			if request.Kind != "" && request.Kind != storedKind {
				return nil, errors.New("changing the kind needs a new config")
			}
			kind, encrypted = storedKind, storedConfig
		}
	}
	if request.Config != nil {
		if err := validateConfig(request.Kind, *request.Config); err != nil {
			return nil, err
		}
		plain, err := json.Marshal(request.Config)
		if err != nil {
			return nil, err
		}
		if encrypted, err = s.secrets.Encrypt(string(plain)); err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	if config.GetEnv().ServiceMode.IsControlPlane() {
		scope := sharedconfig.Scope{AllSites: true}
		if request.Scope != nil {
			scope = *request.Scope
		}
		if !scope.AllSites && len(scope.Sites) == 0 && len(scope.Projects) == 0 {
			return nil, errors.New("choose where the channel can be used: all Sites, some Sites or some projects")
		}
		channel := sharedconfig.SharedChannel{
			ID: id, Name: request.Name, Kind: kind, ConfigEncrypted: encrypted, Scope: scope,
			CreatedBy: &user.ID, CreatedAt: now, UpdatedAt: now,
		}
		if !create {
			var existing sharedconfig.SharedChannel
			if err := storage.GetDb().Where("id = ?", id).First(&existing).Error; err != nil {
				return nil, notFound(err)
			}
			channel.CreatedAt, channel.CreatedBy = existing.CreatedAt, existing.CreatedBy
		}
		if err := storage.GetDb().Save(&channel).Error; err != nil {
			return nil, duplicateName(err, request.Name)
		}
	} else {
		if !request.AllProjects && len(request.ProjectIDs) == 0 {
			return nil, errors.New("choose where the channel can be used: every project or some projects")
		}
		channel := &NotificationChannel{
			ID:              id,
			Name:            request.Name,
			Kind:            kind,
			ConfigEncrypted: encrypted,
			Source:          sharedconfig.SourceLocal,
			AllProjects:     request.AllProjects,
			ProjectIDs:      request.ProjectIDs,
			CreatedBy:       &user.ID,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		if !create {
			existing, err := s.repo.channelByID(id)
			if err != nil {
				return nil, notFound(err)
			}
			channel.CreatedAt, channel.CreatedBy, channel.ProjectID = existing.CreatedAt, existing.CreatedBy, existing.ProjectID
		}
		if err := s.repo.saveChannel(channel); err != nil {
			return nil, err
		}
	}
	action := "updated"
	if create {
		action = "created"
	}
	s.audit.WriteAuditLog(fmt.Sprintf("Notification channel %s: %s", action, request.Name), &user.ID, nil)
	views, err := s.List(user)
	if err != nil {
		return nil, err
	}
	for i := range views {
		if views[i].ID == id {
			return &views[i], nil
		}
	}
	return nil, ErrNotFound
}

// stored is a channel's kind, encrypted config and name, from the central
// table on a control plane, else the Site's own.
func (s *SettingsService) stored(id uuid.UUID) (kind, encrypted, name string, err error) {
	if config.GetEnv().ServiceMode.IsControlPlane() {
		var channel sharedconfig.SharedChannel
		if err := storage.GetDb().Where("id = ?", id).First(&channel).Error; err != nil {
			return "", "", "", notFound(err)
		}
		return channel.Kind, channel.ConfigEncrypted, channel.Name, nil
	}
	channel, err := s.repo.channelByID(id)
	if err != nil {
		return "", "", "", notFound(err)
	}
	return channel.Kind, channel.ConfigEncrypted, channel.Name, nil
}

func (s *SettingsService) canWrite(user *users_models.User) error {
	if !user.CanUpdateSettings() {
		return ErrForbidden
	}
	if config.GetEnv().SiteExchangeEnabled() {
		return ErrManagedCentrally
	}
	return nil
}

func (s *SettingsService) describeEncrypted(kind, encrypted string) string {
	cfg, err := decryptConfig(s.secrets, encrypted)
	if err != nil {
		return ""
	}
	return describe(kind, cfg)
}

func decryptConfig(secrets *users_repositories.SecretKeyRepository, encrypted string) (ChannelConfig, error) {
	var cfg ChannelConfig
	plain, err := secrets.Decrypt(encrypted)
	if err != nil {
		return cfg, err
	}
	return cfg, json.Unmarshal([]byte(plain), &cfg)
}

func duplicateName(err error, name string) error {
	if strings.Contains(err.Error(), "duplicate key") {
		return fmt.Errorf("a channel named %q already exists", name)
	}
	return err
}
