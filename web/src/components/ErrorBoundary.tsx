import { Component, type ErrorInfo, type ReactNode } from 'react';

interface Props {
  readonly title: string;
  readonly children: ReactNode;
}

interface State {
  readonly failure: Error | null;
}

/**
 * ErrorBoundary wraps one panel.
 *
 * Panels fail independently because their data sources do. One panel whose
 * render throws must not blank the console: an operator looking at the dead
 * letter queue during an incident should not lose the outbox depth because the
 * inventory table hit a bad row.
 */
export class ErrorBoundary extends Component<Props, State> {
  public override state: State = { failure: null };

  public static getDerivedStateFromError(error: Error): State {
    return { failure: error };
  }

  public override componentDidCatch(error: Error, info: ErrorInfo): void {
    // The console is the last place this information exists, so it is logged
    // rather than swallowed.
    console.error('panel failed', error, info.componentStack);
  }

  public override render(): ReactNode {
    const { failure } = this.state;
    if (failure) {
      return (
        <section aria-labelledby={`${this.props.title}-error`} className="panel panel-failed">
          <h2 id={`${this.props.title}-error`}>{this.props.title}</h2>
          <p role="alert">
            This panel could not be displayed. The rest of the console is unaffected.
          </p>
          <button
            type="button"
            onClick={() => {
              this.setState({ failure: null });
            }}
          >
            Try again
          </button>
        </section>
      );
    }
    return this.props.children;
  }
}
