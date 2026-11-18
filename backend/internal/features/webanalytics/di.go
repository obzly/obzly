package webanalytics

import (
	"time"

	"obzly/internal/config"
	"obzly/internal/features/audit_logs"
	logs_core "obzly/internal/features/logs/core"
	"obzly/internal/util/logger"
)

var repo = &repository{}

var geo = newCountryDB(func() string { return config.GetEnv().WebAnalyticsGeoIPDB }, logger.GetLogger())

var builder = &Builder{repo, logs_core.GetLogCoreRepository, geo, logger.GetLogger(), utcNow}

var service = &Service{repo, logs_core.GetLogCoreRepository, geo, audit_logs.GetAuditLogService(), utcNow,
	newLiveCache(utcNow)}

var controller = &Controller{service}

func GetBuilder() *Builder { return builder }

func GetController() *Controller { return controller }

func utcNow() time.Time { return time.Now().UTC() }
