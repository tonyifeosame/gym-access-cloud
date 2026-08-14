import { NavLink, Outlet, useNavigate } from 'react-router-dom'

import { roleLabel } from '../auth/roles'
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
  return (
    <SiteProvider>
      <div className="shell">
        <TopBar />
        <div className="shell__body">
          <SideNav />
          <main className="shell__main" id="main">
            <Outlet />
          </main>
        </div>
      </div>
    </SiteProvider>
  )
}

function TopBar() {
  const session = useAuthenticatedSession()
  const { logout } = useSession()
  const navigate = useNavigate()

  async function signOut() {
    await logout()
    navigate('/login', { replace: true })
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
