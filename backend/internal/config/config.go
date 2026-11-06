package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	env_utils "obzly/internal/util/env"
	"obzly/internal/util/logger"
	"obzly/internal/util/tlsutil"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ilyakaznacheev/cleanenv"
	"github.com/joho/godotenv"
)

// SiteIDPattern is a DNS-label-like slug, enforced on SITE_ID and on every id a control
// plane registers or proxies to, so it can appear in a URL path and an `aud` claim
// unescaped.
var SiteIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

var log = logger.GetLogger()

const (
	AppModeWeb        = "web"
	AppModeBackground = "background"
)

// ServiceMode selects which subsystems this process runs (ARCHITECTURE.md "High
// availability"). Every mode is the same binary/image.
type ServiceMode string

const (
	// ServiceModeAll runs everything in one process; the default.
	ServiceModeAll ServiceMode = "all"
	// ServiceModeAPI serves HTTP/gRPC (REST, OTLP, the UI) but runs no background
	// cleanup workers; the tier that scales horizontally.
	ServiceModeAPI ServiceMode = "api"
	// ServiceModeWorker runs only the background workers (one replica, or more with
	// leaderlock failover).
	ServiceModeWorker ServiceMode = "worker"
	// ServiceModeControlPlane runs the multi-site control plane (ARCHITECTURE.md
	// "Multi-site: control plane and Sites"): central identity, the Site registry and
	// grants, the assertion signer/JWKS and the Site proxy. It holds no signal data, and
	// requires PUBLIC_URL.
	ServiceModeControlPlane ServiceMode = "control-plane"
	// ServiceModeBroker runs only the embedded NATS JetStream server, the durable ingest
	// buffer shared by api and worker replicas. No HTTP API, storage, Postgres or Valkey.
	ServiceModeBroker ServiceMode = "broker"
	// ServiceModeQuerier runs a distributed query executor: it takes query fragments over
	// NATS, reads segments from object storage into its cache and returns results over
	// internal gRPC. No HTTP API, Postgres or Valkey.
	ServiceModeQuerier ServiceMode = "querier"
)

// Ingest buffers (INGEST_BUFFER): where accepted batches wait before a
// segment writer takes them.
const (
	IngestBufferValkey    = "valkey"
	IngestBufferJetStream = "jetstream"
)

// ServesHTTP reports whether this mode starts the Gin server. Whether it also owns
// ingest and storage is RunsSignalStorage (false on a control plane).
func (m ServiceMode) ServesHTTP() bool {
	return m == ServiceModeAll || m == ServiceModeAPI || m == ServiceModeControlPlane
}

// RunsBackgroundWorkers reports whether this mode starts the logs/spans/metrics cleanup
// workers (quota, retention, compaction, reconciliation).
func (m ServiceMode) RunsBackgroundWorkers() bool {
	return m == ServiceModeAll || m == ServiceModeWorker
}

// RunsSignalStorage reports whether this mode owns the signal storage engine (storage
// setup, cache eviction, OTLP gRPC receiver, ingest pipeline, shutdown flush). False only
// for a control plane, which proxies queries and never touches segment data.
func (m ServiceMode) RunsSignalStorage() bool {
	return m != ServiceModeControlPlane && m != ServiceModeBroker && m != ServiceModeQuerier
}

// IsControlPlane reports whether this is the control-plane mode, with its own route
// surface.
func (m ServiceMode) IsControlPlane() bool {
	return m == ServiceModeControlPlane
}

// IsBroker: the process runs only the NATS server.
func (m ServiceMode) IsBroker() bool {
	return m == ServiceModeBroker
}

// IsQuerier: the process runs only a query executor.
func (m ServiceMode) IsQuerier() bool {
	return m == ServiceModeQuerier
}

