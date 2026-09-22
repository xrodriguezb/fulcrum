import '@testing-library/jest-dom/vitest';
import { afterAll, afterEach, beforeAll } from 'vitest';
import { cleanup } from '@testing-library/react';
import { server } from './server';

// The network is mocked at the boundary the browser actually uses, so components
// exercise the real client, the real parsing and the real error mapping.
beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' });
});

afterEach(() => {
  server.resetHandlers();
  cleanup();
});

afterAll(() => {
  server.close();
});

// jsdom implements no layout, so it has no scrollIntoView. Stubbing it here
// keeps the component calling the method every browser does, rather than
// carrying a feature check that exists only to satisfy the test environment.
Element.prototype.scrollIntoView = function scrollIntoView(): void {};
