import type { ReactNode } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api, type InventoryPage } from '../../api/client';
import { Panel, StateView } from '../../components/Panel';
import { fromQuery } from '../../lib/requestState';

/** InventoryPanel shows stock positions, refreshed on a slow interval. */
export function InventoryPanel(): ReactNode {
  const query = useQuery({
    queryKey: ['inventory'],
    queryFn: () => api.listInventory(),
    refetchInterval: 5000,
  });
  const state = fromQuery<InventoryPage>(query, (page) => page.items.length === 0);

  return (
    <Panel title="Inventory" id="inventory-panel">
      <StateView state={state} emptyMessage="No stock positions.">
        {(page) => (
          <table>
            <caption className="visually-hidden">Stock by sku</caption>
            <thead>
              <tr>
                <th scope="col">Sku</th>
                <th scope="col">Available</th>
                <th scope="col">Reserved</th>
                <th scope="col">Price</th>
              </tr>
            </thead>
            <tbody>
              {page.items.map((item) => (
                <tr key={item.sku}>
                  <th scope="row">{item.sku}</th>
                  <td>{item.available}</td>
                  <td>{item.reserved}</td>
                  <td>{(item.unit_price_cents / 100).toFixed(2)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </StateView>
    </Panel>
  );
}
