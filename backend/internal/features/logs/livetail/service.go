package logs_livetail

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	logs_core "obzly/internal/features/logs/core"
	projects_services "obzly/internal/features/projects/services"
	users_models "obzly/internal/features/users/models"

	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"
)

type Service struct {
	client         valkey.Client
	projectService *projects_services.ProjectService
	logger         *slog.Logger
}

func NewService(client valkey.Client, projectService *projects_services.ProjectService, logger *slog.Logger) *Service {
	return &Service{client: client, projectService: projectService, logger: logger}
}

// ErrForbidden is a sentinel the controller maps to 403 without matching the
// error string.
var ErrForbidden = errors.New("insufficient permissions to tail project logs")

// CheckAccess runs before any response header is written, so a permission
// failure is a normal 403 rather than a stream that opens and goes silent.
// Tail checks again; a role change mid-tail applies on reconnect.
func (s *Service) CheckAccess(projectID uuid.UUID, user *users_models.User) error {
	canAccess, _, err := s.projectService.CanUserAccessProject(projectID, user)
	if err != nil {
		return fmt.Errorf("failed to verify project access: %w", err)
	}
	if !canAccess {
		return ErrForbidden
	}
	return nil
}

// Tail calls onLog for every published log matching query (nil matches
// everything) until ctx ends or onLog fails, returning that error or nil.
//
// onReady fires only once the subscription is live: neither transport
// replays, so an earlier "ready" frame could hide a dropped log. Read access
// (authz.ProjectRead) is enough, as for search.
func (s *Service) Tail(
	ctx context.Context,
	projectID uuid.UUID,
	query *logs_core.QueryNode,
	user *users_models.User,
	onReady func(),
	onLog func(logs_core.LogItemDTO) error,
) error {
	if err := s.CheckAccess(projectID, user); err != nil {
		return err
	}

	// A NATS handler may still be running when subscribe returns.
	var mu sync.Mutex
	var callbackErr error
	receiveCtx, stop := context.WithCancel(ctx)
	defer stop()
	receiveErr := currentBus(s.client, s.logger).subscribe(receiveCtx, projectID, onReady,
		func(items []*logs_core.LogItem) {
			mu.Lock()
			defer mu.Unlock()
			if callbackErr != nil {
				return
			}
			for _, item := range items {
				if !logs_core.Matches(query, item) {
					continue
				}
				if err := onLog(item.ToDTO()); err != nil {
					callbackErr = err
					stop()
					return
				}
			}
		})

	mu.Lock()
	defer mu.Unlock()
	if callbackErr != nil {
		return callbackErr
	}
	if receiveErr != nil && !errors.Is(receiveErr, context.Canceled) {
		return receiveErr
	}
	return nil
}
