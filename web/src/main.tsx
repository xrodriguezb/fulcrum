import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { App } from './App';
import './styles.css';

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // An operations console is read constantly and its data changes on its
      // own, so a short stale time beats caching aggressively.
      staleTime: 1000,
      retry: 1,
      refetchOnWindowFocus: true,
    },
  },
});

const container = document.getElementById('root');
if (container === null) {
  throw new Error('the root element is missing from index.html');
}

createRoot(container).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <App />
    </QueryClientProvider>
  </StrictMode>,
);
