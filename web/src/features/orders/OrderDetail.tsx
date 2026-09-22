import { useEffect, useRef, type ReactNode } from 'react';
import { StateView } from '../../components/Panel';
import { fromQuery } from '../../lib/requestState';
import type { Order } from '../../api/client';
import { useOrder } from './useOrders';

interface OrderDetailProps {
  readonly orderId: string;
  readonly onClose: () => void;
}

/**
 * OrderDetail shows one order in full: the identifier the list truncates, the
 * customer, and every line with its own subtotal.
 *
 * It is a region rather than a dialog. Nothing here is modal, and a dialog would
 * trap focus in a read only view that an operator wants to leave by pressing
 * escape or by clicking the next row.
 */
export function OrderDetail({ orderId, onClose }: OrderDetailProps): ReactNode {
  const query = useOrder(orderId);
  const state = fromQuery<Order>(query, () => false);
  const region = useRef<HTMLElement>(null);

  useEffect(() => {
    // Focus follows the selection, so a keyboard or screen reader user is put
    // where the new content is instead of being told it exists somewhere.
    region.current?.focus();
  }, [orderId]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent): void => {
      if (event.key === 'Escape') {
        onClose();
      }
    };
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('keydown', onKeyDown);
    };
  }, [onClose]);

  return (
    <section className="order-detail" aria-label="Order detail" ref={region} tabIndex={-1}>
      <div className="panel-header">
        <h3>Order detail</h3>
        <button type="button" className="secondary" onClick={onClose}>
          Close
        </button>
      </div>

      <StateView state={state} emptyMessage="That order has no lines.">
        {(order) => (
          <>
            <dl className="metrics">
              <div>
                <dt>Identifier</dt>
                <dd>
                  <code>{order.id}</code>
                </dd>
              </div>
              <div>
                <dt>Customer</dt>
                <dd>
                  <code>{order.customer_id}</code>
                </dd>
              </div>
              <div>
                <dt>Status</dt>
                <dd>
                  <span className={`status status-${order.status}`}>{order.status}</span>
                </dd>
              </div>
              <div>
                <dt>Created</dt>
                <dd>
                  <time dateTime={order.created_at}>{formatTime(order.created_at)}</time>
                </dd>
              </div>
            </dl>

            <table>
              <caption className="visually-hidden">Lines of this order</caption>
              <thead>
                <tr>
                  <th scope="col">Sku</th>
                  <th scope="col">Quantity</th>
                  <th scope="col">Unit</th>
                  <th scope="col">Subtotal</th>
                </tr>
              </thead>
              <tbody>
                {order.lines.map((line) => (
                  <tr key={line.sku}>
                    <th scope="row">{line.sku}</th>
                    <td>{line.quantity}</td>
                    <td>{formatMoney(line.unit_price_cents, order.currency)}</td>
                    <td>{formatMoney(line.unit_price_cents * line.quantity, order.currency)}</td>
                  </tr>
                ))}
              </tbody>
              <tfoot>
                <tr>
                  <th scope="row" colSpan={3}>
                    Total
                  </th>
                  <td>{formatMoney(order.total_cents, order.currency)}</td>
                </tr>
              </tfoot>
            </table>
          </>
        )}
      </StateView>
    </section>
  );
}

function formatMoney(cents: number, currency: string): string {
  return `${(cents / 100).toFixed(2)} ${currency}`;
}

function formatTime(iso: string): string {
  const parsed = new Date(iso);
  return Number.isNaN(parsed.getTime()) ? iso : parsed.toISOString().replace('T', ' ').slice(0, 19);
}
