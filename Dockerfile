# ========= BUILD FRONTEND =========
# --platform=$BUILDPLATFORM: static JS/HTML/CSS output isn't
# architecture-specific, so this stage should always run natively on
# whatever's actually building the image, not under QEMU emulation for
# the target platform (CI's GitHub-hosted runners are amd64, building
# linux/arm64 - see ci-release.yml). Without this, `npm run build` reruns
# under emulation, and esbuild's own native binary is genuinely flaky
# there - hit a real "[vite:esbuild] The service was stopped: write
# EPIPE" crash testing this, not just slow. Still correct even now that
# CI only targets one non-native platform (arm64) rather than a matrix -
# it's "run this stage natively" either way. BUILDPLATFORM is a global
# arg buildx predefines automatically, no explicit ARG declaration
# needed.
FROM --platform=$BUILDPLATFORM node:26.13.0-alpine AS frontend-build

WORKDIR /frontend

# Add version for the frontend build
ARG APP_VERSION=dev
ENV VITE_APP_VERSION=$APP_VERSION

COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./

# Copy .env.production.example to .env
RUN if [ -f .env.production.example ]; then \
    cp .env.production.example .env; \
    else \
    echo "Error: .env.production.example not found" && exit 1; \
    fi

RUN npm run build

# ========= BUILD BACKEND =========
# Backend build stage, always on the build machine's own architecture:
# the target's binary is cross-compiled, never built under emulation (the
# CI runner is arm64, and a CGO build under QEMU is several times slower).
# DuckDB is linked through CGO, so a target other than the build machine
# needs its C/C++ cross toolchain; duckdb-go-bindings ships prebuilt static
# libraries for linux-amd64 and linux-arm64.
#
# Pinned to bookworm, as is the runtime stage (glibc 2.36): a CGO binary
# built against a newer glibc fails there with "GLIBC_2.38 not found"
# (seen after the Phase 6 rebuild). Bookworm's cross toolchains target the
# same glibc.
FROM --platform=$BUILDPLATFORM golang:1.27.3-bookworm AS backend-build

ARG TARGETOS
ARG TARGETARCH
ARG BUILDARCH

