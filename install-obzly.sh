#!/bin/bash
#
# Standalone-VM installer for Obzly. Installs Docker if needed, generates
# a random Postgres password on first run, writes docker-compose.yml + .env,
# opens the needed ports if ufw is active, and starts Obzly.
#
# Safe to re-run: an existing install's docker-compose.yml and Postgres
# password are left untouched (re-generating the password would stop it
# matching the already-initialized Postgres data volume) - re-running sets
# the image tag in .env to this installer's, pulls it and restarts.
#
# Install from a release (the copy attached to each GitHub release has its
# image tag pinned to that release):
#   curl -fsSL https://github.com/obzly/obzly/releases/latest/download/install-obzly.sh | sudo bash
#
# Options (arguments, e.g. `| sudo bash -s -- --open-firewall`):
#   --open-firewall     If ufw is active, open the HTTP and OTLP ports
#                       without asking (they are plain HTTP/gRPC).
#   --no-open-firewall  Never touch ufw.
#   Without either, the script asks on the terminal, and leaves ufw alone
#   when there is no terminal to ask on. OBZLY_OPEN_FIREWALL=yes|no does
#   the same as the flags.
#
# Env vars you can set before running this script to override defaults:
#   OBZLY_IMAGE_TAG   Image tag to install (default: this installer's
#                     release, or latest for a copy from the repository)
#   OBZLY_HTTP_PORT   Host port for the web UI/API (default: 4005)
#   OBZLY_OTLP_PORT   Host port for the OTLP gRPC log receiver (default: 4317)
#   ADMIN_EMAIL         Email for the bootstrap super-admin account
#                       (default: "admin" - or "superadmin" when
#                       OBZLY_ROLE=control-plane, so it can never collide
#                       with a Site's own root admin). Only applied on first
#                       install; edit .env afterwards to change it later
#                       (renames the account in place).
#
#   OBZLY_ROLE          "standalone" (default), "control-plane", or "site" -
#                       ARCHITECTURE.md "Multi-site: control plane and
#                       Sites". This script is normally run piped from curl
#                       (see README.md), so role selection is an env var,
#                       not a `read`-prompted interactive question - there's
#                       no terminal stdin to prompt against in that mode.
#   PUBLIC_URL          Required when OBZLY_ROLE=control-plane: this
#                       control plane's real, public, browser-facing
#                       origin (scheme + host[:port], no path) - becomes
#                       the `iss` of every assertion it signs.
#   SITE_ID             Required when OBZLY_ROLE=site: this Site's identity
#                       inside the control plane (the slug you'll register
#                       it under).
#   CONTROL_PLANE_ISSUER  Required when OBZLY_ROLE=site: the control
#                       plane's exact PUBLIC_URL.
#   SITE_NAME / CONTROL_PLANE_JWKS_URL / CONTROL_PLANE_EGRESS_CIDRS
#                       Optional, OBZLY_ROLE=site only - see
#                       backend/.env.production.example for what each does.

set -euo pipefail

# The release job rewrites this line in the copy it attaches to a release.
OBZLY_DEFAULT_IMAGE_TAG="latest"

# Everything runs from main, called on the last line: a download cut off
# halfway (curl | bash) then defines nothing and runs nothing.
main() {

OPEN_FIREWALL="${OBZLY_OPEN_FIREWALL:-ask}"
while [ $# -gt 0 ]; do
    case "$1" in
        --open-firewall) OPEN_FIREWALL=yes ;;
        --no-open-firewall) OPEN_FIREWALL=no ;;
        *)
            echo "Error: unknown option $1 (see the top of install-obzly.sh)" >&2
            exit 1
            ;;
    esac
    shift
done

# Check if script is run as root
if [ "$(id -u)" -ne 0 ]; then
    echo "Error: This script must be run as root (sudo ./install-obzly.sh)" >&2
    exit 1
fi

OBZLY_IMAGE_TAG="${OBZLY_IMAGE_TAG:-$OBZLY_DEFAULT_IMAGE_TAG}"
OBZLY_HTTP_PORT="${OBZLY_HTTP_PORT:-4005}"
OBZLY_OTLP_PORT="${OBZLY_OTLP_PORT:-4317}"
ADMIN_EMAIL="${ADMIN_EMAIL:-}"
OBZLY_ROLE="${OBZLY_ROLE:-standalone}"
PUBLIC_URL="${PUBLIC_URL:-}"
SITE_ID="${SITE_ID:-}"
SITE_NAME="${SITE_NAME:-}"
CONTROL_PLANE_ISSUER="${CONTROL_PLANE_ISSUER:-}"
CONTROL_PLANE_JWKS_URL="${CONTROL_PLANE_JWKS_URL:-}"
CONTROL_PLANE_EGRESS_CIDRS="${CONTROL_PLANE_EGRESS_CIDRS:-}"

