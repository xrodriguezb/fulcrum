import type { ReactNode } from 'react';
import { Panel } from '../../components/Panel';
import { useOperationalState } from './useOperationalState';

/**
 * OperationsPanel shows the numbers that tell an operator whether the pipeline is
 * keeping up: outbox depth, how old the oldest unpublished event is, dead
 * letters, and idempotency claims that never completed.
 */
export function OperationsPanel(): ReactNode {
  const { snapshot, error, connection } = useOperationalState();

  return (
    <Panel
      title="Pipeline"
      id="ops-panel"
      actions={
        <span className={`connection connection-${connection}`} role="status">
          {connection === 'live' ? 'live' : connection === 'polling' ? 'polling' : 'connecting'}
        </span>
      }
    >
      {error !== null && snapshot === null ? (
        <div role="alert" className="error">
          <p>{error.title}</p>
          <p className="muted">
            {error.detail} <code>{error.code}</code>
          </p>
        </div>
      ) : null}

      {snapshot === null ? (
        <p className="muted" role="status">
          Loading...
        </p>
      ) : (
        <dl className="metrics">
          <div>
            <dt>Outbox pending</dt>
            <dd>{snapshot.outbox.pending}</dd>
          </div>
          <div>
            <dt>Failing</dt>
            <dd>{snapshot.outbox.failing}</dd>
          </div>
          <div>
            <dt>Published</dt>
            <dd>{snapshot.outbox.published}</dd>
          </div>
          <div>
            <dt>Oldest unpublished</dt>
            <dd>{snapshot.outbox.oldest_unpublished_seconds.toFixed(1)}s</dd>
          </div>
          <div>
            <dt>Dead letters</dt>
            <dd>{snapshot.dead_letters}</dd>
          </div>
          <div>
            <dt>Stuck idempotency keys</dt>
            <dd>{snapshot.stuck_idempotency_keys}</dd>
          </div>
        </dl>
      )}
    </Panel>
  );
}
