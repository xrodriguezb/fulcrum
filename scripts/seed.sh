#!/usr/bin/env bash
# Seeds inventory. Idempotent: running it twice resets the quantities rather than
# adding to them, so a demo can be rerun without recreating the database.
set -euo pipefail

COMPOSE_FILE="${COMPOSE_FILE:-deploy/compose.yaml}"
SKU="${1:-WIDGET-001}"
AVAILABLE="${2:-5}"
PRICE_CENTS="${3:-1050}"

docker compose -f "${COMPOSE_FILE}" exec -T postgres \
  psql -v ON_ERROR_STOP=1 -U fulcrum -d fulcrum -c "
INSERT INTO inventory_items (sku, available, reserved, unit_price_cents, currency, version)
VALUES ('${SKU}', ${AVAILABLE}, 0, ${PRICE_CENTS}, 'EUR', 1)
ON CONFLICT (sku) DO UPDATE
SET available = EXCLUDED.available, reserved = 0, version = inventory_items.version + 1;
" > /dev/null

echo "seeded ${SKU} with ${AVAILABLE} units at ${PRICE_CENTS} cents"
