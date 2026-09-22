import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { ErrorBoundary } from './ErrorBoundary';

function Exploding(): never {
  throw new Error('the panel data was not what it claimed');
}

describe('ErrorBoundary', () => {
  it('contains a failing panel and leaves its siblings alone', () => {
    // React logs the caught error, which is noise in the test output rather than
    // a signal: the assertion below is the signal.
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined);

    render(
      <>
        <ErrorBoundary title="Broken">
          <Exploding />
        </ErrorBoundary>
        <section aria-labelledby="sibling">
          <h2 id="sibling">Working panel</h2>
          <p>Still here</p>
        </section>
      </>,
    );

    expect(screen.getByRole('alert')).toHaveTextContent('This panel could not be displayed');
    expect(screen.getByText('Still here')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Try again' })).toBeInTheDocument();

    consoleError.mockRestore();
  });
});
