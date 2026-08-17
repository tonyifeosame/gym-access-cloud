import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, type RenderResult } from '@testing-library/react'
import type { ReactNode } from 'react'

import { NotificationsProvider } from '../components/Notifications'
import { SessionProvider } from '../session/SessionProvider'

/**
 * Rendering helpers.
 *
 * The query client is built PER TEST with retries off. Retries turn a test that
 * asserts an error state into one that waits for three round trips and then
 * times out, and a shared client leaks one test's cache into the next — both
 * failures look like flakiness rather than like the configuration mistakes they
 * are.
 */
export function makeTestQueryClient(overrides: { gcTime?: number } = {}): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: false,
        // Zero by default so one test's cache cannot leak into the next. A test
        // that seeds an OBSERVER-LESS entry with setQueryData and then awaits
        // something before asserting on it needs a window, because an entry
        // nothing is watching is collected on the next tick.
        gcTime: overrides.gcTime ?? 0,
        staleTime: 0,
      },
      mutations: { retry: false },
    },
  })
}

/** Renders with a query client only — for a hook or component needing no session. */
export function renderWithQuery(
  ui: ReactNode,
  client: QueryClient = makeTestQueryClient(),
): RenderResult & { queryClient: QueryClient } {
  const result = render(
    <QueryClientProvider client={client}>
      <NotificationsProvider>{ui}</NotificationsProvider>
    </QueryClientProvider>,
  )
  return { ...result, queryClient: client }
}

/**
 * Renders inside a real SessionProvider, which fetches GET /auth/me.
 *
 * The caller seeds the mock server's session first, so this exercises the same
 * restoration path the app uses at boot rather than injecting a session object
 * the app would never receive that way.
 */
export function renderWithSession(
  ui: ReactNode,
  client: QueryClient = makeTestQueryClient(),
): RenderResult & { queryClient: QueryClient } {
  const result = render(
    <QueryClientProvider client={client}>
      <NotificationsProvider>
        <SessionProvider>{ui}</SessionProvider>
      </NotificationsProvider>
    </QueryClientProvider>,
  )
  return { ...result, queryClient: client }
}

/** Wrapper form, for renderHook. */
export function queryWrapper(client: QueryClient = makeTestQueryClient()) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return (
      <QueryClientProvider client={client}>
        <NotificationsProvider>{children}</NotificationsProvider>
      </QueryClientProvider>
    )
  }
}
