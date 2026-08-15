import { createContext, useCallback, useContext, useMemo, type ReactNode } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'

import * as platform from './api'
import { platformKeys } from './data'
import type { PlatformSession } from './types'

/**
 * The platform administrator's session.
 *
 * A SECOND, INDEPENDENT SESSION, deliberately not folded into SessionProvider.
 * The two identities are different tables with different cookies on the server,
 * and both cookies are Path=/ — so the browser genuinely offers each to the
 * other's routes and only the middleware names keep them apart. A single
 * provider holding "the session" would have to guess which one it meant, and the
 * guess would be wrong in the case that matters: a vendor engineer signed in to
 * both at once, on the same installation, in the same browser.
 *
 * Signing out of one therefore does NOT sign out of the other, which is correct
 * and worth stating because it is surprising: they are not the same person's
 * two hats, they are two credentials that happen to share a browser.
 */

export type PlatformStatus = 'loading' | 'authenticated' | 'anonymous' | 'error'

interface PlatformContextValue {
  status: PlatformStatus
  session: PlatformSession | null
  error: Error | null
  login: (email: string, password: string) => Promise<PlatformSession>
  logout: () => Promise<void>
}

const PlatformContext = createContext<PlatformContextValue | null>(null)

export function PlatformSessionProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient()

  const query = useQuery({
    queryKey: platformKeys.session,
    queryFn: ({ signal }) => platform.fetchPlatformSession(signal),
    // Anonymous is an answer, not a failure — and on this surface it is the
    // usual one.
    retry: false,
    staleTime: 30_000,
  })

  const setSession = useCallback(
    (session: PlatformSession | null) => {
      queryClient.setQueryData(platformKeys.session, session)
    },
    [queryClient],
  )

  const login = useCallback(
    async (email: string, password: string) => {
      const session = await platform.platformLogin(email, password)
      setSession(session)
      return session
    },
    [setSession],
  )

  const logout = useCallback(async () => {
    try {
      await platform.platformLogout()
    } finally {
      setSession(null)
      // Only the PLATFORM subtree. A tenant console open in another tab belongs
      // to a different identity and must not be cleared from here.
      void queryClient.removeQueries({ queryKey: platformKeys.companies.all })
    }
  }, [queryClient, setSession])

  const value = useMemo<PlatformContextValue>(() => {
    let status: PlatformStatus
    if (query.isPending) status = 'loading'
    else if (query.isError) status = 'error'
    else if (query.data) status = 'authenticated'
    else status = 'anonymous'

    return { status, session: query.data ?? null, error: query.error ?? null, login, logout }
  }, [query.data, query.error, query.isError, query.isPending, login, logout])

  return <PlatformContext.Provider value={value}>{children}</PlatformContext.Provider>
}

export function usePlatformSession(): PlatformContextValue {
  const value = useContext(PlatformContext)
  if (!value) {
    throw new Error('usePlatformSession must be used inside a PlatformSessionProvider')
  }
  return value
}
