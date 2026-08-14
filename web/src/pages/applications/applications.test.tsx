import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../../api/csrf'
import type { Role, Session } from '../../api/types'
import { makeApplication, makeSession } from '../../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../../test/render'
import { failNext, resetServerState, seed, state } from '../../test/server'
import { ApplicationDetailPage } from './ApplicationDetailPage'
import { ApplicationsPage } from './ApplicationsPage'

/**
 * The Applications module.
 *
 * This is where the product's general-purpose claim is either kept or quietly
 * broken, so the tests are mostly about what the console REFUSES to assume: that
 * the catalog is the one this build knows, that MULTI_PURPOSE is a capability,
 * or that enabling something makes the platform do it.
 */

const CATALOG = [
  'ACCESS_CONTROL',
  'ATTENDANCE',
  'REGISTRATION',
  'CHECK_IN',
  'VERIFICATION',
  'TIME_TRACKING',
  'VISITOR_MANAGEMENT',
]

function signIn(role: Role = 'OWNER', overrides: Partial<Session> = {}) {
  const session = makeSession({
    role,
    operator: { id: 'operator-1', email: 'ops@example.com', full_name: 'Ops', role },
    ...overrides,
  })
  resetServerState(session)
  setCsrfToken(session.csrf_token)
  seed({ available: CATALOG, applications: [] })
  return session
}

function renderApplications(initialPath = '/settings/applications', client = makeTestQueryClient()) {
  const router = createMemoryRouter(
    [
      { path: '/settings/applications', element: <ApplicationsPage /> },
      { path: '/settings/applications/:slug', element: <ApplicationDetailPage /> },
      { path: '/terminals', element: <p>Terminals page</p> },
    ],
    { initialEntries: [initialPath] },
  )
  return renderWithSession(<RouterProvider router={router} />, client)
}

beforeEach(() => setCsrfToken(null))

// ---------------------------------------------------------------------------
// Catalog
// ---------------------------------------------------------------------------

