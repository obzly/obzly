#!/bin/sh
# Installs obzly-probe on Linux (amd64/arm64) as a systemd service.
#
#   curl -fsSL <release>/install.sh | sudo OBZLY_CONTROL_PLANE_URL=https://obzly.example.com \
#     OBZLY_PROBE_TOKEN=obz_prb_... sh
#
# OBZLY_PROBE_VERSION picks a release (default: the latest); OBZLY_RELEASES
# is where releases are downloaded from; OBZLY_PROBE_ENV_ALLOW is written to
# the service's environment file.
set -eu

: "${OBZLY_CONTROL_PLANE_URL:?set OBZLY_CONTROL_PLANE_URL to the control plane address}"
: "${OBZLY_PROBE_TOKEN:?set OBZLY_PROBE_TOKEN to the token shown when the probe was added}"
RELEASES="${OBZLY_RELEASES:-https://github.com/obzly/obzly/releases}"

[ "$(id -u)" -eq 0 ] || { echo "run as root (sudo)" >&2; exit 1; }
command -v systemctl >/dev/null || { echo "systemd is required; use the Docker image otherwise" >&2; exit 1; }

case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac

if [ -n "${OBZLY_PROBE_VERSION:-}" ]; then
  version="${OBZLY_PROBE_VERSION#v}"
  base="$RELEASES/download/v$version"
else
  base="$RELEASES/latest/download"
  version=latest
fi

workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT
echo "Downloading obzly-probe ($version, linux/$arch)..."
curl -fsSL -o "$workdir/obzly-probe" "$base/obzly-probe_linux_$arch"
curl -fsSL -o "$workdir/checksums.txt" "$base/checksums.txt"
expected=$(grep " obzly-probe_linux_$arch\$" "$workdir/checksums.txt" | cut -d' ' -f1)
actual=$(sha256sum "$workdir/obzly-probe" | cut -d' ' -f1)
[ -n "$expected" ] && [ "$expected" = "$actual" ] || { echo "checksum mismatch, not installing" >&2; exit 1; }

install -m 0755 "$workdir/obzly-probe" /usr/local/bin/obzly-probe
id obzly-probe >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin obzly-probe

umask 077
cat > /etc/obzly-probe.env <<CONFIG
OBZLY_CONTROL_PLANE_URL=$OBZLY_CONTROL_PLANE_URL
OBZLY_PROBE_TOKEN=$OBZLY_PROBE_TOKEN
# Monitors' \${env:NAME} placeholders read OBZLY_SECRET_* variables set here,
# and the names OBZLY_PROBE_ENV_ALLOW lists (comma-separated, a trailing *
# is a prefix); never OBZLY_PROBE_*, AWS_*, AZURE_* or GOOGLE_*.
OBZLY_PROBE_ENV_ALLOW=${OBZLY_PROBE_ENV_ALLOW:-}
CONFIG
chmod 0600 /etc/obzly-probe.env

curl -fsSL -o /etc/systemd/system/obzly-probe.service "$base/obzly-probe.service"
systemctl daemon-reload
systemctl enable --now obzly-probe
systemctl restart obzly-probe
echo "obzly-probe $(/usr/local/bin/obzly-probe version) is running: journalctl -u obzly-probe -f"
