package main

import (
	"context"
	"crypto/tls"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"obzly/internal/broker"
	"obzly/internal/cache"
	"obzly/internal/config"
	"obzly/internal/downdetect"
	access_tokens "obzly/internal/features/access_tokens"
	"obzly/internal/features/aiusage"
	"obzly/internal/features/alerting"
	"obzly/internal/features/api_keys"
	"obzly/internal/features/audit_logs"
	control_plane_controllers "obzly/internal/features/control_plane/controllers"
	control_plane_services "obzly/internal/features/control_plane/services"
	correlation_api "obzly/internal/features/correlation/api"
	correlation_core "obzly/internal/features/correlation/core"
	correlation_repository "obzly/internal/features/correlation/repository"
	correlation_worker "obzly/internal/features/correlation/worker"
	dashboards_controllers "obzly/internal/features/dashboards/controllers"
	dashboards_services "obzly/internal/features/dashboards/services"
	"obzly/internal/features/datamigration"
	"obzly/internal/features/disk"
	"obzly/internal/features/inventory"
	logs_cleanup "obzly/internal/features/logs/cleanup"
	logs_core "obzly/internal/features/logs/core"
	logs_jetstream "obzly/internal/features/logs/jetstream"
	logs_livetail "obzly/internal/features/logs/livetail"
	logs_parquetstore "obzly/internal/features/logs/parquetstore"
	logs_querying "obzly/internal/features/logs/querying"
	logs_receiving "obzly/internal/features/logs/receiving"
	mcp_server "obzly/internal/features/mcp"
	metrics_cleanup "obzly/internal/features/metrics/cleanup"
	metrics_core "obzly/internal/features/metrics/core"
	metrics_jetstream "obzly/internal/features/metrics/jetstream"
	metrics_parquetstore "obzly/internal/features/metrics/parquetstore"
	metrics_promapi "obzly/internal/features/metrics/promapi"
	metrics_querying "obzly/internal/features/metrics/querying"
	"obzly/internal/features/oauth"
	"obzly/internal/features/organizations"
	"obzly/internal/features/orgplan"
	"obzly/internal/features/otlp"
	otlp_logs "obzly/internal/features/otlp/logs"
	otlp_metrics "obzly/internal/features/otlp/metrics"
	otlp_traces "obzly/internal/features/otlp/traces"
	projects_controllers "obzly/internal/features/projects/controllers"
	projects_services "obzly/internal/features/projects/services"
	prometheus_remotewrite "obzly/internal/features/prometheus/remotewrite"
	"obzly/internal/features/servicemap"
	"obzly/internal/features/sharedconfig"
	spans_cleanup "obzly/internal/features/spans/cleanup"
	spans_core "obzly/internal/features/spans/core"
	spans_jetstream "obzly/internal/features/spans/jetstream"
	spans_parquetstore "obzly/internal/features/spans/parquetstore"
	"obzly/internal/features/synthetics"
	system_catalog "obzly/internal/features/system/catalog"
	system_healthcheck "obzly/internal/features/system/healthcheck"
	system_integrity "obzly/internal/features/system/integrity"
	system_selfmetrics "obzly/internal/features/system/selfmetrics"
	system_selftracing "obzly/internal/features/system/selftracing"
	system_siteinfo "obzly/internal/features/system/siteinfo"
	system_upgradestatus "obzly/internal/features/system/upgradestatus"
	system_usage "obzly/internal/features/system/usage"
	traces_jaeger "obzly/internal/features/traces/jaeger"
	traces_querying "obzly/internal/features/traces/querying"
	users_controllers "obzly/internal/features/users/controllers"
	mfa_controllers "obzly/internal/features/users/mfa/controllers"
	users_middleware "obzly/internal/features/users/middleware"
	users_services "obzly/internal/features/users/services"
	sso_controllers "obzly/internal/features/users/sso/controllers"
	"obzly/internal/query"
	"obzly/internal/query/partcache"
	"obzly/internal/storage"
	"obzly/internal/storage/segments"
	"obzly/internal/telemetry"
	"obzly/internal/util/bodylimit"
	cache_utils "obzly/internal/util/cache"
	env_utils "obzly/internal/util/env"
	"obzly/internal/util/leaderlock"
	"obzly/internal/util/logger"
	"obzly/internal/util/requestlog"
	"obzly/internal/util/security_headers"
	"obzly/internal/util/tlsutil"
	_ "obzly/swagger" // swagger docs

	// Object storage drivers: s3blob covers AWS S3 and S3-compatible endpoints
	// (MinIO). GCS/Azure need their driver blank-imported here too:
	_ "gocloud.dev/blob/s3blob"
	//   _ "gocloud.dev/blob/gcsblob"
	//   _ "gocloud.dev/blob/azureblob"

	"github.com/gin-contrib/cors"
	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"go.opentelemetry.io/otel/attribute"
	"golang.org/x/term"
	"google.golang.org/grpc"
)

// @title Obzly Backend API
// @version 1.0
// @description API for Obzly
// @termsOfService http://swagger.io/terms/

// @host localhost:4005
// @BasePath /api/v1
// @schemes http
func main() {
	if len(os.Args) > 1 && os.Args[1] == "reconcile" {
		os.Exit(runReconcile(os.Args[2:]))
	}

	log := logger.GetLogger()
	config.StartListeningForShutdownSignal()

	mode := config.GetEnv().ServiceMode
	log.Info("Starting obzly", "serviceMode", mode)

	// First, so a worker replica and a slow startup are observable too.
	startMetricsServer(log)
	// Before any DuckDB handle opens.
	segments.SetMaxSpillSize(config.GetEnv().QuerierSpillMaxSize)

	if mode.IsBroker() {
		runBroker(log)
		return
	}
	if mode.IsQuerier() {
		runQuerier(log)
		return
	}

	// Before setUpDependencies and before any request or worker runs: callers
	// fetch the repository lazily, so this is the one place the storage engine is
	// wired. A control plane never owns segments, so it wires none.
	if mode.RunsSignalStorage() {
		setUpLocalWriter(log, mode)
		setUpLogsStorage(log)
		setUpSpansStorage(log)
		setUpMetricsStorage(log)
		setUpCorrelationStorage(log)

		// A no-op without a cold tier and cheap with one, so it runs in every mode.
		go runPeriodicColdCacheEviction(log)
	}

	setUpDependencies()

	cache_utils.TestCacheConnection()

	if mode.RunsSignalStorage() {
		testLogsStorageConnection(log)
		testSpansStorageConnection(log)
		testMetricsStorageConnection(log)
		testCorrelationStorageConnection(log)
	}

	// Migrations and admin bootstrap run in every mode; both are safe to run on
	// several replicas at once (leader lock, single-root-admin index).
	runMigrations(log)
	segments.SetPartialDownloads(config.GetEnv().QueryPartialDownloads == nil || *config.GetEnv().QueryPartialDownloads)
	if err := system_upgradestatus.RecordRelease(); err != nil {
		log.Warn("Failed to record this release's schema", "error", err)
	}

	if mode.RunsSignalStorage() {
		startSelfTracing(log)
	}

	if config.GetEnv().UsesJetStream() {
		setUpBroker(log)
	}
	if config.GetEnv().UsesRemoteQuery() {
		registry := query.NewRegistry(broker.Default().Conn, config.GetEnv().QuerierToken,
			queryResultsClientTLS(log), log)
		query.SetDefaultRegistry(registry)
		go registry.Executors() // first discovery now, not on the first search
		log.Info("Distributed query on: searches use SERVICE_MODE=querier replicas when any run")
	}

	system_selfmetrics.Start(context.Background(), log, system_selfmetrics.Options{
		Catalog:              mode.RunsBackgroundWorkers(),
		ObjectStorageEnabled: config.GetEnv().ObjectStorageURL != "",
		JetStream:            mode.RunsBackgroundWorkers() && config.GetEnv().UsesJetStream(),
	})

	// Control plane only, after runMigrations: the signing-key table must exist.
	if mode.IsControlPlane() {
		if err := control_plane_services.EnsureSigningKey(); err != nil {
			log.Error("Failed to set up control-plane signing key", "error", err)
			os.Exit(1)
		}
	}

	err := users_services.GetUserService().CreateInitialAdmin()
	if err != nil {
		log.Error("Failed to create initial admin", "error", err)
		os.Exit(1)
	}

	if err := users_services.GetUserService().SyncRootAdminEmail(); err != nil {
		log.Error("Failed to sync root admin email from ADMIN_EMAIL", "error", err)
		os.Exit(1)
	}

	handlePasswordReset(log)

	orgplan.Start(context.Background(), log)
	// Monitor alerts ride the shared-config document: built here on a control
	// plane, kept as managed rules on a Site.
	sharedconfig.SetMonitorAlerts(synthetics.GetService().MonitorAlertsFor, alerting.ApplyManagedRules)
	sharedconfig.SetMaintenance(synthetics.GetService().MaintenanceFor, alerting.ApplyManagedSilences)
	synthetics.SetCloudTokenSigner(control_plane_services.SignCloudToken)
	alerting.SetAICostSource(aiusage.GetAIUsageService().EvaluateBudgetRule)
	sharedconfig.Start(context.Background(), log)

	if mode.RunsBackgroundWorkers() {
		runBackgroundCleanupWorkers(log)
		if config.GetEnv().UsesJetStream() {
			startIngestConsumers(log)
		}
	}

	if !mode.ServesHTTP() {
		log.Info("SERVICE_MODE=worker: background workers started, not serving HTTP/gRPC")
		waitForShutdownSignal(log)
		return
	}

	go generateSwaggerDocs(log)

	gin.SetMode(gin.ReleaseMode)
	// Not gin.Default(): its gin.Logger() logs raw query strings (?api_key=...)
	// and is open to log injection. requestlog redacts.
	ginApp := gin.New()
	ginApp.Use(gin.Recovery())
	ginApp.Use(requestlog.Middleware(log))
	ginApp.Use(telemetry.GinMiddleware())

	// Without this Gin trusts X-Forwarded-For from anyone, so a client could spoof
	// ClientIP() (project IP allow-lists, stored client_ip).
	if err := ginApp.SetTrustedProxies(trustedProxyList(config.GetEnv().TrustedProxies)); err != nil {
		log.Error("Invalid TRUSTED_PROXIES", "error", err)
		os.Exit(1)
	}

	ginApp.Use(security_headers.Middleware())

	// /sites/ (the control-plane proxy) is excluded: a Site's response may already
	// be gzipped and would be corrupted by a second gzip.
	ginApp.Use(gzip.Gzip(
		gzip.DefaultCompression,
		gzip.WithExcludedExtensions(
			[]string{".png", ".gif", ".jpeg", ".jpg", ".ico", ".svg", ".pdf", ".mp4"},
		),
		gzip.WithExcludedPathsRegexs([]string{"^/sites/", "^/mcp$"}),
	))

	enableCors(ginApp)
	setUpRoutes(ginApp)

	// Ingest and the OTLP gRPC listener only exist on a replica that holds signal
	// data; a control plane proxies those to Sites.
	var grpcServer *grpc.Server
	if mode.RunsSignalStorage() {
		runIngestPipeline(log)
	}
	// MCP for AI assistants: tools call this router's REST API as the user.
	oauth.GetOAuthController().RegisterPublicRoutes(ginApp)
	ginApp.Any("/mcp", gin.WrapH(mcp_server.NewHandler(ginApp, mode.IsControlPlane(), config.GetEnv().AppVersion)))
	mountFrontend(ginApp)
	if mode.RunsSignalStorage() {
		grpcServer = startOtlpGrpcServer(log)
	}

	startServerWithGracefulShutdown(log, ginApp, grpcServer)
}