describe('the catalog', () => {
  it('renders every capability the SERVER offers, with a description', async () => {
    signIn()
    renderApplications()

    expect(await screen.findByText('Access Control')).toBeInTheDocument()
    for (const label of [
      'Attendance',
      'Registration',
      'Check-in',
      'Verification',
      'Time Tracking',
      'Visitor Management',
    ]) {
      expect(screen.getByText(label)).toBeInTheDocument()
    }
    // Capability information, not just a name.
    expect(screen.getByText(/Record presence against a schedule/)).toBeInTheDocument()
  })

  it('shows enabled and not-enabled state per capability', async () => {
    signIn()
    seed({
      available: CATALOG,
      applications: [makeApplication({ code: 'ATTENDANCE', enabled: true })],
    })
    renderApplications()

    const attendance = (await screen.findByText('Attendance')).closest('li') as HTMLElement
    expect(within(attendance).getByText('Enabled')).toBeInTheDocument()

    const checkIn = screen.getByText('Check-in').closest('li') as HTMLElement
    expect(within(checkIn).getByText('Not enabled')).toBeInTheDocument()
  })

  it('is a legitimate, fully working state for a company with nothing enabled', async () => {
    // Every company starts here. It must never read as an error or a
    // misconfiguration.
    signIn()
    renderApplications()

    await screen.findByText('Access Control')
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.getAllByText('Not enabled')).toHaveLength(CATALOG.length)
  })

  it('reports a failed load as an error rather than an empty catalog', async () => {
    signIn()
    failNext('applications', 500)
    renderApplications()

    expect(await screen.findByRole('alert')).toHaveTextContent(/Failed to retrieve applications/)
    expect(screen.queryByText('Access Control')).not.toBeInTheDocument()
  })

  it('handles a platform that offers nothing at all', async () => {
    signIn()
    seed({ available: [], applications: [] })
    renderApplications()

    expect(await screen.findByText('No capabilities offered')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// The architecture rule
// ---------------------------------------------------------------------------

describe('MULTI_PURPOSE is a terminal mode, not an application', () => {
  it('never appears in the catalog, even if the server sends it', async () => {
    // Defensive: the real server excludes it, and a future one that did not
    // must still not be able to turn it into a company toggle here.
    signIn()
    seed({ available: [...CATALOG, 'MULTI_PURPOSE'], applications: [] })
    renderApplications()

    await screen.findByText('Access Control')
    expect(screen.queryByText('Multi Purpose')).not.toBeInTheDocument()
    expect(screen.queryByText('MULTI_PURPOSE')).not.toBeInTheDocument()
  })

  it('is refused a detail page, and explained rather than 404ed', async () => {
    signIn()
    renderApplications('/settings/applications/multi-purpose')

    expect(await screen.findByText('Not a capability')).toBeInTheDocument()
    expect(
      screen.getByText(/terminal operating mode, not a capability a company enables/),
    ).toBeInTheDocument()
  })

  it('explains on the catalog page how capabilities relate to terminal modes', async () => {
    // Four concepts meet here and are routinely confused; the page names the
    // relationship rather than leaving it to be inferred.
    signIn()
    renderApplications()

    expect(await screen.findByText('How this relates to your terminals')).toBeInTheDocument()
    expect(screen.getByText(/Multi-purpose is a terminal setting, not a capability/)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Unknown / future codes
// ---------------------------------------------------------------------------

describe('capabilities newer than this console', () => {
  it('RENDERS AN UNKNOWN CODE rather than crashing or dropping it', async () => {
    // A capability added to the platform must appear without a frontend
    // release. Hiding it would silently conceal part of what a customer has.
    signIn()
    seed({ available: [...CATALOG, 'ROOM_BOOKING'], applications: [] })
    renderApplications()

    // Humanised from the code, and marked as not understood.
    expect(await screen.findByText('Room Booking')).toBeInTheDocument()
    const entry = screen.getByText('Room Booking').closest('li') as HTMLElement
    expect(within(entry).getByText('Unrecognised')).toBeInTheDocument()
    expect(within(entry).getByText('ROOM_BOOKING')).toBeInTheDocument()
  })

  it('is not silently treated as some other capability', async () => {
    signIn()
    seed({ available: ['ROOM_BOOKING'], applications: [] })
    renderApplications()

    await screen.findByText('Room Booking')
    // None of the known labels leaked in.
    for (const label of ['Access Control', 'Attendance', 'Check-in']) {
      expect(screen.queryByText(label)).not.toBeInTheDocument()
    }
  })

  it('gives an unknown capability a working detail page', async () => {
    signIn()
    seed({ available: ['ROOM_BOOKING'], applications: [] })
    renderApplications('/settings/applications/room-booking')

    expect(await screen.findByRole('heading', { name: 'Room Booking', level: 1 })).toBeInTheDocument()
    expect(screen.getByText('Newer than this console')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Enable' })).toBeInTheDocument()
  })

  it('can be enabled like any other', async () => {
    const user = userEvent.setup()
    signIn()
    seed({ available: ['ROOM_BOOKING'], applications: [] })
    renderApplications('/settings/applications/room-booking')

    await user.click(await screen.findByRole('button', { name: 'Enable' }))
    await waitFor(() => expect(screen.getByText('Enabled')).toBeInTheDocument())
  })

  it('reports a slug the platform does not offer as unavailable', async () => {
    signIn()
    seed({ available: CATALOG, applications: [] })
    renderApplications('/settings/applications/not-a-real-thing')

    expect(await screen.findByText('Not a capability')).toBeInTheDocument()
    expect(screen.getByText(/does not offer a capability by that name/)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Honesty about what enabling does
// ---------------------------------------------------------------------------

describe('what enabling actually does', () => {
  it('says plainly that no behaviour is switched on yet', async () => {
    // The most important sentence on the page. An owner reading "Access
    // Control" and expecting doors to change behaviour has been misled.
    signIn()
    renderApplications()

    expect(
      await screen.findByText('Enabling a capability does not switch on behaviour yet'),
    ).toBeInTheDocument()
    expect(screen.getByText(/does not yet evaluate any of these workflows/)).toBeInTheDocument()
  })

  it('repeats it on the detail page', async () => {
    signIn()
    renderApplications('/settings/applications/attendance')

    expect(await screen.findByText('No behaviour is switched on yet')).toBeInTheDocument()
  })

  it('states that dependencies and conflicts are not modelled', async () => {
    // Better than an empty "Dependencies:" heading implying the answer is none.
    signIn()
    renderApplications('/settings/applications/attendance')

    expect(await screen.findByText('Dependencies and conflicts')).toBeInTheDocument()
    expect(
      screen.getByText(/does not currently record relationships between/),
    ).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Roles
// ---------------------------------------------------------------------------

describe('role restrictions', () => {
  it('lets an OWNER enable and disable', async () => {
    const user = userEvent.setup()
    signIn('OWNER')
    renderApplications()

    const attendance = (await screen.findByText('Attendance')).closest('li') as HTMLElement
    await user.click(within(attendance).getByRole('button', { name: 'Enable' }))

    await waitFor(() =>
      expect(within(attendance).getByText('Enabled')).toBeInTheDocument(),
    )
  })

  it('lets an ADMIN read but offers no controls', async () => {
    signIn('ADMIN')
    renderApplications()

    await screen.findByText('Access Control')
    expect(screen.getByText('Read only')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Enable' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Disable' })).not.toBeInTheDocument()
  })

  it('shows an ADMIN the settings as read-only text, not an editor', async () => {
    signIn('ADMIN')
    seed({
      available: CATALOG,
      applications: [makeApplication({ code: 'ATTENDANCE', enabled: true, settings: { a: 1 } })],
    })
    renderApplications('/settings/applications/attendance')

    await screen.findByRole('heading', { name: 'Attendance', level: 1 })
    expect(screen.queryByLabelText('Settings JSON')).not.toBeInTheDocument()
    expect(screen.getByText(/"a": 1/)).toBeInTheDocument()
  })

  it('is refused by the server for a non-OWNER even if a control were reached', async () => {
    // The gate here is a courtesy; the API is the boundary. Proven by calling
    // the mutation as an ADMIN and seeing the 403.
    const user = userEvent.setup()
    signIn('OWNER')
    renderApplications()
    await screen.findByText('Attendance')

    // Downgrade the session under the page, then act.
    if (state.session) state.session = { ...state.session, role: 'ADMIN' }
    const attendance = screen.getByText('Attendance').closest('li') as HTMLElement
    await user.click(within(attendance).getByRole('button', { name: 'Enable' }))

    expect(await screen.findByText(/Insufficient permissions/)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Mutations and cache
// ---------------------------------------------------------------------------

describe('enabling and disabling', () => {
  it('REFETCHES THE SESSION, so navigation reflects the change', async () => {
    // Navigation is derived from session.applications. Without this the console
    // changes everywhere except its own menu.
    //
    // Asserted as a REFETCH rather than as the isInvalidated flag: a mounted
    // SessionProvider reacts to the invalidation immediately, so the flag has
    // already cleared by the time a test could read it. The extra GET /auth/me
    // is the durable evidence.
    const user = userEvent.setup()
    signIn('OWNER')
    renderApplications()

    const attendance = (await screen.findByText('Attendance')).closest('li') as HTMLElement
    const before = state.requests.filter((r) => r.url.includes('/auth/me')).length

    await user.click(within(attendance).getByRole('button', { name: 'Enable' }))

    await waitFor(() =>
      expect(
        state.requests.filter((r) => r.url.includes('/auth/me')).length,
      ).toBeGreaterThan(before),
    )
  })

  it('warns before disabling that assigned terminals resolve to nothing', async () => {
    const user = userEvent.setup()
    signIn('OWNER')
    seed({
      available: CATALOG,
      applications: [makeApplication({ code: 'ATTENDANCE', enabled: true })],
    })
    renderApplications('/settings/applications/attendance')

    await user.click(await screen.findByRole('button', { name: 'Disable' }))

    expect(screen.getByText(/resolve to nothing/)).toBeInTheDocument()
    // And that assignments are kept rather than rewritten.
    expect(screen.getByText(/kept, not rewritten/)).toBeInTheDocument()
  })

  it('reports a failed toggle without claiming success', async () => {
    const user = userEvent.setup()
    signIn('OWNER')
    failNext('set-application', 500)
    renderApplications()

    const attendance = (await screen.findByText('Attendance')).closest('li') as HTMLElement
    await user.click(within(attendance).getByRole('button', { name: 'Enable' }))

    expect(await screen.findByText(/Could not enable Attendance/)).toBeInTheDocument()
    expect(within(attendance).getByText('Not enabled')).toBeInTheDocument()
  })

  it('saves settings without silently enabling a disabled capability', async () => {
    // The API defaults `enabled` to true when omitted, so saving settings on a
    // disabled capability would switch it on. The request sends it explicitly.
    const user = userEvent.setup()
    signIn('OWNER')
    seed({
      available: CATALOG,
      applications: [makeApplication({ code: 'ATTENDANCE', enabled: false })],
    })
    renderApplications('/settings/applications/attendance')

    await screen.findByRole('heading', { name: 'Attendance', level: 1 })
    const editor = screen.getByLabelText('Settings JSON')
    await user.clear(editor)
    await user.type(editor, '{{"grace_minutes":10}')
    await user.click(screen.getByRole('button', { name: 'Save settings' }))

    await waitFor(() =>
      expect(state.applications.find((a) => a.code === 'ATTENDANCE')?.settings).toEqual({
        grace_minutes: 10,
      }),
    )
    expect(state.applications.find((a) => a.code === 'ATTENDANCE')?.enabled).toBe(false)
  })

  it('refuses malformed settings JSON without sending it', async () => {
    const user = userEvent.setup()
    signIn('OWNER')
    renderApplications('/settings/applications/attendance')

    const editor = await screen.findByLabelText('Settings JSON')
    await user.clear(editor)
    await user.type(editor, 'not json')

    const before = state.requests.filter((r) => r.method === 'PUT').length
    await user.click(screen.getByRole('button', { name: 'Save settings' }))

    await waitFor(() => expect(editor).toHaveAttribute('aria-invalid', 'true'))
    expect(state.requests.filter((r) => r.method === 'PUT')).toHaveLength(before)
  })
})

// ---------------------------------------------------------------------------
// Isolation and disclosure
// ---------------------------------------------------------------------------

describe('isolation and disclosure', () => {
  it('shows only the signed-in company’s configuration', async () => {
    // The API scopes by company; the console renders what came back and names
    // the company it belongs to rather than implying a global setting.
    signIn('OWNER')
    seed({
      available: CATALOG,
      applications: [makeApplication({ code: 'ATTENDANCE', enabled: true })],
    })
    renderApplications('/settings/applications/attendance')

    await screen.findByRole('heading', { name: 'Attendance', level: 1 })
    expect(screen.getByText(/for Northwind Logistics/)).toBeInTheDocument()
  })

  it('discloses no credential or biometric material anywhere', async () => {
    signIn('OWNER')
    seed({
      available: CATALOG,
      applications: [makeApplication({ code: 'ATTENDANCE', enabled: true })],
    })
    renderApplications()

    await screen.findByText('Access Control')
    const text = (document.body.textContent ?? '').toLowerCase()
    for (const forbidden of [
      'api_key',
      'atd_',
      'ats_',
      'password_hash',
      'token_hash',
      'fingerprint',
      'template',
    ]) {
      expect(text).not.toContain(forbidden)
    }
  })
})

// ---------------------------------------------------------------------------
// General-purpose language
// ---------------------------------------------------------------------------

describe('the product stays general-purpose', () => {
  it('uses no industry-specific vocabulary anywhere on either page', async () => {
    // The console is sold to schools, factories, warehouses, events and
    // residential sites as readily as to a gym. A single stray word here would
    // tell every other customer this was not built for them.
    signIn('OWNER')
    renderApplications()
    await screen.findByText('Access Control')
    let text = (document.body.textContent ?? '').toLowerCase()

    const forbidden = ['gym', 'membership', 'member ', 'trainer', 'workout', 'class ', 'branch']
    for (const word of forbidden) {
      expect(text, `catalog page mentions "${word.trim()}"`).not.toContain(word)
    }

    renderApplications('/settings/applications/attendance')
    await screen.findByRole('heading', { name: 'Attendance', level: 1 })
    text = (document.body.textContent ?? '').toLowerCase()
    for (const word of forbidden) {
      expect(text, `detail page mentions "${word.trim()}"`).not.toContain(word)
    }
  })

  it('describes capabilities in terms that fit any organisation', async () => {
    signIn('OWNER')
    renderApplications()

    await screen.findByText('Access Control')
    // "door, barrier or lock" rather than a turnstile at a gym.
    expect(screen.getByText(/door, barrier or lock/)).toBeInTheDocument()
    expect(screen.getByText(/event or appointment/)).toBeInTheDocument()
    expect(screen.getByText(/not on the roster/)).toBeInTheDocument()
  })
})
