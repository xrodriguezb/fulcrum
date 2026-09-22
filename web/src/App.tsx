import type { MouseEvent, ReactNode } from 'react';
import { ErrorBoundary } from './components/ErrorBoundary';
import { regionId } from './components/Panel';
import { OrdersPanel } from './features/orders/OrdersPanel';
import { InventoryPanel } from './features/inventory/InventoryPanel';
import { OperationsPanel } from './features/ops/OperationsPanel';
import { DeadLettersPanel } from './features/ops/DeadLettersPanel';

/**
 * App lays the console out as landmarks. Each panel has its own error boundary,
 * because the panels fail independently and an operator triaging an incident
 * should not lose the whole page to one of them.
 */
const panels = [
  { id: 'ops-panel', title: 'Pipeline' },
  { id: 'orders-panel', title: 'Orders' },
  { id: 'inventory-panel', title: 'Inventory' },
  { id: 'dlq-panel', title: 'Dead letters' },
] as const;

export function App(): ReactNode {
  return (
    <>
      <header className="app-header">
        <h1>Fulcrum operations console</h1>
        <p className="muted">Order intake, inventory and the event pipeline.</p>
      </header>

      <nav className="panel-nav" aria-label="Panels">
        <ul>
          {panels.map((panel) => (
            <li key={panel.id}>
              <a
                href={`#${regionId(panel.id)}`}
                onClick={(event: MouseEvent<HTMLAnchorElement>) => {
                  // The hash alone scrolls without moving focus in several
                  // browsers, which leaves a keyboard user looking at a panel
                  // while still tabbing through the one above it.
                  event.preventDefault();
                  const target = document.getElementById(regionId(panel.id));
                  target?.focus();
                  target?.scrollIntoView({ block: 'start' });
                }}
              >
                {panel.title}
              </a>
            </li>
          ))}
        </ul>
      </nav>

      <main className="app-main">
        <ErrorBoundary title="Pipeline">
          <OperationsPanel />
        </ErrorBoundary>

        <ErrorBoundary title="Orders">
          <OrdersPanel />
        </ErrorBoundary>

        <ErrorBoundary title="Inventory">
          <InventoryPanel />
        </ErrorBoundary>

        <ErrorBoundary title="Dead letters">
          <DeadLettersPanel />
        </ErrorBoundary>
      </main>

      <footer className="app-footer">
        <p className="muted">
          Delivery is at-least-once. The consumer deduplicates, so one event has one effect.
        </p>
      </footer>
    </>
  );
}
