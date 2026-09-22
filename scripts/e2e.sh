#!/usr/bin/env bash
# Compose smoke test: the primary flow, idempotent replay, a broker outage and
# its recovery, and the oversell check.
#
# It runs in CI and locally through `make e2e`, against the images the pipeline
# just built rather than against a development server.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

cleanup() {
  if [ "${FULCRUM_KEEP_STACK:-0}" != "1" ]; then
    compose down -v --remove-orphans > /dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

echo "== bringing the stack up =="
# From a clean state, always. The script asserts exact row counts, so a database
# left behind by a previous run or by the demo would fail it for the wrong reason.
compose down -v --remove-orphans > /dev/null 2>&1 || true
compose up -d --build
wait_for_stack

echo
echo "== seeding inventory =="
scripts/seed.sh WIDGET-001 5 1050

echo
echo "== primary flow =="
status="$(create_order "e2e-$(date +%s)-1" WIDGET-001 2)"
require "order created" 201 "${status}"
order_id="$(order_body | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')"
[ -n "${order_id}" ] || { echo "FAIL the response carried no order id" >&2; exit 1; }

available="$(psql_query "SELECT available FROM inventory_items WHERE sku = 'WIDGET-001'")"
require "inventory reserved" 3 "${available}"

echo
echo "== idempotent replay =="
key="e2e-replay-$(date +%s)"
first_status="$(create_order "${key}" WIDGET-001 1)"
first_body="$(order_body)"
second_status="$(create_order "${key}" WIDGET-001 1)"
second_body="$(order_body)"
require "first request" 201 "${first_status}"
require "replayed request" 201 "${second_status}"
require "replay returns the stored bytes" "${first_body}" "${second_body}"
orders="$(psql_query "SELECT count(*) FROM orders")"
require "one order per key" 2 "${orders}"

echo
echo "== the consumer confirms orders =="
deadline=$((SECONDS + 60))
confirmed=0
while [ "${SECONDS}" -lt "${deadline}" ]; do
  confirmed="$(psql_query "SELECT count(*) FROM orders WHERE status = 'confirmed'")"
  [ "${confirmed}" = "2" ] && break
  sleep 1
done
require "orders confirmed by the consumer" 2 "${confirmed}"

echo
echo "== broker outage =="
compose stop nats > /dev/null
scripts/seed.sh WIDGET-002 50 500 > /dev/null
for i in $(seq 1 5); do
  status="$(create_order "e2e-outage-$(date +%s)-${i}" WIDGET-002 1)"
  require "order accepted while the broker is down" 201 "${status}"
done
pending="$(psql_query "SELECT count(*) FROM outbox_events WHERE published_at IS NULL")"
if [ "${pending}" -lt 5 ]; then
  echo "FAIL the outbox did not hold the events: ${pending} pending" >&2
  exit 1
fi
echo "ok   outbox holds ${pending} unpublished events"

echo
echo "== broker recovery =="
compose start nats > /dev/null
wait_for_healthy nats
deadline=$((SECONDS + 120))
while [ "${SECONDS}" -lt "${deadline}" ]; do
  pending="$(psql_query "SELECT count(*) FROM outbox_events WHERE published_at IS NULL")"
  [ "${pending}" = "0" ] && break
  sleep 2
done
require "backlog drained" 0 "${pending}"

duplicates="$(psql_query "
SELECT coalesce(max(count), 0) FROM (
  SELECT count(*) AS count FROM processed_events GROUP BY consumer_name, event_id
) counts")"
require "no event processed twice" 1 "${duplicates}"

echo
echo "== oversell check =="
oversold="$(psql_query "SELECT count(*) FROM inventory_items WHERE available < 0 OR reserved < 0")"
require "nothing oversold" 0 "${oversold}"

echo
echo "end to end checks passed"
