import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../../api/csrf'
import type { Role, Session } from '../../api/types'
import { keys } from '../../data/keys'
import { makeSession, makeSite, makeTerminal, SITE_A, SITE_B } from '../../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../../test/render'
import { failNext, resetServerState, resetTerminalModes, seed, state } from '../../test/server'
import { TerminalDetailPage } from './TerminalDetailPage'
import { TerminalsListPage } from './TerminalsListPage'
import { filterTerminals, presentStatuses, readHealth } from './health'

/**
 * The Terminals module.
 *
 * Terminals are where the console meets physical hardware, so most of what is
 * asserted below is about not misleading somebody standing in front of a door:
 * a status the server owns, a failure that does not read as an empty fleet, and
 * an application assignment shown honestly even when it resolves to nothing.
 */

const SITES = [
  makeSite({ id: SITE_A.site_id, name: SITE_A.site_name }),
  makeSite({ id: SITE_B.site_id, name: SITE_B.site_name }),
]

const FLEET = [
  makeTerminal({
    serial_number: 'AT-0001',
    device_name: 'North Gate',
    site_public_id: SITE_A.site_id,
    site_name: SITE_A.site_name,
    status: 'ONLINE',
  }),
  makeTerminal({
    id: 2,
    public_id: 'terminal-public-2',
    serial_number: 'AT-0002',
    device_name: 'Loading Bay',
    site_public_id: SITE_B.site_id,
    site_name: SITE_B.site_name,
    status: 'OFFLINE',
    firmware_version: '1.1.0',
    current_firmware_version: '1.2.0',
    firmware_outdated: true,
  }),
  makeTerminal({
    id: 3,
    public_id: 'terminal-public-3',
    serial_number: 'AT-0003',
    device_name: 'Reception',
    site_public_id: SITE_A.site_id,
    site_name: SITE_A.site_name,
    status: 'ERROR',
    last_heartbeat_at: undefined,
  }),
]

function signIn(role: Role = 'ADMIN', overrides: Partial<Session> = {}) {
  const session = makeSession({
    role,
    operator: { id: 'operator-1', email: 'ops@example.com', full_name: 'Ops', role },
    ...overrides,
  })
  resetServerState(session)
  resetTerminalModes()
  setCsrfToken(session.csrf_token)
  seed({ sites: SITES, terminals: FLEET })
  return session
}

function renderTerminals(initialPath = '/terminals', client = makeTestQueryClient()) {
  const router = createMemoryRouter(
    [
      { path: '/terminals', element: <TerminalsListPage /> },
      { path: '/terminals/:serial', element: <TerminalDetailPage /> },
      { path: '/sites/:siteId', element: <p>Site page</p> },
    ],
    { initialEntries: [initialPath] },
  )
  return renderWithSession(<RouterProvider router={router} />, client)
}

beforeEach(() => setCsrfToken(null))

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

describe('health reading', () => {
  it('takes the server’s status as authoritative', () => {
    // The offline sweep is configured per deployment and is not visible here.
    // A console that invented its own cutoff would eventually contradict the
    // platform, and an operator seeing two answers cannot tell which is right.
    const health = readHealth(makeTerminal({ status: 'OFFLINE' }))
    expect(health.status).toBe('OFFLINE')
    expect(health.tone).toBe('warning')
  })

  it('treats OFFLINE as a warning and ERROR as the state needing a person', () => {
    expect(readHealth(makeTerminal({ status: 'OFFLINE' })).tone).toBe('warning')
    expect(readHealth(makeTerminal({ status: 'ERROR' })).tone).toBe('danger')
    expect(readHealth(makeTerminal({ status: 'ONLINE' })).tone).toBe('positive')
  })

  it('flags the contradiction of "online but never reported"', () => {
    const health = readHealth(
      makeTerminal({ status: 'ONLINE', last_heartbeat_at: undefined }),
    )
    expect(health.neverReported).toBe(true)
    expect(health.note).toMatch(/never sent a heartbeat/)
  })

  it('renders an unknown status rather than hiding it', () => {
    // Firmware may report a state this build predates.
    const health = readHealth(makeTerminal({ status: 'REBOOTING' }))
    expect(health.status).toBe('REBOOTING')
    expect(health.tone).toBe('neutral')
  })

  it('measures heartbeat age without deciding what it means', () => {
    const now = new Date('2026-08-14T12:00:00Z')
    const health = readHealth(
      makeTerminal({ last_heartbeat_at: '2026-08-14T11:58:00Z' }),
      now,
    )
    expect(health.heartbeatAgeSeconds).toBe(120)
  })
})