// trustedProxyList turns TRUSTED_PROXIES into Gin's slice; empty means nil,
// which trusts no proxy.
func trustedProxyList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	var proxies []string
	for _, entry := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			proxies = append(proxies, trimmed)
		}
	}
	return proxies
}

// startMetricsServer serves Prometheus /metrics on METRICS_ADDR, a listener
// the control plane never proxies. A bind failure exits.
func startMetricsServer(log *slog.Logger) {
	env := config.GetEnv()
	telemetry.BuildInfo.WithLabelValues(env.AppVersion, string(env.ServiceMode)).Set(1)

	// A broker or query executor never uses Postgres.
	if !env.ServiceMode.IsBroker() && !env.ServiceMode.IsQuerier() {
		sqlDB, err := storage.GetDb().DB()
		if err != nil {
			log.Error("Failed to get database handle for metrics", "error", err)
			os.Exit(1)
		}
		telemetry.RegisterDBStats(sqlDB)
	}

	if !env.MetricsEnabled() {
		log.Info("Metrics listener disabled (METRICS_ADDR=off)")
		return
	}
	listener, err := telemetry.Listen(env.MetricsAddr)
	if err != nil {
		log.Error("Failed to start metrics listener", "addr", env.MetricsAddr, "error", err)
		os.Exit(1)
	}
	telemetry.Serve(listener, log)
	log.Info("Metrics listening", "addr", env.MetricsAddr)
}

// runBroker is SERVICE_MODE=broker: only the NATS JetStream server, until
// SIGTERM, then lame-duck shutdown so clients move to the other brokers.
// Kubernetes probes use the server's own /healthz on NATS_MONITOR_PORT.
func runBroker(log *slog.Logger) {
	env := config.GetEnv()
	srv, err := broker.StartServer(broker.ServerConfig{
		Name:        env.NatsServerName,
		StoreDir:    env.JetStreamDir,
		Token:       env.NatsToken,
		ClientPort:  env.NatsPort,
		ClusterPort: env.NatsClusterPort,
		MonitorPort: env.NatsMonitorPort,
		ClusterName: env.NatsClusterName,
		Routes:      env.NatsRouteList(),
	}, log)
	if err != nil {
		log.Error("Failed to start the NATS broker", "error", err)
		os.Exit(1)
	}
	log.Info("NATS broker running", "name", env.NatsServerName, "routes", len(env.NatsRouteList()))

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit
	log.Info("Shutdown signal received, entering lame duck mode")
	srv.LameDuckShutdown()
	srv.WaitForShutdown()
	log.Info("NATS broker stopped")
}