type EnvVariables struct {
	DatabaseDsn     string            `env:"DATABASE_DSN"      required:"true"`
	EnvMode         env_utils.EnvMode `env:"ENV_MODE"          required:"true"`
	BackendRootPath string            `env:"BACKEND_ROOT_PATH" required:"true"`
	// HTTPPort is the main Gin server's port (routes, frontend, OTLP/HTTP); defaults to
	// DefaultHTTPPort.
	HTTPPort string `env:"HTTP_PORT"`
	// cache
	ValkeyHost     string `env:"VALKEY_HOST"     required:"true"`
	ValkeyPort     string `env:"VALKEY_PORT"     required:"true"`
	ValkeyUsername string `env:"VALKEY_USERNAME" required:"false"`
	ValkeyPassword string `env:"VALKEY_PASSWORD" required:"false"`
	ValkeyIsSsl    bool   `env:"VALKEY_IS_SSL"   required:"true"`
	// logs storage engine (ARCHITECTURE.md); LogsDataDir has a default.
	LogsDataDir string `env:"LOGS_DATA_DIR"`
	// ObjectStorageURL is a gocloud.dev/blob bucket URL ("s3://bucket", "gs://bucket",
	// "azblob://container", "file:///data/cold"). Empty means local disk only, no cold
	// tier. For MinIO or another S3-compatible endpoint, use s3:// with query params, e.g.
	//   s3://obzly-segments?endpoint=http://minio:9000&region=us-east-1&use_path_style=true&disable_https=true&hostname_immutable=true
	// Credentials come from the standard AWS SDK v2 chain.
	ObjectStorageURL string `env:"OBJECT_STORAGE_URL"`
	// ColdCacheMaxSizeMB caps the local cache of cold-tier segments downloaded from
	// object storage (enforced by segments.EvictColdCache); has a default.
	ColdCacheMaxSizeMB int64 `env:"COLD_CACHE_MAX_SIZE_MB"`
	// LogContextDefaultFields (LOG_CONTEXT_DEFAULT_FIELDS): comma-separated
	// fields added to a log context's default source scope
	// (logs_core.ContextScopeFields), e.g. "service_name,deployment".
	LogContextDefaultFields string `env:"LOG_CONTEXT_DEFAULT_FIELDS"`
	// OtlpGrpcPort is the OTLP gRPC receiver's port (default 4317). gRPC needs its own
	// listener; OTLP/HTTP shares the main server at /v1/logs etc.
	OtlpGrpcPort string `env:"OTLP_GRPC_PORT"`
	// OtlpGrpcTLSCertFile / OtlpGrpcTLSKeyFile: PEM files the OTLP gRPC
	// receiver serves TLS with (re-read when they change). Unset means
	// plaintext, fine only behind something that terminates TLS: port 4317
	// is often exposed directly and API keys travel in its metadata.
	OtlpGrpcTLSCertFile string `env:"OTLP_GRPC_TLS_CERT_FILE"`
	OtlpGrpcTLSKeyFile  string `env:"OTLP_GRPC_TLS_KEY_FILE"`
	// MetricsAddr is the internal listener for Prometheus /metrics (host:port,
	// default ":9464"; "off" disables it). Separate from HTTP_PORT so it can
	// be closed to everything but the scraper; the control plane never
	// proxies it.
	MetricsAddr string `env:"METRICS_ADDR"`

	// --- NATS JetStream ingest buffer. Off unless INGEST_BUFFER=jetstream, except
	// for SERVICE_MODE=broker. ---

	// IngestBuffer: IngestBufferValkey (default) or IngestBufferJetStream. "valkey"
	// keeps its name for existing configs but now means no buffer: receivers write to
	// their own segment writer.
	IngestBuffer string `env:"INGEST_BUFFER"`
	// NatsURL: the broker cluster's client URL(s), comma-separated, for api
	// and worker replicas. Unset in SERVICE_MODE=all, which runs the server
	// in-process instead.
	NatsURL string `env:"NATS_URL"`
	// NatsToken authenticates clients to the broker and brokers to each
	// other. Required wherever NATS listens on the network.
	NatsToken string `env:"NATS_TOKEN"`
	// NatsServerName must be unique per broker; defaults to the hostname
	// (the pod name in a StatefulSet).
	NatsServerName string `env:"NATS_SERVER_NAME"`
	// NatsClusterName and NatsRoutes (comma-separated nats-route://host:port
	// of every broker, itself included) form the broker cluster.
	NatsClusterName string `env:"NATS_CLUSTER_NAME"`
	NatsRoutes      string `env:"NATS_ROUTES"`
	// Broker listeners: clients (4222), peers (6222), monitoring and the
	// health probes (8222).
	NatsPort        int `env:"NATS_PORT"`
	NatsClusterPort int `env:"NATS_CLUSTER_PORT"`
	NatsMonitorPort int `env:"NATS_MONITOR_PORT"`
	// JetStreamDir holds the broker's file store; defaults under LOGS_DATA_DIR.
	JetStreamDir string `env:"JETSTREAM_DIR"`
	// JetStreamReplicas: ingest stream replicas - 3 on a broker cluster, 1
	// in-process (the defaults).
	JetStreamReplicas int `env:"JETSTREAM_REPLICAS"`
	// IngestStreamMaxBytes caps each signal's ingest stream (the worker lag
	// budget); a full stream rejects new batches.
	IngestStreamMaxBytes int64 `env:"INGEST_STREAM_MAX_BYTES"`
	// A worker writes a project's L0 segment once this many bytes of batches
	// are buffered, or once the oldest is INGEST_L0_MAX_AGE old - rows are
	// queryable only after that, so max-age is the low-volume freshness.
	IngestL0TargetBytes int64         `env:"INGEST_L0_TARGET_BYTES"`
	IngestL0MaxAge      time.Duration `env:"INGEST_L0_MAX_AGE"`

	// --- Distributed query. api replicas send query fragments to SERVICE_MODE=querier
	// replicas when QUERIER_TOKEN is set and any are running; otherwise they run queries
	// themselves. ---

	// QuerierToken authenticates coordinators to executors' result
	// listeners; the same value on api and querier replicas.
	QuerierToken string `env:"QUERIER_TOKEN"`
	// QuerierGRPCAddr: an executor's internal result listener. Never expose
	// it outside the Site.
	QuerierGRPCAddr string `env:"QUERIER_GRPC_ADDR"`
	// QuerierAdvertiseAddr: how api replicas reach that listener; defaults
	// to the hostname and the listener's port (set it to the pod IP and
	// port on Kubernetes).
	QuerierAdvertiseAddr string `env:"QUERIER_ADVERTISE_ADDR"`
	// QueryResultsTLS*: TLS on the executors' result listener, which
	// carries QUERIER_TOKEN. Executors serve with the cert/key; coordinators
	// dial TLS when any of these is set, trusting the CA file (or the system
	// roots). Executors advertise pod IPs, so either the cert has IP SANs or
	// SERVER_NAME names a DNS SAN it does have.
	QueryResultsTLSCertFile   string `env:"QUERY_RESULTS_TLS_CERT_FILE"`
	QueryResultsTLSKeyFile    string `env:"QUERY_RESULTS_TLS_KEY_FILE"`
	QueryResultsTLSCAFile     string `env:"QUERY_RESULTS_TLS_CA_FILE"`
	QueryResultsTLSServerName string `env:"QUERY_RESULTS_TLS_SERVER_NAME"`
	// QuerierConcurrency: fragments an executor runs at once; more wait.
	QuerierConcurrency int `env:"QUERIER_CONCURRENCY"`
	// QuerierSpillMaxSize caps the executor's DuckDB spill files (DuckDB
	// size syntax, "8GB"), which share the querier volume with the cold
	// cache; a search that would spill past it fails instead of filling the
	// disk.
	QuerierSpillMaxSize string `env:"QUERIER_SPILL_MAX_SIZE"`
	// QuerierWarmHoursRaw is QUERIER_WARM_HOURS as given: on start an executor caches its
	// share of this many recent hours of segments, so the first wide search after a
	// restart doesn't read them all from object storage. Unset means
	// DefaultQuerierWarmHours; "0" off. Read it through QuerierWarmHours.
	QuerierWarmHoursRaw string `env:"QUERIER_WARM_HOURS"`
	QuerierWarmHours    int
	// Partition result cache for aggregations (logs histogram, dashboard series):
	// finished partitions are kept in Valkey for QUERY_CACHE_TTL_HOURS ("0" off);
	// partitions ending within QUERY_CACHE_DELAY_SECONDS of now are always read fresh,
	// as rows still arrive. Read through QueryCacheTTL/Delay.
	QueryCacheTTLHoursRaw     string `env:"QUERY_CACHE_TTL_HOURS"`
	QueryCacheDelaySecondsRaw string `env:"QUERY_CACHE_DELAY_SECONDS"`
	// QueryPartialDownloads (QUERY_PARTIAL_DOWNLOADS, default true): read a
	// large cold segment's needed columns from object storage instead of
	// downloading it whole (segments/partial.go).
	QueryPartialDownloads *bool `env:"QUERY_PARTIAL_DOWNLOADS"`
	QueryCacheTTL         time.Duration
	QueryCacheDelay       time.Duration
	// QueryMaxScanGBRaw is QUERY_MAX_SCAN_GB as given: the most compressed data, in GiB,
	// one user query may plan to read; over it the query is refused before reading (a
	// count is skipped). Background work is never limited. Unset means
	// DefaultQueryMaxScanGB; "0" off. Read it through QueryMaxScanBytes.
	QueryMaxScanGBRaw string `env:"QUERY_MAX_SCAN_GB"`
	QueryMaxScanBytes int64
	// Alerting. AlertEgressAllowlist: CIDRs alert webhooks may reach although not public
	// (in-network receivers); AlertEgressDenylist: CIDRs they may never reach (e.g. the
	// cluster's service range, which the binary can't discover). AlertEvalShards: rule
	// evaluation is split this many ways, each under its own leader lock (default 1).
	// SMTP is set in Obzly settings; old ALERT_SMTP_* vars are imported once.
	AlertEgressAllowlist string `env:"ALERT_EGRESS_ALLOWLIST"`
	AlertEgressDenylist  string `env:"ALERT_EGRESS_DENYLIST"`
	AlertEvalShards      int    `env:"ALERT_EVAL_SHARDS"`
	// Infrastructure inventory: assets not seen for InventoryRetentionDays
	// (default 30) are dropped; terminated pods after 7 days.
	InventoryRetentionDays int `env:"INVENTORY_RETENTION_DAYS"`
	// InventoryMaxAssetsPerProject caps one project's inventory (default
	// 50000); past it the least recently seen assets are dropped.
	InventoryMaxAssetsPerProject int `env:"INVENTORY_MAX_ASSETS_PER_PROJECT"`
	// APIMaxBodyMB caps request bodies on /api/v1 routes that set no limit
	// of their own (default 5); ingest routes keep their own limits.
	APIMaxBodyMB int `env:"API_MAX_BODY_MB"`
	// The integrity audit: a sample of uploaded segments checked against object storage
	// every IntegrityAuditHours (default 168; negative turns it off),
	// IntegrityAuditPercent of them (default 1) up to IntegrityAuditMaxSamples (default 500).
	IntegrityAuditHours      int     `env:"INTEGRITY_AUDIT_HOURS"`
	IntegrityAuditPercent    float64 `env:"INTEGRITY_AUDIT_PERCENT"`
	IntegrityAuditMaxSamples int     `env:"INTEGRITY_AUDIT_MAX_SAMPLES"`
	// ShutdownDrainSeconds is how long an HTTP-serving replica keeps serving after
	// SIGTERM, with /system/ready failing, so the load balancer can deregister it. 0
	// (default) suits a single container; behind a load balancer set it above the
	// readiness probe period, and terminationGracePeriodSeconds above it plus shutdown.
	ShutdownDrainSeconds int `env:"SHUTDOWN_DRAIN_SECONDS"`
	// SelfTracingSampleRateRaw is SELF_TRACING_SAMPLE_RATE as given: the fraction (0 to
	// 1) of this replica's requests and background passes traced into the system
	// project. Unset means DefaultSelfTracingSampleRate; "0" off. Read it through
	// SelfTracingSampleRate.
	SelfTracingSampleRateRaw string `env:"SELF_TRACING_SAMPLE_RATE"`
	SelfTracingSampleRate    float64
	// oauth
	GitHubClientID     string `env:"GITHUB_CLIENT_ID"`
	GitHubClientSecret string `env:"GITHUB_CLIENT_SECRET"`
	GoogleClientID     string `env:"GOOGLE_CLIENT_ID"`
	GoogleClientSecret string `env:"GOOGLE_CLIENT_SECRET"`
	// RootAdminEmail (ADMIN_EMAIL), if set, is the bootstrap super-admin's email, and the
	// account is renamed to it on startup if it differs. Callers must use
	// EffectiveRootAdminEmail, never this field.
	RootAdminEmail string `env:"ADMIN_EMAIL"`
	// ServiceMode defaults to ServiceModeAll when unset.
	ServiceMode ServiceMode `env:"SERVICE_MODE"`
	// TrustedProxies: comma-separated CIDRs/IPs of reverse proxies in front of this
	// server, passed to Gin's SetTrustedProxies. Only then are X-Forwarded-For/X-Real-IP
	// trusted for ClientIP() (IP allow-listing, stored client_ip). Unset, the TCP peer is
	// used: trusting those headers from anyone lets callers spoof their IP.
	TrustedProxies string `env:"TRUSTED_PROXIES"`

	// --- Multi-site (ARCHITECTURE.md "Multi-site: control plane and Sites"). All
	// optional; unset, a deployment is a standalone install. ---

	// PublicURL is the browser-facing origin (scheme + host[:port], no path), e.g.
	// "https://obzly.example.com", used for OAuth metadata and MCP tokens. On a control
	// plane it is required and is the `iss` of every assertion, which must match each
	// Site's CONTROL_PLANE_ISSUER exactly.
	PublicURL string `env:"PUBLIC_URL"`
	// SiteAllowInsecureURLs lets a control plane register Sites with an http:// api_url.
	// For local compose setups only; never on an internet-reachable control plane.
	SiteAllowInsecureURLs bool `env:"SITE_ALLOW_INSECURE_URLS"`
	// ControlPlaneEgressAllowlist (control plane): comma-separated CIDRs the control
	// plane may connect to for Sites. Empty = anything allowed by the SSRF floor; set it
	// to the Sites' ranges for defense in depth against a mis-registered api_url.
	ControlPlaneEgressAllowlist string `env:"CONTROL_PLANE_EGRESS_ALLOWLIST"`

	// SiteID (Site) is the slug this backend is registered under, and the `aud` every
	// assertion for it carries, so assertions for other Sites are rejected. Required
	// with ControlPlaneIssuer.
	SiteID string `env:"SITE_ID"`
	// SiteName (Site) is a label shown by GET /api/v1/site/info. Defaults to SiteID.
	SiteName string `env:"SITE_NAME"`
	// ControlPlaneIssuer (Site) enables token exchange: the exact `iss` assertions must
	// carry (the control plane's PUBLIC_URL). Unset, the exchange endpoint answers 404.
	ControlPlaneIssuer string `env:"CONTROL_PLANE_ISSUER"`
	// ControlPlaneJWKSURL (Site) is where the control plane's signing keys are fetched;
	// defaults to "<CONTROL_PLANE_ISSUER>/.well-known/jwks.json". Override it to use an
	// internal address; `iss` is still compared against ControlPlaneIssuer.
	ControlPlaneJWKSURL string `env:"CONTROL_PLANE_JWKS_URL"`
	// ControlPlaneLinkByEmail (Site, default true): whether a central user with no linked
	// identity may be attached to an existing local account with the same verified email.
	// Never the root admin or an account with its own sign-in (password, SSO, GitHub/
	// Google), only password-less ones such as invites. False always creates a new account.
	ControlPlaneLinkByEmail *bool `env:"CONTROL_PLANE_LINK_BY_EMAIL"`
	// ControlPlaneEgressCIDRs (Site): comma-separated CIDRs the control plane's calls come
	// from. When set, the exchange endpoint rejects other callers before doing any work.
	// Empty = no source restriction (signature/aud/exp checks still apply).
	ControlPlaneEgressCIDRs string `env:"CONTROL_PLANE_EGRESS_CIDRS"`

	// AppVersion is the build's version (Dockerfile APP_VERSION build arg), shown by
	// GET /api/v1/site/info. Defaults to "dev".
	AppVersion string `env:"APP_VERSION"`
}

