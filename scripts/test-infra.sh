#!/usr/bin/env bash
# Validates the Chunk 0.2 docker-compose.yml structure.
# Does NOT require Docker to be running — static checks only.
# If Docker CLI is present it also runs: docker compose config
set -euo pipefail

PASS=0
FAIL=0
ROOT=$(git rev-parse --show-toplevel)
COMPOSE="$ROOT/deploy/docker-compose.yml"

check() {
  local label="$1" cmd="$2"
  if eval "$cmd" &>/dev/null; then
    printf 'PASS  %s\n' "$label"
    PASS=$((PASS + 1))
  else
    printf 'FAIL  %s\n' "$label" >&2
    FAIL=$((FAIL + 1))
  fi
}

# File exists
check "file exists: deploy/docker-compose.yml" "[ -f '$COMPOSE' ]"

# Required services declared
for svc in postgres redis kafka kafka-ui; do
  check "service defined: $svc" "grep -qE '^\s{2}$svc:' '$COMPOSE'"
done

# Every service has a healthcheck block (4 services → 4 healthcheck: lines)
HEALTHCHECK_COUNT=$(grep -c 'healthcheck:' "$COMPOSE" || true)
check "4 healthchecks defined (one per service)" "[ '$HEALTHCHECK_COUNT' -eq 4 ]"

# Named volumes declared at top level
for vol in postgres_data redis_data kafka_data; do
  check "named volume declared: $vol" "grep -qE '^\s{2}$vol:' '$COMPOSE'"
done

# KRaft: no zookeeper SERVICE (word may appear in comments, but not as a service)
check "no zookeeper service" "! grep -qE '^\s{2}zookeeper:' '$COMPOSE'"

# Dual-listener pattern: INTERNAL for containers, EXTERNAL for host
check "INTERNAL listener defined" "grep -q 'INTERNAL' '$COMPOSE'"
check "EXTERNAL listener defined" "grep -q 'EXTERNAL' '$COMPOSE'"

# kafka-ui depends_on kafka (condition: service_healthy)
check "kafka-ui depends_on kafka" "grep -q 'condition: service_healthy' '$COMPOSE'"

# No deprecated 'version:' top-level key (Compose Spec no longer needs it)
check "no deprecated 'version:' key" "! grep -qE '^version:' '$COMPOSE'"

# docker compose validate (only if docker is available)
if command -v docker &>/dev/null; then
  check "docker compose config valid" \
    "docker compose -f '$COMPOSE' config --quiet"
else
  printf 'SKIP  docker compose config (docker not found)\n'
fi

printf '\nResults: %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