// runQuerier runs this process as a query executor: fragments arrive over
// NATS, segments come from object storage, results leave over gRPC.
func runQuerier(log *slog.Logger) {
	env := config.GetEnv()
	var resultsTLS *tls.Config
	if env.QueryResultsTLSCertFile != "" {
		var err error
		resultsTLS, err = tlsutil.ServerConfig(env.QueryResultsTLSCertFile, env.QueryResultsTLSKeyFile)
		if err != nil {
			log.Error("Failed to load the query results TLS certificate", "error", err)
			os.Exit(1)
		}
	}
	store, err := segments.NewObjectStore(context.Background(), env.ObjectStorageURL)
	if err != nil {
		log.Error("Failed to open object storage bucket", "url", env.ObjectStorageURL, "error", err)
		os.Exit(1)
	}
	client, err := broker.Connect(env.NatsURL, env.NatsToken, "obzly-querier-"+env.NatsServerName)
	if err != nil {
		log.Error("Failed to connect to the NATS broker", "error", err)
		os.Exit(1)
	}
	resultsDir := filepath.Join(env.LogsDataDir, "query-results")
	db, err := openQueryExecutorDB(
		segments.HandleMemoryLimit(queryExecutorMemoryFraction), filepath.Join(resultsDir, "spill"),
		env.QuerierSpillMaxSize,
	)
	if err != nil {
		log.Error("Failed to open the query executor's DuckDB", "error", err)
		os.Exit(1)
	}
	telemetry.RegisterDuckDB("query_executor", db)
	go runPeriodicColdCacheEviction(log)
	// The id file follows the disk: with a persistent cache volume this
	// executor keeps its share of segments, and its cache, across restarts.
	affinity, err := segments.LoadOrCreateWriterID(env.LogsDataDir)
	if err != nil {
		log.Error("Failed to load the query executor's affinity id", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if env.QuerierWarmHours > 0 {
		// Lists executors only, never fetches results: no client TLS needed.
		registry := query.NewRegistry(client.Conn, env.QuerierToken, nil, log)
		cacheDir := filepath.Join(env.LogsDataDir, "cache")
		go warmQuerierCache(ctx, log, registry, store, affinity)
		go func() {
			err := query.ServePrefetch(ctx, client.Conn, registry, affinity, func(record *segments.SegmentRecord) {
				segments.WarmCache(ctx, store, cacheDir, []*segments.SegmentRecord{record}, record.SizeBytes)
			}, log)
			if err != nil {
				log.Warn("Pre-fetching uploaded segments is off", "error", err)
			}
		}()
	}
	err = query.Serve(ctx, query.ServeConfig{
		Conn:          client.Conn,
		Executor:      query.NewLocalExecutor(env.NatsServerName, db, store, filepath.Join(env.LogsDataDir, "cache")),
		ResultsDir:    resultsDir,
		ListenAddr:    env.QuerierGRPCAddr,
		AdvertiseAddr: env.QuerierAdvertiseAddr,
		Token:         env.QuerierToken,
		Concurrency:   env.QuerierConcurrency,
		Log:           log,
		Affinity:      affinity,
		TLS:           resultsTLS,
	})
	client.Close()
	// A clean Close is what deletes DuckDB's spill files.
	if closeErr := db.Close(); closeErr != nil {
		log.Warn("Failed to close the query executor's DuckDB", "error", closeErr)
	}
	if err != nil {
		log.Error("Query executor stopped", "error", err)
		os.Exit(1)
	}
	log.Info("Query executor stopped")
}

// queryExecutorMemoryFraction is the executor DuckDB's share of the pod's
// memory limit; DuckDB's default 80% leaves Go, gRPC and the cache too little.
const queryExecutorMemoryFraction = 0.6

// openQueryExecutorDB opens the executor's DuckDB capped at limit ("" keeps
// DuckDB's default), spilling to spillDir up to maxSpill.
func openQueryExecutorDB(limit, spillDir, maxSpill string) (*sql.DB, error) {
	// DuckDB deletes its spill files only on a clean Close, so an executor
	// that was OOM-killed left them on the persistent volume.
	if err := os.RemoveAll(spillDir); err != nil {
		return nil, fmt.Errorf("failed to clear the duckdb spill directory: %w", err)
	}
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, err
	}
	if _, err := segments.ConfigureMemory(db, limit, spillDir); err != nil {
		_ = db.Close()
		return nil, err
	}
	if limit != "" && maxSpill != "" {
		setting := fmt.Sprintf("SET max_temp_directory_size = '%s'", strings.ReplaceAll(maxSpill, "'", "''"))
		if _, err := db.Exec(setting); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("failed to cap the duckdb spill size: %w", err)
		}
	}
	if err := segments.RegisterNormalizationFunctions(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// querierWarmPeerWait: after this executor is discoverable, how long it
// waits for the others restarting with it before deciding its share.
const querierWarmPeerWait = 15 * time.Second

// warmQuerierCache downloads this executor's share of the last
// QUERIER_WARM_HOURS of segments into its cache, up to half of it (the rest
// stays for what queries bring in), so the first wide search after a
// restart doesn't read everything from object storage.
func warmQuerierCache(
	ctx context.Context, log *slog.Logger, registry *query.Registry, store *segments.ObjectStore, affinity string,
) {
	env := config.GetEnv()
	discoverable := func() bool {
		for _, executor := range registry.Executors() {
			if withAffinity, ok := executor.(interface{ Affinity() string }); ok &&
				withAffinity.Affinity() == affinity {
				return true
			}
		}
		return false
	}
	for !discoverable() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(querierWarmPeerWait):
	}

	started := time.Now()
	records, err := (&segments.SegmentRepository{}).ListUploadedSince(
		time.Now().UTC().Add(-time.Duration(env.QuerierWarmHours) * time.Hour))
	if err != nil {
		log.Warn("Failed to list segments to warm the query cache", "error", err)
		return
	}
	owned := query.Owned(records, registry.Executors(), affinity)
	result := segments.WarmCache(ctx, store, filepath.Join(env.LogsDataDir, "cache"), owned,
		env.ColdCacheMaxSizeMB<<20/2)
	log.Info("Query cache warmed", "hours", env.QuerierWarmHours, "segments", len(owned),
		"downloaded", result.Downloaded, "alreadyCached", result.Cached, "failed", result.Failed,
		"bytes", result.Bytes, "duration", time.Since(started).Round(time.Second))
}

// setUpBroker connects to the JetStream ingest buffer (in-process in
// SERVICE_MODE=all) and creates the ingest streams. Failure stops the replica.
func setUpBroker(log *slog.Logger) {
	env := config.GetEnv()
	name := fmt.Sprintf("obzly-%s-%s", env.ServiceMode, env.NatsServerName)

	var client *broker.Client
	var err error
	if env.EmbedsBroker() {
		client, err = broker.StartInProcess(env.NatsServerName, env.JetStreamDir, log)
	} else {
		client, err = broker.Connect(env.NatsURL, env.NatsToken, name)
	}
	if err != nil {
		log.Error("Failed to connect to the NATS broker", "error", err)
		os.Exit(1)
	}

	settings := broker.StreamSettings{Replicas: env.JetStreamReplicas, MaxBytes: env.IngestStreamMaxBytes}
	if err := client.EnsureIngestStreamsWithRetry(settings, log); err != nil {
		log.Error("Failed to set up the ingest streams", "error", err)
		os.Exit(1)
	}
	broker.SetDefault(client)
	segments.SetUploadNotifier(func(record *segments.SegmentRecord) { query.PublishUploaded(client.Conn, record) })
	log.Info("JetStream ingest buffer connected",
		"embedded", env.EmbedsBroker(), "replicas", env.JetStreamReplicas)
}

// queryHandleMemoryFraction: each search DuckDB handle's share of the
// container's memory limit (DuckDB would default each to 80%).
const queryHandleMemoryFraction = 0.4

// workerQuerierMemoryLimit caps a worker's logs search DuckDB; with the
// compactor's 1 GB it fits the 3 GiB deploy/kubernetes/eks/ha gives a worker.
const workerQuerierMemoryLimit = "1GB"

// ingestConsumers stops the JetStream shard runners and waits for them to
// release their shards (flush, upload, ack). Nil unless they run.
var ingestConsumers func()

// ingestAckMargin: AckWait is the L0 max-age plus this, time to flush and
// upload before JetStream redelivers.
const ingestAckMargin = 10 * time.Minute

// startIngestConsumers makes this all/worker replica a segment writer for its
// share of each signal's ingest shards.
func startIngestConsumers(log *slog.Logger) {
	env := config.GetEnv()
	logsStore, okLogs := logs_core.GetLogCoreRepository().(*logs_parquetstore.LogParquetStore)
	aiStore, okAI := logs_core.GetAIRepository().(*logs_parquetstore.LogParquetStore)
	spansStore, okSpans := spans_core.GetSpanCoreRepository().(*spans_parquetstore.SpanParquetStore)
	metricsStore, okMetrics := metrics_core.GetMetricCoreRepository().(*metrics_parquetstore.MetricParquetStore)
	if !okLogs || !okAI || !okSpans || !okMetrics {
		log.Error("JetStream ingest needs the Parquet stores")
		os.Exit(1)
	}
	logsWriter := mustDurableWriter(log, "logs", logsStore.NewDurableWriter)
	aiWriter := mustDurableWriter(log, "AI logs", aiStore.NewDurableWriter)
	spansWriter := mustDurableWriter(log, "spans", spansStore.NewDurableWriter)
	metricsWriter := mustDurableWriter(log, "metrics", metricsStore.NewDurableWriter)

	target, maxAge := env.IngestL0TargetBytes, env.IngestL0MaxAge
	sinks := map[string]broker.ShardHandler{
		broker.SignalLogs:    logs_jetstream.NewSink(logsWriter, aiWriter, target, maxAge, log),
		broker.SignalSpans:   spans_jetstream.NewSink(spansWriter, target, maxAge, log),
		broker.SignalMetrics: metrics_jetstream.NewSink(metricsWriter, metricsWriter.Series(), target, maxAge, log),
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for signal, sink := range sinks {
		runner, err := broker.NewShardRunner(broker.Default(), cache.GetCache(), signal,
			maxAge+ingestAckMargin, sink, log)
		if err != nil {
			log.Error("Failed to set up an ingest consumer", "signal", signal, "error", err)
			os.Exit(1)
		}
		// Compaction follows the same shard leases.
		broker.RegisterRunner(runner)
		wg.Add(1)
		go func() {
			defer wg.Done()
			runner.Run(ctx)
		}()
	}
	ingestConsumers = func() {
		cancel()
		wg.Wait()
	}
	log.Info("JetStream ingest consumers started", "l0TargetBytes", target, "l0MaxAge", maxAge)
}

// mustDurableWriter exits when a store's durable writer can't be created.
func mustDurableWriter[W any](log *slog.Logger, name string, newWriter func() (W, error)) W {
	writer, err := newWriter()
	if err != nil {
		log.Error("Failed to set up a JetStream writer", "stream", name, "error", err)
		os.Exit(1)
	}
	return writer
}

func stopIngestConsumers(log *slog.Logger) {
	if ingestConsumers == nil {
		return
	}
	ingestConsumers()
	log.Info("JetStream ingest consumers released their shards")
}

// selfTracing is set once startSelfTracing installs a tracer provider.
var selfTracing *system_selftracing.Tracing

// selfTracingShutdownTimeout bounds the final span flush at shutdown.
const selfTracingShutdownTimeout = 5 * time.Second

// startSelfTracing traces SELF_TRACING_SAMPLE_RATE of this replica's requests
// and background passes into the system project, via the local spans store.
// A failure disables tracing, never the replica.
func startSelfTracing(log *slog.Logger) {
	env := config.GetEnv()
	if env.SelfTracingSampleRate == 0 {
		log.Info("Self-tracing disabled (SELF_TRACING_SAMPLE_RATE=0)")
		return
	}

	project, err := projects_services.GetProjectService().EnsureSystemProject()
	if err != nil {
		log.Error("Self-tracing disabled: failed to set up the system project", "error", err)
		return
	}

	selfTracing = system_selftracing.Start(system_selftracing.Options{
		ProjectID:  project.ID,
		SampleRate: env.SelfTracingSampleRate,
		Attributes: []attribute.KeyValue{
			attribute.String("service.version", env.AppVersion),
			attribute.String("obzly.service_mode", string(env.ServiceMode)),
			attribute.String("obzly.site_id", env.SiteID),
		},
	})
	log.Info("Self-tracing enabled", "sampleRate", env.SelfTracingSampleRate, "projectId", project.ID)
}

// stopSelfTracing writes buffered spans into the spans store. Call it
// before the final segment flush.
func stopSelfTracing(log *slog.Logger) {
	if selfTracing == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), selfTracingShutdownTimeout)
	defer cancel()
	if err := selfTracing.Shutdown(ctx); err != nil {
		log.Warn("Failed to flush self-tracing spans", "error", err)
	}
}