case "$OBZLY_ROLE" in
    standalone|control-plane|site) ;;
    *)
        echo "Error: OBZLY_ROLE must be 'standalone', 'control-plane', or 'site' (got: $OBZLY_ROLE)" >&2
        exit 1
        ;;
esac
if [ "$OBZLY_ROLE" = "control-plane" ] && [ -z "$PUBLIC_URL" ]; then
    echo "Error: OBZLY_ROLE=control-plane requires PUBLIC_URL to be set (e.g. PUBLIC_URL=https://control-plane.example.com)." >&2
    exit 1
fi
if [ "$OBZLY_ROLE" = "site" ] && { [ -z "$SITE_ID" ] || [ -z "$CONTROL_PLANE_ISSUER" ]; }; then
    echo "Error: OBZLY_ROLE=site requires both SITE_ID and CONTROL_PLANE_ISSUER to be set." >&2
    exit 1
fi

# Set up logging
LOG_FILE="/var/log/obzly-install.log"
INSTALL_DIR="/opt/obzly"

log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1" | tee -a "$LOG_FILE"
}

# Create log file if doesn't exist
touch "$LOG_FILE"
log "Starting Obzly installation..."

if ! command -v apt-get &> /dev/null; then
    log "Error: this script only supports Debian/Ubuntu (apt-get not found)." >&2
    log "See the Docker Compose install method in README.md for other distros." >&2
    exit 1
fi

# DOCKER_APT_DISTRO picks which of Docker's own apt repos to add below
# (SECURITY_REVIEW.md Low: installer's Docker repo setup was hardcoded to
# "linux/ubuntu" regardless of the actual distro - harmless on Ubuntu
# itself, but on a real Debian host `apt-get update` against
# download.docker.com/linux/ubuntu/dists/<debian-codename> 404s, since
# Docker publishes separate per-distro repos with non-overlapping codename
# sets - verified directly: both
# https://download.docker.com/linux/debian/dists/bookworm/Release and
# .../trixie/Release resolve, matching the same host's Ubuntu path, but
# under "debian" not "ubuntu"). /etc/os-release's ID field is what these
# repos are actually keyed by; Raspberry Pi OS (raspbian) uses Docker's
# debian repo per Docker's own install docs, since it's Debian-based.
# Anything else this script hasn't verified against a real Docker apt repo
# fails clearly here instead of silently trying (and likely 404ing on) a
# guess.
DOCKER_APT_DISTRO=""
if [ -f /etc/os-release ]; then
    # shellcheck source=/dev/null
    . /etc/os-release
    case "${ID:-}" in
        ubuntu) DOCKER_APT_DISTRO="ubuntu" ;;
        debian|raspbian) DOCKER_APT_DISTRO="debian" ;;
    esac
fi
if [ -z "$DOCKER_APT_DISTRO" ]; then
    log "Error: this script only supports Ubuntu and Debian (detected: ${ID:-unknown})." >&2
    log "See the Docker Compose install method in README.md for other distros." >&2
    exit 1
fi

# Create installation directory
log "Creating installation directory..."
if [ ! -d "$INSTALL_DIR" ]; then
    mkdir -p "$INSTALL_DIR"
    log "Created directory: $INSTALL_DIR"
else
    log "Directory already exists: $INSTALL_DIR"
fi

# Check if Docker is installed
if ! command -v docker &> /dev/null; then
    log "Docker not found. Installing Docker..."

    # Install Docker
    apt-get update
    apt-get remove -y docker docker-engine docker.io containerd runc || true
    apt-get install -y ca-certificates curl gnupg lsb-release
    mkdir -p /etc/apt/keyrings
    curl -fsSL "https://download.docker.com/linux/$DOCKER_APT_DISTRO/gpg" | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/$DOCKER_APT_DISTRO $(lsb_release -cs) stable" > /etc/apt/sources.list.d/docker.list
    apt-get update
    apt-get install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin
    log "Docker installed successfully"
else
    log "Docker already installed"
fi

# Make sure Docker starts on boot - containers with `restart: unless-stopped`
# only come back automatically if the daemon itself does.
systemctl enable docker >/dev/null 2>&1 || log "Warning: could not enable docker on boot (no systemd?)"
systemctl start docker >/dev/null 2>&1 || true

# Pick whichever Docker Compose is actually available - the v2 plugin
# (`docker compose`) or, on older installs, the standalone `docker-compose`.
if docker compose version &> /dev/null; then
    COMPOSE="docker compose"
