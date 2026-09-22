import { useEffect, useRef, useState } from 'react';
import { api, type OperationalSnapshot } from '../../api/client';
import { toApiError, type ApiError } from '../../lib/requestState';

export type StreamStatus = 'connecting' | 'live' | 'polling';

export interface OperationalState {
  readonly snapshot: OperationalSnapshot | null;
  readonly error: ApiError | null;
  readonly connection: StreamStatus;
}

/** pollInterval is the fallback cadence when the stream is unavailable. */
const pollInterval = 3000;

/** reconnectDelay is how long the hook waits before trying the stream again. */
const reconnectDelay = 5000;

/**
 * useOperationalState keeps the console's live numbers current.
 *
 * Server sent events are the primary source and polling is the fallback, not the
 * other way round: a proxy that drops event streams is common enough that a
 * console which only streams is a console that silently stops updating. The
 * connection state is exposed so the interface can say which one is in use
 * instead of pretending they are the same.
 */
export function useOperationalState(): OperationalState {
  const [snapshot, setSnapshot] = useState<OperationalSnapshot | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  const [connection, setConnection] = useState<StreamStatus>('connecting');
  const pollTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    let cancelled = false;
    let source: EventSource | null = null;
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null;

    const poll = (): void => {
      void api
        .snapshot()
        .then((next) => {
          if (cancelled) return;
          setSnapshot(next);
          setError(null);
        })
        .catch((cause: unknown) => {
          if (cancelled) return;
          setError(toApiError(cause));
        })
        .finally(() => {
          if (cancelled) return;
          pollTimer.current = setTimeout(poll, pollInterval);
        });
    };

    const stopPolling = (): void => {
      if (pollTimer.current !== null) {
        clearTimeout(pollTimer.current);
        pollTimer.current = null;
      }
    };

    const connect = (): void => {
      if (typeof EventSource === 'undefined') {
        setConnection('polling');
        poll();
        return;
      }

      source = new EventSource('/api/v1/ops/stream');

      source.addEventListener('open', () => {
        if (cancelled) return;
        setConnection('live');
        stopPolling();
      });

      source.addEventListener('snapshot', (event: MessageEvent<string>) => {
        if (cancelled) return;
        try {
          setSnapshot(JSON.parse(event.data) as OperationalSnapshot);
          setError(null);
        } catch (cause) {
          setError(toApiError(cause));
        }
      });

      source.addEventListener('error', () => {
        if (cancelled) return;
        // The stream is gone. Polling takes over immediately so the numbers keep
        // moving, and a reconnection is attempted in the background.
        source?.close();
        source = null;
        setConnection('polling');
        stopPolling();
        poll();
        reconnectTimer = setTimeout(() => {
          if (cancelled) return;
          setConnection('connecting');
          connect();
        }, reconnectDelay);
      });
    };

    // The first value comes from a plain request, so the console shows numbers
    // before the stream has said anything.
    void api
      .snapshot()
      .then((next) => {
        if (!cancelled) setSnapshot(next);
      })
      .catch((cause: unknown) => {
        if (!cancelled) setError(toApiError(cause));
      });

    connect();

    return () => {
      cancelled = true;
      source?.close();
      stopPolling();
      if (reconnectTimer !== null) {
        clearTimeout(reconnectTimer);
      }
    };
  }, []);

  return { snapshot, error, connection };
}
