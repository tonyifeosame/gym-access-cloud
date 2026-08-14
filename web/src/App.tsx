import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider } from 'react-router-dom'

import { ApiError } from './api/client'
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

export function App({ queryClient = createQueryClient() }: { queryClient?: QueryClient }) {
  return (
    <QueryClientProvider client={queryClient}>
      <SessionProvider>
        <RouterProvider router={router} />
      </SessionProvider>
    </QueryClientProvider>
  )
}
