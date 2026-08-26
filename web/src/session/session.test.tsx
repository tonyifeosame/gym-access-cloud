import { QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { RouterProvider, createMemoryRouter } from 'react-router-dom'
import { describe, expect, it } from 'vitest'

import { createQueryClient } from '../App'
import { LoginPage } from '../auth/LoginPage'
import { RequireAuth } from '../auth/guards'
import { AppShell } from '../layout/AppShell'
import { DashboardPage } from '../pages/DashboardPage'
import { TerminalsListPage } from '../pages/terminals/TerminalsListPage'
import { ApplicationPlaceholder } from '../pages/NotImplemented'
import { SessionProvider } from './SessionProvider'
import { makeSession, SITE_A, SITE_B } from '../test/fixtures'
import { resetServerState, state } from '../test/server'

/**
 * The session flow end to end, against a mock of the real API contracts:
 * restoration on load, sign in, sign out, and what the shell renders from the
 * session it gets back.
 */

function renderApp(initialPath = '/') {
  const router = createMemoryRouter(
    [
      { path: '/login', element: <LoginPage /> },
      {
        path: '/',
        element: (
          <RequireAuth>
            <AppShell />
          </RequireAuth>
        ),
        children: [
          { index: true, element: <DashboardPage /> },
          /*
            A SECOND SCREEN, so the site-scope tests can render something that
            is NOT the overview. That distinction is the whole of what those
            tests assert: the selection has exactly one reader, and a control
            for it in the shell would sit above four screens that ignore it.
          */
          { path: 'terminals', element: <TerminalsListPage /> },
          { path: 'applications/:slug', element: <ApplicationPlaceholder /> },
        ],
      },
    ],
    { initialEntries: [initialPath] },
  )

  return render(
    <QueryClientProvider client={createQueryClient()}>
      <SessionProvider>
        <RouterProvider router={router} />
      </SessionProvider>
    </QueryClientProvider>,
  )
}

describe('operator session', () => {
  it('sends an anonymous visitor to the login form', async () => {
    resetServerState(null)
    renderApp()

    expect(await screen.findByRole('button', { name: /sign in/i })).toBeInTheDocument()
  })

  it('restores an existing session from the cookie without asking again', async () => {
    // The credential is an HttpOnly cookie, so restoration is entirely GET /me.
    resetServerState(makeSession({ company: { id: 'c1', name: 'Meridian Clinics', slug: 'mc' } }))
    renderApp()

    // Scoped to the shell's own banner. The dashboard behind it now also names
    // the company in its context panel, which is correct -- this assertion is
    // about the SHELL having restored a session, not about a unique string.
    const banner = await screen.findByRole('banner')
    expect(within(banner).getByText('Meridian Clinics')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Overview' })).toBeInTheDocument()
  })

  it('signs in and lands on the console', async () => {
    resetServerState(null)
    const user = userEvent.setup()
    renderApp()

    await user.type(await screen.findByLabelText('Email'), 'ops@example.com')
    await user.type(screen.getByLabelText('Password'), 'a-long-enough-password')
    await user.click(screen.getByRole('button', { name: /sign in/i }))

    expect(await screen.findByRole('heading', { name: 'Overview' })).toBeInTheDocument()
  })

  it('shows one message for every credential failure', async () => {
    resetServerState(null)
    state.loginStatus = 401
    const user = userEvent.setup()
    renderApp()

    await user.type(await screen.findByLabelText('Email'), 'ops@example.com')
    await user.type(screen.getByLabelText('Password'), 'wrong-password')
    await user.click(screen.getByRole('button', { name: /sign in/i }))

    // Nothing about whether the account exists, matching the API's uniform 401.
    expect(await screen.findByRole('alert')).toHaveTextContent('Invalid email or password.')
  })

  it('tells an operator how long to wait when rate limited', async () => {
    resetServerState(null)
    state.loginStatus = 429
    state.loginRetryAfter = 120
    const user = userEvent.setup()
    renderApp()

    await user.type(await screen.findByLabelText('Email'), 'ops@example.com')
    await user.type(screen.getByLabelText('Password'), 'wrong-password')
    await user.click(screen.getByRole('button', { name: /sign in/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent('2 minutes')
  })

  it('signs out and returns to the login form', async () => {
    resetServerState(makeSession())
    const user = userEvent.setup()
    renderApp()

    await user.click(await screen.findByRole('button', { name: /sign out/i }))

    expect(await screen.findByRole('button', { name: /sign in/i })).toBeInTheDocument()
  })
})

describe('the console the session describes', () => {
  it('renders no application navigation for a company with none enabled', async () => {
    resetServerState(makeSession({ applications: [] }))
    renderApp()

    expect(await screen.findByRole('heading', { name: 'Overview' })).toBeInTheDocument()

    // Said in both places on purpose: the navigation explains the gap, and so
    // does the page. Scoped so the assertion is about the navigation.
    const nav = screen.getByRole('navigation', { name: 'Console' })
    expect(within(nav).getByText(/no applications are enabled/i)).toBeInTheDocument()

    // Platform resources are still there: they are not modules.
    expect(screen.getByRole('link', { name: 'People' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Terminals' })).toBeInTheDocument()
  })

  it('builds navigation from the capabilities the company has enabled', async () => {
    resetServerState(
      makeSession({
        applications: [
          { code: 'TIME_TRACKING', settings: {} },
          { code: 'VISITOR_MANAGEMENT', settings: {} },
        ],
      }),
    )
    renderApp()

    expect(await screen.findByRole('link', { name: 'Time Tracking' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Visitor Management' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Attendance' })).not.toBeInTheDocument()
  })

  it('shows a placeholder for an enabled but unbuilt application', async () => {
    resetServerState(makeSession({ applications: [{ code: 'ATTENDANCE', settings: {} }] }))
    renderApp('/applications/attendance')

    expect(await screen.findByRole('heading', { name: 'Attendance' })).toBeInTheDocument()
    expect(screen.getByText(/not built yet/i)).toBeInTheDocument()
    // The SPECIFIC gap rather than "coming soon". An operator who followed a
    // navigation entry here has been told the capability is enabled, and a
    // vague placeholder invites them to assume the work is happening somewhere
    // and only the screen is missing. Nothing is happening at all.
    expect(screen.getByText(/Nothing records attendance/i)).toBeInTheDocument()
  })

  it('says so when a capability is not enabled for the company', async () => {
    resetServerState(makeSession({ applications: [] }))
    renderApp('/applications/attendance')

    expect(await screen.findByRole('heading', { name: /not enabled/i })).toBeInTheDocument()
  })

  it('hides the operator area from roles below ADMIN', async () => {
    resetServerState(makeSession({ role: 'MANAGER' }))
    renderApp()

    await screen.findByRole('heading', { name: 'Overview' })
    expect(screen.queryByRole('link', { name: 'Operators' })).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Applications' })).not.toBeInTheDocument()
  })
})

describe('site context', () => {
  /*
    THE OWNER'S CASE, WHICH IS EVERY OWNER OF EVERY COMPANY.

    `all_sites` is true by role and `sites` is empty, because an owner holds no
    explicit grants -- so the switcher's options came to exactly one, and the
    shell rendered a select box offering "All sites" and nothing else in the top
    bar of every screen. On Terminals that sat above the toolbar's own Site
    filter, which does narrow that page's rows: two controls with the same
    label, and the inert one both higher and more prominent.

    The SELECTION is unchanged and still defaults to every site -- the overview
    reads it and must keep working. What is gone is a control for a choice that
    was never a choice.
  */
  it('renders no site control for an operator with nothing to choose between', async () => {
    resetServerState(makeSession({ all_sites: true, sites: [] }))
    renderApp()

    await screen.findByRole('heading', { name: 'Overview' })
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
    expect(screen.queryByRole('option', { name: 'All sites' })).not.toBeInTheDocument()
  })

  /*
    ---------------------------------------------------------------------------
    THE SCOPE CONTROL SITS ON THE SCREEN IT SCOPES
    ---------------------------------------------------------------------------

    `SiteContext.selected` has exactly ONE reader in the application: the
    overview. Every other screen ignores it. So a select in the shell's top bar
    -- above the navigation, on every page -- made a claim the product could not
    honour: an operator picked a site, opened Terminals, People, Events or
    Activity, and saw the whole company with the name of one site still sitting
    at the top of the page.

    That failure is silent and confident, which is what makes it worth a test.
    Nothing errors and every page renders; the figures are simply about a
    different set of doors than the reader believes. On Events that is a safety
    question -- "was anybody refused here today" -- answered over every site.

    IT WAS NOT MADE GLOBAL INSTEAD because it cannot honestly be: people carry
    no site on this platform, the audit endpoint takes no site, and the two
    screens that CAN be narrowed already have their own site filter in their own
    toolbars. The two tests below are the two halves of the property.
  */
  it('KEEPS THE SITE SELECT OFF THE SHELL, which spans screens that ignore it', async () => {
    resetServerState(makeSession({ role: 'MANAGER', all_sites: false, sites: [SITE_A, SITE_B] }))
    renderApp()

    await screen.findByRole('heading', { name: 'Overview' })

    // The choice is real for this operator -- two grants -- so the control does
    // render. It renders inside the page, not in the banner.
    const banner = screen.getByRole('banner')
    expect(within(banner).queryByRole('combobox')).not.toBeInTheDocument()
    expect(screen.getByRole('combobox')).toBeInTheDocument()
  })

  it('OFFERS NO SITE SCOPE ON A SCREEN THAT DOES NOT READ ONE', async () => {
    /*
      Terminals has its own Site filter, over its own rows, backed by the list
      endpoint. What must not be here is a SECOND site control in the chrome
      above it -- which is what the top bar used to supply, inert, and more
      prominent than the one that worked.
    */
    resetServerState(makeSession({ role: 'MANAGER', all_sites: false, sites: [SITE_A, SITE_B] }))
    renderApp('/terminals')

    await screen.findByRole('heading', { name: 'Terminals' })

    const banner = screen.getByRole('banner')
    expect(within(banner).queryByRole('combobox')).not.toBeInTheDocument()
    expect(within(banner).queryByText('Showing')).not.toBeInTheDocument()

    // The page's own filter is untouched and still the one working control.
    expect(screen.getByLabelText('Site')).toBeInTheDocument()
  })

  it('still states the site of an operator who has exactly one, on every screen', async () => {
    /*
      NOT A CONTROL, AND THEREFORE NOT MISLEADING. An operator holding one grant
      has no scope to set, but every screen they open genuinely IS that site --
      the API enforces the grant on every request. Stating it in the chrome is
      true everywhere, which is precisely what the select was not.
    */
    resetServerState(makeSession({ role: 'MANAGER', all_sites: false, sites: [SITE_A] }))
    renderApp('/terminals')

    await screen.findByRole('heading', { name: 'Terminals' })
    const banner = screen.getByRole('banner')
    expect(within(banner).getByText(SITE_A.site_name)).toBeInTheDocument()
    expect(within(banner).queryByRole('combobox')).not.toBeInTheDocument()
  })

  it('remembers the selected site per company', async () => {
    resetServerState(makeSession({ all_sites: true, sites: [SITE_A, SITE_B] }))
    const user = userEvent.setup()
    const { unmount } = renderApp()

    await screen.findByRole('heading', { name: 'Overview' })
    await user.selectOptions(screen.getByRole('combobox'), SITE_B.site_id)

    expect(window.localStorage.getItem('accesslink.site.company-1')).toBe(SITE_B.site_id)

    unmount()
    resetServerState(makeSession({ all_sites: true, sites: [SITE_A, SITE_B] }))
    renderApp()

    await waitFor(() => expect(screen.getByRole('combobox')).toHaveValue(SITE_B.site_id))
  })

  it('ignores a remembered site whose grant has been revoked', async () => {
    window.localStorage.setItem('accesslink.site.company-1', SITE_B.site_id)
    resetServerState(makeSession({ all_sites: false, sites: [SITE_A] }))
    renderApp()

    await screen.findByRole('heading', { name: 'Overview' })
    // Only one site remains, so the switcher collapses to a label rather than
    // restoring a scope the API would now refuse.
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
    expect(screen.getByText(SITE_A.site_name)).toBeInTheDocument()
  })

  it('does not offer "all sites" to a scoped operator', async () => {
    // AUTHORIZATION IS UNCHANGED BY THE MOVE. The options are still built from
    // the operator's own grants, and "all sites" is still offered only to
    // somebody who reaches all of them.
    resetServerState(makeSession({ role: 'MANAGER', all_sites: false, sites: [SITE_A, SITE_B] }))
    renderApp()

    await screen.findByRole('heading', { name: 'Overview' })
    const scoped = screen.getByRole('combobox')
    expect(within(scoped).queryByRole('option', { name: 'All sites' })).not.toBeInTheDocument()
    expect(scoped).toHaveValue(SITE_A.site_id)
  })
})