// waitForShutdownSignal is the SERVICE_MODE=worker counterpart of
// startServerWithGracefulShutdown: block until a shutdown signal, then flush.
func waitForShutdownSignal(log *slog.Logger) {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit
	log.Info("Shutdown signal received")

	stopSelfTracing(log)
	flushAllSegmentsOnce(log)
	uploadOwnSegmentsBeforeExit(log)
	stopIngestConsumers(log)
	broker.ShutdownDefault()

	log.Info("Worker shut down")
}

// startOtlpGrpcServer starts the shared OTLP gRPC receiver on OtlpGrpcPort
// (default 4317), with every signal's service registered. A bind failure exits.
func startOtlpGrpcServer(log *slog.Logger) *grpc.Server {
	env := config.GetEnv()
	port := env.OtlpGrpcPort

	var tlsConfig *tls.Config
	if env.OtlpGrpcTLSCertFile != "" {
		var err error
		tlsConfig, err = tlsutil.ServerConfig(env.OtlpGrpcTLSCertFile, env.OtlpGrpcTLSKeyFile)
		if err != nil {
			log.Error("Failed to load the OTLP gRPC TLS certificate", "error", err)
			os.Exit(1)
		}
	} else {
		log.Warn("OTLP gRPC is plaintext: API keys cross the network unencrypted unless TLS is " +
			"terminated in front of it; set OTLP_GRPC_TLS_CERT_FILE and OTLP_GRPC_TLS_KEY_FILE to serve TLS")
	}

	listener, err := otlp.Listen(port)
	if err != nil {
		log.Error("Failed to start OTLP gRPC server", "error", err)
		os.Exit(1)
	}

	grpcServer := otlp.NewGRPCServer(tlsConfig)
	otlp_logs.RegisterGRPCServer(grpcServer)
	otlp_traces.RegisterGRPCServer(grpcServer)
	otlp_metrics.RegisterGRPCServer(grpcServer)

	go func() {
		log.Info("OTLP gRPC server listening", "port", port, "tls", tlsConfig != nil)
		if err := grpcServer.Serve(listener); err != nil {
			log.Error("OTLP gRPC server stopped unexpectedly", "error", err)
		}
	}()

	return grpcServer
}

// queryResultsClientTLS is how coordinators dial executors' result
// listeners: nil (plaintext) unless QUERY_RESULTS_TLS_* asks for TLS.
func queryResultsClientTLS(log *slog.Logger) *tls.Config {
	env := config.GetEnv()
	if !env.QueryResultsClientTLS() {
		return nil
	}
	tlsConfig, err := tlsutil.ClientConfig(env.QueryResultsTLSCAFile, env.QueryResultsTLSServerName)
	if err != nil {
		log.Error("Invalid query results TLS settings", "error", err)
		os.Exit(1)
	}
	return tlsConfig
}

