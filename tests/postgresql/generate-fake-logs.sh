#!/usr/bin/env bash
#
# Appends synthetic PostgreSQL log lines forever, matching the default
# log_line_prefix format the PostgreSQL data source's generated OTel
# Collector config (frontend/src/features/projects/ui/dataSources/
# postgresqlDataSource.ts) parses - so the collector can tail this file
# with zero config changes. Note the regex there expects two spaces
# between the timezone and "[pid]"; matched exactly here.
set -euo pipefail

LOG_PATH="${LOG_PATH:-/var/log/postgresql/postgresql.log}"
INTERVAL_SECONDS="${LOG_INTERVAL_SECONDS:-2}"

mkdir -p "$(dirname "$LOG_PATH")"
touch "$LOG_PATH"

# level|message - kept together so the message matches its level, same as
# real Postgres output. Mostly LOG (routine statement/duration logging),
# occasional WARNING/ERROR.
ENTRIES=(
  "LOG|duration: 12.345 ms  statement: SELECT * FROM users WHERE id = \$1"
  "LOG|duration: 3.201 ms  statement: INSERT INTO orders (user_id, total) VALUES (\$1, \$2)"
  "LOG|connection authorized: user=app_user database=app_db"
  "LOG|connection received: host=10.0.0.12 port=54321"
  "LOG|checkpoint starting: time"
  "WARNING|there is no transaction in progress"
  "ERROR|duplicate key value violates unique constraint \"users_email_key\""
  "ERROR|deadlock detected"
)

echo "Generating fake PostgreSQL logs to $LOG_PATH every ${INTERVAL_SECONDS}s..."

while true; do
  entry="${ENTRIES[$((RANDOM % ${#ENTRIES[@]}))]}"
  level="${entry%%|*}"
  message="${entry#*|}"
  timestamp=$(date -u +"%Y-%m-%d %H:%M:%S.%6N UTC")
  pid=$((RANDOM % 9000 + 1000))

  echo "${timestamp}  [${pid}] ${level}:  ${message}" >>"$LOG_PATH"

  sleep "$INTERVAL_SECONDS"
done