elif command -v docker-compose &> /dev/null; then
    COMPOSE="docker-compose"
else
    log "Docker Compose not found. Installing the Compose plugin..."
    apt-get update
    apt-get install -y docker-compose-plugin
    COMPOSE="docker compose"
fi
log "Using Compose command: $COMPOSE"

# Generate (once) a random Postgres password, kept in .env. Never
# regenerated on re-run: Postgres only reads POSTGRES_PASSWORD on the very
# first startup against an empty data volume, so a changed password here
# would stop matching what's actually in ./obzly-data/pgdata and lock
# Obzly out of its own database.
ENV_FILE="$INSTALL_DIR/.env"
if [ ! -f "$ENV_FILE" ]; then
    log "Generating a random Postgres password..."
    if command -v openssl &> /dev/null; then
        GENERATED_PASSWORD="$(openssl rand -hex 16)"
    else
        GENERATED_PASSWORD="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
    fi

    cat > "$ENV_FILE" << EOF
# Generated by install-obzly.sh on $(date '+%Y-%m-%d %H:%M:%S').
# Do not commit this file - it holds your Postgres password.
POSTGRES_PASSWORD=$GENERATED_PASSWORD
OBZLY_IMAGE_TAG=$OBZLY_IMAGE_TAG
# Email for the bootstrap super-admin account (blank = the role's default:
# "admin", or "superadmin" on a control plane). Editing this and restarting
# renames the existing account.
ADMIN_EMAIL=$ADMIN_EMAIL
EOF
    chmod 600 "$ENV_FILE"
    log ".env created at $ENV_FILE (mode 600)"
else
    log ".env already exists at $ENV_FILE - keeping existing password"
    # Re-running an installer upgrades to its image tag.
    if grep -q '^OBZLY_IMAGE_TAG=' "$ENV_FILE"; then
        sed -i "s/^OBZLY_IMAGE_TAG=.*/OBZLY_IMAGE_TAG=$OBZLY_IMAGE_TAG/" "$ENV_FILE"
    else
        echo "OBZLY_IMAGE_TAG=$OBZLY_IMAGE_TAG" >> "$ENV_FILE"
    fi
    log "Image tag set to $OBZLY_IMAGE_TAG in $ENV_FILE"
fi

# Write docker-compose.yml, but only on first install - never overwrite an
# existing one, so re-running this script can't silently discard any manual
# edits (e.g. a bumped image tag, added OBJECT_STORAGE_URL, custom ports).
COMPOSE_FILE="$INSTALL_DIR/docker-compose.yml"
if [ ! -f "$COMPOSE_FILE" ]; then
    log "Writing docker-compose.yml to $INSTALL_DIR (role: $OBZLY_ROLE)"

    # ROLE_ENV_EXTRA/SERVICE_MODE_LINE inject ARCHITECTURE.md's "Multi-site:
    # control plane and Sites" vars into the obzly service's environment
    # below when OBZLY_ROLE isn't the default "standalone" - see
    # docker-compose.control-plane.yml.example/docker-compose.multisite.yml.example
    # for the leaner, role-specific compose shapes this keeps things simple
    # relative to (this installer always writes the same full standalone
    # shape, just with extra env vars set - harmless ones like an unused
    # OTLP port stay present even in control-plane mode, since
    # SERVICE_MODE=control-plane already makes the app itself ignore them).
    SERVICE_MODE_LINE=""
    ROLE_ENV_EXTRA=""
    case "$OBZLY_ROLE" in
        control-plane)
            SERVICE_MODE_LINE="      SERVICE_MODE: control-plane"
            ROLE_ENV_EXTRA="      PUBLIC_URL: \"$PUBLIC_URL\"
      SITE_ALLOW_INSECURE_URLS: \"false\""
            ;;
        site)
            ROLE_ENV_EXTRA="      SITE_ID: \"$SITE_ID\"
      SITE_NAME: \"${SITE_NAME:-$SITE_ID}\"
      CONTROL_PLANE_ISSUER: \"$CONTROL_PLANE_ISSUER\""
            if [ -n "$CONTROL_PLANE_JWKS_URL" ]; then
                ROLE_ENV_EXTRA="$ROLE_ENV_EXTRA
      CONTROL_PLANE_JWKS_URL: \"$CONTROL_PLANE_JWKS_URL\""
            fi
            if [ -n "$CONTROL_PLANE_EGRESS_CIDRS" ]; then
                ROLE_ENV_EXTRA="$ROLE_ENV_EXTRA
      CONTROL_PLANE_EGRESS_CIDRS: \"$CONTROL_PLANE_EGRESS_CIDRS\""
            fi
            ;;
    esac

    cat > "$COMPOSE_FILE" << EOF
