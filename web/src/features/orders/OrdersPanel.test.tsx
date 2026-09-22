import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, http } from 'msw';
import { OrdersPanel } from './OrdersPanel';
import { renderWithClient, renderWithRefetch } from '../../test/render';
import { server } from '../../test/server';
import { problem, sampleOrder } from '../../test/handlers';

describe('OrdersPanel', () => {
  it('shows a loading state before the first response arrives', async () => {
    server.use(
      http.get('/api/v1/orders', async () => {
        await new Promise((resolve) => setTimeout(resolve, 50));
        return HttpResponse.json({ items: [], total: 0, limit: 20, offset: 0 });
      }),
    );

    renderWithClient(<OrdersPanel />);

    expect(screen.getByText('Loading...')).toBeInTheDocument();
    await waitFor(() => {
      expect(screen.getByText('No orders yet.')).toBeInTheDocument();
    });
  });

  it('distinguishes an empty list from a missing one', async () => {
    renderWithClient(<OrdersPanel />);

    expect(await screen.findByText('No orders yet.')).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });

  it('lists the orders it receives', async () => {
    server.use(
      http.get('/api/v1/orders', () =>
        HttpResponse.json({ items: [sampleOrder], total: 1, limit: 20, offset: 0 }),
      ),
    );

    renderWithClient(<OrdersPanel />);

    const table = await screen.findByRole('table');
    expect(within(table).getByText('WIDGET-001 x2')).toBeInTheDocument();
    expect(within(table).getByText('21.00 EUR')).toBeInTheDocument();
  });

  it('creates an order and announces the result', async () => {
    const user = userEvent.setup();
    let created = false;

    server.use(
      http.post('/api/v1/orders', () => {
        created = true;
        return HttpResponse.json(sampleOrder, { status: 201 });
      }),
      http.get('/api/v1/orders', () =>
        HttpResponse.json({
          items: created ? [sampleOrder] : [],
          total: created ? 1 : 0,
          limit: 20,
          offset: 0,
        }),
      ),
    );

    renderWithClient(<OrdersPanel />);

    await user.type(screen.getByLabelText('Sku'), 'widget-001');
    await user.clear(screen.getByLabelText('Quantity'));
    await user.type(screen.getByLabelText('Quantity'), '2');
    await user.click(screen.getByRole('button', { name: 'Reserve inventory' }));

    expect(await screen.findByText(/Order .* created\./)).toBeInTheDocument();
    await waitFor(() => {
      expect(screen.getByRole('table')).toBeInTheDocument();
    });
  });

  it('rejects an invalid sku without calling the api', async () => {
    const user = userEvent.setup();
    let calls = 0;
    server.use(
      http.post('/api/v1/orders', () => {
        calls += 1;
        return HttpResponse.json(sampleOrder, { status: 201 });
      }),
    );

    renderWithClient(<OrdersPanel />);

    await user.type(screen.getByLabelText('Sku'), 'ab');
    await user.click(screen.getByRole('button', { name: 'Reserve inventory' }));

    expect(await screen.findByText(/A sku is three to thirty two characters/)).toBeInTheDocument();
    expect(calls).toBe(0);
  });

  // The case the optimistic update exists for: the stock went while the operator
  // was typing, so the row that was added has to disappear again.
  it('rolls back the optimistic row when inventory is insufficient', async () => {
    const user = userEvent.setup();

    server.use(
      http.get('/api/v1/orders', () =>
        HttpResponse.json({ items: [], total: 0, limit: 20, offset: 0 }),
      ),
      http.post('/api/v1/orders', () =>
        problem(409, 'INVENTORY_INSUFFICIENT', 'The requested quantity is no longer available.'),
      ),
    );

    renderWithClient(<OrdersPanel />);
    expect(await screen.findByText('No orders yet.')).toBeInTheDocument();

    await user.type(screen.getByLabelText('Sku'), 'WIDGET-001');
    await user.click(screen.getByRole('button', { name: 'Reserve inventory' }));

    expect(await screen.findByText(/That quantity is no longer available/)).toBeInTheDocument();

    // The optimistic row is gone and the empty state is back.
    await waitFor(() => {
      expect(screen.getByText('No orders yet.')).toBeInTheDocument();
    });
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });

  it('reports a server failure with its stable code', async () => {
    const user = userEvent.setup();
    server.use(
      http.post('/api/v1/orders', () =>
        problem(500, 'INTERNAL_ERROR', 'The request could not be completed.'),
      ),
    );

    renderWithClient(<OrdersPanel />);

    await user.type(screen.getByLabelText('Sku'), 'WIDGET-001');
    await user.click(screen.getByRole('button', { name: 'Reserve inventory' }));

    const messages = await screen.findAllByText(/INTERNAL_ERROR/);
    expect(messages.length).toBeGreaterThan(0);
    expect(screen.getByText(/4bf92f3577b34da6a3ce929d0e0e4736/)).toBeInTheDocument();
  });

  it('renders an error state when the list cannot be read', async () => {
    server.use(
      http.get('/api/v1/orders', () =>
        problem(503, 'SERVICE_UNAVAILABLE', 'A dependency is not reachable.'),
      ),
    );

    renderWithClient(<OrdersPanel />);

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('SERVICE_UNAVAILABLE');
  });
});