const (
	// DefaultRootAdminEmail is the bootstrap super-admin's login on a standalone install
	// or Site when ADMIN_EMAIL is unset.
	DefaultRootAdminEmail = "admin"
	// DefaultControlPlaneRootAdminEmail is that login on a control plane. Different on
	// purpose: central accounts link to Site accounts by email and a Site's root admin is
	// never linkable, so a shared "admin" would leave the central root admin unable to
	// get an identity on any Site.
	DefaultControlPlaneRootAdminEmail = "superadmin"
)

// EffectiveRootAdminEmail is ADMIN_EMAIL when set, otherwise the mode's default. Startup
// renames an existing root admin to it, so an older control plane's "admin" becomes
// "superadmin".
func (e EnvVariables) EffectiveRootAdminEmail() string {
	if e.RootAdminEmail != "" {
		return e.RootAdminEmail
	}
	if e.ServiceMode.IsControlPlane() {
		return DefaultControlPlaneRootAdminEmail
	}
	return DefaultRootAdminEmail
}

// LinkByEmail resolves ControlPlaneLinkByEmail's "unset means true" default.
func (e EnvVariables) LinkByEmail() bool {
	return e.ControlPlaneLinkByEmail == nil || *e.ControlPlaneLinkByEmail
}

// SiteExchangeEnabled reports whether this process accepts control-plane assertions
// (Site side).
func (e EnvVariables) SiteExchangeEnabled() bool {
	return e.ControlPlaneIssuer != ""
}