RUN set -e; \
    if [ "$TARGETARCH" != "$BUILDARCH" ]; then \
      case "$TARGETARCH" in \
        amd64) pkgs="g++-x86-64-linux-gnu libc6-dev-amd64-cross" ;; \
        arm64) pkgs="g++-aarch64-linux-gnu libc6-dev-arm64-cross" ;; \
        *) echo "unsupported TARGETARCH $TARGETARCH" >&2; exit 1 ;; \
      esac; \
      apt-get update && apt-get install -y --no-install-recommends $pkgs && rm -rf /var/lib/apt/lists/*; \
    fi

# goose runs in the image (migrations at startup), so it's built for the
# target; pure Go. A cross-compiled `go install` lands in bin/<os>_<arch>.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go install github.com/pressly/goose/v3/cmd/goose@v3.29.0 && \
    if [ -f "/go/bin/${TARGETOS}_${TARGETARCH}/goose" ]; then mv "/go/bin/${TARGETOS}_${TARGETARCH}/goose" /go/bin/goose; fi
# swag only runs here.
RUN go install github.com/swaggo/swag/cmd/swag@v1.16.6

# Set working directory
WORKDIR /app

# Install Go dependencies
COPY backend/go.mod backend/go.sum ./
RUN go mod download

# Create required directories for embedding
RUN mkdir -p /app/ui/build

# Copy frontend build output for embedding
COPY --from=frontend-build /frontend/dist /app/ui/build

# Generate Swagger documentation
COPY backend/ ./
RUN swag init -d . -g cmd/main.go -o swagger

# Compile the backend. CGO_ENABLED=1 for DuckDB (internal/storage/segments);
# natively the image's own gcc, otherwise the cross toolchain above.
RUN set -e; \
    cc=gcc; cxx=g++; \
    if [ "$TARGETARCH" != "$BUILDARCH" ]; then \
      case "$TARGETARCH" in \
        amd64) cc=x86_64-linux-gnu-gcc; cxx=x86_64-linux-gnu-g++ ;; \
        arm64) cc=aarch64-linux-gnu-gcc; cxx=aarch64-linux-gnu-g++ ;; \
      esac; \
    fi; \
    CC=$cc CXX=$cxx CGO_ENABLED=1 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -o /app/main ./cmd


# ========= RUNTIME =========
# Postgres, OpenSearch and Valkey used to be installed and orchestrated
# inside this image (see git history if you need the old approach). That's
# gone along with OpenSearch itself (ARCHITECTURE.md's Phase 2 storage
# engine rewrite — logs live in local Parquet files, no separate search
# service to run) and Postgres/Valkey are now external services the app
# connects to via DATABASE_DSN/VALKEY_HOST (docker-compose.yml.example),
# matching how local dev already worked. This image now holds only the app
# itself.
FROM debian:bookworm-slim

# Add version metadata to runtime image
ARG APP_VERSION=dev
LABEL org.opencontainers.image.version=$APP_VERSION
ENV APP_VERSION=$APP_VERSION

# Set production mode for Docker containers
ENV ENV_MODE=production

# ca-certificates: outbound HTTPS (OAuth providers, optional object storage).
# curl: container HEALTHCHECK.
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl && \
    rm -rf /var/lib/apt/lists/*

# A non-root user to run the app as (SECURITY_REVIEW.md #23: "container
# runs as root") - previously nothing here ever dropped root, so a
# container-escape-class bug in anything this process touches (the CGO
# DuckDB engine parsing untrusted Parquet-adjacent input, a future
# dependency vulnerability, ...) would hand an attacker root inside the
# container for free, one less thing standing between that and the host.
# Fixed uid/gid 1000 (not `useradd`'s next-available default) so it's
# predictable across rebuilds and matches the common convention other
# widely-used images (e.g. node:*) already use. The container still
# STARTS as root (no static USER instruction here) - start.sh below fixes
# up /obzly-data's ownership and drops to this user itself before ever
# running the app, so an existing deployment's root-owned data needs no
# manual chown on upgrade; see start.sh's own comment for the full
# reasoning on why that has to happen at container start rather than here
# at build time.
RUN groupadd --system --gid 1000 obzly && \
    useradd --system --uid 1000 --gid obzly --no-create-home --shell /usr/sbin/nologin obzly

WORKDIR /app

# Copy Goose from build stage (installed via go install to /go/bin) — the
# app shells out to it at startup to run migrations against DATABASE_DSN.
COPY --from=backend-build /go/bin/goose /usr/local/bin/goose

# Copy app binary
COPY --from=backend-build /app/main .

# Copy go.mod for BackendRootPath detection
COPY backend/go.mod ./go.mod

# Copy migrations directory
COPY backend/migrations ./migrations

# Copy UI files
COPY --from=backend-build /app/ui/build ./ui/build

# Non-secret defaults only (no DSN or passwords): those come from the
# deployment, so a missing one fails at startup.
COPY backend/.env.production.example /app/.env

# The runtime-config.js generator lives as a real script file (scripts/
# generate-runtime-config.sh), not inlined into the heredoc below - see
# that file's own doc comment for why: this COPY's heredoc does its own
# build-time variable/backslash substitution, and getting a nested
# escaping scheme through that correctly is easy to get subtly, silently
# wrong (confirmed firsthand while fixing this).
COPY scripts/generate-runtime-config.sh /app/generate-runtime-config.sh
RUN chmod +x /app/generate-runtime-config.sh

# Create startup script: inject runtime frontend config (only known at
# container start, e.g. OAuth client IDs) then hand off to the app, which
# runs its own migrations and connects to DATABASE_DSN/VALKEY_HOST itself.
COPY <<EOF /app/start.sh
#!/bin/bash
set -e

echo "Generating runtime configuration..."
/app/generate-runtime-config.sh

echo "Starting Obzly..."

# Drop root before actually running the app (SECURITY_REVIEW.md #23:
# "container runs as root") - but only after fixing up the data
# directory's ownership, still as root, right here. That ordering is what
# makes this work unconditionally, for every deployment shape, with zero
# operator action required:
#   - A brand-new named/anonymous Docker volume at the default
#     /obzly-data: already owned by obzly (inherited from the image at
#     the VOLUME instruction below, which chown'd it at build time) -
#     this chown is a fast no-op.
#   - A brand-new HOST BIND MOUNT (docker-compose.yml.example's
#     ./obzly-data:/obzly-data - the common case): Docker creates that
#     host directory as root:root the first time, regardless of anything
#     baked into the image, since a bind mount entirely overlays whatever
#     the image had at this path - without fixing it up here, a non-root
#     process would fail its very first write on a totally fresh install.
#   - An UPGRADE from an older (root-only) version of this image, where
#     existing files are already root-owned: same fix, same place.
#   - LOGS_DATA_DIR overridden to somewhere other than the default (every
#     signal's storage - logs/spans/metrics - shares this one root, see
#     cmd/main.go's setUpLogsStorage/setUpSpansStorage/setUpMetricsStorage,
#     all reading config.GetEnv().LogsDataDir) - chowning a hardcoded
#     /obzly-data here would silently miss this and crash-loop on
#     startup ("permission denied") the moment a deployment's compose
#     file bind-mounts somewhere else and points LOGS_DATA_DIR at it
#     (seen on a test deployment with LOGS_DATA_DIR=/data/segments over a
#     ./data:/data bind mount). The bind-mounted directory is always
#     LOGS_DATA_DIR's PARENT (the compose file mounts the root; the app
#     appends /segments itself) - chown that, not the literal default
#     path.
# `id -u` guards this for the one case it'd be wrong: someone runs this
# image with `docker run --user ...` (or a Kubernetes securityContext)
# already forcing a non-root UID - chown would just fail there, and
# there's no need for it anyway since whatever set that up is expected to
# have prepared the mount's permissions itself.
if [ "$(id -u)" = "0" ]; then
    DATA_ROOT="$(dirname "\${LOGS_DATA_DIR:-/obzly-data/segments}")"
    # The mount root, not LOGS_DATA_DIR itself: a truly fresh deployment
    # may not have created the "segments" subdirectory yet (the app does
    # that itself, as obzly, on first write) - chown -R on a path that
    # doesn't exist yet would fail outright (set -e). The parent is
    # guaranteed to exist (Docker creates a bind mount's host directory,
    # or a named volume, before the container ever starts), and -R here
    # still catches "segments" once the app creates it moments later,
    # since it'll inherit obzly's ownership from running as obzly by then
    # anyway. Guard against dirname ever resolving to "/" (LOGS_DATA_DIR
    # set to a bare top-level path with no subdirectory) - a recursive
    # chown of the whole filesystem would be catastrophic, and there's
    # nothing useful to fix up in that shape of misconfiguration anyway.
    if [ -n "\$DATA_ROOT" ] && [ "\$DATA_ROOT" != "/" ]; then
        chown -R obzly:obzly "\$DATA_ROOT"
    fi
    exec setpriv --reuid=obzly --regid=obzly --clear-groups ./main
fi

exec ./main
EOF

RUN chmod +x /app/start.sh

# /app (binary, UI, migrations, goose) stays root-owned, so the app can't
# overwrite its own code. The runtime user owns only the data directory and
# runtime-config.js, which start.sh regenerates on every start (also when
# the container is run as uid 1000 from the start).
RUN mkdir -p /obzly-data && touch /app/ui/build/runtime-config.js && \
    chown obzly:obzly /app/ui/build/runtime-config.js && \
    chown -R obzly:obzly /obzly-data

# Main HTTP port (UI, API, OTLP/HTTP). HTTP_PORT overrides the port itself,
# not this declaration - update both if you change it.
EXPOSE 4005
# OTLP gRPC logs receiver (ARCHITECTURE.md, Phase 3) - separate listener
# from the main HTTP port above, since gRPC and Gin can't share one
# net.Listener. OTLP_GRPC_PORT overrides the port itself, not this
# declaration - update both if you change it.
EXPOSE 4317
# Prometheus /metrics (internal; METRICS_ADDR, default :9464, "off" disables).
# Don't publish it on a public load balancer.
EXPOSE 9464

# Health check
HEALTHCHECK --interval=5s --timeout=5s --start-period=60s --retries=30 \
    CMD curl -f "http://localhost:${HTTP_PORT:-4005}/api/v1/system/health" || exit 1

# Volume for the logs storage engine's local (hot-tier) Parquet segments —
# see LOGS_DATA_DIR in docker-compose.yml.example.
VOLUME ["/obzly-data"]

ENTRYPOINT ["/app/start.sh"]
CMD []