// A background refetch that fails must not take the table away. An operator
// reading a list during an incident needs the numbers they already have, marked
// as possibly out of date, rather than an error banner where the table was.
describe('OrdersPanel when a refresh fails', () => {
  it('keeps showing the last good data and says it may be stale', async () => {
    let calls = 0;
    server.use(
      http.get('/api/v1/orders', () => {
        calls += 1;
        if (calls === 1) {
          return HttpResponse.json({ items: [sampleOrder], total: 1, limit: 20, offset: 0 });
        }
        return problem(503, 'SERVICE_UNAVAILABLE', 'A dependency is not reachable.');
      }),
    );

    const { rerenderQuery } = renderWithRefetch(<OrdersPanel />);

    const table = await screen.findByRole('table');
    expect(within(table).getByText('WIDGET-001 x2')).toBeInTheDocument();

    await rerenderQuery();

    await waitFor(() => {
      expect(screen.getByText(/could not be refreshed/)).toBeInTheDocument();
    });
    // The data is still there.
    expect(within(screen.getByRole('table')).getByText('WIDGET-001 x2')).toBeInTheDocument();
  });

  it('opens the detail of the order whose identifier is activated', async () => {
    const user = userEvent.setup();
    const second = { ...sampleOrder, id: 'aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee' };
    server.use(
      http.get('/api/v1/orders', () =>
        HttpResponse.json({ items: [sampleOrder, second], total: 2, limit: 20, offset: 0 }),
      ),
      http.get('/api/v1/orders/:id', ({ params }) =>
        HttpResponse.json({ ...sampleOrder, id: String(params.id) }),
      ),
    );

    renderWithClient(<OrdersPanel />);

    await user.click(await screen.findByRole('button', { name: `Open order ${second.id}` }));

    const detail = await screen.findByRole('region', { name: 'Order detail' });
    expect(within(detail).getByText(second.id)).toBeInTheDocument();
  });

  it('marks the selected row, so the detail is not read against the wrong order', async () => {
    const user = userEvent.setup();
    server.use(
      http.get('/api/v1/orders', () =>
        HttpResponse.json({ items: [sampleOrder], total: 1, limit: 20, offset: 0 }),
      ),
      http.get('/api/v1/orders/:id', () => HttpResponse.json(sampleOrder)),
    );

    renderWithClient(<OrdersPanel />);

    const open = await screen.findByRole('button', { name: `Open order ${sampleOrder.id}` });
    expect(open).toHaveAttribute('aria-expanded', 'false');

    await user.click(open);

    expect(open).toHaveAttribute('aria-expanded', 'true');
  });

  it('returns focus to the row when the detail closes', async () => {
    const user = userEvent.setup();
    server.use(
      http.get('/api/v1/orders', () =>
        HttpResponse.json({ items: [sampleOrder], total: 1, limit: 20, offset: 0 }),
      ),
      http.get('/api/v1/orders/:id', () => HttpResponse.json(sampleOrder)),
    );

    renderWithClient(<OrdersPanel />);

    const open = await screen.findByRole('button', { name: `Open order ${sampleOrder.id}` });
    await user.click(open);
    await screen.findByRole('region', { name: 'Order detail' });

    await user.keyboard('{Escape}');

    await waitFor(() => {
      expect(screen.queryByRole('region', { name: 'Order detail' })).not.toBeInTheDocument();
    });
    expect(open).toHaveFocus();
  });
});