var (
	env  EnvVariables
	once sync.Once
)

func GetEnv() EnvVariables {
	once.Do(loadEnvVariables)
	return env
}

func loadEnvVariables() {
	cwd, err := os.Getwd()
	if err != nil {
		log.Warn("could not get current working directory", "error", err)
		cwd = "."
	}

	backendRoot := cwd
	for {
		if _, err := os.Stat(filepath.Join(backendRoot, "go.mod")); err == nil {
			break
		}

		parent := filepath.Dir(backendRoot)
		if parent == backendRoot {
			break
		}

		backendRoot = parent
	}

	env.BackendRootPath = backendRoot

	envPaths := []string{
		filepath.Join(cwd, ".env"),
		filepath.Join(backendRoot, ".env"),
	}

	var loaded bool
	for _, path := range envPaths {
		log.Info("Trying to load .env", "path", path)
		if err := godotenv.Load(path); err == nil {
			log.Info("Successfully loaded .env", "path", path)
			loaded = true
			break
		}
	}

	if !loaded {
		log.Error("Error loading .env file: could not find .env in any location")
		os.Exit(1)
	}

	err = cleanenv.ReadEnv(&env)
	if err != nil {
		log.Error("Configuration could not be loaded", "error", err)
		os.Exit(1)
	}

	if env.DatabaseDsn == "" {
		log.Error("DATABASE_DSN is empty")
		os.Exit(1)
	}

	if env.EnvMode == "" {
		log.Error("ENV_MODE is empty")
		os.Exit(1)
	}
	if env.EnvMode != "development" && env.EnvMode != "production" {
		log.Error("ENV_MODE is invalid", "mode", env.EnvMode)
		os.Exit(1)
	}
	log.Info("ENV_MODE loaded", "mode", env.EnvMode)

	// Valkey
	if env.ValkeyHost == "" {
		log.Error("VALKEY_HOST is empty")
		os.Exit(1)
	}
	if env.ValkeyPort == "" {
		log.Error("VALKEY_PORT is empty")
		os.Exit(1)
	}

	// Logs storage engine
	if env.LogsDataDir == "" {
		env.LogsDataDir = "/obzly-data/segments"
	}
	log.Info("LOGS_DATA_DIR loaded", "path", env.LogsDataDir)

	if env.ColdCacheMaxSizeMB <= 0 {
		env.ColdCacheMaxSizeMB = 20_000 // 20 GB
	}
	log.Info("COLD_CACHE_MAX_SIZE_MB loaded", "maxSizeMB", env.ColdCacheMaxSizeMB)

	// Main HTTP server port
	if env.HTTPPort == "" {
		env.HTTPPort = DefaultHTTPPort
	}
	log.Info("HTTP_PORT loaded", "port", env.HTTPPort)

	// OTLP gRPC receiver
	if env.OtlpGrpcPort == "" {
		env.OtlpGrpcPort = DefaultOtlpGrpcPort
	}
	log.Info("OTLP_GRPC_PORT loaded", "port", env.OtlpGrpcPort)

	// Fail fast on a bad port: the listeners bind in background goroutines,
	// so a typo would otherwise surface only as a log line from a process
	// that keeps running while serving nothing.
	if err := ValidatePorts(env.HTTPPort, env.OtlpGrpcPort); err != nil {
		log.Error("Invalid port configuration", "error", err)
		os.Exit(1)
	}

	if env.MetricsAddr == "" {
		env.MetricsAddr = DefaultMetricsAddr
		// Same reason the HTTP server binds 127.0.0.1 in development: no
		// firewall prompt on every run.
		if env.EnvMode == env_utils.EnvModeDevelopment {
			env.MetricsAddr = "127.0.0.1" + DefaultMetricsAddr
		}
	}
	if err := ValidateTLSSettings(env); err != nil {
		log.Error("Invalid TLS configuration", "error", err)
		os.Exit(1)
	}

	if err := ValidateMetricsAddr(env.MetricsAddr, env.HTTPPort, env.OtlpGrpcPort); err != nil {
		log.Error("Invalid METRICS_ADDR", "error", err)
		os.Exit(1)
	}
	log.Info("METRICS_ADDR loaded", "addr", env.MetricsAddr)

	rate, err := ParseSelfTracingSampleRate(env.SelfTracingSampleRateRaw)
	if err != nil {
		log.Error("Invalid SELF_TRACING_SAMPLE_RATE", "error", err)
		os.Exit(1)
	}
	env.SelfTracingSampleRate = rate
	log.Info("SELF_TRACING_SAMPLE_RATE loaded", "rate", rate)

	scanBytes, err := ParseQueryMaxScanGB(env.QueryMaxScanGBRaw)
	if err != nil {
		log.Error("Invalid QUERY_MAX_SCAN_GB", "error", err)
		os.Exit(1)
	}
	env.QueryMaxScanBytes = scanBytes
	log.Info("QUERY_MAX_SCAN_GB loaded", "bytes", scanBytes)

	warmHours, err := ParseQuerierWarmHours(env.QuerierWarmHoursRaw)
	if err != nil {
		log.Error("Invalid QUERIER_WARM_HOURS", "error", err)
		os.Exit(1)
	}
	env.QuerierWarmHours = warmHours

	cacheHours, err := ParseWholeNumber(env.QueryCacheTTLHoursRaw, DefaultQueryCacheTTLHours, 24*30)
	if err != nil {
		log.Error("Invalid QUERY_CACHE_TTL_HOURS", "error", err)
		os.Exit(1)
	}
	env.QueryCacheTTL = time.Duration(cacheHours) * time.Hour
	cacheDelay, err := ParseWholeNumber(env.QueryCacheDelaySecondsRaw, DefaultQueryCacheDelaySeconds, 24*3600)
	if err != nil {
		log.Error("Invalid QUERY_CACHE_DELAY_SECONDS", "error", err)
		os.Exit(1)
	}
	env.QueryCacheDelay = time.Duration(cacheDelay) * time.Second

	if env.ShutdownDrainSeconds < 0 || env.ShutdownDrainSeconds > MaxShutdownDrainSeconds {
		log.Error(
			"SHUTDOWN_DRAIN_SECONDS must be from 0 to 300",
			"value", env.ShutdownDrainSeconds,
		)
		os.Exit(1)
	}

	// Service mode
	if env.ServiceMode == "" {
		env.ServiceMode = ServiceModeAll
	}
	switch env.ServiceMode {
	case ServiceModeAll, ServiceModeAPI, ServiceModeWorker, ServiceModeControlPlane, ServiceModeBroker,
		ServiceModeQuerier:
	default:
		log.Error(
			"SERVICE_MODE is invalid - must be \"all\", \"api\", \"worker\", \"control-plane\", \"broker\" or \"querier\"",
			"mode",
			env.ServiceMode,
		)
		os.Exit(1)
	}

	applyBrokerDefaults(&env)
	if err := ValidateBrokerSettings(env); err != nil {
		log.Error("Invalid NATS JetStream settings", "error", err)
		os.Exit(1)
	}
	applyQuerierDefaults(&env)
	if err := ValidateQuerierSettings(env); err != nil {
		log.Error("Invalid distributed query settings", "error", err)
		os.Exit(1)
	}
	log.Info("SERVICE_MODE loaded", "mode", env.ServiceMode)

	if env.AppVersion == "" {
		env.AppVersion = "dev"
	}

	loadMultiSiteVariables()

	log.Info("Environment variables loaded successfully!")
}

