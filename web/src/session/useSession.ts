import { useContext } from 'react'

import { SessionContext, type SessionContextValue } from './SessionProvider'
import type { Session } from '../api/types'

export function useSession(): SessionContextValue {
  const context = useContext(SessionContext)
  if (!context) {
    throw new Error('useSession must be used inside a SessionProvider')
  }
  return context
}

/**
 * The session, for components that only render behind RequireAuth.
 *
 * Throws rather than returning null so a component does not have to narrow a
 * type that cannot be null where it is mounted. If this ever throws, the
 * component is mounted outside the authenticated tree -- which is a routing
 * mistake, not a state to handle.
 */
export function useAuthenticatedSession(): Session {
  const { session } = useSession()
  if (!session) {
    throw new Error('useAuthenticatedSession used outside an authenticated route')
  }
  return session
}