version: "3"

# Postgres and Valkey run as their own containers - see
# https://github.com/obzly/obzly/blob/main/ARCHITECTURE.md for why.
# Managed by install-obzly.sh - safe to hand-edit afterwards, the script
# won't overwrite this file on a re-run.

services:
  postgres:
    image: postgres:18-alpine
    container_name: obzly-postgres
    restart: unless-stopped
    environment:
      POSTGRES_DB: obzly
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: \${POSTGRES_PASSWORD}
    volumes:
      # Postgres 18+ images store data one level up from older 17-and-earlier
      # images (they manage a version-specific subdirectory internally, to
      # support in-place major-version upgrades) - mounting at the old
      # .../data path makes the container refuse to start.
      - ./obzly-data/pgdata:/var/lib/postgresql
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U postgres"]
      interval: 5s
      timeout: 5s
      retries: 10

  valkey:
    image: valkey/valkey:9.2-alpine
    container_name: obzly-valkey
    restart: unless-stopped
    volumes:
      - ./obzly-data/valkey-data:/data
    healthcheck:
      test: ["CMD", "valkey-cli", "ping"]
      interval: 5s
      timeout: 5s
      retries: 10

  obzly:
    container_name: obzly
    image: ghcr.io/obzly/obzly:\${OBZLY_IMAGE_TAG:-$OBZLY_IMAGE_TAG}
    restart: unless-stopped
    ports:
      - "$OBZLY_HTTP_PORT:4005"
      # OTLP gRPC logs receiver. OTLP/HTTP needs no separate port - it
      # shares the port above (POST /v1/logs).
      - "$OBZLY_OTLP_PORT:4317"
    volumes:
      - ./obzly-data:/obzly-data
    environment:
$SERVICE_MODE_LINE
      ENV_MODE: production
      DATABASE_DSN: "host=postgres user=postgres password=\${POSTGRES_PASSWORD} dbname=obzly port=5432 sslmode=disable"
      VALKEY_HOST: valkey
      VALKEY_PORT: "6379"
      VALKEY_IS_SSL: "false"
      LOGS_DATA_DIR: /obzly-data/segments
      # Leave unset for local-disk-only. See backend/.env.production.example
      # for the object storage URL format and what else enabling it needs.
      OBJECT_STORAGE_URL: ""
      OTLP_GRPC_PORT: "4317"
      ADMIN_EMAIL: "\${ADMIN_EMAIL:-}"
$ROLE_ENV_EXTRA
    depends_on:
      postgres:
        condition: service_healthy
      valkey:
        condition: service_healthy
    healthcheck:
      test: ["CMD-SHELL", "curl -f http://localhost:\$\${HTTP_PORT:-4005}/api/v1/system/health"]
      interval: 5s
      timeout: 5s
      retries: 30
      start_period: 60s
EOF
    log "docker-compose.yml created successfully"
else
    log "docker-compose.yml already exists at $COMPOSE_FILE - leaving it as-is"
fi

# If ufw is active, open the ports Obzly needs - otherwise the install
# can succeed and pass its own health check (which runs from inside the
# container's network namespace) while still being unreachable from
# anywhere else, which is a confusing failure mode on a fresh VM.
#
# SECURITY_REVIEW.md #23 ("installer exposes plaintext HTTP/gRPC"): on a
# typical cloud VM (a fresh EC2/Droplet/etc. instance, ufw active by
# default in a lot of hardening guides/images) this line is what actually
# punches both ports straight through to the public internet - and
# neither one has any TLS. Login credentials, session tokens, API keys,
# and every log/metric/trace byte ingested all travel in the clear to
# anyone on the network path. Automating real TLS here (Let's Encrypt via
# Caddy/certbot) needs a domain name pointed at this server, which this
# script has no way to know unattended - see the loud warning after
# startup below instead, which is the honest, in-scope fix: make sure
# nobody walks away from this install without knowing HTTP-only is what
# they got, before they put anything sensitive through it.
#
# So it asks first (on the terminal - stdin is this script when piped from
# curl), and without a terminal or --open-firewall it leaves ufw alone.
OBZLY_FIREWALL_OPENED_TO_INTERNET=false
if [ "$OPEN_FIREWALL" != "no" ] && command -v ufw &> /dev/null && ufw status | grep -q "Status: active"; then
    if [ "$OPEN_FIREWALL" = "ask" ]; then
        OPEN_FIREWALL=no
        # The subshell probes /dev/tty: a failed redirect there can't end the script.
        if (exec </dev/tty) 2>/dev/null; then
            printf 'ufw is active. Open ports %s/tcp and %s/tcp (plain HTTP and OTLP gRPC, no TLS) to every network? [y/N] ' \
                "$OBZLY_HTTP_PORT" "$OBZLY_OTLP_PORT" >/dev/tty
            answer=""
            read -r answer </dev/tty || true
            case "$answer" in
                y|Y|yes|YES) OPEN_FIREWALL=yes ;;
            esac
        fi
    fi
    if [ "$OPEN_FIREWALL" = "yes" ]; then
        log "ufw is active - allowing ports $OBZLY_HTTP_PORT/tcp and $OBZLY_OTLP_PORT/tcp"
        ufw allow "$OBZLY_HTTP_PORT"/tcp || true
        ufw allow "$OBZLY_OTLP_PORT"/tcp || true
        OBZLY_FIREWALL_OPENED_TO_INTERNET=true
    else
        log "ufw is active and was left alone: Obzly is reachable only from this host until you"
        log "  allow it, e.g. ufw allow from 192.0.2.0/24 to any port $OBZLY_HTTP_PORT proto tcp"
    fi
