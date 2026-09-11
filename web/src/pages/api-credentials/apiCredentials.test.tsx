import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Outlet, createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { setCsrfToken } from '../../api/csrf'
import type { APICredential, Role, Session } from '../../api/types'
import { RequireAuth, RequireRole } from '../../auth/guards'
import { keys } from '../../data/keys'
import { platformNav } from '../../layout/navigation'
import { Forbidden } from '../ErrorPage'
import {
  makeAPICredential,
  makeAPICredentialUsage,
  makeSession,
  makeSite,
  SITE_A,
  SITE_B,
} from '../../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../../test/render'
import { failNext, resetServerState, seed, state } from '../../test/server'
import { ApiCredentialDetailPage } from './ApiCredentialDetailPage'
import { ApiCredentialsListPage } from './ApiCredentialsListPage'

/**
 * Integration credentials in the console.
 *
 * Two properties carry most of the weight here and are tested directly rather
 * than implied: that the SECRET IS EPHEMERAL -- shown once, then gone from the
 * DOM, both storages, the URL, the query cache and the mutation cache -- and
 * that the screens are ADMIN-gated in the console the same way the routes are
 * on the server.
 */

const SITES = [
  makeSite({ id: SITE_A.site_id, name: SITE_A.site_name }),
  makeSite({ id: SITE_B.site_id, name: SITE_B.site_name }),
]

const SECRET_SHAPE = /atp_live_[0-9a-f]{64}/

const ROSTER: APICredential[] = [
  makeAPICredential({
    id: 'cred-1',
    name: 'Roster sync',
    key_prefix: 'atp_live_0123abcd',
    scopes: ['members:read', 'sites:read'],
    status: 'ACTIVE',
  }),
  makeAPICredential({
    id: 'cred-2',
    name: 'Lagos kiosk',
    key_prefix: 'atp_live_deadbeef',
    scopes: ['sites:read'],
    all_sites: false,
    sites: [SITE_A],
    last_used_at: undefined,
    last_used_ip: undefined,
    status: 'ACTIVE',
  }),
  makeAPICredential({
    id: 'cred-3',
    name: 'Old exporter',
    key_prefix: 'atp_live_0ff1ce00',
    status: 'REVOKED',
    revoked_at: '2026-08-01T00:00:00Z',
    revoked_reason: 'Vendor contract ended',
    revoked_by_email: 'owner@example.com',
  }),
  makeAPICredential({
    id: 'cred-4',
    name: 'Trial key',
    key_prefix: 'atp_live_11111111',
    status: 'EXPIRED',
    expires_at: '2026-06-01T00:00:00Z',
  }),
]

function signIn(role: Role = 'ADMIN', overrides: Partial<Session> = {}) {
  const session = makeSession({
    role,
    operator: { id: 'operator-1', email: 'ops@example.com', full_name: 'Ops Person', role },
    ...overrides,
  })
  resetServerState(session)
  setCsrfToken(session.csrf_token)
  seed({ sites: SITES, apiCredentials: ROSTER.map((credential) => ({ ...credential })) })
  return session
}

function renderCredentials(
  initialPath = '/settings/api-credentials',
  client = makeTestQueryClient(),
) {
  // RequireAuth above RequireRole, as in router.tsx: the role guard reads the
  // session, and the auth guard is what waits for it to load.
  const router = createMemoryRouter(
    [
      {
        element: (
          <RequireAuth>
            <Outlet />
          </RequireAuth>
        ),
        children: [
          {
            path: '/settings/api-credentials',
            element: (
              <RequireRole minimum="ADMIN">
                <ApiCredentialsListPage />
              </RequireRole>
            ),
          },
          {
            path: '/settings/api-credentials/:credentialId',
            element: (
              <RequireRole minimum="ADMIN">
                <ApiCredentialDetailPage />
              </RequireRole>
            ),
          },
          { path: '/forbidden', element: <Forbidden /> },
          { path: '/sites/:siteId', element: <p>Site page</p> },
          { path: '/operators', element: <p>Operators page</p> },
          { path: '/sites', element: <p>Sites page</p> },
        ],
      },
    ],
    { initialEntries: [initialPath] },
  )
  return renderWithSession(<RouterProvider router={router} />, client)
}

function stubClipboard() {
  const writeText = vi.fn().mockResolvedValue(undefined)
  Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
  return writeText
}

