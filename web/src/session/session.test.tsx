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
    expect(screen.getByText(/not implemented yet/i)).toBeInTheDocument()
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
  it('offers all sites when the operator is unscoped', async () => {
    resetServerState(makeSession({ all_sites: true, sites: [] }))
    renderApp()

    await screen.findByRole('heading', { name: 'Overview' })
    const switcher = screen.getByRole('combobox')
    expect(switcher).toHaveValue('ALL')
    // Scoped to the switcher: the dashboard also reports "All sites" as the
    // current scope, which is the same fact stated in a different place.
    expect(within(switcher).getByRole('option', { name: 'All sites' })).toBeInTheDocument()
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
    resetServerState(makeSession({ role: 'MANAGER', all_sites: false, sites: [SITE_A, SITE_B] }))
    renderApp()

    await screen.findByRole('heading', { name: 'Overview' })
    const scoped = screen.getByRole('combobox')
    expect(within(scoped).queryByRole('option', { name: 'All sites' })).not.toBeInTheDocument()
    expect(scoped).toHaveValue(SITE_A.site_id)
  })
})
