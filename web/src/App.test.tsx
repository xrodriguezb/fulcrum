import { describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import { axe } from 'vitest-axe';
import { HttpResponse, http } from 'msw';
import { App } from './App';
import { renderWithClient } from './test/render';
import { server } from './test/server';
import { emptySnapshot, sampleOrder } from './test/handlers';

class SilentEventSource {
  public constructor(public readonly url: string) {}
  public addEventListener(): void {}
  public close(): void {}
}

describe('App', () => {
  it('lays the console out as landmarks and labelled regions', async () => {
    vi.stubGlobal('EventSource', SilentEventSource);

    renderWithClient(<App />);

    expect(screen.getByRole('banner')).toBeInTheDocument();
    expect(screen.getByRole('main')).toBeInTheDocument();
    expect(screen.getByRole('contentinfo')).toBeInTheDocument();
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent(
      'Fulcrum operations console',
    );

    for (const panel of ['Pipeline', 'Orders', 'Inventory', 'Dead letters']) {
      expect(await screen.findByRole('region', { name: panel })).toBeInTheDocument();
    }

    vi.unstubAllGlobals();
  });

  // The whole console, with data in every panel, has to pass the automated
  // accessibility checks. Zero violations is the bar, not a target.
  it('has no accessibility violations with data in every panel', async () => {
    vi.stubGlobal('EventSource', SilentEventSource);

    server.use(
      http.get('/api/v1/orders', () =>
        HttpResponse.json({ items: [sampleOrder], total: 1, limit: 20, offset: 0 }),
      ),
      http.get('/api/v1/inventory', () =>
        HttpResponse.json({
          items: [
            {
              sku: 'WIDGET-001',
              available: 4,
              reserved: 1,
              unit_price_cents: 1050,
              currency: 'EUR',
              version: 2,
            },
          ],
        }),
      ),
      http.get('/api/v1/ops/outbox', () => HttpResponse.json(emptySnapshot)),
      http.get('/api/v1/ops/dead-letters', () =>
        HttpResponse.json({
          items: [
            {
              id: '6f1a2b3c-4d5e-4f60-8172-839405a6b7c8',
              event_id: '0b7c6f4e-9d3a-4f1b-8c2d-5a6e7f8b9c0d',
              consumer_name: 'order-projector',
              event_type: 'order.created',
              attempts: 5,
              first_failed_at: '2026-09-21T10:00:00Z',
              last_failed_at: '2026-09-21T10:05:00Z',
              failure_reason: 'The event failed repeatedly and exhausted its retry budget.',
              correlation_id: 'corr-1',
            },
          ],
          total: 1,
          limit: 20,
          offset: 0,
        }),
      ),
    );

    const { container } = renderWithClient(<App />);

    await waitFor(() => {
      expect(screen.getAllByRole('table').length).toBeGreaterThan(1);
    });

    const results = await axe(container);
    expect(results.violations).toEqual([]);

    vi.unstubAllGlobals();
  });
});
