import { useEffect, useState } from 'react'
import { NavLink, Outlet, useLocation } from 'react-router-dom'

import { roleLabel } from '../auth/roles'
import { ErrorBoundary } from '../components/ErrorBoundary'
import { SiteProvider } from '../context/SiteContext'
import { useAuthenticatedSession, useSession } from '../session/useSession'
import { navigationFor, type NavItem } from './navigation'
import { SiteIndicator } from './SiteSwitcher'

/**
 * The authenticated frame: identity along the top, navigation down the side,
 * the routed page in the middle.
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
        <SkipToContent />
        <TopBar />
        <div className="shell__body">
          <SideNav />
          {/*
            `tabIndex={-1}` EXISTS FOR THE SKIP LINK. A fragment link moves the
            viewport to its target in every browser, but only moves FOCUS if the
            target can hold it -- otherwise the next Tab carries on from the link
            in the header, which is the half of the journey that matters and the
            half that silently does not happen. Making the landmark
            programmatically focusable is the standard fix and costs nothing: -1
            keeps it out of the tab order, so nobody reaches it by tabbing.
          */}
          <main className="shell__main" id="main" tabIndex={-1}>
            {/*
              THE BOUNDARY GOES INSIDE THE SHELL, not around it. A rendering
              failure on one screen then leaves the navigation and the sign-out
              button working — so an operator can go somewhere else, or leave,
              without reloading. A boundary wrapped around the whole shell would
              take all of that down with the page that broke.

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

/**
 * The way past the navigation, for anybody who cannot skip it by looking.
 *
 * ---------------------------------------------------------------------------
 * WHAT IT COSTS NOT TO HAVE ONE
 * ---------------------------------------------------------------------------
 *
 * The sidebar is the same eleven-or-so links on every screen. Without this, a
 * keyboard or screen-reader user pays THIRTEEN TAB STOPS to reach the page
 * content, on every navigation, for the whole session -- measured in Chrome at
 * 1280px against /people. The `id="main"` on the landmark below was already
 * here, waiting for a link that was never written.
 *
 * axe does not flag the absence: its `bypass` rule is satisfied by the presence
 * of landmarks, which this console has. That is a reasonable rule and it is why
 * a green accessibility pass was still hiding this.
 *
 * ---------------------------------------------------------------------------
 * WHY IT MANAGES FOCUS ITSELF
 * ---------------------------------------------------------------------------
 *
 * `preventDefault` and an explicit `focus()`, rather than letting the browser
 * follow the fragment. Two reasons, and the first is the one that matters:
 *
 *   FOCUS, NOT JUST SCROLL. Browsers differ on whether following a fragment
 *   moves focus to a `tabindex="-1"` target or merely sets the sequential
 *   navigation starting point. Doing it here means the next Tab lands inside
 *   the page on every engine, which is the entire purpose of the control.
 *
 *   NO `#main` LEFT IN THE ADDRESS BAR. This is a single-page app; a fragment
 *   that survives into the next route is litter, and it would be copied into
 *   any URL an operator shared from that point on.
 *
 * The `href` stays `#main` regardless, because that is what makes it announce
 * as a link to the main content rather than as a button of unknown purpose.
 *
 * IT IS THE FIRST FOCUSABLE ELEMENT IN THE DOM, which is what makes it the
 * first Tab stop -- no positive `tabIndex` anywhere in this console, so document
 * order is tab order. It is visually hidden until it takes focus, so a pointer
 * user never sees it; `.skip-link` in primitives.css is the whole of that.
 */
function SkipToContent() {
  return (
    <a
      className="skip-link"
      href="#main"
      onClick={(event) => {
        const main = document.getElementById('main')
        if (!main) return
        event.preventDefault()
        // focus() scrolls the element into view on its own. An explicit
        // scrollIntoView() beside it is not belt-and-braces, it is a second
        // scroll that can fight the first.
        main.focus()
      }}
    >
      Skip to main content
    </a>
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

      {/*
        WHERE THIS OPERATOR IS, NOT A CONTROL OVER WHERE THEY ARE LOOKING.

        The site SELECT used to live here, and its position made a claim the
        product could not honour: only the overview reads the selection, so
        every other screen ignored it while it sat above them naming one site.
        It now lives on the overview, which is the screen it governs. What is
        left here is the read-only fact for an operator granted exactly one site
        -- true on every screen, because the API enforces the grant on every
        request -- and nothing at all for anybody else.
      */}
      <div className="topbar__context">
        <SiteIndicator />
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
  const location = useLocation()
  const { platform, modules } = navigationFor(session)
  const [open, setOpen] = useState(false)

  /*
    COLLAPSED ON SMALL SCREENS, AND THIS IS WHY.

    The side column becomes a flat strip below 900px, which put all eleven
    links above the page. On a phone that meant scrolling the entire menu
    before reaching the heading of the screen you had just opened -- on the
    overview, roughly a fifth of a very long page spent on navigation you had
    already used.

    THE BUTTON AND THE LINKS ARE BOTH ALWAYS IN THE DOM. Only CSS hides the
    panel, and only below the breakpoint, so the desktop column is untouched,
    assistive technology sees one tree, and no test has to open a menu to find
    a link. `aria-expanded` carries the state for anyone who cannot see it.
  */
  useEffect(() => {
    // Following a link on a phone must not leave the menu covering what you
    // navigated to.
    setOpen(false)
  }, [location.pathname])

  return (
    <nav className={open ? 'sidenav sidenav--open' : 'sidenav'} aria-label="Console">
      <button
        type="button"
        className="sidenav__toggle"
        aria-expanded={open}
        aria-controls="sidenav-panel"
        onClick={() => setOpen((wasOpen) => !wasOpen)}
      >
        Menu
      </button>

      <div className="sidenav__panel" id="sidenav-panel">
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
      </div>
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
