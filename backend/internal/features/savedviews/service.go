// Package savedviews keeps named Cost Explorer and Posture Trends views per user: private, or
// shared read-only with everyone (docs/CLOUD_COST_POSTURE.md "Views").
package savedviews

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	users_models "obzly/internal/features/users/models"
	"obzly/internal/storage"
)

const (
	KindCost    = "cost"
	KindPosture = "posture"

	maxStateBytes   = 16 << 10
	maxNameLength   = 60
	maxViewsPerUser = 100
)

var (
	ErrNotFound  = errors.New("view not found")
	ErrForbidden = errors.New("only its owner can change a view; save a copy instead")
)

type savedView struct {
	ID        uuid.UUID       `gorm:"column:id;primaryKey"`
	OwnerID   uuid.UUID       `gorm:"column:owner_id"`
	Kind      string          `gorm:"column:kind"`
	Name      string          `gorm:"column:name"`
	Shared    bool            `gorm:"column:shared"`
	State     json.RawMessage `gorm:"column:state;type:jsonb"`
	CreatedAt time.Time       `gorm:"column:created_at"`
	UpdatedAt time.Time       `gorm:"column:updated_at"`
}

func (savedView) TableName() string { return "saved_views" }

type defaultView struct {
	UserID uuid.UUID `gorm:"column:user_id;primaryKey"`
	Kind   string    `gorm:"column:kind;primaryKey"`
	ViewID uuid.UUID `gorm:"column:view_id"`
}

func (defaultView) TableName() string { return "user_default_views" }