func startServerWithGracefulShutdown(log *slog.Logger, app *gin.Engine, grpcServer *grpc.Server) {
	host := ""
	if config.GetEnv().EnvMode == env_utils.EnvModeDevelopment {
		// Localhost in dev avoids a Windows firewall prompt on each run.
		host = "127.0.0.1"
	}

	srv := &http.Server{
		Addr:    host + ":" + config.GetEnv().HTTPPort,
		Handler: app,
		// ReadHeaderTimeout is the slowloris guard; ReadTimeout is generous for large
		// ingest batches. No WriteTimeout: Live Tail holds SSE responses open.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			// Exit rather than run without an HTTP server: a busy or bad HTTP_PORT
			// must be loud.
			log.Error("HTTP server could not start", "addr", srv.Addr, "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit
	log.Info("Shutdown signal received")

	// /system/ready already fails (config.IsShouldShutdown), but the load
	// balancer only notices at its next probe; keep serving until it has
	// deregistered this replica, or requests arriving meanwhile are refused.
	if drain := time.Duration(config.GetEnv().ShutdownDrainSeconds) * time.Second; drain > 0 {
		log.Info("Draining before shutdown", "delay", drain)
		time.Sleep(drain)
	}

	// Stop accepting work in every protocol before flushing: a request arriving
	// after the flush would write rows that are never flushed. 10s grace for
	// in-flight requests.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Error("Server forced to shutdown:", "error", err)
	}
	// nil on a control plane, which never starts the OTLP gRPC listener.
	if grpcServer != nil {
		stopGRPCWithDeadline(log, grpcServer, grpcShutdownTimeout)
	}

	// A control plane has no ingest pipeline or segment buffers; these steps would
	// panic on its unwired storage engine.
	if !config.GetEnv().ServiceMode.RunsSignalStorage() {
		log.Info("Server gracefully stopped")
		return
	}

	// Before the flush below, so buffered self-trace spans land in segments.
	stopSelfTracing(log)

	// Flush buffered rows, or they are lost. Every producer is stopped by now.
	flushAllSegmentsOnce(log)

	// Last chance for this replica's newest segments: on an emptyDir they
	// die with the pod.
	uploadOwnSegmentsBeforeExit(log)

	stopIngestConsumers(log)
	broker.ShutdownDefault()

	log.Info("Server gracefully stopped")
}

// grpcShutdownTimeout bounds GracefulStop, which otherwise waits forever on
// a client that keeps a stream open.
const grpcShutdownTimeout = 10 * time.Second

// stopGRPCWithDeadline lets in-flight OTLP exports finish for up to timeout,
// then closes whatever is left so the buffered-row flush that follows still
// runs inside the pod's termination grace period.
func stopGRPCWithDeadline(log *slog.Logger, server *grpc.Server, timeout time.Duration) {
	stopped := make(chan struct{})
	go func() {
		server.GracefulStop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(timeout):
		log.Warn("OTLP gRPC server did not stop gracefully in time, forcing", "timeout", timeout)
		server.Stop()
		<-stopped
	}
}

// setUpRoutes mounts this replica's HTTP surface: setUpIdentityRoutes (users,
// SSO, MFA, settings) in every HTTP mode, and setUpSiteRoutes (signal data)
// only where RunsSignalStorage(). Both share one protected group so the MFA
// ordering rule lives in one place.
func setUpRoutes(r *gin.Engine) {
	mode := config.GetEnv().ServiceMode
	v1 := r.Group("/api/v1")

	// Swagger UI only in development: it maps every endpoint, unauthenticated.
	if config.GetEnv().EnvMode == env_utils.EnvModeDevelopment {
		v1.GET("/docs/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	}

	// Bodies are capped on every /api/v1 route but logs receiving, which
	// keeps its own larger limit (mounted on v1 itself in setUpSiteRoutes);
	// OTLP and remote write live outside /api/v1 with theirs.
	limited := v1.Group("", bodylimit.Middleware(int64(config.GetEnv().APIMaxBodyMB)<<20))
	protected := setUpIdentityRoutes(r, limited)
	// Obzly settings → Email and Notification channels, in every mode.
	alerting.GetSettingsController().RegisterRoutes(protected)
	sharedconfig.GetController().RegisterRoutes(protected)
	sharedconfig.GetController().RegisterPublicRoutes(r)

	if mode.RunsSignalStorage() {
		setUpSiteRoutes(r, v1, protected)
	}
	if mode.IsControlPlane() {
		setUpControlPlaneRoutes(r, protected)
	}
}

// setUpControlPlaneRoutes mounts the Site registry and grants API, the public
// JWKS endpoint and the Site reverse proxy. It must run before mountFrontend
// so /sites/:siteId/*path wins over the SPA fallback.
func setUpControlPlaneRoutes(r *gin.Engine, protected *gin.RouterGroup) {
	control_plane_controllers.GetJWKSController().RegisterRoutes(r)
	control_plane_controllers.GetSiteController().RegisterRoutes(protected)
	control_plane_controllers.GetGrantController().RegisterRoutes(protected)
	control_plane_controllers.GetSSOSiteMappingController().RegisterRoutes(protected)
	organizations.GetController().RegisterRoutes(protected)
	organizations.GetController().RegisterSCIMRoutes(r.Group("/scim/v2"))
	organizations.GetController().RegisterSitePlanRoutes(r)
	control_plane_controllers.GetSigningKeyController().RegisterRoutes(protected)
	synthetics.GetController().RegisterRoutes(protected)
	synthetics.GetProbeController().RegisterRoutes(r)

	// The proxy isn't on `protected` (rooted at /api/v1): a Site's /api/v1 is at
	// /sites/{id}/api/v1, so it gets its own group with the same middlewares.
	authMiddleware := users_middleware.AuthMiddleware(users_services.GetUserService())
	control_plane_controllers.GetProxyController().
		RegisterRoutes(r, authMiddleware, users_middleware.RequireMFAIfEnforced())
}

// setUpIdentityRoutes mounts the identity/admin surface and returns the
// protected group (Bearer auth + MFA enforcement already applied) for the
// caller to keep registering data routes onto.
func setUpIdentityRoutes(r *gin.Engine, v1 *gin.RouterGroup) *gin.RouterGroup {
	// Public routes: only user auth.
	userController := users_controllers.GetUserController()
	userController.RegisterRoutes(v1)
	// Login-time MFA endpoints, authorized by the challenge token.
	mfa_controllers.GetMFAController().RegisterPublicRoutes(v1)
	downdetect.GetDowndetectController().RegisterRoutes(v1)
	system_healthcheck.GetHealthcheckController().RegisterRoutes(v1)
	// Unauthenticated, secret-free "who am I" for registering this backend as a Site.
	system_siteinfo.GetController().RegisterRoutes(v1)

	userService := users_services.GetUserService()
	authMiddleware := users_middleware.AuthMiddleware(userService)

	protected := v1.Group("")
	protected.Use(authMiddleware)

	// Self-service MFA routes go BEFORE the MFA-enforcement middleware: they're how
	// an admin satisfies it. Everything registered after requires MFA when enforced.
	mfa_controllers.GetMFAController().RegisterProtectedRoutes(protected)
	protected.Use(users_middleware.RequireMFAIfEnforced())

	audit_logs.GetAuditLogController().RegisterRoutes(protected)
	userController.RegisterProtectedRoutes(protected)
	users_controllers.GetSettingsController().RegisterRoutes(protected)
	sso_controllers.GetConnectorController().RegisterRoutes(protected)
	users_controllers.GetManagementController().RegisterRoutes(protected)
	users_controllers.GetUserPlanController().RegisterRoutes(protected)
	access_tokens.GetAccessTokenController().RegisterRoutes(protected)
	oauth.GetOAuthController().RegisterRoutes(protected)
	// Served everywhere: a control plane reports its own schema, a Site
	// adds its rewrite and segment backlog (the service checks the mode).
	system_upgradestatus.GetController().RegisterRoutes(protected)

	return protected
}

// setUpSiteRoutes mounts everything that reads or writes signal data -
// the half a control plane never serves itself.
func setUpSiteRoutes(r *gin.Engine, v1 *gin.RouterGroup, protected *gin.RouterGroup) {
	logs_receiving.GetReceivingController().RegisterRoutes(v1)

	// OTLP/HTTP receivers at the root (POST /v1/logs, /v1/traces), the default
	// path of every OTLP exporter.
	otlp_logs.GetHTTPController().RegisterRoutes(r)
	otlp_traces.GetHTTPController().RegisterRoutes(r)
	otlp_metrics.GetHTTPController().RegisterRoutes(r)

	// Prometheus remote write: root-mounted with API-key auth, like OTLP/HTTP.
	prometheus_remotewrite.GetHTTPController().RegisterRoutes(r)

	// Jaeger-compatible query API: its own API-key auth, so on the root router.
	traces_jaeger.GetJaegerController().RegisterRoutes(r)

	// Prometheus-compatible query API: Grafana datasources send an API key, not a
	// Bearer token, so it's root-mounted with its own auth.
	metrics_promapi.GetController().RegisterRoutes(r)

	disk.GetDiskController().RegisterRoutes(protected)
	system_usage.GetController().RegisterRoutes(protected)
	projects_controllers.GetProjectController().RegisterRoutes(protected)
	projects_controllers.GetMembershipController().RegisterRoutes(protected)
	api_keys.GetApiKeyController().RegisterRoutes(protected)
	logs_querying.GetLogQueryController().RegisterRoutes(protected)
	logs_livetail.GetController().RegisterRoutes(protected)
	traces_querying.GetTraceQueryController().RegisterRoutes(protected)
	metrics_querying.GetMetricQueryController().RegisterRoutes(protected)
	servicemap.GetServiceMapController().RegisterRoutes(protected)
	datamigration.GetController().RegisterRoutes(protected)
	correlation_api.GetCorrelationController().RegisterRoutes(protected)
	dashboards_controllers.GetDashboardController().RegisterRoutes(protected)
	aiusage.GetAIUsageController().RegisterRoutes(protected)
	aiusage.GetBudgetController().RegisterRoutes(protected)
	alerting.GetController().RegisterRoutes(protected)
	inventory.GetController().RegisterRoutes(protected)
	// Organizations live where users are managed: here only on a standalone
	// install (its own control plane); a Site enrolled in one has its
	// organizations there.
	if !config.GetEnv().SiteExchangeEnabled() {
		organizations.GetController().RegisterRoutes(protected)
		organizations.GetController().RegisterSCIMRoutes(r.Group("/scim/v2"))
	}
}

func setUpDependencies() {
	audit_logs.SetupDependencies()
	logs_core.SetupDependencies()
	spans_core.SetupDependencies()
	metrics_core.SetupDependencies()
	dashboards_services.SetupDependencies()

	if config.GetEnv().ServiceMode.IsControlPlane() {
		// Wiring only; main() ensures the signing key after migrations.
		control_plane_services.SetupDependencies(audit_logs.GetAuditLogService())
	}
}

// runIngestPipeline starts the background tasks tied to this replica serving
// ingest/query HTTP traffic (SERVICE_MODE all or api).
func runIngestPipeline(log *slog.Logger) {
	log.Info("Starting ingest pipeline...")

	// At most one replica, non-blocking: CleanupPendingQueries wipes ALL
	// concurrent-query slots in Valkey, which would zero other running replicas'
	// counters on a rolling deploy. Stale slots expire by TTL anyway.
	_, err := leaderlock.RunExclusive(context.Background(), cache.GetCache(), "cleanup-pending-queries", 30*time.Second,
		func(ctx context.Context) error {
			return logs_querying.GetLogQueryService().CleanupPendingQueries()
		})
	if err != nil {
		log.Error("Failed to cleanup pending queries on startup", slog.String("error", err.Error()))
	}

	// Same for Live Tail's connection tracking.
	_, err = leaderlock.RunExclusive(
		context.Background(),
		cache.GetCache(),
		"cleanup-pending-live-tails",
		30*time.Second,
		func(ctx context.Context) error {
			return logs_livetail.GetConcurrentTailLimiter().CleanupAllTailSlots()
		},
	)
	if err != nil {
		log.Error("Failed to cleanup pending live tails on startup", slog.String("error", err.Error()))
	}

	go drainLegacyLogsQueue(log)

	// Flushes this replica's own write buffer, so it's tied to local ingest.
	go runPeriodicSegmentFlush(log)

	// all/worker replicas upload after each compaction tick; an api replica has
	// no such tick, and nobody else can read its disk.
	if !config.GetEnv().ServiceMode.RunsBackgroundWorkers() && config.GetEnv().ObjectStorageURL != "" {
		go runPeriodicSegmentUpload(log)
	}

	log.Info("Ingest pipeline started successfully")
}

// runBackgroundCleanupWorkers starts the logs/spans/metrics quota, retention,
// compaction and reconciliation workers (SERVICE_MODE all or worker). Each
// pass is leader-locked, so many replicas are safe.
func runBackgroundCleanupWorkers(log *slog.Logger) {
	log.Info("Starting background cleanup workers...")

	logs_cleanup.GetLogCleanupBackgroundService().StartWorkers()
	spans_cleanup.GetSpanCleanupBackgroundService().StartWorkers()
	metrics_cleanup.GetMetricCleanupBackgroundService().StartWorkers()
	correlation_worker.GetCorrelationBackgroundService().StartWorkers()
	system_catalog.Start(context.Background(), log)
	// Rewrites legacy segments and dashboards to the attrnames rule
	// (leader-locked, stops when done).
	datamigration.GetWorker().Start(context.Background())
	// Alert rules (sharded leader locks) and notification delivery (outbox
	// rows claimed with SKIP LOCKED).
	alerting.GetEvaluator().Start(context.Background())
	alerting.GetDispatcher().Start(context.Background())
	// Puts back AI budget rules an older replica removed (leader-locked).
	aiusage.GetAIUsageService().StartBudgetRuleSync(context.Background())
	// The infrastructure inventory, rebuilt every 5 minutes (leader-locked).
	inventory.GetBuilder().Start(context.Background())
	// A sample of uploaded segments against object storage, weekly.
	if url := config.GetEnv().ObjectStorageURL; url != "" {
		if store, err := segments.NewObjectStore(context.Background(), url); err != nil {
			log.Error("Integrity audit off: can't open object storage", "error", err)
		} else {
			system_integrity.Start(context.Background(), store, log)
		}
	}

	log.Info("Background cleanup workers started successfully")
}

// segmentFlushInterval bounds how long a below-threshold project's rows sit
// unqueryable in RAM before being flushed to a segment.
const segmentFlushInterval = 30 * time.Second

// runPeriodicSegmentFlush force-flushes the logs/spans/metrics writers so
// low-traffic projects show up in queries within a bounded time.
func runPeriodicSegmentFlush(log *slog.Logger) {
	ticker := time.NewTicker(segmentFlushInterval)
	defer ticker.Stop()

	for range ticker.C {
		flushAllSegmentsOnce(log)
	}
}

// segmentUploadInterval: how often an api replica uploads its own flushed
// segments. Until then they exist only on its local disk.
const segmentUploadInterval = 15 * time.Second

// shutdownUploadTimeout caps the final upload so shutdown fits in the pod's
// termination grace period (60s in the HA manifests, after the drain and
// the HTTP and gRPC deadlines).
const shutdownUploadTimeout = 15 * time.Second

// setUpLocalWriter gives this process its stream_segments.writer_id, stored in
// LOGS_DATA_DIR so it follows the disk. Worker replicas also upload rows
// written before writer_id existed.
func setUpLocalWriter(log *slog.Logger, mode config.ServiceMode) {
	writerID, err := segments.LoadOrCreateWriterID(config.GetEnv().LogsDataDir)
	if err != nil {
		log.Error("Failed to set up segment writer id", "error", err)
		os.Exit(1)
	}
	segments.SetLocalWriter(writerID, mode.RunsBackgroundWorkers())
	log.Info("Segment writer id loaded", "writerId", writerID)
	go runWriterHeartbeat(log, writerID)
}

// runWriterHeartbeat keeps this writer in segment_writers, so its pending
// segments aren't taken for orphans (segments.MarkOrphansLost). The first
// beat waits a tick, for migrations to create the table on an upgrade;
// nothing this young can be an orphan anyway.
func runWriterHeartbeat(log *slog.Logger, writerID string) {
	ticker := time.NewTicker(segments.WriterHeartbeatInterval)
	defer ticker.Stop()

	for range ticker.C {
		if config.IsShouldShutdown() {
			return
		}
		if err := segments.HeartbeatWriter(writerID, time.Now().UTC()); err != nil {
			log.Warn("Failed to record segment writer heartbeat", "error", err)
		}
	}
}

// runPeriodicSegmentUpload uploads this replica's own pending segments on a
// ticker - see runIngestPipeline for when it runs.
func runPeriodicSegmentUpload(log *slog.Logger) {
	ticker := time.NewTicker(segmentUploadInterval)
	defer ticker.Stop()

	for range ticker.C {
		if config.IsShouldShutdown() {
			return
		}
		uploadAllSegmentsOnce(log)
	}
}

// uploadOwnSegmentsBeforeExit runs one bounded upload pass after the final
// flush. A pass cut short leaves its segments pending; on a persistent
// volume they upload after the restart.
func uploadOwnSegmentsBeforeExit(log *slog.Logger) {
	if config.GetEnv().ObjectStorageURL == "" {
		return
	}

	done := make(chan struct{})
	go func() {
		uploadAllSegmentsOnce(log)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(shutdownUploadTimeout):
		log.Warn("Final segment upload did not finish before shutdown", "timeout", shutdownUploadTimeout)
	}
}

func uploadAllSegmentsOnce(log *slog.Logger) {
	for _, repository := range logs_core.GetAllRepositories() {
		if err := repository.UploadPendingSegments(); err != nil {
			log.Error("Failed to upload pending log segments", slog.String("error", err.Error()))
		}
	}
	if err := spans_core.GetSpanCoreRepository().UploadPendingSegments(); err != nil {
		log.Error("Failed to upload pending span segments", slog.String("error", err.Error()))
	}
	if err := metrics_core.GetMetricCoreRepository().UploadPendingSegments(); err != nil {
		log.Error("Failed to upload pending metric segments", slog.String("error", err.Error()))
	}
}

// coldCacheEvictionInterval is far longer than the flush interval: the cache
// takes many downloads to approach ColdCacheMaxSizeMB.
const coldCacheEvictionInterval = 10 * time.Minute

// runPeriodicColdCacheEviction enforces COLD_CACHE_MAX_SIZE_MB on the cold-tier
// download cache all three signals share. A no-op without a cold tier.
func runPeriodicColdCacheEviction(log *slog.Logger) {
	if config.GetEnv().ObjectStorageURL == "" {
		return
	}

	cacheDir := filepath.Join(config.GetEnv().LogsDataDir, "cache")
	maxBytes := config.GetEnv().ColdCacheMaxSizeMB * 1024 * 1024

	evictOnce := func() {
		if err := segments.EvictColdCache(cacheDir, maxBytes); err != nil {
			log.Error("Failed to evict cold-tier cache", slog.String("error", err.Error()))
		}
	}

	evictOnce() // don't wait a full interval after a restart that inherited an already-oversized cache

	ticker := time.NewTicker(coldCacheEvictionInterval)
	defer ticker.Stop()
	for range ticker.C {
		evictOnce()
	}
}

// drainLegacyLogsQueue stores logs an older release left in the Valkey queue.
func drainLegacyLogsQueue(log *slog.Logger) {
	stored, err := logs_receiving.DrainLegacyQueue(log)
	if err != nil {
		log.Error("Failed to drain the legacy Valkey logs queue", "stored", stored, "error", err)
		return
	}
	if stored > 0 {
		log.Info("Drained the legacy Valkey logs queue", "stored", stored)
	}
}

func flushAllSegmentsOnce(log *slog.Logger) {
	for _, repository := range logs_core.GetAllRepositories() {
		if err := repository.ForceFlush(); err != nil {
			log.Error("Failed to flush pending log segments", slog.String("error", err.Error()))
		}
	}
	if err := spans_core.GetSpanCoreRepository().ForceFlush(); err != nil {
		log.Error("Failed to flush pending span segments", slog.String("error", err.Error()))
	}
	if err := metrics_core.GetMetricCoreRepository().ForceFlush(); err != nil {
		log.Error("Failed to flush pending metric segments", slog.String("error", err.Error()))
	}
}

// generateSwaggerDocs runs swag init in development; new docs show only after
// a restart, since swag generates Go files.
func generateSwaggerDocs(log *slog.Logger) {
	if config.GetEnv().EnvMode == env_utils.EnvModeProduction {
		return
	}

	currentDir, err := os.Getwd()
	if err != nil {
		log.Error("Failed to get current directory", "error", err)
		return
	}

	cmd := exec.Command("swag", "init", "-d", currentDir, "-g", "cmd/main.go", "-o", "swagger")

	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Error("Failed to generate Swagger docs", "error", err, "output", string(output))
		return
	}

	log.Info("Swagger documentation generated successfully")
}

// setUpLogsStorage builds the logs storage engine and wires it in via
// logs_core.SetRepository. OBJECT_STORAGE_URL unset means local disk only.
func setUpLogsStorage(log *slog.Logger) {
	dataDir := config.GetEnv().LogsDataDir

	var opts []logs_parquetstore.Option
	objectStorageURL := config.GetEnv().ObjectStorageURL
	if objectStorageURL != "" {
		log.Info("Object storage configured, enabling cold tier", "url", objectStorageURL)

		store, err := segments.NewObjectStore(context.Background(), objectStorageURL)
		if err != nil {
			log.Error("Failed to open object storage bucket", "url", objectStorageURL, "error", err)
			os.Exit(1)
		}

		cacheDir := filepath.Join(dataDir, "cache")
		opts = append(opts, logs_parquetstore.WithObjectStore(store, cacheDir))
	}

	// Searches include rows still pending in JetStream, so they don't lag
	// by INGEST_L0_MAX_AGE (the broker connects after this).
	if env := config.GetEnv(); env.UsesJetStream() && env.ServiceMode.ServesHTTP() {
		tail, err := logs_jetstream.NewTail(filepath.Join(dataDir, "tail", "logs"), log)
		if err != nil {
			log.Error("Failed to set up the logs tail", "error", err)
			os.Exit(1)
		}
		opts = append(opts, logs_parquetstore.WithTail(tail))
	}

	// Histograms and dashboard series reuse finished partitions' counts.
	if env := config.GetEnv(); env.ServiceMode.ServesHTTP() {
		if partitions := partcache.New(partcache.Valkey{Client: cache.GetCache()}, env.QueryCacheTTL,
			env.QueryCacheDelay); partitions != nil {
			opts = append(opts, logs_parquetstore.WithPartitionCache(partitions))
		}
	}

	// A worker's searches are background work in a memory-capped container: an
	// oversized one should fail, not kill the worker.
	if config.GetEnv().ServiceMode == config.ServiceModeWorker {
		opts = append(opts, logs_parquetstore.WithQuerierMemoryLimit(workerQuerierMemoryLimit))
	} else {
		opts = append(
			opts,
			logs_parquetstore.WithQuerierMemoryLimit(segments.HandleMemoryLimit(queryHandleMemoryFraction)),
		)
	}

	repository, err := logs_parquetstore.New(dataDir, opts...)
	if err != nil {
		log.Error("Failed to set up logs storage", "dataDir", dataDir, "error", err)
		os.Exit(1)
	}

	cleanupStaleTempSegments(log, "logs", repository.CleanupStaleTempFiles)

	logs_core.SetRepository(repository)

	aiRepository, err := logs_parquetstore.New(
		dataDir, append(opts, logs_parquetstore.WithStream(logs_core.StreamAILogs))...,
	)
	if err != nil {
		log.Error("Failed to set up AI logs storage", "dataDir", dataDir, "error", err)
		os.Exit(1)
	}

	cleanupStaleTempSegments(log, logs_core.StreamAILogs, aiRepository.CleanupStaleTempFiles)

	logs_core.SetAIRepository(aiRepository)
}

func testLogsStorageConnection(log *slog.Logger) {
	log.Info("Testing logs storage connection...")

	err := logs_core.GetLogCoreRepository().TestConnection()
	if err != nil {
		log.Error("Failed to connect to logs storage", "error", err)
		os.Exit(1)
	}

	log.Info("Logs storage connection test successful")
}

// setUpSpansStorage builds the spans storage engine and wires it in via
// spans_core.SetRepository. It shares LOGS_DATA_DIR: the writer namespaces by
// stream, so one directory and bucket serve every signal.
func setUpSpansStorage(log *slog.Logger) {
	dataDir := config.GetEnv().LogsDataDir

	var opts []spans_parquetstore.Option
	objectStorageURL := config.GetEnv().ObjectStorageURL
	if objectStorageURL != "" {
		store, err := segments.NewObjectStore(context.Background(), objectStorageURL)
		if err != nil {
			log.Error("Failed to open object storage bucket for spans", "url", objectStorageURL, "error", err)
			os.Exit(1)
		}

		cacheDir := filepath.Join(dataDir, "cache")
		opts = append(opts, spans_parquetstore.WithObjectStore(store, cacheDir))
	}

	if env := config.GetEnv(); env.UsesJetStream() && env.ServiceMode.ServesHTTP() {
		tail, err := spans_jetstream.NewTail(filepath.Join(dataDir, "tail", "spans"), log)
		if err != nil {
			log.Error("Failed to set up the spans tail", "error", err)
			os.Exit(1)
		}
		opts = append(opts, spans_parquetstore.WithTail(tail))
	}

	opts = append(
		opts,
		spans_parquetstore.WithQuerierMemoryLimit(segments.HandleMemoryLimit(queryHandleMemoryFraction)),
	)

	repository, err := spans_parquetstore.New(dataDir, opts...)
	if err != nil {
		log.Error("Failed to set up spans storage", "dataDir", dataDir, "error", err)
		os.Exit(1)
	}

	cleanupStaleTempSegments(log, "spans", repository.CleanupStaleTempFiles)

	spans_core.SetRepository(repository)
}

func testSpansStorageConnection(log *slog.Logger) {
	log.Info("Testing spans storage connection...")

	err := spans_core.GetSpanCoreRepository().TestConnection()
	if err != nil {
		log.Error("Failed to connect to spans storage", "error", err)
		os.Exit(1)
	}

	log.Info("Spans storage connection test successful")
}

// setUpMetricsStorage builds the metrics storage engine and wires it in via
// metrics_core.SetRepository, sharing LOGS_DATA_DIR like spans.
func setUpMetricsStorage(log *slog.Logger) {
	dataDir := config.GetEnv().LogsDataDir

	var opts []metrics_parquetstore.Option
	objectStorageURL := config.GetEnv().ObjectStorageURL
	if objectStorageURL != "" {
		store, err := segments.NewObjectStore(context.Background(), objectStorageURL)
		if err != nil {
			log.Error("Failed to open object storage bucket for metrics", "url", objectStorageURL, "error", err)
			os.Exit(1)
		}

		cacheDir := filepath.Join(dataDir, "cache")
		opts = append(opts, metrics_parquetstore.WithObjectStore(store, cacheDir))
	}

	if env := config.GetEnv(); env.UsesJetStream() && env.ServiceMode.ServesHTTP() {
		tail, err := metrics_jetstream.NewTail(filepath.Join(dataDir, "tail", "metrics"), log)
		if err != nil {
			log.Error("Failed to set up the metrics tail", "error", err)
			os.Exit(1)
		}
		opts = append(opts, metrics_parquetstore.WithTail(tail))
	}

	opts = append(
		opts,
		metrics_parquetstore.WithQuerierMemoryLimit(segments.HandleMemoryLimit(queryHandleMemoryFraction)),
	)

	repository, err := metrics_parquetstore.New(dataDir, opts...)
	if err != nil {
		log.Error("Failed to set up metrics storage", "dataDir", dataDir, "error", err)
		os.Exit(1)
	}

	cleanupStaleTempSegments(log, "metrics", repository.CleanupStaleTempFiles)

	metrics_core.SetRepository(repository)
}

// cleanupStaleTempSegments removes "*.parquet.tmp" files a crash left
// mid-flush. Failure only logs: queries are catalog-driven, so a stray temp
// file is inert.
func cleanupStaleTempSegments(log *slog.Logger, stream string, cleanup func() (int, error)) {
	removed, err := cleanup()
	if err != nil {
		log.Warn("Failed to clean up stale temp segment files", "stream", stream, "error", err)
		return
	}
	if removed > 0 {
		log.Info("Cleaned up stale temp segment files from a prior crash", "stream", stream, "count", removed)
	}
}

func testMetricsStorageConnection(log *slog.Logger) {
	log.Info("Testing metrics storage connection...")

	err := metrics_core.GetMetricCoreRepository().TestConnection()
	if err != nil {
		log.Error("Failed to connect to metrics storage", "error", err)
		os.Exit(1)
	}

	log.Info("Metrics storage connection test successful")
}

// setUpCorrelationStorage wires correlation_core.SetRepository to Postgres:
// low volume and needs joins, so not a segment stream.
func setUpCorrelationStorage(log *slog.Logger) {
	log.Info("Setting up correlation storage...")
	correlation_core.SetRepository(correlation_repository.New())
}

func testCorrelationStorageConnection(log *slog.Logger) {
	log.Info("Testing correlation storage connection...")

	err := correlation_core.GetIncidentSessionRepository().TestConnection()
	if err != nil {
		log.Error("Failed to connect to correlation storage", "error", err)
		os.Exit(1)
	}

	log.Info("Correlation storage connection test successful")
}

// runMigrations runs `goose up` behind a Valkey lock so replicas starting
// together serialize; the losers then run it too, a no-op.
func runMigrations(log *slog.Logger) {
	log.Info("Running database migrations...")

	err := leaderlock.RunExclusiveBlocking(
		context.Background(), cache.GetCache(), "db-migrations",
		2*time.Minute, 3*time.Minute, 500*time.Millisecond,
		func(ctx context.Context) error {
			cmd := exec.CommandContext(ctx, "goose", "up")
			cmd.Env = append(
				os.Environ(),
				"GOOSE_DRIVER=postgres",
				"GOOSE_DBSTRING="+config.GetEnv().DatabaseDsn,
			)
			cmd.Dir = config.GetEnv().BackendRootPath

			output, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("goose up failed: %w (output: %s)", err, string(output))
			}

			log.Info("Database migrations completed successfully", "output", string(output))
			return nil
		},
	)
	if err != nil {
		log.Error("Failed to run migrations", "error", err)
		os.Exit(1)
	}
}

func enableCors(ginApp *gin.Engine) {
	if config.GetEnv().EnvMode == env_utils.EnvModeDevelopment {
		ginApp.Use(cors.New(cors.Config{
			AllowOrigins: []string{"*"},
			AllowMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"},
			AllowHeaders: []string{
				"Origin",
				"Content-Length",
				"Content-Type",
				"Authorization",
				"Accept",
				"Accept-Language",
				"Accept-Encoding",
				"Access-Control-Request-Method",
				"Access-Control-Request-Headers",
				"Access-Control-Allow-Methods",
				"Access-Control-Allow-Headers",
				"Access-Control-Allow-Origin",
			},
			AllowCredentials: true,
		}))
	}
}

func mountFrontend(ginApp *gin.Engine) {
	ginApp.NoRoute(frontendFallback("./ui/build", config.GetEnv().ServiceMode.IsControlPlane()))
}

// frontendFallback serves static files and index.html for unknown GET paths.
// Anything else is a JSON 404: answering index.html turns client bugs into
// "Unexpected token '<'" and swallowed OTLP exports on a control plane.
func frontendFallback(staticDir string, isControlPlane bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Cleaned against "/" so ".." can't climb out of staticDir.
		requestPath := filepath.Clean("/" + c.Request.URL.Path)
		c.Request.URL.Path = requestPath
		isPageRequest := c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead
		if strings.HasPrefix(requestPath, "/v1/") && isControlPlane {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "this is a control plane: send telemetry to a Site's URL, not the control plane's",
			})
			return
		}
		if !isPageRequest || strings.HasPrefix(requestPath, "/api/") || strings.HasPrefix(requestPath, "/sites/") ||
			strings.HasPrefix(requestPath, "/v1/") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}

		filePath := filepath.Join(staticDir, requestPath)
		if info, err := os.Stat(filePath); err == nil && !info.IsDir() {
			c.Header("Cache-Control", cacheControlFor(requestPath))
			c.File(filePath)
			return
		}

		// index.html itself: never cached, see cacheControlFor.
		c.Header("Cache-Control", cacheControlFor("/index.html"))
		c.File(filepath.Join(staticDir, "index.html"))
	}
}

