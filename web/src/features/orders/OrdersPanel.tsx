import { useCallback, useRef, useState, type ReactNode } from 'react';
import { Panel, StateView } from '../../components/Panel';
import { fromQuery } from '../../lib/requestState';
import type { OrderPage } from '../../api/client';
import { useOrders } from './useOrders';
import { CreateOrderForm } from './CreateOrderForm';
import { OrderDetail } from './OrderDetail';

/** OrdersPanel shows the order list, the creation form and one open detail. */
export function OrdersPanel(): ReactNode {
  const query = useOrders();
  const state = fromQuery<OrderPage>(query, (page) => page.items.length === 0);
  const [selected, setSelected] = useState<string | null>(null);
  // The triggers are held by id rather than by index: the list reorders as
  // orders arrive, and focus has to return to the row, not to the position.
  const triggers = useRef(new Map<string, HTMLButtonElement>());

  const close = useCallback(() => {
    const restore = selected === null ? undefined : triggers.current.get(selected);
    setSelected(null);
    restore?.focus();
  }, [selected]);

  return (
    <Panel title="Orders" id="orders-panel">
      <CreateOrderForm />
      <StateView state={state} emptyMessage="No orders yet.">
        {(page) => (
          <table>
            <caption className="visually-hidden">Orders, newest first</caption>
            <thead>
              <tr>
                <th scope="col">Order</th>
                <th scope="col">Status</th>
                <th scope="col">Lines</th>
                <th scope="col">Total</th>
              </tr>
            </thead>
            <tbody>
              {page.items.map((order) => (
                <tr key={order.id} className={order.id === selected ? 'row-selected' : undefined}>
                  <th scope="row">
                    <button
                      type="button"
                      className="link"
                      aria-label={`Open order ${order.id}`}
                      aria-expanded={order.id === selected}
                      onClick={() => {
                        setSelected(order.id);
                      }}
                      ref={(node) => {
                        if (node === null) {
                          triggers.current.delete(order.id);
                          return;
                        }
                        triggers.current.set(order.id, node);
                      }}
                    >
                      <code>{order.id.slice(0, 8)}</code>
                    </button>
                  </th>
                  <td>
                    <span className={`status status-${order.status}`}>{order.status}</span>
                  </td>
                  <td>{order.lines.map((line) => `${line.sku} x${line.quantity}`).join(', ')}</td>
                  <td>{formatMoney(order.total_cents, order.currency)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </StateView>

      {selected === null ? null : <OrderDetail orderId={selected} onClose={close} />}
    </Panel>
  );
}

function formatMoney(cents: number, currency: string): string {
  return `${(cents / 100).toFixed(2)} ${currency}`;
}
