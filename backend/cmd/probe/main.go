// obzly-probe runs synthetic checks from where it is installed: it pulls
// its monitors from an Obzly control plane, runs them on schedule, and
// sends the results to a Site project as OTLP. See docs/SYNTHETICS.md.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"obzly/internal/synthetics/checks"
	"obzly/internal/synthetics/report"
	"obzly/internal/util/egress"
)

// shutdownGrace bounds each shutdown step, well inside Kubernetes' default 30 s grace period.
const shutdownGrace = 5 * time.Second

// version is set at build time (-ldflags "-X main.version=...").
var version = "dev"

type settings struct {
	controlPlaneURL string
	token           string
	dataDir         string
	concurrency     int
	taskWait        time.Duration
	healthAddr      string
	deny            string
	instance        string
	envAllow        string
}

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println(version)
		return
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	config, err := loadSettings()
	if err != nil {
		log.Error("Invalid configuration", "error", err)
		os.Exit(2)
	}
	deny, err := egress.ParseCIDRs(config.deny)
	if err != nil {
		log.Error("Invalid OBZLY_PROBE_DENY_CIDRS", "error", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	httpClient := &http.Client{Timeout: 30 * time.Second}
	api := &controlPlane{
		base: strings.TrimRight(config.controlPlaneURL, "/"), token: config.token, http: httpClient,
		instance: config.instance,
	}
	sender := newSender(&report.Sender{Client: report.NewClient(30 * time.Second), Version: version}, log)
	agent := &probe{
		api: api, sender: sender, log: log, policy: checks.ProbePolicy(deny),
		cachePath: filepath.Join(config.dataDir, "config.json"), slots: make(chan struct{}, config.concurrency),
		taskWait: config.taskWait, checks: map[string]*scheduled{}, instance: config.instance,
		env: checks.NewEnvPolicy(config.envAllow, os.LookupEnv), testSlots: make(chan struct{}, maxTestRuns),
	}
	api.onMembers, api.ownedChecks = agent.setMembers, agent.ownedCount
	if config.healthAddr != "" {
		go agent.serveHealth(config.healthAddr)
	}
	log.Info("obzly-probe starting", "version", version, "controlPlane", api.base, "concurrency", config.concurrency,
		"instance", config.instance)

	senderDone := make(chan struct{})
	go func() {
		defer close(senderDone)
		sender.run(ctx)
	}()
	go agent.runTasks(ctx)
	agent.run(ctx)
	shutdown(api, agent, sender, senderDone, log)
}

// shutdown leaves the probe group, so the other members take this one's monitors at once rather
// than after it goes quiet, then sends the results of runs that were in progress.
func shutdown(api *controlPlane, agent *probe, sender *sender, senderDone <-chan struct{}, log *slog.Logger) {
	leaveCtx, cancelLeave := context.WithTimeout(context.Background(), shutdownGrace)
	if err := api.leave(leaveCtx); err != nil {
		log.Warn("Failed to tell the control plane this instance is leaving", "error", err)
	}
	cancelLeave()
	agent.waitForRuns(shutdownGrace)
	<-senderDone
	flushCtx, cancelFlush := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancelFlush()
	sender.flush(flushCtx, true)
	log.Info("obzly-probe stopped")
}

func loadSettings() (settings, error) {
	config := settings{
		controlPlaneURL: os.Getenv("OBZLY_CONTROL_PLANE_URL"),
		token:           os.Getenv("OBZLY_PROBE_TOKEN"),
		dataDir:         envOr("OBZLY_PROBE_DATA_DIR", "/var/lib/obzly-probe"),
		healthAddr:      os.Getenv("OBZLY_PROBE_HEALTH_ADDR"),
		deny:            os.Getenv("OBZLY_PROBE_DENY_CIDRS"),
		concurrency:     256,
		taskWait:        25 * time.Second,
		instance:        os.Getenv("OBZLY_PROBE_INSTANCE"),
		envAllow:        os.Getenv("OBZLY_PROBE_ENV_ALLOW"),
	}
	// An instance's name in its group: the pod name in Kubernetes.
	if config.instance == "" {
		config.instance, _ = os.Hostname()
	}
	if config.controlPlaneURL == "" || config.token == "" {
		return config, fmt.Errorf("OBZLY_CONTROL_PLANE_URL and OBZLY_PROBE_TOKEN are required")
	}
	if raw := os.Getenv("OBZLY_PROBE_MAX_CONCURRENCY"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 {
			return config, fmt.Errorf("OBZLY_PROBE_MAX_CONCURRENCY must be a positive number")
		}
		config.concurrency = value
	}
	if raw := os.Getenv("OBZLY_PROBE_TASK_WAIT"); raw != "" {
		value, err := time.ParseDuration(raw)
		if err != nil || value < time.Second || value > 55*time.Second {
			return config, fmt.Errorf("OBZLY_PROBE_TASK_WAIT must be between 1s and 55s")
		}
		config.taskWait = value
	}
	if err := os.MkdirAll(config.dataDir, 0o700); err != nil {
		return config, fmt.Errorf("OBZLY_PROBE_DATA_DIR: %w", err)
	}
	return config, nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
