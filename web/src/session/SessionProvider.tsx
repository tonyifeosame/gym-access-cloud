import { createContext, useCallback, useEffect, useMemo, type ReactNode } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'

import { setUnauthenticatedHandler } from '../api/client'
import { clearCsrfToken } from '../api/csrf'
import * as endpoints from '../api/endpoints'
import type { Session } from '../api/types'

export type SessionStatus = 'loading' | 'authenticated' | 'anonymous' | 'error'

export interface SessionContextValue {
  status: SessionStatus
  session: Session | null
  error: Error | null
  login: (email: string, password: string) => Promise<Session>
  logout: () => Promise<void>
  refresh: () => Promise<void>
}

export const SessionContext = createContext<SessionContextValue | null>(null)

export const SESSION_QUERY_KEY = ['session'] as const

/**
 * Holds the operator session for the whole app.
 *
 * SESSION RESTORATION IS JUST GET /auth/me. The credential is an HttpOnly
 * cookie the browser sends on its own, so "am I still signed in" is a question
 * only the server can answer -- there is nothing in storage to read, which is
 * the point of the cookie being HttpOnly. Every reload asks.
 *
 * A 401 from ANY request, not just this one, ends the session: it may expire, an
 * administrator may disable the account, or a password change elsewhere may
 * revoke it, and each of those arrives on whatever request was in flight.
 */
export function SessionProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient()

  const query = useQuery({
    queryKey: SESSION_QUERY_KEY,
    queryFn: ({ signal }) => endpoints.fetchSession(signal),
    // Anonymous is an answer, not a failure, so there is nothing to retry. A
    // genuine outage surfaces as an error state rather than a login form.
    retry: false,
    staleTime: 30_000,
    refetchOnWindowFocus: true,
  })

  const setSession = useCallback(
    (session: Session | null) => {
      queryClient.setQueryData(SESSION_QUERY_KEY, session)
    },
    [queryClient],
  )

  useEffect(() => {
    setUnauthenticatedHandler(() => {
      clearCsrfToken()
      setSession(null)
      // Anything already fetched belonged to a session that no longer exists.
      void queryClient.invalidateQueries()
    })
    return () => setUnauthenticatedHandler(null)
  }, [queryClient, setSession])

  const login = useCallback(
    async (email: string, password: string) => {
      const session = await endpoints.login(email, password)
      setSession(session)
      return session
    },
    [setSession],
  )

  const logout = useCallback(async () => {
    try {
      await endpoints.logout()
    } finally {
      // Even if the call failed, this browser is finished with the session --
      // and the cookie has been cleared server-side on any successful path.
      setSession(null)
      queryClient.clear()
    }
  }, [queryClient, setSession])

  const refresh = useCallback(async () => {
    await queryClient.invalidateQueries({ queryKey: SESSION_QUERY_KEY })
  }, [queryClient])

  const value = useMemo<SessionContextValue>(() => {
    let status: SessionStatus
    if (query.isPending) status = 'loading'
    else if (query.isError) status = 'error'
    else if (query.data) status = 'authenticated'
    else status = 'anonymous'

    return {
      status,
      session: query.data ?? null,
      error: query.error ?? null,
      login,
      logout,
      refresh,
    }
  }, [query.data, query.error, query.isError, query.isPending, login, logout, refresh])

  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>
}