// ViewDTO is a view as its reader sees it; Mine says whether they may change it.
type ViewDTO struct {
	ID        uuid.UUID       `json:"id"`
	Kind      string          `json:"kind"`
	Name      string          `json:"name"`
	Shared    bool            `json:"shared"`
	State     json.RawMessage `json:"state"     swaggertype:"object"`
	Owner     string          `json:"owner"`
	Mine      bool            `json:"mine"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

// ViewList is every view a user can open, theirs first, and their default.
type ViewList struct {
	Views     []ViewDTO  `json:"views"`
	DefaultID *uuid.UUID `json:"defaultId"`
}

type ViewRequest struct {
	Kind   string          `json:"kind"`
	Name   string          `json:"name"`
	Shared bool            `json:"shared"`
	State  json.RawMessage `json:"state"  swaggertype:"object"`
}

type DefaultRequest struct {
	Kind string `json:"kind"`
	// ViewID nil clears the default.
	ViewID *uuid.UUID `json:"viewId"`
}

type Service struct {
	now func() time.Time
}

func (s *Service) List(user *users_models.User, kind string) (*ViewList, error) {
	if err := checkKind(kind); err != nil {
		return nil, err
	}
	var rows []struct {
		ID         uuid.UUID
		OwnerID    uuid.UUID
		Kind       string
		Name       string
		Shared     bool
		State      string
		UpdatedAt  time.Time
		OwnerEmail string
		OwnerName  string
	}
	err := storage.GetDb().Table("saved_views v").
		Select("v.*, u.email AS owner_email, u.name AS owner_name").
		Joins("JOIN users u ON u.id = v.owner_id").
		Where("v.kind = ? AND (v.owner_id = ? OR v.shared)", kind, user.ID).
		Order(clause.Expr{SQL: "v.owner_id = ? DESC, lower(v.name)", Vars: []any{user.ID}}).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	list := &ViewList{Views: make([]ViewDTO, 0, len(rows))}
	for _, row := range rows {
		owner := row.OwnerName
		if owner == "" {
			owner = row.OwnerEmail
		}
		view := savedView{ID: row.ID, OwnerID: row.OwnerID, Kind: row.Kind, Name: row.Name, Shared: row.Shared,
			State: json.RawMessage(row.State), UpdatedAt: row.UpdatedAt}
		list.Views = append(list.Views, dto(view, owner, row.OwnerID == user.ID))
	}
	var defaults []defaultView
	if err := storage.GetDb().Where("user_id = ? AND kind = ?", user.ID, kind).Find(&defaults).Error; err != nil {
		return nil, err
	}
	if len(defaults) == 1 {
		list.DefaultID = &defaults[0].ViewID
	}
	return list, nil
}

func (s *Service) Create(user *users_models.User, request ViewRequest) (*ViewDTO, error) {
	view, err := viewFrom(request)
	if err != nil {
		return nil, err
	}
	var count int64
	if err := storage.GetDb().Model(&savedView{}).Where("owner_id = ?", user.ID).Count(&count).Error; err != nil {
		return nil, err
	}
	if count >= maxViewsPerUser {
		return nil, fmt.Errorf("you can keep at most %d views", maxViewsPerUser)
	}
	view.ID, view.OwnerID, view.CreatedAt, view.UpdatedAt = uuid.New(), user.ID, s.now(), s.now()
	if err := storage.GetDb().Create(view).Error; err != nil {
		return nil, duplicate(err, view.Name)
	}
	result := dto(*view, ownerName(user), true)
	return &result, nil
}

func (s *Service) Update(user *users_models.User, id uuid.UUID, request ViewRequest) (*ViewDTO, error) {
	existing, err := s.ownView(user, id)
	if err != nil {
		return nil, err
	}
	request.Kind = existing.Kind
	view, err := viewFrom(request)
	if err != nil {
		return nil, err
	}
	err = storage.GetDb().Model(&savedView{}).Where("id = ?", id).Updates(map[string]any{
		"name": view.Name, "shared": view.Shared, "state": string(view.State), "updated_at": s.now(),
	}).Error
	if err != nil {
		return nil, duplicate(err, view.Name)
	}
	view.ID, view.OwnerID, view.UpdatedAt = id, user.ID, s.now()
	result := dto(*view, ownerName(user), true)
	return &result, nil
}

func (s *Service) Delete(user *users_models.User, id uuid.UUID) error {
	if _, err := s.ownView(user, id); err != nil {
		return err
	}
	return storage.GetDb().Delete(&savedView{}, "id = ?", id).Error
}

// SetDefault opens kind's tab with the view (one the user can see), or with none.
func (s *Service) SetDefault(user *users_models.User, request DefaultRequest) (*ViewList, error) {
	if err := checkKind(request.Kind); err != nil {
		return nil, err
	}
	db := storage.GetDb()
	if request.ViewID == nil {
		if err := db.Where("user_id = ? AND kind = ?", user.ID, request.Kind).Delete(&defaultView{}).Error; err != nil {
			return nil, err
		}
		return s.List(user, request.Kind)
	}
	var view savedView
	err := db.Where("id = ? AND kind = ? AND (owner_id = ? OR shared)", *request.ViewID, request.Kind, user.ID).
		First(&view).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	err = db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "kind"}},
		DoUpdates: clause.AssignmentColumns([]string{"view_id"}),
	}).Create(&defaultView{UserID: user.ID, Kind: request.Kind, ViewID: view.ID}).Error
	if err != nil {
		return nil, err
	}
	return s.List(user, request.Kind)
}

func (s *Service) ownView(user *users_models.User, id uuid.UUID) (*savedView, error) {
	var view savedView
	err := storage.GetDb().Where("id = ?", id).First(&view).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if view.OwnerID != user.ID {
		if !view.Shared {
			return nil, ErrNotFound
		}
		return nil, ErrForbidden
	}
	return &view, nil
}

func viewFrom(request ViewRequest) (*savedView, error) {
	if err := checkKind(request.Kind); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(request.Name)
	if name == "" || len(name) > maxNameLength {
		return nil, fmt.Errorf("a view's name is 1 to %d characters", maxNameLength)
	}
	if len(request.State) == 0 || len(request.State) > maxStateBytes {
		return nil, errors.New("a view's state is a JSON object of up to 16 KB")
	}
	var object map[string]any
	if json.Unmarshal(request.State, &object) != nil {
		return nil, errors.New("a view's state is a JSON object")
	}
	return &savedView{Kind: request.Kind, Name: name, Shared: request.Shared, State: request.State}, nil
}

func checkKind(kind string) error {
	if kind != KindCost && kind != KindPosture {
		return errors.New("kind must be cost or posture")
	}
	return nil
}

func dto(view savedView, owner string, mine bool) ViewDTO {
	return ViewDTO{ID: view.ID, Kind: view.Kind, Name: view.Name, Shared: view.Shared, State: view.State,
		Owner: owner, Mine: mine, UpdatedAt: view.UpdatedAt}
}

func ownerName(user *users_models.User) string {
	if user.Name != "" {
		return user.Name
	}
	return user.Email
}

func duplicate(err error, name string) error {
	if err != nil && strings.Contains(err.Error(), "SQLSTATE 23505") {
		return fmt.Errorf("you already have a view named %q", name)
	}
	return err
}
