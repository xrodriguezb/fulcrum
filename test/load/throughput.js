import http from "k6/http";
import { check } from "k6";
import { Counter, Rate } from "k6/metrics";
import { uuidv4 } from "https://jslib.k6.io/k6-utils/1.4.0/index.js";

// What this answers: how many orders a second the write path sustains, and at
// what latency, when inventory is not the constraint.
//
// The contention scenario in reservation.js measures the opposite: it starves
// the shelf on purpose, so most of its responses are refusals and its rate says
// nothing about throughput. Both numbers matter, and reporting one as the other
// is how a load test ends up flattering the system.

const created = new Counter("orders_created");
const rejected = new Counter("orders_rejected");
const accepted = new Rate("accepted_orders");

const API = __ENV.API || "http://localhost:8080";
const SKU = __ENV.SKU || "THROUGHPUT-001";
const CUSTOMER = "11111111-2222-4333-8444-555555555555";
const RATE = Number(__ENV.RATE || 100);
const DURATION = __ENV.DURATION || "30s";

export const options = {
  summaryTrendStats: ["avg", "min", "med", "p(90)", "p(95)", "p(99)", "max"],
  scenarios: {
    // A constant arrival rate rather than a fixed number of virtual users:
    // closed model load hides saturation, because slow responses simply lower
    // the offered rate until the system looks fine at every level.
    sustained: {
      executor: "constant-arrival-rate",
      rate: RATE,
      timeUnit: "1s",
      duration: DURATION,
      preAllocatedVUs: Math.max(10, Math.ceil(RATE / 4)),
      maxVUs: Math.max(50, RATE * 2),
    },
  },
  thresholds: {
    // Inventory is stocked for the whole run, so a refusal here is a real
    // failure rather than the expected answer it is under contention.
    orders_rejected: ["count==0"],
    accepted_orders: ["rate>0.99"],
    http_req_duration: ["p(95)<1000"],
    // A dropped iteration means k6 could not start the request at the offered
    // rate, so the reported throughput would be below the offered one for a
    // reason that is not the system under test.
    dropped_iterations: ["count==0"],
  },
};

export function setup() {
  const response = http.get(`${API}/api/v1/inventory`);
  if (response.status !== 200) {
    throw new Error(`cannot read inventory: status ${response.status}`);
  }
  const item = response.json().items.find((entry) => entry.sku === SKU);
  if (!item) {
    throw new Error(
      `the sku ${SKU} is not seeded, run scripts/seed.sh ${SKU} <units>`,
    );
  }

  // The run buys one unit per iteration, so the shelf has to outlast it.
  const needed = RATE * durationSeconds(DURATION);
  if (item.available < needed) {
    throw new Error(
      `the sku ${SKU} holds ${item.available} units and the run needs ${needed}: this would measure refusals, not throughput`,
    );
  }
  return { available: item.available };
}

export default function () {
  const payload = JSON.stringify({
    customer_id: CUSTOMER,
    lines: [{ sku: SKU, quantity: 1 }],
  });

  const response = http.post(`${API}/api/v1/orders`, payload, {
    headers: {
      "Content-Type": "application/json",
      "Idempotency-Key": `throughput-${uuidv4()}`,
    },
  });

  if (response.status === 201) {
    created.add(1);
  } else {
    rejected.add(1);
  }

  accepted.add(response.status === 201);
  check(response, { "order created": (r) => r.status === 201 });
}

export function handleSummary(data) {
  return {
    stdout: textSummary(data),
    "/scripts/throughput-summary.json": JSON.stringify(data, null, 2),
  };
}

function durationSeconds(value) {
  const match = /^(\d+)([sm])$/.exec(value);
  if (match === null) {
    throw new Error(`cannot read the duration ${value}`);
  }
  return match[2] === "m" ? Number(match[1]) * 60 : Number(match[1]);
}

function textSummary(data) {
  const values = (name) => data.metrics[name]?.values ?? {};
  const duration = values("http_req_duration");
  const thresholds = [];

  for (const [name, metric] of Object.entries(data.metrics)) {
    for (const [expression, result] of Object.entries(
      metric.thresholds ?? {},
    )) {
      thresholds.push(`${result.ok ? "pass" : "FAIL"}  ${name} ${expression}`);
    }
  }

  return [
    "",
    "sustained throughput",
    `  offered      ${RATE} orders/s for ${DURATION}`,
    `  completed    ${values("iterations").count ?? 0}`,
    `  achieved     ${(values("iterations").rate ?? 0).toFixed(1)} orders/s`,
    `  created      ${values("orders_created").count ?? 0}`,
    `  rejected     ${values("orders_rejected").count ?? 0}`,
    `  dropped      ${values("dropped_iterations").count ?? 0}`,
    `  p50          ${(duration.med ?? 0).toFixed(1)} ms`,
    `  p95          ${(duration["p(95)"] ?? 0).toFixed(1)} ms`,
    `  p99          ${(duration["p(99)"] ?? 0).toFixed(1)} ms`,
    "",
    "thresholds",
    ...thresholds.map((line) => `  ${line}`),
    "",
  ].join("\n");
}
