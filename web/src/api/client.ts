import type { components } from './schema';
import { ApiError } from '../lib/requestState';

export type Order = components['schemas']['Order'];
export type OrderPage = components['schemas']['OrderPage'];
export type InventoryPage = components['schemas']['InventoryPage'];
export type InventoryItem = components['schemas']['InventoryItem'];
export type OperationalSnapshot = components['schemas']['OperationalSnapshot'];
export type DeadLetterPage = components['schemas']['DeadLetterPage'];
export type DeadLetter = components['schemas']['DeadLetter'];
export type Problem = components['schemas']['Problem'];
export type CreateOrderRequest = components['schemas']['CreateOrderRequest'];

/**
 * The base url is relative so the console works behind any host. In development
 * the Vite proxy forwards it to the API, in the container the web server does.
 */
const baseUrl = '/api/v1';

/** request performs a call and turns a problem document into a typed error. */
async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers);
  headers.set('Accept', 'application/json');

  const response = await fetch(`${baseUrl}${path}`, { ...init, headers });

  if (!response.ok) {
    throw await toProblem(response);
  }
  return (await response.json()) as T;
}

async function toProblem(response: Response): Promise<ApiError> {
  try {
    const body: unknown = await response.json();
    if (typeof body === 'object' && body !== null) {
      const problem = body as Partial<Problem>;
      return new ApiError({
        code: problem.code ?? 'UNKNOWN_ERROR',
        title: problem.title ?? 'The request failed',
        detail: problem.detail ?? '',
        status: problem.status ?? response.status,
        traceId: problem.trace_id,
      });
    }
  } catch {
    // A response that is not a problem document still has to render as one.
  }
  return new ApiError({
    code: 'UNKNOWN_ERROR',
    title: 'The request failed',
    detail: `The server answered with status ${response.status}.`,
    status: response.status,
  });
}

export const api = {
  listOrders: (limit = 20): Promise<OrderPage> => request<OrderPage>(`/orders?limit=${limit}`),

  listInventory: (): Promise<InventoryPage> => request<InventoryPage>('/inventory'),

  snapshot: (): Promise<OperationalSnapshot> => request<OperationalSnapshot>('/ops/outbox'),

  deadLetters: (limit = 20): Promise<DeadLetterPage> =>
    request<DeadLetterPage>(`/ops/dead-letters?limit=${limit}`),

  createOrder: (body: CreateOrderRequest, idempotencyKey: string): Promise<Order> =>
    request<Order>('/orders', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        // The key is generated once per submission and reused by a retry, which
        // is the whole point of the header.
        'Idempotency-Key': idempotencyKey,
      },
      body: JSON.stringify(body),
    }),
};

/** newIdempotencyKey produces one key per submission attempt. */
export function newIdempotencyKey(): string {
  return crypto.randomUUID();
}
