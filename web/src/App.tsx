import type { ReactNode } from 'react';
import { ErrorBoundary } from './components/ErrorBoundary';
import { OrdersPanel } from './features/orders/OrdersPanel';
import { InventoryPanel } from './features/inventory/InventoryPanel';
import { OperationsPanel } from './features/ops/OperationsPanel';
import { DeadLettersPanel } from './features/ops/DeadLettersPanel';

/**
 * App lays the console out as landmarks. Each panel has its own error boundary,
 * because the panels fail independently and an operator triaging an incident
 * should not lose the whole page to one of them.
 */
export function App(): ReactNode {
  return (
    <>
      <header className="app-header">
        <h1>Fulcrum operations console</h1>
        <p className="muted">Order intake, inventory and the event pipeline.</p>
      </header>

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