// loadMultiSiteVariables validates and defaults the multi-site settings. Every check
// exits at startup: a misconfigured control plane or Site would otherwise only fail on
// the first login.
func loadMultiSiteVariables() {
	// Control-plane side.
	env.PublicURL = strings.TrimRight(strings.TrimSpace(env.PublicURL), "/")
	if env.ServiceMode.IsControlPlane() {
		if env.PublicURL == "" {
			log.Error(
				"PUBLIC_URL is required when SERVICE_MODE=control-plane (the browser-facing origin, e.g. https://obzly.example.com)",
			)
			os.Exit(1)
		}
		if err := validateOriginURL(env.PublicURL); err != nil {
			log.Error("PUBLIC_URL is invalid", "value", env.PublicURL, "error", err)
			os.Exit(1)
		}
		log.Info("PUBLIC_URL loaded", "url", env.PublicURL)
		if env.SiteAllowInsecureURLs {
			log.Warn(
				"SITE_ALLOW_INSECURE_URLS is enabled - Sites may be registered with plain http:// URLs; only use this for local/internal deployments",
			)
		}
	}
	if err := validateCIDRList(env.ControlPlaneEgressAllowlist); err != nil {
		log.Error("CONTROL_PLANE_EGRESS_ALLOWLIST is invalid", "error", err)
		os.Exit(1)
	}
	for name, raw := range map[string]string{
		"ALERT_EGRESS_ALLOWLIST": env.AlertEgressAllowlist, "ALERT_EGRESS_DENYLIST": env.AlertEgressDenylist,
	} {
		if err := validateCIDRList(raw); err != nil {
			log.Error(name+" is invalid", "error", err)
			os.Exit(1)
		}
	}
	if env.IntegrityAuditHours == 0 {
		env.IntegrityAuditHours = 168
	}
	if env.IntegrityAuditPercent <= 0 {
		env.IntegrityAuditPercent = 1
	}
	if env.IntegrityAuditMaxSamples <= 0 {
		env.IntegrityAuditMaxSamples = 500
	}
	if env.AlertEvalShards <= 0 {
		env.AlertEvalShards = 1
	}
	if env.InventoryRetentionDays <= 0 {
		env.InventoryRetentionDays = 30
	}
	if env.InventoryMaxAssetsPerProject <= 0 {
		env.InventoryMaxAssetsPerProject = 50000
	}
	if env.APIMaxBodyMB <= 0 {
		env.APIMaxBodyMB = 5
	}

	// Site side.
	env.ControlPlaneIssuer = strings.TrimRight(strings.TrimSpace(env.ControlPlaneIssuer), "/")
	env.SiteID = strings.TrimSpace(env.SiteID)
	if env.SiteExchangeEnabled() {
		if err := validateOriginURL(env.ControlPlaneIssuer); err != nil {
			log.Error("CONTROL_PLANE_ISSUER is invalid", "value", env.ControlPlaneIssuer, "error", err)
			os.Exit(1)
		}
		if env.SiteID == "" {
			log.Error(
				"SITE_ID is required when CONTROL_PLANE_ISSUER is set - it must match the slug this Site was registered under in the control plane",
			)
			os.Exit(1)
		}
		if !SiteIDPattern.MatchString(env.SiteID) {
			log.Error(
				"SITE_ID is invalid - lowercase letters, digits and hyphens only, max 63 chars, must start with a letter or digit",
				"value",
				env.SiteID,
			)
			os.Exit(1)
		}
		if env.ControlPlaneJWKSURL == "" {
			env.ControlPlaneJWKSURL = env.ControlPlaneIssuer + "/.well-known/jwks.json"
		}
		if _, err := url.ParseRequestURI(env.ControlPlaneJWKSURL); err != nil {
			log.Error("CONTROL_PLANE_JWKS_URL is invalid", "value", env.ControlPlaneJWKSURL, "error", err)
			os.Exit(1)
		}
		if env.SiteName == "" {
			env.SiteName = env.SiteID
		}
		if err := validateCIDRList(env.ControlPlaneEgressCIDRs); err != nil {
			log.Error("CONTROL_PLANE_EGRESS_CIDRS is invalid", "error", err)
			os.Exit(1)
		}
		log.Info("Site token exchange enabled",
			"siteId", env.SiteID, "issuer", env.ControlPlaneIssuer, "jwksUrl", env.ControlPlaneJWKSURL,
			"linkByEmail", env.LinkByEmail())
	}
}

