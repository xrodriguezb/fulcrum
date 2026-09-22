import type { ReactElement, ReactNode } from 'react';
import { render, type RenderResult } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';

/**
 * renderWithClient gives each test its own query client, so one test's cache
 * cannot make another pass. Retries are off: a test that waits for a retry is a
 * test that is slow for no reason.
 */
export function renderWithClient(ui: ReactElement): RenderResult {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, staleTime: 0, gcTime: 0 },
      mutations: { retry: false },
    },
  });

  function Wrapper({ children }: { readonly children: ReactNode }): ReactNode {
    return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
  }

  return render(ui, { wrapper: Wrapper });
}

/**
 * renderWithRefetch exposes a way to force a refetch, which is how a test can
 * exercise the case where fresh data fails while stale data is still on screen.
 */
export function renderWithRefetch(ui: ReactElement): RenderResult & {
  rerenderQuery: () => Promise<void>;
} {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, staleTime: 0, gcTime: 0 },
      mutations: { retry: false },
    },
  });

  function Wrapper({ children }: { readonly children: ReactNode }): ReactNode {
    return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
  }

  const result = render(ui, { wrapper: Wrapper });
  return {
    ...result,
    rerenderQuery: async () => {
      await client.refetchQueries();
    },
  };
}
