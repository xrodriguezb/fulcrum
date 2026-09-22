import { HttpResponse, http } from 'msw';

export const emptySnapshot = {
  outbox: { pending: 0, failing: 0, published: 0, oldest_unpublished_seconds: 0 },
  dead_letters: 0,
  stuck_idempotency_keys: 0,
  observed_at: '2026-09-21T10:00:00Z',
};

export const sampleOrder = {
  id: '0b7c6f4e-9d3a-4f1b-8c2d-5a6e7f8b9c0d',
  customer_id: '11111111-2222-4333-8444-555555555555',
  status: 'pending' as const,
  total_cents: 2100,
  currency: 'EUR',
  lines: [{ sku: 'WIDGET-001', quantity: 2, unit_price_cents: 1050 }],
  created_at: '2026-09-21T10:00:00Z',
};

/** Default handlers describe a healthy but empty system. */
export const handlers = [
  http.get('/api/v1/orders', () =>
    HttpResponse.json({ items: [], total: 0, limit: 20, offset: 0 }),
  ),
  http.get('/api/v1/inventory', () => HttpResponse.json({ items: [] })),
  http.get('/api/v1/ops/outbox', () => HttpResponse.json(emptySnapshot)),
  http.get('/api/v1/ops/dead-letters', () =>
    HttpResponse.json({ items: [], total: 0, limit: 20, offset: 0 }),
  ),
];

/** problem builds an RFC 9457 response, which is what the API really returns. */
export function problem(status: number, code: string, detail: string): Response {
  return HttpResponse.json(
    {
      type: `https://fulcrum.dev/problems/${code.toLowerCase().replaceAll('_', '-')}`,
      title: code,
      status,
      code,
      detail,
      instance: '/api/v1/orders',
      trace_id: '4bf92f3577b34da6a3ce929d0e0e4736',
    },
    { status, headers: { 'Content-Type': 'application/problem+json' } },
  );
}
