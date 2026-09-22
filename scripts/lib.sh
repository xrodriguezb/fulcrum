#!/usr/bin/env bash
# Shared helpers for the end to end script and the demo.

COMPOSE_FILE="${COMPOSE_FILE:-deploy/compose.yaml}"
API_PORT="${FULCRUM_API_PORT:-8080}"
API="http://localhost:${API_PORT}"
CUSTOMER_ID="11111111-2222-4333-8444-555555555555"

compose() {
  docker compose -f "${COMPOSE_FILE}" "$@"
}

psql_query() {
  compose exec -T postgres psql -qtAX -U fulcrum -d fulcrum -c "$1"
}

# wait_for_healthy polls compose's own health status rather than sleeping, so the
# script is as fast as the stack and never races it.
wait_for_healthy() {
  local service="$1" deadline=$((SECONDS + 180))
  while [ "${SECONDS}" -lt "${deadline}" ]; do
    local status
    status="$(compose ps --format '{{.Service}} {{.Health}}' | awk -v s="${service}" '$1 == s {print $2}')"
    if [ "${status}" = "healthy" ]; then
      return 0
    fi
    sleep 1
  done
  echo "service ${service} did not become healthy" >&2
  compose ps
  return 1
}

wait_for_stack() {
  for service in postgres nats api worker web; do
    wait_for_healthy "${service}"
  done
}

# create_order posts one order and prints "<status> <body>".
create_order() {
  local key="$1" sku="$2" quantity="$3"
  curl -sS -o /tmp/fulcrum-order-body -w '%{http_code}' \
    -X POST "${API}/api/v1/orders" \
    -H 'Content-Type: application/json' \
    -H "Idempotency-Key: ${key}" \
    -d "{\"customer_id\":\"${CUSTOMER_ID}\",\"lines\":[{\"sku\":\"${sku}\",\"quantity\":${quantity}}]}"
}

order_body() {
  cat /tmp/fulcrum-order-body
}

# require asserts an expected value and fails the script when it does not hold.
require() {
  local description="$1" expected="$2" actual="$3"
  if [ "${expected}" != "${actual}" ]; then
    echo "FAIL ${description}: expected ${expected}, got ${actual}" >&2
    return 1
  fi
  echo "ok   ${description}: ${actual}"
}
