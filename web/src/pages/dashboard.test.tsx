import { screen, waitFor, within } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../api/csrf'
import type { Role, Session } from '../api/types'
import { RequireAuth } from '../auth/guards'
import { SiteProvider } from '../context/SiteContext'
import { makePerson, makeSession, makeSite, makeTerminal, SITE_A, SITE_B } from '../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../test/render'
import { failNext, resetServerState, seed } from '../test/server'
import { DashboardPage, collectAttention, countFleet } from './DashboardPage'

/**
 * The overview.
 *
 * The property under test throughout is that NOTHING IS FABRICATED. Every figure
 * traces to something the API computed, every gap is stated rather than filled,
 * and the page never implies that an enabled capability is doing anything.
 */

const SITES = [
  makeSite({ id: SITE_A.site_id, name: SITE_A.site_name }),
  makeSite({ id: SITE_B.site_id, name: SITE_B.site_name }),
]

const FLEET = [
  makeTerminal({ serial_number: 'AT-0001', site_public_id: SITE_A.site_id, status: 'ONLINE' }),
  makeTerminal({
    id: 2,
    serial_number: 'AT-0002',
    site_public_id: SITE_B.site_id,
    status: 'OFFLINE',
    firmware_outdated: true,
  }),
  makeTerminal({
    id: 3,
    serial_number: 'AT-0003',
    site_public_id: SITE_A.site_id,
    status: 'ERROR',
    last_heartbeat_at: undefined,
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
  seed({
    sites: SITES,
    terminals: FLEET,
    people: [makePerson({ external_id: 'P-1' }), makePerson({ id: 'p2', external_id: 'P-2' })],
  })
  return session
}

function renderDashboard(client = makeTestQueryClient()) {
  const router = createMemoryRouter(
    [
      {
        path: '/',
        // RequireAuth exactly as the real router does it: the dashboard reads
        // the session unconditionally and is only ever mounted behind the
        // guard that has already resolved one.
        element: (
          <RequireAuth>
            <SiteProvider>
              <DashboardPage />
            </SiteProvider>
          </RequireAuth>
        ),
      },
      { path: '/terminals', element: <p>Terminals</p> },
      { path: '/sites', element: <p>Sites</p> },
      { path: '/people', element: <p>People</p> },
      { path: '/settings/applications', element: <p>Applications</p> },
    ],
    { initialEntries: ['/'] },
  )
  return renderWithSession(<RouterProvider router={router} />, client)
}

beforeEach(() => setCsrfToken(null))

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

describe('fleet counting', () => {
  it('rolls a terminal list into the same shape the summary endpoint returns', () => {
    expect(countFleet(FLEET)).toEqual({
      total: 3,
      online: 1,
      offline: 1,
      updating: 0,
      error: 1,
      disabled: 0,
      provisioning: 0,
      firmware_outdated: 1,
    })
  })

  it('counts an empty fleet as zero rather than throwing', () => {
    expect(countFleet([])).toMatchObject({ total: 0, online: 0 })
  })
})

describe('attention items', () => {
  const fleet = countFleet(FLEET)

  it('reports only what the platform actually said', () => {
    const items = collectAttention({
      fleet,
      terminals: FLEET,
      inactiveSites: 0,
      applicationCount: 1,
      peopleTotal: 5,
    })
    const ids = items.map((item) => item.id)

    expect(ids).toContain('terminals-error')
    expect(ids).toContain('terminals-offline')
    expect(ids).toContain('terminals-never-reported')
    expect(ids).toContain('terminals-outdated')
    // Nothing invented: no application or people prompt when both are fine.
    expect(ids).not.toContain('no-applications')
    expect(ids).not.toContain('no-people')
  })

  it('orders faults above things that are merely incomplete', () => {
    const items = collectAttention({
      fleet,
      terminals: FLEET,
      inactiveSites: 1,
      applicationCount: 0,
      peopleTotal: 0,
    })
    expect(items[0]?.id).toBe('terminals-error')
    expect(items.at(-1)?.id).toBe('no-people')
  })

  it('is empty for a healthy, configured deployment', () => {
    const healthy = [makeTerminal({ status: 'ONLINE' })]
    expect(
      collectAttention({
        fleet: countFleet(healthy),
        terminals: healthy,
        inactiveSites: 0,
        applicationCount: 2,
        peopleTotal: 10,
      }),
    ).toEqual([])
  })

  it('says nothing at all when the fleet has not loaded', () => {
    // Undefined is not zero. "0 terminals online" is a claim, and the alarming
    // one to make by accident.
    const items = collectAttention({
      fleet: undefined,
      terminals: [],
      inactiveSites: 0,
      applicationCount: 1,
      peopleTotal: 1,
    })
    expect(items).toEqual([])
  })
})

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

describe('dashboard data', () => {
  it('shows company, operator and site context', async () => {
    signIn()
    renderDashboard()

    // The lead is assembled from several nodes, so read the header's text.
    const heading = await screen.findByRole('heading', { name: 'Overview', level: 1 })
    const header = heading.closest('.page__header') as HTMLElement
    expect(header.textContent).toContain('Northwind Logistics · Ops Person (Administrator)')

    const context = screen.getByRole('region', { name: 'Your context' })
    expect(within(context).getByText('Every site in this company')).toBeInTheDocument()
  })

  it('shows totals the API actually computes', async () => {
    signIn()
    renderDashboard()

    const tiles = await screen.findByRole('region', { name: 'Platform totals' })
    // Two sites, three terminals, two people.
    await waitFor(() => expect(within(tiles).getByText('2 active')).toBeInTheDocument())
    expect(within(tiles).getByText('Terminals')).toBeInTheDocument()
    expect(within(tiles).getByText('People')).toBeInTheDocument()
  })

  it('reads the people total from the envelope rather than counting a page', async () => {
    // limit=1 fetches one row; `total` is the server's count over the roster.
    signIn()
    seed({
      sites: SITES,
      terminals: FLEET,
      people: Array.from({ length: 120 }, (_, i) =>
        makePerson({ id: `p${i}`, external_id: `P-${i}` }),
      ),
    })
    renderDashboard()

    const tiles = await screen.findByRole('region', { name: 'Platform totals' })
    await waitFor(() => expect(within(tiles).getByText('120')).toBeInTheDocument())
  })

  it('breaks down terminal health from the server’s own rollup', async () => {
    signIn()
    renderDashboard()

    const health = await screen.findByRole('region', { name: /Terminal health/ })
    expect(await within(health).findByText('Online')).toBeInTheDocument()
    expect(within(health).getByText('Reporting an error')).toBeInTheDocument()
    expect(within(health).getByText('Behind on firmware')).toBeInTheDocument()
  })

  it('lists what needs attention, derived from real state', async () => {
    signIn()
    renderDashboard()

    const attention = await screen.findByRole('region', { name: 'Needs your attention' })
    expect(
      await within(attention).findByText(/1 terminal reporting a fault/),
    ).toBeInTheDocument()
    expect(within(attention).getByText(/1 terminal offline/)).toBeInTheDocument()
    expect(within(attention).getByText(/never reported in/)).toBeInTheDocument()
  })

  it('shows no attention section for a healthy deployment', async () => {
    signIn('ADMIN', { applications: [{ code: 'ATTENDANCE', settings: {} }] })
    seed({
      sites: SITES,
      terminals: [makeTerminal({ status: 'ONLINE' })],
      people: [makePerson()],
    })
    renderDashboard()

    await screen.findByRole('region', { name: 'Platform totals' })
    await waitFor(() =>
      expect(screen.queryByRole('region', { name: 'Needs your attention' })).not.toBeInTheDocument(),
    )
  })
})

// ---------------------------------------------------------------------------
// Honesty
// ---------------------------------------------------------------------------

describe('the dashboard does not fabricate', () => {
  it('shows a dash, never a zero, for a figure it does not have', async () => {
    // "0 people" and "we could not ask" must not look the same. Driven by a
    // failing request rather than a race, so the assertion is deterministic.
    signIn()
    failNext('people', 500)
    renderDashboard()

    const tiles = await screen.findByRole('region', { name: 'Platform totals' })
    const peopleTile = within(tiles).getByText('People').closest('a, article') as HTMLElement
    expect(within(peopleTile).getByText('—')).toBeInTheDocument()
    expect(within(peopleTile).queryByText('0')).not.toBeInTheDocument()
  })

  it('states that capabilities are not running, rather than implying they are', async () => {
    // A list of enabled capabilities on an operations dashboard reads as a list
    // of things that are happening. They are not.
    signIn('ADMIN', { applications: [{ code: 'ATTENDANCE', settings: {} }] })
    renderDashboard()

    expect(await screen.findByText('These are not running yet')).toBeInTheDocument()
    expect(screen.getByText(/no attendance is being calculated/i)).toBeInTheDocument()
  })

  it('admits there is no activity feed instead of showing something adjacent', async () => {
    signIn()
    renderDashboard()

    const activity = await screen.findByRole('region', { name: 'Recent activity' })
    expect(within(activity).getByText(/Not available in this console yet/)).toBeInTheDocument()
    expect(within(activity).getByText(/no audit trail of changes/)).toBeInTheDocument()
  })

  it('reports a failed fleet load as an error rather than an empty fleet', async () => {
    signIn()
    failNext('terminals-summary', 500)
    renderDashboard()

    const health = await screen.findByRole('region', { name: /Terminal health/ })
    await waitFor(() =>
      expect(within(health).getByRole('alert')).toHaveTextContent(/Failed to retrieve summary/),
    )
  })

  it('treats a company with nothing configured as a working state', async () => {
    signIn('ADMIN', { applications: [] })
    seed({ sites: [], terminals: [], people: [] })
    renderDashboard()

    await screen.findByRole('region', { name: 'Platform totals' })
    expect(await screen.findByText('No applications enabled')).toBeInTheDocument()
    expect(screen.getByText(/normal starting state/)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Scope
// ---------------------------------------------------------------------------

describe('site scope', () => {
  it('describes a scoped operator’s access accurately', async () => {
    signIn('MANAGER', { all_sites: false, sites: [SITE_A] })
    renderDashboard()

    const context = await screen.findByRole('region', { name: 'Your context' })
    expect(within(context).getByText(/1 site: Lagos Depot/)).toBeInTheDocument()
  })

  it('labels the scope it is showing', async () => {
    signIn()
    renderDashboard()

    const context = await screen.findByRole('region', { name: 'Your context' })
    expect(within(context).getByText('Everything you can reach')).toBeInTheDocument()
  })

  it('remembers a single-site operator’s only site as the selection', async () => {
    // SiteProvider defaults a scoped operator to their one site rather than to
    // an "all sites" view the API would not honour.
    signIn('MANAGER', { all_sites: false, sites: [SITE_A] })
    renderDashboard()

    const context = await screen.findByRole('region', { name: 'Your context' })
    expect(within(context).getByText(/Lagos Depot only/)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Language and disclosure
// ---------------------------------------------------------------------------

describe('general-purpose language and disclosure', () => {
  it('uses no industry-specific vocabulary', async () => {
    signIn('ADMIN', { applications: [{ code: 'ACCESS_CONTROL', settings: {} }] })
    renderDashboard()

    await screen.findByRole('region', { name: 'Platform totals' })
    const text = (document.body.textContent ?? '').toLowerCase()
    for (const word of ['gym', 'membership', 'trainer', 'workout', 'student', 'employee', 'customer']) {
      expect(text, `dashboard mentions "${word}"`).not.toContain(word)
    }
  })

  it('discloses no credential or biometric material', async () => {
    signIn()
    renderDashboard()

    await screen.findByRole('region', { name: 'Platform totals' })
    const text = (document.body.textContent ?? '').toLowerCase()
    for (const forbidden of ['api_key', 'atd_', 'ats_', 'password_hash', 'token_hash', 'fingerprint', 'template']) {
      expect(text).not.toContain(forbidden)
    }
  })
})
