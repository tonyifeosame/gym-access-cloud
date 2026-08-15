import { Link, Navigate, Outlet, useLocation } from 'react-router-dom'

import { ErrorBoundary } from '../components/ErrorBoundary'
import { ErrorState, LoadingState } from '../components/states'
import { usePlatformSession } from './PlatformSessionProvider'

/**
 * The chrome around platform administration.
 *
 * DELIBERATELY DISTINCT FROM THE OPERATOR CONSOLE. A vendor engineer may be
 * signed in to both at once, in the same browser, on the same installation --
 * the two identities are different tables with different cookies and neither
 * signs the other out. If the two surfaces looked alike, the mistake that
 * follows is somebody believing they are suspending a site when they are
 * suspending a customer.
 *
 * So this shell says whose surface it is on every screen, and carries no
 * navigation into the tenant console at all.
 */
export function PlatformShell() {
  const { status, session, error, logout } = usePlatformSession()
  const location = useLocation()

  if (status === 'loading') {
    return <LoadingState label="Restoring your session…" />
  }

  if (status === 'error') {
    return (
      <div className="page">
        <ErrorState title="Cannot reach AccessLink" error={error} />
      </div>
    )
  }

  if (status === 'anonymous') {
    const next = `${location.pathname}${location.search}`
    return <Navigate to={`/platform/login?next=${encodeURIComponent(next)}`} replace />
  }

  return (
    <div className="shell shell--platform">
      <header className="topbar">
        <div className="topbar__brand">
          <Link to="/platform" className="topbar__product">
            AccessLink
          </Link>
          {/* The label that stops this being mistaken for a tenant console. It
              is a badge rather than quiet text for the same reason: somebody
              glancing at the top of the page has to see which surface they are
              on before they act. */}
          <span className="topbar__scope">Platform administration</span>
        </div>

        <div className="topbar__account">
          <span className="topbar__operator">{session?.admin.email}</span>
          <button type="button" className="button button--quiet" onClick={() => void logout()}>
            Sign out
          </button>
        </div>
      </header>

      <div className="shell__body">
        <main className="shell__main" id="main">
          {/* Inside the shell, keyed on the path — see AppShell for why. */}
          <ErrorBoundary resetKey={location.pathname} label="This page">
            <Outlet />
          </ErrorBoundary>
        </main>
      </div>
    </div>
  )
}
