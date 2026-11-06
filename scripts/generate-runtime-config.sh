#!/bin/bash
# Generates /app/ui/build/runtime-config.js from the container's own
# environment at startup - the frontend bundle is built once (baked into
# the image) but a handful of settings (OAuth client IDs, IS_CLOUD,
# WEBSITE_URL) are only known at deploy time, so this file is regenerated
# fresh every time the container starts (see start.sh/Dockerfile).
#
# Deliberately a real script file, COPY'd into the image, rather than
# inlined into start.sh's own Dockerfile heredoc: Dockerfile's
# `COPY <<EOF` heredoc performs its OWN variable/backslash substitution at
# BUILD time (needed so ${DOLLAR}-prefixed references meant for the
# CONTAINER's runtime environment - IS_CLOUD, GITHUB_CLIENT_ID, etc. -
# survive into the generated start.sh as literal shell rather than being
# evaluated against the empty build-time environment). Getting an escaped
# string ($'\n', \', \\) through that AND a nested bash heredoc's own
# quoting correctly is exactly the kind of thing that's easy to get
# subtly wrong without it being obvious from a build log - confirmed
# firsthand: an earlier version of this fix looked correct in the built
# image (docker run --entrypoint cat showed the expected-looking script)
# but a live test with IS_CLOUD="test' + alert(1) + '" showed the
# injection still worked, because the backslashes it needed had been
# silently mangled by Docker's own heredoc processing before bash ever
# saw them. A plain file has no such double-substitution problem: what's
# in the file on disk is exactly what bash evaluates at container start.
set -e

# Escapes a value for safe embedding inside a single-quoted JS string
# literal below - without this, a value containing a single quote or
# backslash (an accidental copy-paste error into one of these env vars is
# the realistic case today, since only the deployer sets them - but cheap
# to close properly regardless) could break out of the string literal and
# inject arbitrary JS into runtime-config.js, which every visitor's
# browser loads and executes as part of the page.
js_escape() {
    local value="$1"
    value="${value//\\/\\\\}"
    value="${value//\'/\\\'}"
    value="${value//$'\n'/\\n}"
    printf '%s' "$value"
}

# MODE tells the SPA which role this container plays (ARCHITECTURE.md
# "Multi-site: control plane and Sites"): 'control-plane' makes it render
# the Site picker and address every Site-scoped API call via
# /sites/{siteId}/api/v1/...; anything else (including the single-site
# default, SERVICE_MODE unset/all/api) is 'site' and behaves exactly as
# before. CONTROL_PLANE_URL/SITE_ID/SITE_NAME are only consumed by a
# Site's own login page (a "sign in via the control plane" link + banner)
# and are harmless empty.
case "${SERVICE_MODE:-all}" in
    control-plane) UI_MODE="control-plane" ;;
    *)             UI_MODE="site" ;;
esac

cat > /app/ui/build/runtime-config.js << JSEOF
// Runtime configuration injected at container startup
// This file is generated dynamically and should not be edited manually
window.__RUNTIME_CONFIG__ = {
  IS_CLOUD: '$(js_escape "${IS_CLOUD:-false}")',
  GITHUB_CLIENT_ID: '$(js_escape "${GITHUB_CLIENT_ID:-}")',
  GOOGLE_CLIENT_ID: '$(js_escape "${GOOGLE_CLIENT_ID:-}")',
  WEBSITE_URL: '$(js_escape "${WEBSITE_URL:-https://obzly.com}")',
  MODE: '$(js_escape "${UI_MODE}")',
  CONTROL_PLANE_URL: '$(js_escape "${CONTROL_PLANE_ISSUER:-}")',
  SITE_ID: '$(js_escape "${SITE_ID:-}")',
  SITE_NAME: '$(js_escape "${SITE_NAME:-}")'
};
JSEOF
