import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider } from 'react-router-dom'

import { ApiError } from './api/client'
import { NotificationsProvider } from './components/Notifications'
import { router } from './router'
import { SessionProvider } from './session/SessionProvider'

/**
 * Retry policy.
 *
 * A 4xx will not become a 2xx by asking again -- an authorization failure, a
 * missing record or a rate limit are all answers, not hiccups -- so only
 * transport failures and 5xx are retried. Retrying a 429 in particular would
 * make the situation it reports worse.
 */
function shouldRetry(failureCount: number, error: unknown): boolean {
  if (error instanceof ApiError && error.status < 500) return false
  return failureCount < 2
}

export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: shouldRetry,
        refetchOnWindowFocus: false,
        staleTime: 30_000,
      },
      mutations: { retry: false },
    },
  })
}

/** The router type, taken from the provider so it cannot drift from it. */
type AppRouter = Parameters<typeof RouterProvider>[0]['router']

/**
 * The provider tree. EXACTLY ONE OF EACH, and the order is load-bearing:
 *
 *   QueryClientProvider    the cache everything else reads and writes
 *   └── NotificationsProvider
 *       └── SessionProvider
 *           └── RouterProvider
 *
 * NOTIFICATIONS SIT ABOVE THE ROUTER, which is the only reason a message
 * survives the navigation that usually follows the action producing it --
 * "person removed" is raised on a detail page and read on the list it returns
 * to. Mounted inside the router it would unmount with the page and never be
 * seen. It also sits above SessionProvider so a failure raised while a session
 * is ending is not torn down by the thing that is ending.
 *
 * `router` is injectable for the same reason `queryClient` is: the real one is a
 * browser router built at module load, and a test that had to reconstruct this
 * tree to drive it would be asserting against a copy rather than against what
 * ships. App.test.tsx renders THIS component.
 */
export function App({
  queryClient = createQueryClient(),
  router: appRouter = router,
}: {
  queryClient?: QueryClient
  router?: AppRouter
}) {
  return (
    <QueryClientProvider client={queryClient}>
      <NotificationsProvider>
        <SessionProvider>
          <RouterProvider router={appRouter} />
        </SessionProvider>
      </NotificationsProvider>
    </QueryClientProvider>
  )
}