/** Everywhere a secret could survive the panel. Asserts it is in none of them. */
async function expectSecretGone(secret: string, client: ReturnType<typeof makeTestQueryClient>) {
  expect(secret).toMatch(SECRET_SHAPE)
  expect(document.body.textContent).not.toContain(secret)
  expect(JSON.stringify(window.localStorage)).not.toContain(secret)
  expect(JSON.stringify(window.sessionStorage)).not.toContain(secret)
  expect(window.location.href).not.toContain(secret)
  for (const entry of client.getQueryCache().getAll()) {
    expect(JSON.stringify(entry.state.data ?? null)).not.toContain(secret)
  }
  // The mutation that minted it is collected the moment the panel resets it
  // (gcTime 0 on the issue and rotate hooks); the removal lands a tick later.
  await waitFor(() => {
    for (const mutation of client.getMutationCache().getAll()) {
      expect(JSON.stringify(mutation.state.data ?? null)).not.toContain(secret)
    }
  })
}

beforeEach(() => setCsrfToken(null))

// ---------------------------------------------------------------------------
// Role gating
// ---------------------------------------------------------------------------

describe('role gating', () => {
  it('offers the navigation entry to ADMIN and OWNER only', () => {
    expect(platformNav('VIEWER').map((item) => item.id)).not.toContain('api-credentials')
    expect(platformNav('MANAGER').map((item) => item.id)).not.toContain('api-credentials')
    expect(platformNav('ADMIN').map((item) => item.id)).toContain('api-credentials')
    expect(platformNav('OWNER').map((item) => item.id)).toContain('api-credentials')

    const entry = platformNav('ADMIN').find((item) => item.id === 'api-credentials')
    expect(entry).toMatchObject({ label: 'API access', path: '/settings/api-credentials', minimumRole: 'ADMIN' })
  })

  it('sends a MANAGER who types the URL to the forbidden page, and never calls the API', async () => {
    signIn('MANAGER')
    renderCredentials()

    expect(await screen.findByRole('heading', { name: 'Not available to you' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'API access' })).not.toBeInTheDocument()
    expect(state.requests.filter((r) => r.url.includes('/api-credentials'))).toHaveLength(0)
  })

  it('sends a MANAGER off the detail page too', async () => {
    signIn('MANAGER')
    renderCredentials('/settings/api-credentials/cred-1')
    expect(await screen.findByRole('heading', { name: 'Not available to you' })).toBeInTheDocument()
  })

  it('lets an OWNER through by the hierarchy', async () => {
    signIn('OWNER')
    renderCredentials()
    expect(await screen.findByRole('heading', { name: 'API access' })).toBeInTheDocument()
    expect(await screen.findByText('Roster sync')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

describe('credential list', () => {
  it('shows name, key prefix, status, scopes, site access, last use and issuer', async () => {
    signIn()
    renderCredentials()

    const row = (await screen.findByText('Roster sync')).closest('tr') as HTMLElement
    expect(within(row).getByText('atp_live_0123abcd…')).toBeInTheDocument()
    expect(within(row).getByText('Active')).toBeInTheDocument()
    expect(within(row).getByText('Read members')).toBeInTheDocument()
    expect(within(row).getByText('Read sites')).toBeInTheDocument()
    expect(within(row).getByText('All sites')).toBeInTheDocument()
    expect(within(row).getByText('ops@example.com')).toBeInTheDocument()

    const kiosk = screen.getByText('Lagos kiosk').closest('tr') as HTMLElement
    expect(within(kiosk).getByText('1 site')).toBeInTheDocument()
    expect(within(kiosk).getByText('Never')).toBeInTheDocument()
  })

  it('hides revoked and expired credentials until asked, with a count', async () => {
    const user = userEvent.setup()
    signIn()
    renderCredentials()

    await screen.findByText('Roster sync')
    expect(screen.queryByText('Old exporter')).not.toBeInTheDocument()
    expect(screen.queryByText('Trial key')).not.toBeInTheDocument()

    await user.click(screen.getByLabelText('Show revoked and expired (2)'))
    const revoked = (await screen.findByText('Old exporter')).closest('tr') as HTMLElement
    expect(within(revoked).getByText('Revoked')).toBeInTheDocument()
    const expired = screen.getByText('Trial key').closest('tr') as HTMLElement
    expect(within(expired).getByText('Expired')).toBeInTheDocument()
  })

  it('never renders anything secret-shaped', async () => {
    signIn()
    renderCredentials()
    await screen.findByText('Roster sync')
    expect(document.body.textContent).not.toMatch(SECRET_SHAPE)
  })

  it('explains an empty company rather than showing a bare table', async () => {
    signIn()
    seed({ apiCredentials: [] })
    renderCredentials()
    expect(await screen.findByText('No integration credentials')).toBeInTheDocument()
  })

  it('surfaces a server failure with a retry', async () => {
    signIn()
    failNext('api-credentials-list', 500)
    renderCredentials()
    expect(await screen.findByText('Failed to retrieve integration credentials')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Try again|Retry/ })).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Issue
// ---------------------------------------------------------------------------

describe('issuing a credential', () => {
  it('offers only the scopes with live endpoints and lists the rest as unavailable', async () => {
    const user = userEvent.setup()
    signIn()
    renderCredentials()

    await user.click(await screen.findByRole('button', { name: 'Issue credential' }))
    const dialog = screen.getByRole('dialog')

    expect(within(dialog).getByLabelText(/^Read members/)).toBeInTheDocument()
    expect(within(dialog).getByLabelText(/^Read sites/)).toBeInTheDocument()
    // Not offered as controls...
    expect(within(dialog).queryByLabelText(/^Change members/)).not.toBeInTheDocument()
    expect(within(dialog).queryByLabelText(/^Manage webhooks/)).not.toBeInTheDocument()
    // ...but named, under a heading that says why.
    expect(within(dialog).getByRole('heading', { name: 'Not available yet' })).toBeInTheDocument()
    expect(within(dialog).getByText('Manage webhooks')).toBeInTheDocument()
  })

  it('requires a name and at least one scope, and warns that no sites means every site', async () => {
    const user = userEvent.setup()
    signIn()
    renderCredentials()

    await user.click(await screen.findByRole('button', { name: 'Issue credential' }))
    const dialog = screen.getByRole('dialog')
    expect(within(dialog).getByText('This credential will reach all sites')).toBeInTheDocument()

    await user.click(within(dialog).getByRole('button', { name: 'Issue credential' }))
    expect(await within(dialog).findByText('Name is required.')).toBeInTheDocument()
    expect(within(dialog).getByText('Choose at least one scope.')).toBeInTheDocument()
    expect(state.requests.filter((r) => r.method === 'POST')).toHaveLength(0)

    // Choosing a site removes the warning and says what it narrowed to.
    await user.click(within(dialog).getByLabelText(SITE_A.site_name))
    expect(within(dialog).queryByText('This credential will reach all sites')).not.toBeInTheDocument()
    expect(within(dialog).getByText(/Narrowed to 1 site/)).toBeInTheDocument()
  })

  it('shows the secret ONCE, requires acknowledgement, ignores Escape, then leaves nothing behind', async () => {
    const user = userEvent.setup()
    const writeText = stubClipboard()
    signIn()
    const client = makeTestQueryClient()
    renderCredentials('/settings/api-credentials', client)

    await user.click(await screen.findByRole('button', { name: 'Issue credential' }))
    const dialog = screen.getByRole('dialog')
    await user.type(within(dialog).getByLabelText(/Name/), 'Payroll export')
    await user.click(within(dialog).getByLabelText(/^Read members/))
    await user.click(within(dialog).getByLabelText(SITE_B.site_name))
    await user.click(within(dialog).getByRole('button', { name: 'Issue credential' }))

    // The panel replaces the form, with the warning first and the value once.
    const input = (await screen.findByLabelText('Secret')) as HTMLInputElement
    const secret = input.value
    expect(secret).toMatch(SECRET_SHAPE)
    expect(input).toHaveAttribute('readonly')
    expect(screen.getByText(/shown once and cannot be recovered/)).toBeInTheDocument()
    expect(document.body.textContent?.match(new RegExp(secret, 'g'))?.length ?? 0).toBe(0) // an input's value is not text content
    expect(screen.getAllByDisplayValue(secret)).toHaveLength(1)

    // Copy is an explicit action.
    await user.click(screen.getByRole('button', { name: 'Copy key' }))
    expect(writeText).toHaveBeenCalledWith(secret)

    // Escape does not dismiss it.
    await user.keyboard('{Escape}')
    expect(screen.getByLabelText('Secret')).toBeInTheDocument()

    // Dismissal requires the acknowledgement.
    const done = screen.getByRole('button', { name: 'Done' })
    expect(done).toBeDisabled()
    await user.click(screen.getByLabelText('I have stored this key somewhere safe'))
    expect(done).toBeEnabled()
    await user.click(done)
    await waitFor(() => expect(screen.queryByLabelText('Secret')).not.toBeInTheDocument())

    // The list refreshed with the new row (no secret in it)...
    const row = (await screen.findByText('Payroll export')).closest('tr') as HTMLElement
    expect(within(row).getByText('1 site')).toBeInTheDocument()
    // ...and the secret is gone from everywhere it could have been kept.
    await expectSecretGone(secret, client)
  })

  it('omits expiry for the default lifetime and sends the end of a chosen day', async () => {
    const user = userEvent.setup()
    signIn()
    renderCredentials()

    await user.click(await screen.findByRole('button', { name: 'Issue credential' }))
    let dialog = screen.getByRole('dialog')
    await user.type(within(dialog).getByLabelText(/Name/), 'Default lifetime')
    await user.click(within(dialog).getByLabelText(/^Read sites/))
    await user.click(within(dialog).getByRole('button', { name: 'Issue credential' }))
    await user.click(await screen.findByLabelText('I have stored this key somewhere safe'))
    await user.click(screen.getByRole('button', { name: 'Done' }))
    await screen.findByText('Default lifetime')
    // The fixture records the server default only when the KEY was absent.
    expect(state.apiCredentials.find((c) => c.name === 'Default lifetime')?.expires_at).toBe(
      '2027-09-01T10:00:00Z',
    )

    await user.click(screen.getByRole('button', { name: 'Issue credential' }))
    dialog = screen.getByRole('dialog')
    await user.type(within(dialog).getByLabelText(/Name/), 'Dated')
    await user.click(within(dialog).getByLabelText(/^Read sites/))
    await user.type(within(dialog).getByLabelText(/Expires on/), '2031-03-15')
    await user.click(within(dialog).getByRole('button', { name: 'Issue credential' }))
    await user.click(await screen.findByLabelText('I have stored this key somewhere safe'))
    await user.click(screen.getByRole('button', { name: 'Done' }))
    await screen.findByText('Dated')
    expect(state.apiCredentials.find((c) => c.name === 'Dated')?.expires_at).toBe(
      '2031-03-15T23:59:59Z',
    )
  })

  it('refuses a past expiry before asking the server', async () => {
    const user = userEvent.setup()
    signIn()
    renderCredentials()

    await user.click(await screen.findByRole('button', { name: 'Issue credential' }))
    const dialog = screen.getByRole('dialog')
    await user.type(within(dialog).getByLabelText(/Name/), 'Stale')
    await user.click(within(dialog).getByLabelText(/^Read sites/))
    await user.type(within(dialog).getByLabelText(/Expires on/), '2020-01-01')
    await user.click(within(dialog).getByRole('button', { name: 'Issue credential' }))
    expect(await within(dialog).findByText('The expiry must be in the future.')).toBeInTheDocument()
    expect(state.requests.filter((r) => r.method === 'POST')).toHaveLength(0)
  })

  it("reports the server's issuer-role refusal in words the operator can act on", async () => {
    const user = userEvent.setup()
    signIn()
    failNext('api-credentials-issue', 403)
    renderCredentials()

    await user.click(await screen.findByRole('button', { name: 'Issue credential' }))
    const dialog = screen.getByRole('dialog')
    await user.type(within(dialog).getByLabelText(/Name/), 'Too much')
    await user.click(within(dialog).getByLabelText(/^Read sites/))
    await user.click(within(dialog).getByRole('button', { name: 'Issue credential' }))

    expect(
      await within(dialog).findByText(/Your role cannot grant this scope\. Ask an owner/),
    ).toBeInTheDocument()
    // The form is still there to correct.
    expect(within(dialog).getByLabelText(/Name/)).toHaveValue('Too much')
    expect(screen.queryByLabelText('Secret')).not.toBeInTheDocument()
  })

  it('explains the per-company cap from its error code', async () => {
    const user = userEvent.setup()
    signIn()
    failNext('api-credentials-issue', 409)
    renderCredentials()

    await user.click(await screen.findByRole('button', { name: 'Issue credential' }))
    const dialog = screen.getByRole('dialog')
    await user.type(within(dialog).getByLabelText(/Name/), 'Twenty-first')
    await user.click(within(dialog).getByLabelText(/^Read sites/))
    await user.click(within(dialog).getByRole('button', { name: 'Issue credential' }))
    expect(
      await within(dialog).findByText(/maximum number of live credentials/),
    ).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Detail
// ---------------------------------------------------------------------------

describe('credential detail', () => {
  it('shows the metadata, site access and usage, and never a secret', async () => {
    signIn()
    seed({
      apiCredentialUsage: { 'cred-1': makeAPICredentialUsage({ id: 'cred-1' }) },
    })
    renderCredentials('/settings/api-credentials/cred-1')

    expect(await screen.findByRole('heading', { name: 'Roster sync' })).toBeInTheDocument()
    expect(screen.getByText('atp_live_0123abcd…')).toBeInTheDocument()
    expect(screen.getByText('Active')).toBeInTheDocument()
    expect(screen.getByText('Read members')).toBeInTheDocument()
    expect(screen.getByText('203.0.113.7')).toBeInTheDocument()
    expect(screen.getByText(/reads/)).toHaveTextContent('all sites')

    // Usage: the rollup rows, served and refused as separate columns.
    const usage = await screen.findByRole('table', { name: /Requests per day/ })
    expect(within(usage).getByText('2026-09-10')).toBeInTheDocument()
    expect(within(usage).getByText('412')).toBeInTheDocument()
    expect(within(usage).getByText('3')).toBeInTheDocument()

    expect(screen.getByRole('button', { name: 'Rotate' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Revoke' })).toBeInTheDocument()
    expect(document.body.textContent).not.toMatch(SECRET_SHAPE)
  })

  it('lists a site restriction as links', async () => {
    signIn()
    renderCredentials('/settings/api-credentials/cred-2')
    expect(await screen.findByRole('heading', { name: 'Lagos kiosk' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: SITE_A.site_name })).toHaveAttribute(
      'href',
      `/sites/${SITE_A.site_id}`,
    )
  })

  it('says a revoked credential cannot be recovered and offers no lifecycle controls', async () => {
    signIn()
    renderCredentials('/settings/api-credentials/cred-3')
    expect(await screen.findByRole('heading', { name: 'Old exporter' })).toBeInTheDocument()
    expect(screen.getByText('This credential is revoked')).toBeInTheDocument()
    expect(screen.getByText(/cannot be shown or recovered/)).toBeInTheDocument()
    expect(screen.getByText(/Vendor contract ended/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Rotate' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Revoke' })).not.toBeInTheDocument()
  })

  it('shows an empty usage window as such', async () => {
    signIn()
    renderCredentials('/settings/api-credentials/cred-2')
    expect(await screen.findByText('No use recorded')).toBeInTheDocument()
  })

  it('explains an unknown id', async () => {
    signIn()
    renderCredentials('/settings/api-credentials/nope')
    expect(await screen.findByRole('heading', { name: 'Credential not found' })).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Rotate
// ---------------------------------------------------------------------------

describe('rotating a credential', () => {
  it('defaults to an immediate cutover, shows the new secret once, and leaves nothing behind', async () => {
    const user = userEvent.setup()
    stubClipboard()
    signIn()
    const client = makeTestQueryClient()
    renderCredentials('/settings/api-credentials/cred-1', client)

    await user.click(await screen.findByRole('button', { name: 'Rotate' }))
    const dialog = screen.getByRole('dialog')
    expect(within(dialog).getByLabelText(/^Immediately/)).toBeChecked()
    expect(within(dialog).getByLabelText(/^72 hours/)).not.toBeChecked()
    await user.type(within(dialog).getByLabelText(/Reason/), 'Key seen in a log')
    await user.click(within(dialog).getByRole('button', { name: 'Rotate credential' }))

    const input = (await screen.findByLabelText('New secret')) as HTMLInputElement
    const secret = input.value
    expect(secret).toMatch(SECRET_SHAPE)
    expect(screen.getByText(/already stopped working/)).toBeInTheDocument()

    await user.keyboard('{Escape}')
    expect(screen.getByLabelText('New secret')).toBeInTheDocument()

    const done = screen.getByRole('button', { name: 'Done' })
    expect(done).toBeDisabled()
    await user.click(screen.getByLabelText('I have stored this key somewhere safe'))
    await user.click(done)
    await waitFor(() => expect(screen.queryByLabelText('New secret')).not.toBeInTheDocument())

    // The old row is now out of service (no grace) and says where its replacement is.
    expect(await screen.findByText('This credential is expired')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'the new credential' })).toBeInTheDocument()
    expect(state.apiCredentials.find((c) => c.id === 'cred-1')?.status).toBe('EXPIRED')
    await expectSecretGone(secret, client)
  })

  it('sends the chosen grace and describes it on the panel', async () => {
    const user = userEvent.setup()
    stubClipboard()
    signIn()
    renderCredentials('/settings/api-credentials/cred-1')

    await user.click(await screen.findByRole('button', { name: 'Rotate' }))
    const dialog = screen.getByRole('dialog')
    await user.click(within(dialog).getByLabelText(/^72 hours/))
    await user.click(within(dialog).getByRole('button', { name: 'Rotate credential' }))

    await screen.findByLabelText('New secret')
    expect(screen.getByText(/keeps working for/)).toHaveTextContent('72 hours')
    expect(state.apiCredentials.find((c) => c.id === 'cred-1')?.status).toBe('IN_GRACE')
  })

  it('reports a failure and keeps the form', async () => {
    const user = userEvent.setup()
    signIn()
    failNext('api-credentials-rotate', 500)
    renderCredentials('/settings/api-credentials/cred-1')

    await user.click(await screen.findByRole('button', { name: 'Rotate' }))
    const dialog = screen.getByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: 'Rotate credential' }))
    expect(
      await within(dialog).findByText('Failed to rotate the integration credential'),
    ).toBeInTheDocument()
    expect(screen.queryByLabelText('New secret')).not.toBeInTheDocument()
    expect(state.apiCredentials.find((c) => c.id === 'cred-1')?.status).toBe('ACTIVE')
  })
})

// ---------------------------------------------------------------------------
// Revoke
// ---------------------------------------------------------------------------

describe('revoking a credential', () => {
  it('requires the name to be typed, records the reason, and marks the row revoked', async () => {
    const user = userEvent.setup()
    signIn()
    renderCredentials('/settings/api-credentials/cred-1')

    await user.click(await screen.findByRole('button', { name: 'Revoke' }))
    const dialog = screen.getByRole('dialog')
    expect(within(dialog).getByText(/cannot be undone and the secret cannot be shown again/)).toBeInTheDocument()
    const confirm = within(dialog).getByRole('button', { name: 'Revoke credential' })
    expect(confirm).toBeDisabled()

    await user.type(within(dialog).getByLabelText(/Reason/), 'Integration retired')
    await user.type(within(dialog).getByLabelText(/Roster sync/), 'Roster sync')
    expect(confirm).toBeEnabled()
    await user.click(confirm)

    expect(await screen.findByText('This credential is revoked')).toBeInTheDocument()
    expect(screen.getByText(/Integration retired/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Revoke' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Rotate' })).not.toBeInTheDocument()
    const stored = state.apiCredentials.find((c) => c.id === 'cred-1')
    expect(stored?.status).toBe('REVOKED')
    expect(stored?.revoked_reason).toBe('Integration retired')
  })

  it('surfaces a failure without pretending', async () => {
    const user = userEvent.setup()
    signIn()
    failNext('api-credentials-revoke', 500)
    renderCredentials('/settings/api-credentials/cred-1')

    await user.click(await screen.findByRole('button', { name: 'Revoke' }))
    const dialog = screen.getByRole('dialog')
    await user.type(within(dialog).getByLabelText(/Roster sync/), 'Roster sync')
    await user.click(within(dialog).getByRole('button', { name: 'Revoke credential' }))
    expect(
      await within(dialog).findByText('Failed to revoke the integration credential'),
    ).toBeInTheDocument()
    expect(state.apiCredentials.find((c) => c.id === 'cred-1')?.status).toBe('ACTIVE')
  })
})

// ---------------------------------------------------------------------------
// Cache hygiene
// ---------------------------------------------------------------------------

describe('what the query cache holds', () => {
  it('caches the list and detail without any secret-shaped value', async () => {
    signIn()
    const client = makeTestQueryClient({ gcTime: 60_000 })
    renderCredentials('/settings/api-credentials/cred-1', client)
    await screen.findByRole('heading', { name: 'Roster sync' })
    expect(JSON.stringify(client.getQueryData(keys.apiCredentials.detail('cred-1')))).not.toMatch(
      SECRET_SHAPE,
    )
  })
})