// cacheControlFor picks Cache-Control by filename: Vite's hashed assets cache
// forever; index.html and runtime-config.js (rewritten at each start) always
// revalidate; everything else gets a short cache.
func cacheControlFor(urlPath string) string {
	switch {
	case strings.HasSuffix(urlPath, "/index.html"), strings.HasSuffix(urlPath, "/runtime-config.js"):
		return "no-cache"
	case strings.Contains(urlPath, "/assets/"):
		return "public, max-age=31536000, immutable"
	default:
		return "public, max-age=3600"
	}
}

func handlePasswordReset(log *slog.Logger) {
	// --new-password still works for existing automation, but a password argument
	// shows in `ps` and shell history; --email alone prompts for it instead.
	newPassword := flag.String(
		"new-password", "",
		"Set a new password for the user (insecure - visible via ps/shell history; "+
			"omit this and pass only --email to be prompted instead)",
	)
	email := flag.String("email", "", "Email (or root admin login) of the user to reset")
	resetMFA := flag.Bool(
		"reset-mfa", false,
		"With --email: clear the user's MFA (lost authenticator) instead of resetting the password. "+
			"Admins set it up again at their next sign-in. Add --new-password to reset both.",
	)
	resetBootstrapToken := flag.Bool(
		"reset-bootstrap-token", false,
		"Print a new bootstrap token for the root admin (the first one is logged only once, at creation). "+
			"Only while the root admin has no password.",
	)

	flag.Parse()

	if *resetBootstrapToken {
		printNewBootstrapToken(log)
	}

	if *email == "" {
		if *resetMFA {
			log.Error("--reset-mfa needs --email <user email or root admin login>")
			os.Exit(1)
		}
		return
	}

	if *resetMFA {
		resetUserMFA(*email, log)
		if *newPassword == "" {
			os.Exit(0)
		}
	}

	log.Info("Found reset password command - reseting password...")

	password := *newPassword
	if password == "" {
		var err error
		password, err = promptForPassword()
		if err != nil {
			log.Error("Failed to read password", "error", err)
			os.Exit(1)
		}
	}

	resetPassword(*email, password, log)
}

