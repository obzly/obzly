package ingestion

import (
	"time"

	"github.com/google/uuid"

	"obzly/internal/util/logger"
)

var repo = &repository{}

var tracker = &Tracker{repo: repo, logger: logger.GetLogger(), now: utcNow}

var service = &Service{repo, utcNow, &statusCache{ttl: statusCacheTTL, entries: map[uuid.UUID]*IngestionStatusDTO{}}}

var controller = &Controller{service}

// GetTracker is the receivers' hook (Observe*); main runs it on replicas
// that accept ingest.
func GetTracker() *Tracker { return tracker }

func GetController() *Controller { return controller }

func utcNow() time.Time { return time.Now().UTC() }
