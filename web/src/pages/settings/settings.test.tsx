import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../../api/csrf'
import type { Role, Session } from '../../api/types'
import { RequireAuth } from '../../auth/guards'
import { platformNav } from '../../layout/navigation'
import { makeSession } from '../../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../../test/render'
import { failNext, resetServerState, state } from '../../test/server'
import { SettingsPage } from './SettingsPage'

/**
 * Settings, and the navigation that reaches it.
 *
 * The scopes are the thing under test: this page owns YOUR ACCOUNT and shows the
 * COMPANY read-only, and points at the other three rather than duplicating them.
 * A second place to edit site settings would be a second place for them to
 * disagree.
 */

function signIn(role: Role = 'MANAGER', overrides: Partial<Session> = {}) {
  const session = makeSession({
    role,
    operator: { id: 'operator-1', email: 'ops@example.com', full_name: 'Ops Person', role },
    ...overrides,
  })
  resetServerState(session)
  setCsrfToken(session.csrf_token)
  return session
}

function renderSettings(client = makeTestQueryClient()) {
  const router = createMemoryRouter(
    [
      {
        path: '/settings',
        element: (
          <RequireAuth>
            <SettingsPage />
          </RequireAuth>
        ),
      },
      { path: '/sites', element: <p>Sites</p> },
      { path: '/terminals', element: <p>Terminals</p> },
      { path: '/operators', element: <p>Operators</p> },
      { path: '/settings/applications', element: <p>Applications</p> },
    ],
    { initialEntries: ['/settings'] },
  )
  return renderWithSession(<RouterProvider router={router} />, client)
}

beforeEach(() => setCsrfToken(null))

// ---------------------------------------------------------------------------
// Scopes
// ---------------------------------------------------------------------------

