// Package broker embeds the NATS JetStream server used as Obzly's durable
// ingest buffer (ARCHITECTURE.md "Scale architecture"). It runs in-process in
// SERVICE_MODE=all (no listener, R1 on local disk) or as a SERVICE_MODE=broker
// StatefulSet of three pods (R3); api and worker replicas connect as clients.
package broker

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/nats-io/nats-server/v2/server"
)

// routeUser is the fixed username of the broker-to-broker (cluster route)
// credentials; the password is the shared token.
const routeUser = "obzly"

// readyTimeout bounds how long a starting server may take to accept
// connections.
const readyTimeout = 30 * time.Second

// lameDuckDuration: on SIGTERM a broker stops accepting connections and
// moves its clients to the other brokers over this long, inside the pod's
// termination grace period.
const lameDuckDuration = 30 * time.Second

// ServerConfig describes one embedded NATS server.
type ServerConfig struct {
	// Name must be unique in a cluster (the pod name in a StatefulSet).
	Name string
	// StoreDir holds JetStream's file store.
	StoreDir string
	// InProcess: no network listener at all - SERVICE_MODE=all, where the
	// only client is this process.
	InProcess bool
	// Token authenticates clients and, as the route password, peers.
	Token string
	// ClientPort, ClusterPort, MonitorPort: listeners of a broker pod.
	ClientPort, ClusterPort, MonitorPort int
	// ClusterName and Routes (nats-route://host:port, without credentials)
	// join the broker pods; empty Routes means a single broker.
	ClusterName string
	Routes      []string
}

// slogAdapter routes the NATS server's log lines into Obzly's logger.
type slogAdapter struct {
	log *slog.Logger
}

// StartServer starts an embedded NATS server with JetStream and waits until
// it accepts connections.
func StartServer(cfg ServerConfig, log *slog.Logger) (*server.Server, error) {
	opts, err := serverOptions(cfg)
	if err != nil {
		return nil, err
	}

	srv, err := server.NewServer(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to create NATS server: %w", err)
	}
	srv.SetLoggerV2(&slogAdapter{log: log.With("component", "nats")}, false, false, false)
	srv.Start()

	if !srv.ReadyForConnections(readyTimeout) {
		srv.Shutdown()
		return nil, errors.New("NATS server did not become ready")
	}
	return srv, nil
}

func (a *slogAdapter) Noticef(format string, v ...any) { a.log.Info(fmt.Sprintf(format, v...)) }
func (a *slogAdapter) Warnf(format string, v ...any)   { a.log.Warn(fmt.Sprintf(format, v...)) }
func (a *slogAdapter) Fatalf(format string, v ...any)  { a.log.Error(fmt.Sprintf(format, v...)) }
func (a *slogAdapter) Errorf(format string, v ...any)  { a.log.Error(fmt.Sprintf(format, v...)) }
func (a *slogAdapter) Debugf(format string, v ...any)  { a.log.Debug(fmt.Sprintf(format, v...)) }
func (a *slogAdapter) Tracef(format string, v ...any)  { a.log.Debug(fmt.Sprintf(format, v...)) }

func serverOptions(cfg ServerConfig) (*server.Options, error) {
	if cfg.Name == "" {
		return nil, errors.New("broker server name is required")
	}
	if cfg.StoreDir == "" {
		return nil, errors.New("JetStream store directory is required")
	}

	opts := &server.Options{
		ServerName: cfg.Name,
		JetStream:  true,
		StoreDir:   cfg.StoreDir,
		MaxPayload: maxServerPayload,
		// Room for a few shards' fetches at once on one worker's connection
		// before the broker calls it a slow consumer (default 64 MB).
		MaxPending: maxClientPending,
		// Obzly handles SIGTERM itself and shuts the server down in order.
		NoSigs: true,
	}
	if cfg.InProcess {
		opts.DontListen = true
		return opts, nil
	}

	if cfg.Token == "" {
		return nil, errors.New("a broker that listens on the network needs a token")
	}
	opts.Host = "0.0.0.0"
	opts.Port = cfg.ClientPort
	opts.LameDuckDuration = lameDuckDuration
	opts.Authorization = cfg.Token
	opts.HTTPPort = cfg.MonitorPort

	if len(cfg.Routes) > 0 {
		routes, err := routeURLs(cfg.Routes, cfg.Token)
		if err != nil {
			return nil, err
		}
		opts.Routes = routes
		opts.Cluster = server.ClusterOpts{
			Name:     cfg.ClusterName,
			Host:     "0.0.0.0",
			Port:     cfg.ClusterPort,
			Username: routeUser,
			Password: cfg.Token,
		}
	}
	return opts, nil
}

// routeURLs parses peer route URLs and adds the route credentials.
func routeURLs(routes []string, token string) ([]*url.URL, error) {
	parsed := make([]*url.URL, 0, len(routes))
	for _, route := range routes {
		u, err := url.Parse(route)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("invalid NATS route %q", route)
		}
		u.User = url.UserPassword(routeUser, token)
		parsed = append(parsed, u)
	}
	return parsed, nil
}