// validateOriginURL accepts exactly scheme://host[:port], with no path, query, fragment
// or userinfo: `iss` is compared by string equality, so anything extra never matches.
func validateOriginURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errInvalidOrigin("scheme must be http or https")
	}
	if parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errInvalidOrigin("must be an origin only (scheme://host[:port]), no userinfo/query/fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return errInvalidOrigin("must not include a path")
	}
	return nil
}

type errInvalidOrigin string

func (e errInvalidOrigin) Error() string { return string(e) }

// validateCIDRList parses a comma-separated CIDR list (empty allowed);
// ParseCIDRList is the runtime accessor callers use once validated.
func validateCIDRList(raw string) error {
	_, err := ParseCIDRList(raw)
	return err
}

// ParseCIDRList turns "10.0.0.0/8, 192.168.1.0/24" into networks. A bare IP
// is accepted too and treated as a /32 (or /128). Empty input = nil.
func ParseCIDRList(raw string) ([]*net.IPNet, error) {
	var nets []*net.IPNet
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if !strings.Contains(entry, "/") {
			ip := net.ParseIP(entry)
			if ip == nil {
				return nil, errInvalidOrigin("invalid IP or CIDR: " + entry)
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, network, err := net.ParseCIDR(entry)
		if err != nil {
			return nil, err
		}
		nets = append(nets, network)
	}
	return nets, nil
}

const (
	// DefaultHTTPPort is the main HTTP server's port when HTTP_PORT is unset.
	DefaultHTTPPort = "4005"
	// DefaultOtlpGrpcPort is the OTLP gRPC receiver's port when
	// OTLP_GRPC_PORT is unset (the OTel-conventional collector port).
	DefaultOtlpGrpcPort = "4317"
	// DefaultMetricsAddr is the Prometheus listener when METRICS_ADDR is
	// unset (the OTel-conventional Prometheus exporter port).
	DefaultMetricsAddr = ":9464"
	// MetricsAddrOff disables the metrics listener.
	MetricsAddrOff = "off"
	// MaxShutdownDrainSeconds caps SHUTDOWN_DRAIN_SECONDS; Kubernetes kills
	// the pod at terminationGracePeriodSeconds anyway.
	MaxShutdownDrainSeconds = 300
	// DefaultSelfTracingSampleRate: 1% of requests.
	DefaultSelfTracingSampleRate = 0.01
)

// UsesJetStream reports whether this process runs or connects to the NATS
// JetStream ingest buffer.
func (e EnvVariables) UsesJetStream() bool {
	if e.ServiceMode.IsBroker() {
		return true
	}
	return e.IngestBuffer == IngestBufferJetStream && e.ServiceMode.RunsSignalStorage()
}

// EmbedsBroker: SERVICE_MODE=all with JetStream and no external NATS_URL
// runs the server in-process.
func (e EnvVariables) EmbedsBroker() bool {
	return e.UsesJetStream() && e.ServiceMode == ServiceModeAll && e.NatsURL == ""
}

// ValidateBrokerSettings rejects JetStream settings that cannot work: a
// networked broker without a token, or a replica that must connect to one
// without the URL or token.
func ValidateBrokerSettings(env EnvVariables) error {
	switch env.IngestBuffer {
	case IngestBufferValkey, IngestBufferJetStream:
	default:
		return fmt.Errorf("INGEST_BUFFER must be %q or %q, got %q",
			IngestBufferValkey, IngestBufferJetStream, env.IngestBuffer)
	}
	if !env.UsesJetStream() {
		return nil
	}
	if env.IngestL0MaxAge != 0 && env.IngestL0MaxAge < MinIngestL0MaxAge {
		return fmt.Errorf("INGEST_L0_MAX_AGE must be at least %s, got %s", MinIngestL0MaxAge, env.IngestL0MaxAge)
	}
	if env.ServiceMode.IsBroker() {
		if env.NatsToken == "" {
			return errors.New("SERVICE_MODE=broker needs NATS_TOKEN")
		}
		return nil
	}
	if env.EmbedsBroker() {
		return nil
	}
	if env.NatsURL == "" || env.NatsToken == "" {
		return fmt.Errorf("INGEST_BUFFER=jetstream in SERVICE_MODE=%s needs NATS_URL and NATS_TOKEN", env.ServiceMode)
	}
	return nil
}

// ValidateQuerierSettings: an executor needs the broker, the shared token
// and object storage (it has no other way to read segments).
func ValidateQuerierSettings(env EnvVariables) error {
	if !env.ServiceMode.IsQuerier() {
		return nil
	}
	if env.NatsURL == "" || env.NatsToken == "" {
		return errors.New("SERVICE_MODE=querier needs NATS_URL and NATS_TOKEN")
	}
	if env.QuerierToken == "" {
		return errors.New("SERVICE_MODE=querier needs QUERIER_TOKEN")
	}
	if env.ObjectStorageURL == "" {
		return errors.New("SERVICE_MODE=querier needs OBJECT_STORAGE_URL")
	}
	return nil
}

// ValidateTLSSettings: every certificate needs its key.
func ValidateTLSSettings(env EnvVariables) error {
	if err := tlsutil.ValidatePair("OTLP_GRPC_TLS_CERT_FILE", env.OtlpGrpcTLSCertFile,
		"OTLP_GRPC_TLS_KEY_FILE", env.OtlpGrpcTLSKeyFile); err != nil {
		return err
	}
	return tlsutil.ValidatePair("QUERY_RESULTS_TLS_CERT_FILE", env.QueryResultsTLSCertFile,
		"QUERY_RESULTS_TLS_KEY_FILE", env.QueryResultsTLSKeyFile)
}

// QueryResultsClientTLS: coordinators dial executors' result listeners
// over TLS. An api replica needs only the CA file or the server name, not
// the executors' key.
func (e EnvVariables) QueryResultsClientTLS() bool {
	return e.QueryResultsTLSCertFile != "" || e.QueryResultsTLSCAFile != "" || e.QueryResultsTLSServerName != ""
}

// UsesRemoteQuery: an HTTP-serving replica with the broker and
// QUERIER_TOKEN sends query fragments to executors when any run.
func (e EnvVariables) UsesRemoteQuery() bool {
	return e.QuerierToken != "" && e.UsesJetStream() && e.ServiceMode.ServesHTTP()
}

// DefaultQueryMaxScanGB: about a day of a multi-TB/day project's compressed
// data, far above anything an interactive query on smaller Sites plans.
const DefaultQueryMaxScanGB = 100

// ParseQueryMaxScanGB reads QUERY_MAX_SCAN_GB: "" means
// DefaultQueryMaxScanGB, "0" none, otherwise a whole number of GiB.
func ParseQueryMaxScanGB(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultQueryMaxScanGB << 30, nil
	}
	gb, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || gb < 0 || gb > 1<<20 {
		return 0, fmt.Errorf("must be a whole number of GiB from 0 (off) to 1048576, got %q", raw)
	}
	return gb << 30, nil
}

