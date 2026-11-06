#!/usr/bin/env bash
#
# Appends synthetic Nginx access/error log lines forever, matching the
# exact "combined" access-log and default error-log formats the Nginx
# data source's generated OTel Collector config (frontend/src/features/
# projects/ui/dataSources/nginxDataSource.ts) parses - so the collector
# can tail these files with zero config changes.
set -euo pipefail

ACCESS_LOG="${ACCESS_LOG_PATH:-/var/log/nginx/access.log}"
ERROR_LOG="${ERROR_LOG_PATH:-/var/log/nginx/error.log}"
INTERVAL_SECONDS="${LOG_INTERVAL_SECONDS:-1}"

mkdir -p "$(dirname "$ACCESS_LOG")" "$(dirname "$ERROR_LOG")"
touch "$ACCESS_LOG" "$ERROR_LOG"

IPS=(192.168.1.23 10.0.0.45 203.0.113.7 198.51.100.12 172.16.4.9)
METHODS=(GET GET GET POST PUT DELETE)
PATHS=(/ /api/users /api/users/42 /api/orders /health /static/app.js /login /api/products)
STATUSES=(200 200 200 200 201 204 301 400 401 403 404 404 500 502)
USER_AGENTS=(
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15"
  "curl/8.4.0"
  "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/120.0"
)
ERROR_LEVELS=(error error warn crit)
ERROR_MESSAGES=(
  "upstream timed out (110: Connection timed out) while reading response header from upstream"
  "connect() failed (111: Connection refused) while connecting to upstream"
  "client intended to send too large body"
  "open() failed (2: No such file or directory)"
)

random_element() {
  local -n arr=$1
  echo "${arr[$((RANDOM % ${#arr[@]}))]}"
}

echo "Generating fake Nginx logs to $ACCESS_LOG and $ERROR_LOG every ${INTERVAL_SECONDS}s..."

while true; do
  ip=$(random_element IPS)
  method=$(random_element METHODS)
  path=$(random_element PATHS)
  status=$(random_element STATUSES)
  bytes=$((RANDOM % 5000 + 100))
  ua=$(random_element USER_AGENTS)
  time_str=$(date -u +"%d/%b/%Y:%H:%M:%S +0000")

  echo "${ip} - - [${time_str}] \"${method} ${path} HTTP/1.1\" ${status} ${bytes} \"-\" \"${ua}\"" >>"$ACCESS_LOG"

  # Emit an error line roughly one in five requests, so most output is
  # normal access traffic - closer to what a real error log looks like.
  if ((RANDOM % 5 == 0)); then
    level=$(random_element ERROR_LEVELS)
    message=$(random_element ERROR_MESSAGES)
    error_time=$(date -u +"%Y/%m/%d %H:%M:%S")
    pid=$((RANDOM % 9000 + 1000))
    cid=$((RANDOM % 900 + 1))
    echo "${error_time} [${level}] ${pid}#0: *${cid} ${message}, client: ${ip}, server: _, request: \"${method} ${path} HTTP/1.1\"" >>"$ERROR_LOG"
  fi

  sleep "$INTERVAL_SECONDS"
done
