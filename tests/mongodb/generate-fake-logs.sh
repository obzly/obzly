#!/usr/bin/env bash
#
# Appends synthetic mongod log lines (one JSON object per line, matching
# MongoDB 4.4+'s default log format) forever, to the path the MongoDB data
# source's generated OTel Collector config (frontend/src/features/
# projects/ui/dataSources/mongodbDataSource.ts) tails by default - so the
# collector can pick these up with zero config changes.
set -euo pipefail

LOG_PATH="${LOG_PATH:-/var/log/mongodb/mongod.log}"
INTERVAL_SECONDS="${LOG_INTERVAL_SECONDS:-1}"

mkdir -p "$(dirname "$LOG_PATH")"
touch "$LOG_PATH"

# component, message pairs - kept together so the message always makes
# sense for its component, same as real mongod output.
COMPONENTS=(NETWORK NETWORK COMMAND COMMAND STORAGE WRITE CONTROL ACCESS)
IDS=(22943 51800 51803 4939300 22430 20649 20698 5286307)
MESSAGES=(
  "Connection accepted"
  "Connection ended"
  "command database.collection command: find"
  "command database.collection command: insert"
  "WiredTiger message"
  "Slow query"
  "Set featureCompatibilityVersion"
  "Authentication succeeded"
)
# Mostly informational, occasional warning, rare error - mirrors a
# healthy-but-not-silent mongod.
SEVERITY_WEIGHTS=(I I I I I I I I W W E)

random_element() {
  local -n arr=$1
  echo "${arr[$((RANDOM % ${#arr[@]}))]}"
}

echo "Generating fake MongoDB logs to $LOG_PATH every ${INTERVAL_SECONDS}s..."

while true; do
  idx=$((RANDOM % ${#COMPONENTS[@]}))
  component="${COMPONENTS[$idx]}"
  id="${IDS[$idx]}"
  message="${MESSAGES[$idx]}"
  severity=$(random_element SEVERITY_WEIGHTS)
  timestamp=$(date -u +"%Y-%m-%dT%H:%M:%S.%3N+00:00")
  conn_id=$((RANDOM % 9000 + 1000))
  duration=$((RANDOM % 500 + 1))

  echo "{\"t\":{\"\$date\":\"${timestamp}\"},\"s\":\"${severity}\",\"c\":\"${component}\",\"id\":${id},\"ctx\":\"conn${conn_id}\",\"msg\":\"${message}\",\"attr\":{\"durationMillis\":${duration}}}" >>"$LOG_PATH"

  sleep "$INTERVAL_SECONDS"
done