describe('filtering', () => {
  it('matches serial, name or site, case-insensitively', () => {
    expect(filterTerminals(FLEET, { search: 'at-0002' })).toHaveLength(1)
    expect(filterTerminals(FLEET, { search: 'reception' })).toHaveLength(1)
    expect(filterTerminals(FLEET, { search: 'lagos' })).toHaveLength(2)
  })

  it('filters by site on the PUBLIC id, never the name', () => {
    // Names are editable and not unique; a name match would quietly include
    // another site's hardware.
    expect(filterTerminals(FLEET, { siteId: SITE_A.site_id })).toHaveLength(2)
    expect(filterTerminals(FLEET, { siteId: SITE_B.site_id })).toHaveLength(1)
  })

  it('filters by status and by outdated firmware', () => {
    expect(filterTerminals(FLEET, { status: 'ERROR' })).toHaveLength(1)
    expect(filterTerminals(FLEET, { outdatedOnly: true })).toHaveLength(1)
  })

  it('combines filters', () => {
    expect(
      filterTerminals(FLEET, { siteId: SITE_A.site_id, status: 'ONLINE' }),
    ).toHaveLength(1)
    expect(
      filterTerminals(FLEET, { siteId: SITE_B.site_id, status: 'ONLINE' }),
    ).toHaveLength(0)
  })

  it('reports only the statuses actually present', () => {
    expect(presentStatuses(FLEET)).toEqual(['ERROR', 'OFFLINE', 'ONLINE'])
  })
})

// ---------------------------------------------------------------------------
// Inventory
// ---------------------------------------------------------------------------