fi

# Start Obzly
log "Pulling the Obzly image and starting containers..."
cd "$INSTALL_DIR"
$COMPOSE pull
$COMPOSE up -d

# Quick health check
log "Waiting for container to be ready..."
READY=false
for i in {1..30}; do
    if docker ps --filter "name=obzly" --filter "health=healthy" -q | grep -q .; then
        READY=true
        break
    fi
    sleep 5
done

SERVER_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
ACCESS_URL="http://${SERVER_IP:-localhost}:${OBZLY_HTTP_PORT}"

if [ "$READY" = true ]; then
    log "✅ Obzly is healthy and ready!"
else
    log "⚠️  Obzly did not report healthy within 150s - check 'docker logs obzly' for details."
fi

log "Obzly installation completed."
log "-------------------------------------------"
log "Role: $OBZLY_ROLE"
log "Install directory: $INSTALL_DIR (docker-compose.yml + .env)"
log "Postgres password:  stored in $ENV_FILE (chmod 600)"
log "Access Obzly at: $ACCESS_URL"
case "$OBZLY_ROLE" in
    control-plane)
        log "This is a control plane (ARCHITECTURE.md \"Multi-site: control plane"
        log "and Sites\") - sign in, then register each Site from the \"Sites\" tab."
        ;;
    site)
        log "This install is enrolled as Site \"$SITE_ID\" behind the control plane"
        log "at $CONTROL_PLANE_ISSUER - register it from that control plane's"
        log "\"Sites\" tab (api_url: $ACCESS_URL) and grant yourself access."
        ;;
esac
log ""
log "⚠️  SECURITY WARNING: this is plain, unencrypted HTTP - no TLS."
log "   Your login password, session token, API keys, and every log/"
log "   metric/trace byte sent to Obzly travel in the clear to anyone"
log "   on the network path between a client and this server."
if [ "$OBZLY_FIREWALL_OPENED_TO_INTERNET" = true ]; then
    log "   ufw was active, so ports $OBZLY_HTTP_PORT and $OBZLY_OTLP_PORT were just"
    log "   opened to the public internet, not just your own network."
fi
log "   Before using this for anything beyond local/trusted-network"
log "   testing, put a TLS-terminating reverse proxy (nginx, Caddy,"
log "   Nginx Proxy Manager, ...) in front of Obzly and stop exposing"
log "   ports $OBZLY_HTTP_PORT/$OBZLY_OTLP_PORT directly."
log ""
log "To manage it later:"
log "  cd $INSTALL_DIR && $COMPOSE ps        # status"
log "  cd $INSTALL_DIR && $COMPOSE logs -f   # logs"
log "  re-run the latest release's installer, or set OBZLY_IMAGE_TAG in .env, then"
log "  cd $INSTALL_DIR && $COMPOSE pull && $COMPOSE up -d   # update"
log ""
if [ "$OBZLY_ROLE" = "site" ]; then
    log "To reset THIS Site's own local break-glass admin password (its central-"
    log "identity users, if any, are reset from the control plane instead):"
else
    log "To reset the admin password:"
fi
ROOT_ADMIN_HINT="admin"; [ "$OBZLY_ROLE" = "control-plane" ] && ROOT_ADMIN_HINT="superadmin"
[ -n "$ADMIN_EMAIL" ] && ROOT_ADMIN_HINT="$ADMIN_EMAIL"
log "  docker exec -it obzly ./main --email=\"$ROOT_ADMIN_HINT\"   # prompts for the new password (hidden input)"
}

main "$@"