describe('settings scopes stay separate', () => {
  it('shows your own account, including your session expiry', async () => {
    signIn()
    renderSettings()

    const account = await screen.findByRole('region', { name: 'Your account' })
    expect(within(account).getByText('Ops Person')).toBeInTheDocument()
    expect(within(account).getByText('ops@example.com')).toBeInTheDocument()
    expect(within(account).getByText('Manager')).toBeInTheDocument()
    expect(within(account).getByText(/This session expires/)).toBeInTheDocument()
  })

  it('says an operator cannot change their own name, email or role', async () => {
    // What stops an account quietly granting itself more than it was given.
    signIn()
    renderSettings()

    expect(
      await screen.findByText(/Your name, email and role are set by an administrator/),
    ).toBeInTheDocument()
  })

  it('shows company details read-only, and says WHY they are read-only', async () => {
    // A gap in the platform, not a permission. An owner told it is a permission
    // will go looking for someone with a higher role who also cannot do it.
    signIn('OWNER')
    renderSettings()

    const company = await screen.findByRole('region', { name: 'Company' })
    expect(await within(company).findByText('Northwind Logistics')).toBeInTheDocument()
    expect(within(company).getByText('northwind')).toBeInTheDocument()
    expect(
      within(company).getByText(/gap in the platform rather than a restriction on your role/),
    ).toBeInTheDocument()
    // No editing controls at all, for anyone.
    expect(within(company).queryByRole('textbox')).not.toBeInTheDocument()
  })

  it('points at the other configuration scopes rather than duplicating them', async () => {
    signIn('OWNER')
    renderSettings()

    const elsewhere = await screen.findByRole('region', { name: 'Configured elsewhere' })
    expect(within(elsewhere).getByRole('link', { name: 'Site settings' })).toBeInTheDocument()
    expect(within(elsewhere).getByRole('link', { name: 'Terminal settings' })).toBeInTheDocument()
    expect(
      within(elsewhere).getByRole('link', { name: 'Application settings' }),
    ).toBeInTheDocument()
    // And does not offer an editor for any of them here.
    expect(within(elsewhere).queryByRole('button')).not.toBeInTheDocument()
  })

  it('offers administrative signposts only to those who can use them', async () => {
    signIn('VIEWER')
    renderSettings()

    const elsewhere = await screen.findByRole('region', { name: 'Configured elsewhere' })
    expect(within(elsewhere).getByRole('link', { name: 'Site settings' })).toBeInTheDocument()
    expect(
      within(elsewhere).queryByRole('link', { name: 'Application settings' }),
    ).not.toBeInTheDocument()
    expect(within(elsewhere).queryByRole('link', { name: 'Operators' })).not.toBeInTheDocument()
  })

  it('reports a failed company load without breaking the rest of the page', async () => {
    signIn()
    failNext('company', 500)
    renderSettings()

    const company = await screen.findByRole('region', { name: 'Company' })
    await waitFor(() => expect(within(company).getByRole('alert')).toBeInTheDocument())
    // The account section is unaffected.
    expect(screen.getByRole('region', { name: 'Your account' })).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Password
// ---------------------------------------------------------------------------

describe('changing your own password', () => {
  it('requires the current password and a matching confirmation', async () => {
    const user = userEvent.setup()
    signIn()
    renderSettings()

    await screen.findByRole('region', { name: 'Change your password' })
    const before = state.requests.filter((r) => r.url.includes('/auth/password')).length
    await user.click(screen.getByRole('button', { name: 'Change password' }))

    expect(await screen.findByText(/Current password is required/)).toBeInTheDocument()
    expect(screen.getByText(/New password is required/)).toBeInTheDocument()
    expect(state.requests.filter((r) => r.url.includes('/auth/password'))).toHaveLength(before)
  })

  it('enforces the platform minimum length', async () => {
    const user = userEvent.setup()
    signIn()
    renderSettings()

    await screen.findByRole('region', { name: 'Change your password' })
    await user.type(screen.getByLabelText(/Current password/), 'whatever-it-is')
    await user.type(screen.getByLabelText(/^New password/), 'tooshort')
    await user.type(screen.getByLabelText(/Confirm new password/), 'tooshort')
    await user.click(screen.getByRole('button', { name: 'Change password' }))

    expect(await screen.findByText(/at least 12 characters/)).toBeInTheDocument()
  })

  it('refuses a mismatched confirmation', async () => {
    const user = userEvent.setup()
    signIn()
    renderSettings()

    await screen.findByRole('region', { name: 'Change your password' })
    await user.type(screen.getByLabelText(/Current password/), 'whatever-it-is')
    await user.type(screen.getByLabelText(/^New password/), 'a-long-enough-password')
    await user.type(screen.getByLabelText(/Confirm new password/), 'a-different-password')
    await user.click(screen.getByRole('button', { name: 'Change password' }))

    expect(await screen.findByText(/do not match/)).toBeInTheDocument()
  })

  it('changes it, clears the fields, and says other devices are signed out', async () => {
    const user = userEvent.setup()
    signIn()
    renderSettings()

    await screen.findByRole('region', { name: 'Change your password' })
    // The useful behaviour, and the opposite of the natural assumption.
    expect(screen.getByText(/You stay signed in here/)).toBeInTheDocument()

    await user.type(screen.getByLabelText(/Current password/), 'whatever-it-is')
    await user.type(screen.getByLabelText(/^New password/), 'a-long-enough-password')
    await user.type(screen.getByLabelText(/Confirm new password/), 'a-long-enough-password')
    await user.click(screen.getByRole('button', { name: 'Change password' }))

    await waitFor(() => expect(screen.getByText('Password changed.')).toBeInTheDocument())
    // Nothing is left in the form once the request has succeeded.
    expect(screen.getByLabelText(/Current password/)).toHaveValue('')
    expect(screen.getByLabelText(/^New password/)).toHaveValue('')
  })

  it('never leaves a submitted password anywhere on the page', async () => {
    const user = userEvent.setup()
    signIn()
    renderSettings()

    await screen.findByRole('region', { name: 'Change your password' })
    await user.type(screen.getByLabelText(/Current password/), 'whatever-it-is')
    await user.type(screen.getByLabelText(/^New password/), 'a-long-enough-password')
    await user.type(screen.getByLabelText(/Confirm new password/), 'a-long-enough-password')
    await user.click(screen.getByRole('button', { name: 'Change password' }))

    await waitFor(() => expect(screen.getByText('Password changed.')).toBeInTheDocument())
    expect(document.body.textContent).not.toContain('a-long-enough-password')
  })

  it('explains a 401 as the wrong current password, not an ended session', async () => {
    // The distinction matters: one is a typo, the other means signing in again.
    const user = userEvent.setup()
    signIn()
    failNext('change-password', 401)
    renderSettings()

    await screen.findByRole('region', { name: 'Change your password' })
    await user.type(screen.getByLabelText(/Current password/), 'wrong-password-here')
    await user.type(screen.getByLabelText(/^New password/), 'a-long-enough-password')
    await user.type(screen.getByLabelText(/Confirm new password/), 'a-long-enough-password')
    await user.click(screen.getByRole('button', { name: 'Change password' }))

    expect(await screen.findByText('That is not your current password.')).toBeInTheDocument()
  })

  it('explains a rate limit rather than showing a raw error', async () => {
    const user = userEvent.setup()
    signIn()
    failNext('change-password', 429)
    renderSettings()

    await screen.findByRole('region', { name: 'Change your password' })
    await user.type(screen.getByLabelText(/Current password/), 'whatever-it-is')
    await user.type(screen.getByLabelText(/^New password/), 'a-long-enough-password')
    await user.type(screen.getByLabelText(/Confirm new password/), 'a-long-enough-password')
    await user.click(screen.getByRole('button', { name: 'Change password' }))

    expect(await screen.findByText(/Too many attempts/)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Navigation
// ---------------------------------------------------------------------------

describe('console navigation', () => {
  it('offers the whole platform to an OWNER', () => {
    expect(platformNav('OWNER').map((item) => item.id)).toEqual([
      'dashboard',
      'people',
      'terminals',
      'sites',
      'operators',
      'activity',
      'applications',
      'settings',
    ])
  })

  it('gates administration but never Settings, which holds your own account', () => {
    const viewer = platformNav('VIEWER').map((item) => item.id)
    expect(viewer).toContain('dashboard')
    expect(viewer).toContain('settings')
    expect(viewer).not.toContain('operators')
    expect(viewer).not.toContain('applications')

    const manager = platformNav('MANAGER').map((item) => item.id)
    expect(manager).toContain('settings')
    expect(manager).not.toContain('operators')

    expect(platformNav('ADMIN').map((item) => item.id)).toContain('operators')
  })

  it('refuses an unknown role everything', () => {
    // A build that does not recognise a role cannot reason about it, and
    // refusing is the safe direction to be wrong in.
    expect(platformNav('SUPERUSER')).toEqual([])
  })
})

// ---------------------------------------------------------------------------
// Language and disclosure
// ---------------------------------------------------------------------------

describe('language and disclosure', () => {
  it('uses no industry-specific vocabulary', async () => {
    signIn('OWNER')
    renderSettings()

    await screen.findByRole('region', { name: 'Your account' })
    const text = (document.body.textContent ?? '').toLowerCase()
    for (const word of ['gym', 'membership', 'trainer', 'workout', 'branch']) {
      expect(text, `settings mentions "${word}"`).not.toContain(word)
    }
  })

  it('discloses no credential material', async () => {
    signIn('OWNER')
    renderSettings()

    await screen.findByRole('region', { name: 'Your account' })
    const text = (document.body.textContent ?? '').toLowerCase()
    for (const forbidden of ['api_key', 'atd_', 'ats_', 'password_hash', 'token_hash', 'csrf']) {
      expect(text).not.toContain(forbidden)
    }
  })
})