describe('terminal inventory', () => {
  it('lists every terminal with its status, site and firmware', async () => {
    signIn()
    renderTerminals()

    expect(await screen.findByText('AT-0001')).toBeInTheDocument()

    const bay = screen.getByText('AT-0002').closest('tr') as HTMLElement
    expect(within(bay).getByText('Loading Bay')).toBeInTheDocument()
    expect(within(bay).getByText(SITE_B.site_name)).toBeInTheDocument()
    expect(within(bay).getByText('Offline')).toBeInTheDocument()
    expect(within(bay).getByText('Outdated')).toBeInTheDocument()
  })

  it('shows fleet health from the summary endpoint', async () => {
    signIn()
    renderTerminals()

    const tiles = await screen.findByRole('region', { name: 'Fleet health' })
    expect(within(tiles).getByText('Total')).toBeInTheDocument()
    // Three terminals: one online, one offline, one error, one outdated.
    await waitFor(() => expect(within(tiles).getAllByText('3').length).toBeGreaterThan(0))
  })

  it('says "Never" rather than inventing a time for a terminal that never reported', async () => {
    signIn()
    renderTerminals()

    const reception = (await screen.findByText('AT-0003')).closest('tr') as HTMLElement
    expect(within(reception).getByText('Never')).toBeInTheDocument()
  })

  it('narrows by search, and says how many of how many', async () => {
    const user = userEvent.setup()
    signIn()
    renderTerminals()

    await screen.findByText('AT-0001')
    await user.type(screen.getByLabelText('Search terminals'), 'reception')

    await waitFor(() => expect(screen.queryByText('AT-0001')).not.toBeInTheDocument())
    expect(screen.getByText('AT-0003')).toBeInTheDocument()
    expect(screen.getByText('Showing 1 of 3 terminals')).toBeInTheDocument()
  })

  it('narrows by site and by status', async () => {
    const user = userEvent.setup()
    signIn()
    renderTerminals()

    await screen.findByText('AT-0001')
    await user.selectOptions(screen.getByLabelText('Site'), SITE_B.site_id)

    await waitFor(() => expect(screen.queryByText('AT-0001')).not.toBeInTheDocument())
    expect(screen.getByText('AT-0002')).toBeInTheDocument()

    await user.selectOptions(screen.getByLabelText('Site'), 'ALL')
    await user.selectOptions(screen.getByLabelText('Status'), 'ERROR')
    await waitFor(() => expect(screen.getByText('AT-0003')).toBeInTheDocument())
    expect(screen.queryByText('AT-0002')).not.toBeInTheDocument()
  })

  it('distinguishes "no matches" from "no terminals"', async () => {
    // Conflating them would tell a company with hardware that it has none.
    const user = userEvent.setup()
    signIn()
    renderTerminals()

    await screen.findByText('AT-0001')
    await user.type(screen.getByLabelText('Search terminals'), 'nothing matches this')

    expect(await screen.findByText('No terminals match those filters')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Clear filters' }))
    await waitFor(() => expect(screen.getByText('AT-0001')).toBeInTheDocument())
  })

  it('shows a useful empty state for a company with no terminals', async () => {
    signIn()
    seed({ sites: SITES, terminals: [] })
    renderTerminals()

    expect(await screen.findByText('No terminals yet')).toBeInTheDocument()
    expect(screen.getByText(/registered against one of your sites/)).toBeInTheDocument()
  })

  it('reports a failed load as an error, not as an empty fleet', async () => {
    signIn()
    failNext('terminals-list', 500)
    renderTerminals()

    expect(await screen.findByRole('alert')).toHaveTextContent(/Failed to retrieve terminals/)
    expect(screen.queryByText('No terminals yet')).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Site scoping and tenancy
// ---------------------------------------------------------------------------

describe('site scoping and company isolation', () => {
  it('shows a scoped operator only their granted sites’ terminals', async () => {
    // Narrowed by the API. The console renders what came back rather than
    // filtering a fuller list, which would mean the browser had held hardware
    // the operator is not entitled to see.
    signIn('MANAGER', { all_sites: false, sites: [SITE_A] })
    renderTerminals()

    expect(await screen.findByText('AT-0001')).toBeInTheDocument()
    expect(screen.getByText('AT-0003')).toBeInTheDocument()
    expect(screen.queryByText('AT-0002')).not.toBeInTheDocument()
  })

  it('explains a 403 on an ungranted site’s terminal as a scope problem', async () => {
    signIn('MANAGER', { all_sites: false, sites: [SITE_A] })
    renderTerminals('/terminals/AT-0002')

    expect(await screen.findByText('Not one of your sites')).toBeInTheDocument()
    expect(screen.getByText(/not scoped to/)).toBeInTheDocument()
  })

  it('treats an unknown or another company’s serial as not found', async () => {
    // The API answers 404 for both, deliberately, so it never confirms that a
    // serial is registered to someone else. The console does not embellish it.
    signIn()
    renderTerminals('/terminals/AT-NOPE')

    expect(await screen.findByText('Terminal not found')).toBeInTheDocument()
    expect(screen.getByText(/registered to your company/)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Detail
// ---------------------------------------------------------------------------

describe('terminal detail', () => {
  it('shows health, firmware and the site it stands at', async () => {
    signIn()
    renderTerminals('/terminals/AT-0002')

    await screen.findByRole('heading', { name: 'Loading Bay', level: 1 })
    expect(screen.getByText('Offline')).toBeInTheDocument()
    expect(screen.getByText('1.1.0')).toBeInTheDocument()
    expect(screen.getByText('1.2.0')).toBeInTheDocument()
    expect(screen.getByText('Behind the current build')).toBeInTheDocument()
    // Linked to the site by public id. The page now links to the site from more
    // than one place — the lead, the outage card and the revoke description all
    // point at it — so this asserts every link agrees rather than picking one.
    const siteLinks = screen.getAllByRole('link', { name: SITE_B.site_name })
    expect(siteLinks.length).toBeGreaterThan(0)
    for (const link of siteLinks) {
      expect(link).toHaveAttribute('href', `/sites/${SITE_B.site_id}`)
    }
  })

  it('says a terminal behind the current build is about to UPDATE ITSELF', async () => {
    // This note used to say AccessLink does not push firmware over the air, so
    // an operator reading it would have gone and scheduled a site visit for a
    // terminal that was going to flash itself on its next heartbeat.
    signIn()
    renderTerminals('/terminals/AT-0002')

    await screen.findByText('Behind the current build')
    expect(screen.getByText(/offer that build on its next heartbeat/i)).toBeInTheDocument()
    expect(screen.queryByText(/does not push firmware over the air/i)).not.toBeInTheDocument()
  })

  it('shows what this terminal does during an outage, read from its site', async () => {
    // The question an operator looking at an OFFLINE terminal is one step from
    // asking, and the one where a plausible default would describe a door rather
    // than a record. Read from the site, which is where the platform keeps it and
    // what it actually sends to the terminal.
    signIn()
    renderTerminals('/terminals/AT-0002')

    await screen.findByRole('heading', { name: 'Loading Bay', level: 1 })
    expect(screen.getByText('During an outage')).toBeInTheDocument()
    await waitFor(() =>
      expect(screen.getByText('Keep working for a limited time')).toBeInTheDocument(),
    )
    expect(screen.getByText(/For 12 hours, then it refuses everybody/)).toBeInTheDocument()
  })

  it('surfaces the health note for a terminal that never reported', async () => {
    signIn()
    renderTerminals('/terminals/AT-0003')

    expect(await screen.findByText(/never sent a heartbeat/)).toBeInTheDocument()
  })

  it('still names the one thing the console cannot do: first registration', async () => {
    // This page used to say that removal, reassignment and forced resync were
    // all unavailable, which was true until the lifecycle routes landed
    // (SEC-01) and is now offered in the Lifecycle section.
    //
    // WHAT THE CONSOLE CAN DO CHANGED AGAIN when claim codes landed. Registration
    // still happens on the device, but the console is no longer a bystander: it
    // issues the one-time code for that serial, so the site provisioning key —
    // which a browser must never hold — does not have to be handed to an
    // installer either. The old sentence sent operators to fetch the key.
    signIn()
    renderTerminals('/terminals/AT-0001')

    expect(await screen.findByRole('heading', { name: 'Lifecycle' })).toBeInTheDocument()
    expect(screen.getByText(/Registration itself happens on the device/)).toBeInTheDocument()
    expect(
      screen.getByText(/provisioning key does not need to leave the platform/i),
    ).toBeInTheDocument()
  })

  it('exposes no credential material anywhere on the page', async () => {
    signIn()
    renderTerminals('/terminals/AT-0001')

    await screen.findByRole('heading', { name: 'North Gate', level: 1 })
    const text = document.body.textContent ?? ''
    for (const forbidden of ['api_key', 'device_key', 'atd_', 'ats_', 'fingerprint']) {
      expect(text.toLowerCase()).not.toContain(forbidden)
    }
  })
})

// ---------------------------------------------------------------------------
// Application mode
// ---------------------------------------------------------------------------

describe('application mode', () => {
  it('shows the assignment and what it resolves to', async () => {
    signIn('ADMIN', { applications: [{ code: 'ATTENDANCE', settings: {} }] })
    renderTerminals('/terminals/AT-0001')

    await screen.findByRole('heading', { name: 'North Gate', level: 1 })
    expect(screen.getByText('Assigned mode')).toBeInTheDocument()
    expect(screen.getByText('Multi-purpose')).toBeInTheDocument()
    expect(screen.getByText('Attendance')).toBeInTheDocument()
  })

  it('says plainly when a terminal resolves to nothing', async () => {
    // A company with no applications enabled is a legitimate state, and a
    // multi-purpose terminal in it genuinely serves nothing.
    signIn('ADMIN', { applications: [] })
    renderTerminals('/terminals/AT-0001')

    expect(await screen.findByText('This terminal resolves to nothing')).toBeInTheDocument()
    expect(screen.getByText(/no applications enabled/)).toBeInTheDocument()
  })

  it('offers only capabilities the COMPANY has enabled, plus multi-purpose', async () => {
    const user = userEvent.setup()
    signIn('ADMIN', {
      applications: [
        { code: 'ATTENDANCE', settings: {} },
        { code: 'CHECK_IN', settings: {} },
      ],
    })
    renderTerminals('/terminals/AT-0001')

    await user.click(await screen.findByRole('button', { name: 'Change application mode' }))

    const select = screen.getByLabelText('Application mode')
    const values = within(select).getAllByRole('option').map((o) => (o as HTMLOptionElement).value)
    expect(values).toEqual(['MULTI_PURPOSE', 'ATTENDANCE', 'CHECK_IN'])
    // Not offered: a capability the platform has but this company has not enabled.
    expect(values).not.toContain('ACCESS_CONTROL')
  })

  it('assigns a mode and refreshes the detail without a stale read', async () => {
    const user = userEvent.setup()
    signIn('ADMIN', { applications: [{ code: 'ATTENDANCE', settings: {} }] })
    const client = makeTestQueryClient()
    renderTerminals('/terminals/AT-0001', client)

    await user.click(await screen.findByRole('button', { name: 'Change application mode' }))
    await user.selectOptions(screen.getByLabelText('Application mode'), 'ATTENDANCE')
    await user.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    )
    // The cache holds the response, not a stale copy.
    await waitFor(() =>
      expect(client.getQueryData(keys.terminals.detail('AT-0001'))).toMatchObject({
        application_mode: 'ATTENDANCE',
      }),
    )
    // And the list is invalidated, since it shows the mode too.
    expect(client.getQueryState(keys.terminals.list())?.isInvalidated ?? true).toBe(true)
  })

  it('reports a refused capability without closing the dialog', async () => {
    const user = userEvent.setup()
    // The company has ATTENDANCE enabled in the session, but the mock rejects
    // anything not enabled — so disable it server-side to force the 409 path.
    signIn('ADMIN', { applications: [{ code: 'ATTENDANCE', settings: {} }] })
    renderTerminals('/terminals/AT-0001')

    await user.click(await screen.findByRole('button', { name: 'Change application mode' }))
    await user.selectOptions(screen.getByLabelText('Application mode'), 'ATTENDANCE')

    // Take the capability away between opening the dialog and saving.
    if (state.session) state.session = { ...state.session, applications: [] }
    await user.click(screen.getByRole('button', { name: 'Save' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/not enabled for your company/)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Roles
// ---------------------------------------------------------------------------

describe('role restrictions', () => {
  it('offers configuration to a MANAGER and above', async () => {
    for (const role of ['MANAGER', 'ADMIN', 'OWNER'] as const) {
      signIn(role)
      const { unmount } = renderTerminals('/terminals/AT-0001')
      expect(
        await screen.findByRole('button', { name: 'Change application mode' }),
      ).toBeInTheDocument()
      unmount()
    }
  })

  it('withholds it from a VIEWER, who can still read everything', async () => {
    signIn('VIEWER')
    renderTerminals('/terminals/AT-0001')

    await screen.findByRole('heading', { name: 'North Gate', level: 1 })
    expect(
      screen.queryByRole('button', { name: 'Change application mode' }),
    ).not.toBeInTheDocument()
    // Reading is unaffected — the gate is on the write, as it is server-side.
    expect(screen.getByText('Assigned mode')).toBeInTheDocument()
  })
})
