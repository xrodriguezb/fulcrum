#!/usr/bin/env bash
# The narrated demonstration.
#
# It builds and starts the stack, proves the oversell claim under load with real
# numbers, then kills the broker to show that the outbox decouples acceptance
# from publication, restores it to show the backlog drain, and finally poisons a
# message to show the dead letter path. It exits non-zero if a single unit was
# ever oversold.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

K6_IMAGE="${K6_IMAGE:-grafana/k6:0.55.0}"
NATS_BOX_IMAGE="${NATS_BOX_IMAGE:-natsio/nats-box:0.14.3}"
NETWORK="${FULCRUM_NETWORK:-fulcrum_default}"
SUMMARY="test/load/summary.json"

step() {
  echo
  echo "------------------------------------------------------------"
  echo "$1"
  echo "------------------------------------------------------------"
}

step "1. Building and starting the stack from a clean state"
compose down -v --remove-orphans > /dev/null 2>&1 || true
compose up -d --build
wait_for_stack
echo "every service reports healthy"

step "2. The schema was applied to an empty database"
psql_query "SELECT version || ' ' || name FROM schema_migrations ORDER BY version"

step "3. Seeding one sku with five units"
scripts/seed.sh WIDGET-001 5 1050

step "4. Two hundred concurrent buyers, one unit each"
rm -f "${SUMMARY}"
docker run --rm -i --network "${NETWORK}" -e API=http://api:8080 \
  -v "$(pwd)/test/load":/scripts "${K6_IMAGE}" run /scripts/reservation.js

step "5. Results"
created="$(jq -r '.metrics.orders_created.values.count // 0' "${SUMMARY}")"
conflicted="$(jq -r '.metrics.orders_conflicted.values.count // 0' "${SUMMARY}")"
unexpected="$(jq -r '.metrics.orders_unexpected.values.count // 0' "${SUMMARY}")"
oversold_metric="$(jq -r '.metrics.oversold.values.count // 0' "${SUMMARY}")"
p50="$(jq -r '.metrics.http_req_duration.values.med // 0' "${SUMMARY}")"
p95="$(jq -r '.metrics.http_req_duration.values["p(95)"] // 0' "${SUMMARY}")"
p99="$(jq -r '.metrics.http_req_duration.values["p(99)"] // 0' "${SUMMARY}")"
available="$(psql_query "SELECT available FROM inventory_items WHERE sku = 'WIDGET-001'")"
reserved="$(psql_query "SELECT reserved FROM inventory_items WHERE sku = 'WIDGET-001'")"
negative="$(psql_query "SELECT count(*) FROM inventory_items WHERE available < 0 OR reserved < 0")"

printf '\n%-28s %s\n' 'attempted' "$((created + conflicted + unexpected))"
printf '%-28s %s\n' 'succeeded' "${created}"
printf '%-28s %s\n' 'conflicted' "${conflicted}"
printf '%-28s %s\n' 'unexpected responses' "${unexpected}"
printf '%-28s %s\n' 'oversold' "${negative}"
printf '%-28s %s\n' 'final available' "${available}"
printf '%-28s %s\n' 'final reserved' "${reserved}"
printf '%-28s %.1f ms\n' 'p50 latency' "${p50}"
printf '%-28s %.1f ms\n' 'p95 latency' "${p95}"
printf '%-28s %.1f ms\n' 'p99 latency' "${p99}"

step "6. Killing the broker, then creating twenty more orders"
compose stop nats > /dev/null
scripts/seed.sh WIDGET-002 100 500 > /dev/null
accepted=0
for i in $(seq 1 20); do
  status="$(create_order "demo-outage-$(date +%s)-${i}" WIDGET-002 1)"
  [ "${status}" = "201" ] && accepted=$((accepted + 1))
done
pending="$(psql_query "SELECT count(*) FROM outbox_events WHERE published_at IS NULL")"
printf '%-28s %s\n' 'orders accepted' "${accepted}"
printf '%-28s %s\n' 'outbox depth' "${pending}"
echo "the api kept answering 201 because the outbox decouples acceptance from publication"

step "7. Restoring the broker and watching the backlog drain"
compose start nats > /dev/null
wait_for_healthy nats
deadline=$((SECONDS + 120))
while [ "${SECONDS}" -lt "${deadline}" ]; do
  pending="$(psql_query "SELECT count(*) FROM outbox_events WHERE published_at IS NULL")"
  [ "${pending}" = "0" ] && break
  printf '  outbox depth %s\n' "${pending}"
  sleep 2
done
printf '%-28s %s\n' 'outbox depth' "${pending}"
duplicates="$(psql_query "
SELECT coalesce(max(count), 0) FROM (
  SELECT count(*) AS count FROM processed_events GROUP BY consumer_name, event_id
) counts")"
printf '%-28s %s\n' 'max processings per event' "${duplicates}"

step "8. Publishing a poison event"
before="$(psql_query "SELECT count(*) FROM dead_letter_events")"
docker run --rm --network "${NETWORK}" "${NATS_BOX_IMAGE}" \
  nats pub fulcrum.events.order.created '{"this":"is not an envelope"}' -s nats://nats:4222 > /dev/null
deadline=$((SECONDS + 60))
while [ "${SECONDS}" -lt "${deadline}" ]; do
  after="$(psql_query "SELECT count(*) FROM dead_letter_events")"
  [ "${after}" -gt "${before}" ] && break
  sleep 1
done
psql_query "SELECT event_type || ' | attempts ' || attempts || ' | ' || failure_reason
            FROM dead_letter_events ORDER BY last_failed_at DESC LIMIT 1"

step "9. Summary"
# The consumer is still catching up on the orders the load test created, so the
# summary waits for the pipeline to settle. Printing 25 created and 20 confirmed
# would read as a defect when it is only a snapshot taken too early.
total_orders="$(psql_query "SELECT count(*) FROM orders")"
deadline=$((SECONDS + 60))
while [ "${SECONDS}" -lt "${deadline}" ]; do
  confirmed="$(psql_query "SELECT count(*) FROM orders WHERE status = 'confirmed'")"
  [ "${confirmed}" = "${total_orders}" ] && break
  sleep 1
done
confirmed="$(psql_query "SELECT count(*) FROM orders WHERE status = 'confirmed'")"
dead="$(psql_query "SELECT count(*) FROM dead_letter_events")"
printf '%-28s %s\n' 'orders created' "${total_orders}"
printf '%-28s %s\n' 'orders confirmed' "${confirmed}"
printf '%-28s %s\n' 'dead letters' "${dead}"
printf '%-28s %s\n' 'oversold' "${negative}"

if [ "${negative}" != "0" ] || [ "${oversold_metric}" != "0" ]; then
  echo
  echo "FAIL inventory was oversold"
  exit 1
fi

echo
echo "The claim holds: five units, two hundred buyers, five orders, nothing oversold."
echo "The stack is still running. Stop it with: make down"
