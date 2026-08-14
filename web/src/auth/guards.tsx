import type { ReactNode } from 'react'
import { Navigate, useLocation } from 'react-router-dom'

import type { Role } from '../api/types'
import { ErrorState, LoadingState } from '../components/states'
import { useSession } from '../session/useSession'
import { roleAtLeast } from './roles'

/**
 * Route guards.
 *
 * NEITHER OF THESE IS AN AUTHORIZATION BOUNDARY. The API authenticates and
 * authorizes every request on its own and would refuse anything these let
 * through; their job is to keep an operator out of a screen that would only
 * show them errors. Never move a check out of the backend and into here.
 */

export function RequireAuth({ children }: { children: ReactNode }) {
  const { status, error, refresh } = useSession()
  const location = useLocation()

  if (status === 'loading') {
    return <LoadingState label="Restoring your session…" />
  }

  if (status === 'error') {
    // The API could not be reached. Showing a login form here would be a lie:
    // the operator's credentials are not the problem, and re-entering them
    // would fail the same way.
    return (
      <div className="page">
        <ErrorState
          title="Cannot reach AccessLink"
          error={error}
          onRetry={() => void refresh()}
        />
      </div>
    )
  }

  if (status === 'anonymous') {
    // Remember where they were headed so signing in resumes it.
    const next = `${location.pathname}${location.search}`
    return <Navigate to={`/login?next=${encodeURIComponent(next)}`} replace />
  }

  return <>{children}</>
}

export function RequireRole({ minimum, children }: { minimum: Role; children: ReactNode }) {
  const { session } = useSession()

  if (!session || !roleAtLeast(session.role, minimum)) {
    return <Navigate to="/forbidden" replace />
  }

  return <>{children}</>
}