// ParseWholeNumber reads a whole-number setting: "" means def, otherwise 0
// to maxValue.
func ParseWholeNumber(raw string, def, maxValue int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 || value > maxValue {
		return 0, fmt.Errorf("must be a whole number from 0 to %d, got %q", maxValue, raw)
	}
	return value, nil
}

// ParseQuerierWarmHours reads QUERIER_WARM_HOURS: "" means
// DefaultQuerierWarmHours, "0" off.
func ParseQuerierWarmHours(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultQuerierWarmHours, nil
	}
	hours, err := strconv.Atoi(raw)
	if err != nil || hours < 0 || hours > 24*90 {
		return 0, fmt.Errorf("must be a whole number of hours from 0 (off) to 2160, got %q", raw)
	}
	return hours, nil
}

// ParseSelfTracingSampleRate reads SELF_TRACING_SAMPLE_RATE: "" means
// DefaultSelfTracingSampleRate, otherwise a number from 0 to 1.
func ParseSelfTracingSampleRate(raw string) (float64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultSelfTracingSampleRate, nil
	}
	rate, err := strconv.ParseFloat(raw, 64)
	if err != nil || rate < 0 || rate > 1 {
		return 0, fmt.Errorf("SELF_TRACING_SAMPLE_RATE must be a number from 0 to 1, got %q", raw)
	}
	return rate, nil
}

