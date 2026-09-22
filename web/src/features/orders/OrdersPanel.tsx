import type { ReactNode } from 'react';
import { Panel, StateView } from '../../components/Panel';
import { fromQuery } from '../../lib/requestState';
import type { OrderPage } from '../../api/client';
import { useOrders } from './useOrders';
import { CreateOrderForm } from './CreateOrderForm';

/** OrdersPanel shows the order list and the creation form. */
export function OrdersPanel(): ReactNode {
  const query = useOrders();
  const state = fromQuery<OrderPage>(query, (page) => page.items.length === 0);

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
                <tr key={order.id}>
                  <th scope="row">
                    <code>{order.id.slice(0, 8)}</code>
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
    </Panel>
  );
}

function formatMoney(cents: number, currency: string): string {
  return `${(cents / 100).toFixed(2)} ${currency}`;
}
