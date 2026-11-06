#!/usr/bin/env bash
#
# Appends synthetic MySQL 8.x error-log lines forever, matching the format
# the MySQL data source's generated OTel Collector config
# (frontend/src/features/projects/ui/dataSources/mysqlDataSource.ts)
# parses by default - so the collector can tail this file with zero
# config changes.
set -euo pipefail

LOG_PATH="${LOG_PATH:-/var/log/mysql/error.log}"
INTERVAL_SECONDS="${LOG_INTERVAL_SECONDS:-2}"

mkdir -p "$(dirname "$LOG_PATH")"
touch "$LOG_PATH"

# level|code|message - kept together so level/code/message stay consistent,
# same as real mysqld output.
ENTRIES=(
  "Note|MY-010926|Aborted connection to db: 'app' user: 'app_user' host: 'app-1' (Got an error reading communication packets)"
  "Note|MY-011323|Plugin mysqlx reported: 'Update TCP port to 33060 for X Plugin'"
  "System|MY-010931|/usr/sbin/mysqld: ready for connections. Version: '8.0.35' socket: '/var/run/mysqld/mysqld.sock' port: 3306"
  "Warning|MY-010055|IP address could not be resolved, hostname will not be resolved for this connection"
  "Warning|MY-013360|Plugin group_replication reported: 'Member state is now ONLINE'"
  "ERROR|MY-010735|Timeout waiting for replica dump thread to be killed"
)

echo "Generating fake MySQL error logs to $LOG_PATH every ${INTERVAL_SECONDS}s..."

while true; do
  entry="${ENTRIES[$((RANDOM % ${#ENTRIES[@]}))]}"
  level="${entry%%|*}"
  rest="${entry#*|}"
  code="${rest%%|*}"
  message="${rest#*|}"
  timestamp=$(date -u +"%Y-%m-%dT%H:%M:%S.%6NZ")
  thread_id=$((RANDOM % 50))

  echo "${timestamp} ${thread_id} [${level}] [${code}] [Server] ${message}" >>"$LOG_PATH"

  sleep "$INTERVAL_SECONDS"
done
