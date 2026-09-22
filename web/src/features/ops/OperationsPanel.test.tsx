import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import { HttpResponse, http } from 'msw';
import { OperationsPanel } from './OperationsPanel';
import { renderWithClient } from '../../test/render';
import { server } from '../../test/server';
import { emptySnapshot } from '../../test/handlers';

/**
 * FakeEventSource stands in for the browser implementation, which jsdom does not
 * provide. It is deliberately small: the behaviour under test is what the hook
 * does with open, snapshot and error, not how a real stream is framed.
 */
class FakeEventSource {
  public static instances: FakeEventSource[] = [];

  public readonly url: string;
  public closed = false;
  private readonly listeners = new Map<string, ((event: MessageEvent<string>) => void)[]>();

  public constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }

  public addEventListener(type: string, listener: (event: MessageEvent<string>) => void): void {
    const existing = this.listeners.get(type) ?? [];
    existing.push(listener);
    this.listeners.set(type, existing);
  }

  public close(): void {
    this.closed = true;
  }

  public emit(type: string, data = ''): void {
    for (const listener of this.listeners.get(type) ?? []) {
      listener(new MessageEvent(type, { data }));
    }
  }
}

/** These mirror the constants the hook uses, so the timers advance far enough. */
const pollInterval = 3000;
const reconnectDelay = 5000;

describe('OperationsPanel', () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal('EventSource', FakeEventSource);
    vi.useFakeTimers({ shouldAdvanceTime: true });
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('renders the first snapshot and then updates from the stream', async () => {
    renderWithClient(<OperationsPanel />);

    expect(await screen.findByText('Outbox pending')).toBeInTheDocument();
    await waitFor(() => {
      expect(screen.getByText('Dead letters').nextSibling).toHaveTextContent('0');
    });

    const source = FakeEventSource.instances[0];
    expect(source).toBeDefined();
    if (source === undefined) return;

    source.emit('open');
    source.emit(
      'snapshot',
      JSON.stringify({
        ...emptySnapshot,
        outbox: { pending: 12, failing: 2, published: 40, oldest_unpublished_seconds: 3.5 },
        dead_letters: 4,
      }),
    );

    await waitFor(() => {
      expect(screen.getByText('Outbox pending').nextSibling).toHaveTextContent('12');
    });
    expect(screen.getByText('Dead letters').nextSibling).toHaveTextContent('4');
    expect(screen.getByText('live')).toBeInTheDocument();
  });

  // A proxy that drops event streams is common enough that a console which only
  // streams is a console that silently stops updating.
  it('falls back to polling when the stream fails', async () => {
    let polls = 0;
    server.use(
      http.get('/api/v1/ops/outbox', () => {
        polls += 1;
        return HttpResponse.json({
          ...emptySnapshot,
          outbox: { pending: polls, failing: 0, published: 0, oldest_unpublished_seconds: 0 },
        });
      }),
    );

    renderWithClient(<OperationsPanel />);
    expect(await screen.findByText('Outbox pending')).toBeInTheDocument();

    const source = FakeEventSource.instances[0];
    expect(source).toBeDefined();
    if (source === undefined) return;

    source.emit('error');

    await waitFor(() => {
      expect(screen.getByText('polling')).toBeInTheDocument();
    });
    await waitFor(() => {
      expect(polls).toBeGreaterThan(1);
    });
    expect(source.closed).toBe(true);
  });

  it('reports a failure when nothing can be read at all', async () => {
    server.use(
      http.get('/api/v1/ops/outbox', () => HttpResponse.json({ error: 'nope' }, { status: 500 })),
    );

    renderWithClient(<OperationsPanel />);

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('The request failed');
  });

  it('keeps a history of the outbox depth and draws it', async () => {
    renderWithClient(<OperationsPanel />);
    expect(await screen.findByText('Outbox pending')).toBeInTheDocument();

    const source = FakeEventSource.instances[0];
    expect(source).toBeDefined();
    if (source === undefined) return;

    source.emit('open');
    for (const pending of [3, 11, 5]) {
      source.emit(
        'snapshot',
        JSON.stringify({
          ...emptySnapshot,
          outbox: { pending, failing: 0, published: 0, oldest_unpublished_seconds: 0 },
        }),
      );
    }

    // The peak has to survive in the history after the depth falls again: an
    // operator asking whether the pipeline recovered needs the spike, and a
    // component that only remembers the latest snapshot cannot show it.
    await waitFor(() => {
      expect(screen.getByRole('img')).toHaveAccessibleName(/now 5, peak 11/);
    });
  });

  // A reconnection must leave one source of updates, not two. The stream going
  // down starts polling, and the stream coming back has to stop it again, even
  // when a poll request is still in flight at the moment it returns: the request
  // that lands afterwards is what schedules the next one.
  it('stops polling once the stream comes back', async () => {
    let polls = 0;
    const held: { release: (() => void) | null } = { release: null };
    server.use(
      http.get('/api/v1/ops/outbox', async () => {
        polls += 1;
        if (polls === 2) {
          await new Promise<void>((resolve) => {
            held.release = resolve;
          });
        }
        return HttpResponse.json(emptySnapshot);
      }),
    );

    renderWithClient(<OperationsPanel />);
    expect(await screen.findByText('Outbox pending')).toBeInTheDocument();
    await waitFor(() => {
      expect(polls).toBe(1);
    });

    const first = FakeEventSource.instances[0];
    expect(first).toBeDefined();
    if (first === undefined) return;

    // The stream drops, so polling takes over and its request is left in flight.
    first.emit('error');
    await waitFor(() => {
      expect(polls).toBe(2);
    });
    await waitFor(() => {
      expect(held.release).not.toBeNull();
    });

    // The stream comes back and opens while that request is still outstanding.
    await vi.advanceTimersByTimeAsync(reconnectDelay);
    const second = FakeEventSource.instances[1];
    expect(second).toBeDefined();
    if (second === undefined) return;
    second.emit('open');
    await waitFor(() => {
      expect(screen.getByText('live')).toBeInTheDocument();
    });

    // Only now does the outstanding poll return.
    held.release?.();
    await vi.advanceTimersByTimeAsync(pollInterval * 3);

    expect(polls).toBe(2);
  });

  it('does not draw a trend from a single snapshot', async () => {
    renderWithClient(<OperationsPanel />);

    expect(await screen.findByText('Outbox pending')).toBeInTheDocument();
    expect(screen.queryByRole('img')).not.toBeInTheDocument();
  });
});
