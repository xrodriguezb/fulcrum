import type { ReactNode } from 'react';
import type { RequestState } from '../lib/requestState';

interface PanelProps {
  readonly title: string;
  readonly id: string;
  readonly actions?: ReactNode;
  readonly children: ReactNode;
}

/**
 * Panel is a labelled region, so screen reader users can navigate between them.
 *
 * The region takes tabIndex -1 rather than 0: it is a jump target for the panel
 * navigation, not another stop in an already long tab order.
 */
export function Panel({ title, id, actions, children }: PanelProps): ReactNode {
  return (
    <section className="panel" id={regionId(id)} aria-labelledby={id} tabIndex={-1}>
      {/*
        A div rather than a header element: a header nested in a section is not a
        banner landmark by specification, but role mapping implementations
        disagree, and a page with four banners is a page a screen reader user
        cannot navigate.
      */}
      <div className="panel-header">
        <h2 id={id}>{title}</h2>
        {actions}
      </div>
      {children}
    </section>
  );
}

/** regionId derives the element id of a panel region from its heading id. */
export function regionId(headingId: string): string {
  return `${headingId}-region`;
}

interface StateViewProps<T> {
  readonly state: RequestState<T>;
  readonly emptyMessage: string;
  readonly children: (data: T) => ReactNode;
}

/**
 * StateView renders every case of the union deliberately. A component that only
 * handles success and error is a component that renders nothing while loading
 * and blank when the list is empty, and those look identical to a user.
 */
export function StateView<T>({ state, emptyMessage, children }: StateViewProps<T>): ReactNode {
  switch (state.status) {
    case 'idle':
      return <p className="muted">Nothing requested yet.</p>;
    case 'loading':
      return (
        <p className="muted" role="status">
          Loading...
        </p>
      );
    case 'empty':
      return <p className="muted">{emptyMessage}</p>;
    case 'stale':
      return (
        <>
          <p className="warning" role="status">
            {state.reason}
          </p>
          {children(state.data)}
        </>
      );
    case 'error':
      return (
        <div role="alert" className="error">
          <p>{state.error.title}</p>
          <p className="muted">
            {state.error.detail} <code>{state.error.code}</code>
          </p>
          {state.error.traceId === undefined ? null : (
            <p className="muted">
              Trace <code>{state.error.traceId}</code>
            </p>
          )}
        </div>
      );
    case 'success':
      return children(state.data);
  }
}
