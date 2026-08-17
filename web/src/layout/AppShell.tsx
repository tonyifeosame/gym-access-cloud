import { NavLink, Outlet, useLocation } from 'react-router-dom'

import { roleLabel } from '../auth/roles'
import { ErrorBoundary } from '../components/ErrorBoundary'
import { SiteProvider } from '../context/SiteContext'
import { useAuthenticatedSession, useSession } from '../session/useSession'
import { navigationFor, type NavItem } from './navigation'
import { SiteSwitcher } from './SiteSwitcher'

/**
 * The authenticated frame: identity and site context along the top, navigation
 * down the side, the routed page in the middle.
 *
 * The side navigation is built from the session, so what an operator sees is a
 * function of their company's enabled capabilities and their own role -- not of
 * anything hard-coded about what AccessLink is "for".
 */
export function AppShell() {
  const location = useLocation()

  return (
    <SiteProvider>
      <div className="shell">
        <TopBar />
        <div className="shell__body">
          <SideNav />
          <main className="shell__main" id="main">
            {/*
              THE BOUNDARY GOES INSIDE THE SHELL, not around it. A rendering
              failure on one screen then leaves the navigation, the site
              switcher and the sign-out button working — so an operator can go
              somewhere else, or leave, without reloading. A boundary wrapped
              around the whole shell would take all of that down with the page
              that broke.

              Keyed on the path, so navigating away clears it. Without that, one
              broken screen would keep showing its error for the rest of the
              session no matter where the operator went.
            */}
            <ErrorBoundary resetKey={location.pathname}>
              <Outlet />
            </ErrorBoundary>
          </main>
        </div>
      </div>
    </SiteProvider>
  )
}

function TopBar() {
  const session = useAuthenticatedSession()
  const { logout } = useSession()

  /*
    NO EXPLICIT NAVIGATION HERE, and that is the whole of it.

    logout() ends the session, RequireAuth sees `anonymous`, and it redirects to
    the login form — the same path a session that expires on its own takes, and
    the same one a 401 on any other request takes. Signing out is not a special
    case and does not need its own redirect.

    Adding one back would not be belt-and-braces, it would be a SECOND
    navigation to a DIFFERENT url (`/login`, where the guard sends `/login?next=`),
    and the router would tear the login form down and build it again. Whether
    React coalesces the two updates depends on scheduler timing: it does on one
    Node version and does not on another, which is exactly how this last
    surfaced — as a frontend suite that passed locally and failed in CI.
  */
  async function signOut() {
    await logout()
  }

  return (
    <header className="topbar">
      <div className="topbar__brand">
        <span className="topbar__product">AccessLink</span>
        <span className="topbar__company">{session.company.name}</span>
      </div>

      <div className="topbar__context">
        <SiteSwitcher />
      </div>

      <div className="topbar__account">
        <span className="topbar__operator">
          {session.operator.full_name}
          <span className="topbar__role">{roleLabel(session.role)}</span>
        </span>
        <button type="button" className="button button--quiet" onClick={() => void signOut()}>
          Sign out
        </button>
      </div>
    </header>
  )
}

function SideNav() {
  const session = useAuthenticatedSession()
  const { platform, modules } = navigationFor(session)

  return (
    <nav className="sidenav" aria-label="Console">
      <NavSection title="Platform" items={platform} />

      {modules.length > 0 ? (
        <NavSection title="Applications" items={modules} />
      ) : (
        // Not an error and not an empty-looking bug: a company that has enabled
        // no capabilities is using the platform correctly. Saying so is better
        // than a blank space that reads as something failing to load.
        <section className="sidenav__section">
          <h2 className="sidenav__title">Applications</h2>
          <p className="sidenav__note">
            No applications are enabled for this company yet.
          </p>
        </section>
      )}
    </nav>
  )
}

function NavSection({ title, items }: { title: string; items: NavItem[] }) {
  if (items.length === 0) return null

  return (
    <section className="sidenav__section">
      <h2 className="sidenav__title">{title}</h2>
      <ul className="sidenav__list">
        {items.map((item) => (
          <li key={item.id}>
            <NavLink
              to={item.path}
              end={item.path === '/'}
              className={({ isActive }) =>
                isActive ? 'sidenav__link sidenav__link--active' : 'sidenav__link'
              }
            >
              {item.label}
            </NavLink>
          </li>
        ))}
      </ul>
    </section>
  )
}
