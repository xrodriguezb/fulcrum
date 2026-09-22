import { describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, http } from 'msw';
import { OrderDetail } from './OrderDetail';
import { renderWithClient } from '../../test/render';
import { server } from '../../test/server';
import { problem, sampleOrder } from '../../test/handlers';

const detailedOrder = {
  ...sampleOrder,
  status: 'confirmed' as const,
  total_cents: 3150,
  lines: [
    { sku: 'WIDGET-001', quantity: 2, unit_price_cents: 1050 },
    { sku: 'GADGET-002', quantity: 1, unit_price_cents: 1050 },
  ],
};

describe('OrderDetail', () => {
  it('reads the order by id and shows every line with its own subtotal', async () => {
    server.use(
      http.get(`/api/v1/orders/${sampleOrder.id}`, () => HttpResponse.json(detailedOrder)),
    );

    renderWithClient(<OrderDetail orderId={sampleOrder.id} onClose={vi.fn()} />);

    expect(await screen.findByText('GADGET-002')).toBeInTheDocument();
    // Two of one line and one of another at the same unit price: the subtotals
    // differ, so a component that rendered the unit price twice would pass a
    // weaker assertion than this one.
    expect(screen.getByText('21.00 EUR')).toBeInTheDocument();
    expect(screen.getByText('10.50 EUR')).toBeInTheDocument();
    expect(screen.getByText('31.50 EUR')).toBeInTheDocument();
  });

  it('shows the full identifier, which the list truncates', async () => {
    server.use(
      http.get(`/api/v1/orders/${sampleOrder.id}`, () => HttpResponse.json(detailedOrder)),
    );

    renderWithClient(<OrderDetail orderId={sampleOrder.id} onClose={vi.fn()} />);

    expect(await screen.findByText(sampleOrder.id)).toBeInTheDocument();
  });

  it('renders a problem document rather than an empty panel when the order is gone', async () => {
    server.use(
      http.get(`/api/v1/orders/${sampleOrder.id}`, () =>
        problem(404, 'ORDER_NOT_FOUND', 'No order carries that identifier.'),
      ),
    );

    renderWithClient(<OrderDetail orderId={sampleOrder.id} onClose={vi.fn()} />);

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('No order carries that identifier.');
    expect(alert).toHaveTextContent('ORDER_NOT_FOUND');
  });

  it('closes on the escape key, so the keyboard can leave the way it entered', async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    server.use(
      http.get(`/api/v1/orders/${sampleOrder.id}`, () => HttpResponse.json(detailedOrder)),
    );

    renderWithClient(<OrderDetail orderId={sampleOrder.id} onClose={onClose} />);
    await screen.findByText('GADGET-002');

    await user.keyboard('{Escape}');

    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('moves focus to the detail region so a screen reader lands on the new content', async () => {
    server.use(
      http.get(`/api/v1/orders/${sampleOrder.id}`, () => HttpResponse.json(detailedOrder)),
    );

    renderWithClient(<OrderDetail orderId={sampleOrder.id} onClose={vi.fn()} />);

    await waitFor(() => {
      expect(screen.getByRole('region', { name: /order/i })).toHaveFocus();
    });
  });
});
