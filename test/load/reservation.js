import http from 'k6/http';
import { check } from 'k6';
import { Counter, Rate } from 'k6/metrics';
import { uuidv4 } from 'https://jslib.k6.io/k6-utils/1.4.0/index.js';

// The claim under load: with five units on the shelf and two hundred buyers, five
// orders exist afterwards and the shelf holds zero. Anything else is an oversell.

const created = new Counter('orders_created');
const conflicted = new Counter('orders_conflicted');
const unexpected = new Counter('orders_unexpected');
const oversold = new Counter('oversold');
const acceptable = new Rate('acceptable_responses');

const API = __ENV.API || 'http://localhost:8080';
const SKU = __ENV.SKU || 'WIDGET-001';
const CUSTOMER = '11111111-2222-4333-8444-555555555555';

export const options = {
  // p(99) is not computed by default, and the demo quotes it.
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
  scenarios: {
    // Two hundred buyers arriving at once, which is the shape of the claim.
    contention: {
      executor: 'per-vu-iterations',
      vus: Number(__ENV.VUS || 200),
      iterations: 1,
      maxDuration: '60s',
    },
  },
  thresholds: {
    // The run fails if a single unit was sold twice. This is the assertion the
    // whole repository exists to make, so it is a gate and not a report.
    oversold: ['count==0'],
    // Every response has to be either a creation or a refusal. A 500 is neither.
    orders_unexpected: ['count==0'],
    acceptable_responses: ['rate>0.99'],
    http_req_duration: ['p(95)<2000'],
  },
};

export function setup() {
  const response = http.get(`${API}/api/v1/inventory`);
  if (response.status !== 200) {
    throw new Error(`cannot read inventory: status ${response.status}`);
  }
  const item = response.json().items.find((entry) => entry.sku === SKU);
  if (!item) {
    throw new Error(`the sku ${SKU} is not seeded`);
  }
  return { available: item.available, reserved: item.reserved };
}

export default function () {
  const payload = JSON.stringify({
    customer_id: CUSTOMER,
    lines: [{ sku: SKU, quantity: 1 }],
  });

  const response = http.post(`${API}/api/v1/orders`, payload, {
    headers: {
      'Content-Type': 'application/json',
      // A fresh key per attempt: this measures contention on inventory, not the
      // idempotency path.
      'Idempotency-Key': `load-${uuidv4()}`,
    },
  });

  if (response.status === 201) {
    created.add(1);
  } else if (response.status === 409) {
    conflicted.add(1);
  } else {
    unexpected.add(1);
  }

  acceptable.add(response.status === 201 || response.status === 409);
  check(response, {
    'status is a creation or a refusal': (r) => r.status === 201 || r.status === 409,
  });
}

// handleSummary writes the machine readable summary next to the script so the
// demo can quote real numbers instead of a screenshot of a terminal.
export function handleSummary(data) {
  return {
    stdout: textSummary(data),
    '/scripts/summary.json': JSON.stringify(data, null, 2),
  };
}

function textSummary(data) {
  const values = (name) => data.metrics[name]?.values ?? {};
  const duration = values('http_req_duration');
  const thresholds = [];

  for (const [name, metric] of Object.entries(data.metrics)) {
    for (const [expression, result] of Object.entries(metric.thresholds ?? {})) {
      thresholds.push(`${result.ok ? 'pass' : 'FAIL'}  ${name} ${expression}`);
    }
  }

  return [
    '',
    'reservation load test',
    `  attempted    ${values('iterations').count ?? 0}`,
    `  created      ${values('orders_created').count ?? 0}`,
    `  conflicted   ${values('orders_conflicted').count ?? 0}`,
    `  unexpected   ${values('orders_unexpected').count ?? 0}`,
    `  oversold     ${values('oversold').count ?? 0}`,
    `  p50          ${(duration.med ?? 0).toFixed(1)} ms`,
    `  p95          ${(duration['p(95)'] ?? 0).toFixed(1)} ms`,
    `  p99          ${(duration['p(99)'] ?? 0).toFixed(1)} ms`,
    '',
    'thresholds',
    ...thresholds.map((line) => `  ${line}`),
    '',
  ].join('\n');
}

export function teardown(initial) {
  const response = http.get(`${API}/api/v1/inventory`);
  if (response.status !== 200) {
    throw new Error(`cannot read inventory after the run: status ${response.status}`);
  }
  const item = response.json().items.find((entry) => entry.sku === SKU);

  // Two independent statements of the same invariant. Negative availability
  // would mean the guard failed; a total that no longer matches would mean units
  // were created or destroyed somewhere in the path.
  if (item.available < 0 || item.reserved < 0) {
    oversold.add(1);
  }
  if (item.available + item.reserved !== initial.available + initial.reserved) {
    oversold.add(1);
  }

  console.log(
    `sku ${SKU}: started with ${initial.available} available, ended with ${item.available} available and ${item.reserved} reserved`,
  );
}