// promptForPassword reads a password from stdin with echo off, so it never
// shows on screen, in `ps` or in shell history.
func promptForPassword() (string, error) {
	fmt.Print("New password: ")
	passwordBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", fmt.Errorf("failed to read password from terminal: %w", err)
	}

	password := string(passwordBytes)
	if password == "" {
		return "", fmt.Errorf("password must not be empty")
	}
	return password, nil
}

// printNewBootstrapToken writes the token to stdout only, not the log, so it
// doesn't reach a log pipeline. Exits.
func printNewBootstrapToken(log *slog.Logger) {
	token, err := users_services.GetUserService().ResetBootstrapToken()
	if err != nil {
		log.Error("Failed to reset the bootstrap token", "error", err)
		os.Exit(1)
	}

	fmt.Println("New root admin bootstrap token (the previous one no longer works):")
	fmt.Println(token)
	os.Exit(0)
}

// resetUserMFA clears one user's MFA from the command line. Exits non-zero on
// failure.
func resetUserMFA(email string, log *slog.Logger) {
	wasEnabled, err := users_services.GetUserService().ResetMFAByEmail(email)
	if err != nil {
		log.Error("Failed to reset MFA", "error", err)
		os.Exit(1)
	}

	if wasEnabled {
		log.Info(
			"MFA reset - the user signs in with their password and, if an admin, sets up MFA again",
			"email", email,
		)
	} else {
		log.Info("MFA was not enabled for this user - cleared any unfinished setup", "email", email)
	}
}

func resetPassword(email string, newPassword string, log *slog.Logger) {
	log.Info("Resetting password...")

	userService := users_services.GetUserService()
	err := userService.ChangeUserPasswordByEmail(email, newPassword)
	if err != nil {
		log.Error("Failed to reset password", "error", err)
		os.Exit(1)
	}

	log.Info("Password reset successfully")
	os.Exit(0)
}