// MetricsEnabled reports whether the /metrics listener should start.
func (e EnvVariables) MetricsEnabled() bool {
	return e.MetricsAddr != MetricsAddrOff
}

// ValidateMetricsAddr checks METRICS_ADDR is "off" or host:port with a valid
// port that differs from HTTP_PORT and OTLP_GRPC_PORT.
func ValidateMetricsAddr(addr, httpPort, otlpGrpcPort string) error {
	if addr == MetricsAddrOff {
		return nil
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("METRICS_ADDR must be host:port (e.g. \":9464\") or %q, got %q", MetricsAddrOff, addr)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("METRICS_ADDR port must be from 1 to 65535, got %q", port)
	}
	if port == httpPort || port == otlpGrpcPort {
		return fmt.Errorf("METRICS_ADDR port %s must differ from HTTP_PORT and OTLP_GRPC_PORT", port)
	}
	return nil
}

// ValidatePorts checks HTTP_PORT and OTLP_GRPC_PORT: each a whole number
// from 1 to 65535, and not the same port - the HTTP server and the gRPC
// receiver are separate listeners and can't share one.
func ValidatePorts(httpPort, otlpGrpcPort string) error {
	for _, p := range []struct{ name, value string }{
		{"HTTP_PORT", httpPort},
		{"OTLP_GRPC_PORT", otlpGrpcPort},
	} {
		n, err := strconv.Atoi(p.value)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("%s must be a port number from 1 to 65535, got %q", p.name, p.value)
		}
	}
	a, _ := strconv.Atoi(httpPort)
	b, _ := strconv.Atoi(otlpGrpcPort)
	if a == b {
		return fmt.Errorf("HTTP_PORT and OTLP_GRPC_PORT must differ (both %d)", a)
	}
	return nil
}

// Broker defaults (ARCHITECTURE.md "Scale architecture").
const (
	DefaultNatsClusterName      = "obzly"
	DefaultNatsPort             = 4222
	DefaultNatsClusterPort      = 6222
	DefaultNatsMonitorPort      = 8222
	DefaultIngestStreamMaxBytes = 10 << 30 // 10 GiB per signal
	DefaultIngestL0TargetBytes  = 64 << 20
	DefaultIngestL0MaxAge       = time.Hour
	// MinIngestL0MaxAge keeps low-volume projects from writing (and
	// uploading) a tiny object every few seconds.
	MinIngestL0MaxAge = 5 * time.Minute
)

// Distributed query defaults.
const (
	DefaultQuerierGRPCAddr        = ":4319"
	DefaultQuerierWarmHours       = 24
	DefaultQueryCacheTTLHours     = 24
	DefaultQueryCacheDelaySeconds = 300
)

func applyQuerierDefaults(env *EnvVariables) {
	if env.QuerierGRPCAddr == "" {
		env.QuerierGRPCAddr = DefaultQuerierGRPCAddr
	}
	if env.QuerierConcurrency <= 0 {
		env.QuerierConcurrency = runtime.NumCPU()
	}
	if strings.TrimSpace(env.QuerierSpillMaxSize) == "" {
		env.QuerierSpillMaxSize = "8GB"
	}
}

func applyBrokerDefaults(env *EnvVariables) {
	if env.IngestBuffer == "" {
		env.IngestBuffer = IngestBufferValkey
	}
	if env.NatsServerName == "" {
		env.NatsServerName, _ = os.Hostname()
	}
	if env.NatsClusterName == "" {
		env.NatsClusterName = DefaultNatsClusterName
	}
	if env.NatsPort == 0 {
		env.NatsPort = DefaultNatsPort
	}
	if env.NatsClusterPort == 0 {
		env.NatsClusterPort = DefaultNatsClusterPort
	}
	if env.NatsMonitorPort == 0 {
		env.NatsMonitorPort = DefaultNatsMonitorPort
	}
	if env.JetStreamDir == "" {
		env.JetStreamDir = filepath.Join(env.LogsDataDir, "jetstream")
	}
	if env.JetStreamReplicas == 0 {
		env.JetStreamReplicas = 3
		if env.EmbedsBroker() {
			env.JetStreamReplicas = 1
		}
	}
	if env.IngestStreamMaxBytes == 0 {
		env.IngestStreamMaxBytes = DefaultIngestStreamMaxBytes
	}
	if env.IngestL0TargetBytes == 0 {
		env.IngestL0TargetBytes = DefaultIngestL0TargetBytes
	}
	if env.IngestL0MaxAge == 0 {
		env.IngestL0MaxAge = DefaultIngestL0MaxAge
	}
}

// NatsRouteList splits NATS_ROUTES.
func (e EnvVariables) NatsRouteList() []string {
	var routes []string
	for _, route := range strings.Split(e.NatsRoutes, ",") {
		if trimmed := strings.TrimSpace(route); trimmed != "" {
			routes = append(routes, trimmed)
		}
	}
	return routes
}
