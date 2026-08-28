import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../../api/csrf'
import type { Role, Session } from '../../api/types'
import { keys } from '../../data/keys'
import {
  makePendingTerminal,
  makeSession,
  makeSite,
  makeTerminal,
  SITE_A,
  SITE_B,
} from '../../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../../test/render'
import { expectNoDoorWording } from '../../test/vocabulary'
import {
  failNext,
  resetServerState,
  resetTerminalModes,
  seed,
  setTerminalMode,
  state,
} from '../../test/server'
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
    expect(health.note).toMatch(/never checked in/)
  })

  /*
    WHAT "REACHABLE" IS FOR, AND WHY IT IS NOT `status !== 'ONLINE'`.

    Every screen that needed "can the platform get a message to this terminal"
    was asking it that way, and it is false for four of the six states: a
    terminal reporting a fault, one installing an update, one deliberately
    disabled and one still provisioning are all in contact. The console told
    every one of their operators the terminal was offline.

    This is still the server's answer, not a locally invented one: it is read
    off the status the sweep sets and the heartbeat the device sends.
  */
  describe('reachability', () => {
    it('is false only for OFFLINE and for a terminal that never checked in', () => {
      expect(readHealth(makeTerminal({ status: 'OFFLINE' })).reachable).toBe(false)
      expect(
        readHealth(makeTerminal({ status: 'ONLINE', last_heartbeat_at: undefined }))
          .reachable,
      ).toBe(false)
    })

    it('is true for a terminal that is in contact and unwell', () => {
      // The four states the old test called offline. Each of them has checked
      // in; an operator sent to the site for any of them has been sent for
      // nothing.
      for (const status of ['ERROR', 'UPDATING', 'DISABLED', 'PROVISIONING'] as const) {
        expect(readHealth(makeTerminal({ status })).reachable).toBe(true)
      }
    })

    it('describes each state as what it is, never as "offline"', () => {
      expect(readHealth(makeTerminal({ status: 'ERROR' })).note).toMatch(
        /still checking in/,
      )
      expect(readHealth(makeTerminal({ status: 'UPDATING' })).note).toMatch(
        /installing a firmware update/,
      )
      expect(readHealth(makeTerminal({ status: 'DISABLED' })).note).toMatch(
        /will not let anybody in/,
      )
      expect(readHealth(makeTerminal({ status: 'OFFLINE' })).note).toMatch(
        /has not heard from this terminal/,
      )

      for (const status of ['ERROR', 'UPDATING', 'DISABLED', 'PROVISIONING'] as const) {
        expect(readHealth(makeTerminal({ status })).note).not.toMatch(/offline/i)
      }
    })

    it('tells a newly approved terminal apart from one that is stuck', () => {
      // PROVISIONING and no heartbeat is the NORMAL state seconds after
      // approval, and it is the one a customer watches. It must not read like a
      // fault.
      const fresh = readHealth(
        makeTerminal({ status: 'PROVISIONING', last_heartbeat_at: undefined }),
      )
      expect(fresh.note).toMatch(/finishes on its own/)
      expect(fresh.note).not.toMatch(/offline/i)
    })

    it('does not assert what an offline terminal does at the door', () => {
      // That is the site's offline policy, and it has its own card. A terminal
      // at a DENY_ALL site refuses everybody the moment it drops.
      const note = readHealth(makeTerminal({ status: 'OFFLINE' })).note
      expect(note).toMatch(/set by its site/)
      expect(note).not.toMatch(/keeps working/i)
    })
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

  /*
    THE ONLY WAY IN THAT IS NOT A MOUSE.

    DataTable's `onRowClick` is documented as a pointer convenience that must
    not be the only route to a row's destination, and the reason it carries no
    role or tabindex is that "every table using this renders a real link in its
    primary column". This one rendered a bare <code>, so tabbing through the
    fleet went from the toolbar back to the top of the page and a terminal could
    not be opened from a keyboard at all.

    Nothing automated caught it: axe has no rule for a keyboard path that was
    never built, and there is no ARIA here for it to contradict. So this is the
    check.
  */
  it('gives every row a real link, so a terminal can be opened without a mouse', async () => {
    signIn()
    renderTerminals()

    const row = (await screen.findByText('AT-0001')).closest('tr') as HTMLElement
    const link = within(row).getByRole('link', { name: 'AT-0001' })
    expect(link).toHaveAttribute('href', '/terminals/AT-0001')

    // Every row, not just the first: a fleet with one reachable terminal and
    // two unreachable ones would pass a spot check and strand the rest.
    const table = screen.getByRole('table')
    expect(within(table).getAllByRole('link')).toHaveLength(FLEET.length)
  })

  it('links a terminal by its serial even when it has no name yet', async () => {
    // The link is on the serial rather than the name because the name is
    // optional -- an unnamed terminal renders an em dash, and a link with no
    // accessible name is worse than no link.
    signIn()
    seed({ sites: SITES, terminals: [makeTerminal({ serial_number: 'AT-9', device_name: '' })] })
    renderTerminals()

    expect(await screen.findByRole('link', { name: 'AT-9' })).toHaveAttribute(
      'href',
      '/terminals/AT-9',
    )
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

    // THE COPY CHANGED WITH THE FLOW, and the old sentence is the interesting
    // half. It said registration "happens on the device, using the site's
    // provisioning key" — a credential that registers every terminal at a site
    // for ever, cannot be recovered, and is exactly what a customer must never
    // be handling. It also described a procedure that needs a serial cable.
    //
    // Both are now wrong as well as unsafe: a terminal announces itself and is
    // added with a code it displays on its own screen.
    expect(screen.getByText(/connect it to Wi-Fi from your phone/i)).toBeInTheDocument()
    expect(screen.queryByText(/provisioning key/i)).not.toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /add your first terminal/i }),
    ).toBeInTheDocument()
  })

  /*
    WHAT A NEW CUSTOMER MEETS, AND WHAT THEY USED TO.

    This screen opened with five tiles reading zero, a search box that can match
    nothing, two filters over an empty set and the line "0 terminals" -- and the
    one paragraph telling them what to do underneath all of it. Statistics about
    a fleet that does not exist are not information, and filters over nothing
    are not controls.

    Both come back the moment there is one terminal, which the test below this
    one holds.
  */
  it('puts onboarding first on an empty fleet, with no statistics or filters over nothing', async () => {
    signIn()
    seed({ sites: SITES, terminals: [] })
    renderTerminals()

    expect(await screen.findByText('No terminals yet')).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Fleet health' })).not.toBeInTheDocument()
    expect(screen.queryByLabelText('Search terminals')).not.toBeInTheDocument()
    expect(screen.queryByLabelText('Status')).not.toBeInTheDocument()
    expect(screen.queryByLabelText('Site')).not.toBeInTheDocument()
    expect(screen.queryByText('0 terminals')).not.toBeInTheDocument()
  })

  it('brings the statistics and filters back as soon as there is a fleet', async () => {
    signIn()
    renderTerminals()

    await screen.findByText('AT-0001')
    expect(screen.getByRole('region', { name: 'Fleet health' })).toBeInTheDocument()
    expect(screen.getByLabelText('Search terminals')).toBeInTheDocument()
  })

  it('reports a failed load as an error, not as an empty fleet', async () => {
    signIn()
    failNext('terminals-list', 500)
    renderTerminals()

    expect(await screen.findByRole('alert')).toHaveTextContent(/Failed to retrieve terminals/)
    expect(screen.queryByText('No terminals yet')).not.toBeInTheDocument()
    // A failed read is not an empty fleet, so the filters must NOT be
    // suppressed: the operator's own narrowing is still on screen to undo.
    expect(screen.getByLabelText('Search terminals')).toBeInTheDocument()
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

    expect(await screen.findByText(/never checked in/)).toBeInTheDocument()
  })

  /*
    THE THIRD ANSWER ABOUT FIRMWARE.

    The badge was a two-way branch on `firmware_outdated`, so a terminal that
    has never reported a version -- no heartbeat yet, or a build that predates
    version reporting -- rendered an em dash next to a green "Current". The
    console asserted a terminal was up to date while showing it had no idea what
    it was running. The fleet list was always honest about the same terminal,
    which is how the two screens came to disagree.
  */
  it('says firmware is "Not reported" rather than calling an unknown version current', async () => {
    signIn()
    seed({
      sites: SITES,
      terminals: [
        makeTerminal({
          serial_number: 'AT-NEW',
          device_name: 'Just approved',
          firmware_version: '',
          firmware_outdated: false,
          last_heartbeat_at: undefined,
          status: 'PROVISIONING',
        }),
      ],
    })
    renderTerminals('/terminals/AT-NEW')

    await screen.findByRole('heading', { name: 'Just approved', level: 1 })
    expect(screen.getByText('Not reported')).toBeInTheDocument()
    expect(screen.queryByText('Current')).not.toBeInTheDocument()
  })

  it('still says "Current" for a terminal that has reported an up-to-date build', async () => {
    signIn()
    renderTerminals('/terminals/AT-0001')

    await screen.findByRole('heading', { name: 'North Gate', level: 1 })
    expect(screen.getByText('Current')).toBeInTheDocument()
  })

  /*
    NOT OFFLINE, AND THE CONSOLE MUST STOP SAYING SO.

    The Network panel's warning was gated on `status !== 'ONLINE'`, so it
    appeared over four states it is false for. The worst of them is an ERROR
    terminal that checked in minutes ago: the page showed the check-in on a card
    and then, two hundred pixels below, told the operator the unit was
    unreachable. One of those sends somebody to the site for nothing.
  */
  it('does not call a terminal offline when it is in contact and reporting a fault', async () => {
    signIn()
    seed({
      sites: SITES,
      terminals: [
        makeTerminal({
          serial_number: 'AT-ERR',
          device_name: 'Side Door',
          status: 'ERROR',
          last_heartbeat_at: '2026-08-15T09:00:00Z',
        }),
      ],
    })
    renderTerminals('/terminals/AT-ERR')

    const network = await screen.findByRole('region', { name: 'Network' })
    expect(within(network).queryByText(/The terminal is offline/i)).not.toBeInTheDocument()
    expect(
      within(network).queryByText(/cannot be reached right now/i),
    ).not.toBeInTheDocument()
    expect(screen.getByText(/still checking in/)).toBeInTheDocument()
  })

  it('says a terminal mid-update is installing, not that it is unreachable', async () => {
    signIn()
    seed({
      sites: SITES,
      terminals: [
        makeTerminal({
          serial_number: 'AT-UPD',
          device_name: 'Back Door',
          status: 'UPDATING',
          last_heartbeat_at: '2026-08-15T09:00:00Z',
        }),
      ],
    })
    renderTerminals('/terminals/AT-UPD')

    const network = await screen.findByRole('region', { name: 'Network' })
    expect(within(network).queryByText(/The terminal is offline/i)).not.toBeInTheDocument()
    expect(within(network).getByText(/writing a new build to itself/i)).toBeInTheDocument()
  })

  it('still warns, in the same words, for a terminal that really is offline', async () => {
    // The correction narrows the claim; it does not withdraw it. OFFLINE is the
    // state the warning was written for and it reads exactly as it did.
    signIn()
    renderTerminals('/terminals/AT-0002')

    const network = await screen.findByRole('region', { name: 'Network' })
    expect(within(network).getByText(/The terminal is offline/i)).toBeInTheDocument()
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
// Terminals waiting to be set up
// ---------------------------------------------------------------------------

/*
  P0-4. THE 403 LOOP NOBODY COULD SEE.

  `GET /console/terminal-announcements` is MANAGER on the server. This panel
  called it with no gate and the hook polls every ten seconds, so a VIEWER
  opening the fleet page took a 403 on load and another six times a minute for as
  long as the tab stayed open — for a list they were never going to be shown.
  The panel renders null when the list is empty, so nothing appeared on screen
  and nothing reported it.

  The overview had already hit this and gated the same hook on the same named
  action. This page did not inherit the fix.
*/
describe('the waiting-to-be-set-up panel', () => {
  it('makes NO request to the manager-only endpoint as a VIEWER', async () => {
    signIn('VIEWER')
    seed({ pendingTerminals: [makePendingTerminal()] })
    renderTerminals('/terminals')

    // Wait for the page itself to have settled, so this is "the viewer's whole
    // page load" rather than "we looked before anything happened".
    await screen.findByRole('heading', { name: 'Terminals', level: 1 })
    await screen.findByText('AT-0001')

    const asked = state.requests.filter((request) =>
      request.url.includes('/console/terminal-announcements'),
    )
    expect(asked).toHaveLength(0)
  })

  it('shows a viewer no broken or empty panel where it would have been', async () => {
    signIn('VIEWER')
    seed({ pendingTerminals: [makePendingTerminal()] })
    renderTerminals('/terminals')

    await screen.findByRole('heading', { name: 'Terminals', level: 1 })
    expect(
      screen.queryByRole('heading', { name: 'Waiting to be set up' }),
    ).not.toBeInTheDocument()
    // And no error surfaced in its place either.
    expect(screen.queryByText(/forbidden/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/something went wrong/i)).not.toBeInTheDocument()
  })

  /*
    THE OTHER HALF OF THE GATE, and the reason it is `viewPendingTerminals`
    rather than `addTerminals`: the server splits seeing from approving on
    purpose, because the person who unpacked the box is often not an
    administrator. A manager must still see the list — and be told who can act
    on it.
  */
  it('still shows a MANAGER the list, without the buttons', async () => {
    signIn('MANAGER')
    seed({ pendingTerminals: [makePendingTerminal()] })
    renderTerminals('/terminals')

    await screen.findByRole('heading', { name: 'Waiting to be set up' })
    expect(screen.getByText(/waiting for an administrator to approve/i)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument()

    // The request WAS made, which is the point of the split.
    expect(
      state.requests.filter((request) =>
        request.url.includes('/console/terminal-announcements'),
      ).length,
    ).toBeGreaterThan(0)
  })

  it('gives an ADMIN the list and the actions, unchanged', async () => {
    signIn('ADMIN')
    seed({ pendingTerminals: [makePendingTerminal()] })
    renderTerminals('/terminals')

    await screen.findByRole('heading', { name: 'Waiting to be set up' })
    expect(screen.getByText(/waiting to be approved/i)).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Approve' })).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Application mode
// ---------------------------------------------------------------------------

describe('application mode', () => {
  it('shows the assignment and what it is currently serving', async () => {
    signIn('ADMIN', { applications: [{ code: 'ATTENDANCE', settings: {} }] })
    renderTerminals('/terminals/AT-0001')

    await screen.findByRole('heading', { name: 'North Gate', level: 1 })
    expect(screen.getByText('Assigned to')).toBeInTheDocument()
    expect(screen.getByText('Currently serving')).toBeInTheDocument()
    expect(screen.getByText('Multi-purpose')).toBeInTheDocument()
    expect(screen.getByText('Attendance')).toBeInTheDocument()
  })

  /*
    P0-2. THE FIRST TERMINAL OF EVERY NEW CUSTOMER LANDS HERE.

    Signup enables no features and every terminal defaults to multi-purpose, so
    this combination — multi-purpose, nothing enabled — is what a customer meets
    the moment they finish pairing their first unit.

    IT WORKS. `database/authorization.go` clears the application for a
    multi-purpose terminal and SKIPS the capability gate entirely, so people are
    admitted normally. The page used to say "This terminal resolves to nothing"
    in warning styling, which told a customer standing at working hardware that
    it was inert.

    The old test asserted that wording and its comment asserted the false claim
    behind it ("genuinely serves nothing"). Both are replaced rather than
    deleted: the state still has to be explained, it just has to be explained
    truthfully.
  */
  it('does NOT tell a new customer their multi-purpose terminal is broken', async () => {
    signIn('ADMIN', { applications: [] })
    renderTerminals('/terminals/AT-0001')

    await screen.findByRole('heading', { name: 'North Gate', level: 1 })

    // The claim that was false.
    expect(screen.queryByText(/resolves to nothing/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/nothing for it to serve/i)).not.toBeInTheDocument()
    // And no warning styling anywhere in this section.
    const section = screen.getByRole('region', { name: 'What this terminal does' })
    expect(section.querySelector('.notice--warning')).toBeNull()
  })

  it('explains multi-purpose in plain language instead', async () => {
    signIn('ADMIN', { applications: [] })
    renderTerminals('/terminals/AT-0001')

    expect(await screen.findByText('Multi-purpose, which is ready to use')).toBeInTheDocument()
    expect(screen.getByText(/serves whatever your company turns on/i)).toBeInTheDocument()
    // The reassurance that matters: having no features on is not a fault.
    expect(screen.getByText(/does not stop this terminal working/i)).toBeInTheDocument()
    // "Currently serving" must not read as "nothing".
    expect(screen.getByText('Anything your company turns on')).toBeInTheDocument()
    expect(screen.queryByText('Nothing')).not.toBeInTheDocument()
  })

  /*
    THE OTHER WAY `effective_applications` GOES EMPTY, and the one that IS a
    fault. A terminal assigned to a feature the company has since turned off
    meets the capability gate and is refused with APPLICATION_NOT_ENABLED —
    nobody gets in. Collapsing this with multi-purpose is what produced the bug
    in the first place, so the two are asserted apart.
  */
  it('STILL WARNS when a terminal is assigned to a feature that is now off', async () => {
    signIn('ADMIN', { applications: [] })
    setTerminalMode('AT-0001', 'ATTENDANCE')
    renderTerminals('/terminals/AT-0001')

    expect(
      await screen.findByText('This terminal is not letting anyone in'),
    ).toBeInTheDocument()
    expect(screen.getByText(/people are refused at it/i)).toBeInTheDocument()
    // And it says what to do about it, both ways round.
    expect(screen.getByText(/switched back on/i)).toBeInTheDocument()
  })

  it('shows a feature-specific terminal as serving that feature', async () => {
    signIn('ADMIN', { applications: [{ code: 'ATTENDANCE', settings: {} }] })
    setTerminalMode('AT-0001', 'ATTENDANCE')
    renderTerminals('/terminals/AT-0001')

    await screen.findByRole('heading', { name: 'North Gate', level: 1 })
    const section = screen.getByRole('region', { name: 'What this terminal does' })
    expect(within(section).getAllByText('Attendance').length).toBe(2)
    // Neither notice applies: it is doing exactly what it was set up to do.
    expect(section.querySelector('.notice')).toBeNull()
  })

  it('leaves an OFFLINE terminal reading as multi-purpose, not as misconfigured', async () => {
    // Being unreachable is a health state with its own reporting further up the
    // page. It must not also make the feature section claim the terminal is set
    // up wrong — two unrelated problems reported as one is how an operator ends
    // up changing a setting to fix a network fault.
    signIn('ADMIN', { applications: [] })
    renderTerminals('/terminals/AT-0002')

    await screen.findByRole('heading', { name: 'Loading Bay', level: 1 })
    const section = screen.getByRole('region', { name: 'What this terminal does' })
    expect(section.querySelector('.notice--warning')).toBeNull()
    expect(within(section).getByText('Multi-purpose')).toBeInTheDocument()
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

    await user.click(await screen.findByRole('button', { name: 'Change feature' }))

    const select = screen.getByLabelText('Feature')
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

    await user.click(await screen.findByRole('button', { name: 'Change feature' }))
    await user.selectOptions(screen.getByLabelText('Feature'), 'ATTENDANCE')
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

    await user.click(await screen.findByRole('button', { name: 'Change feature' }))
    await user.selectOptions(screen.getByLabelText('Feature'), 'ATTENDANCE')

    // Take the capability away between opening the dialog and saving.
    if (state.session) state.session = { ...state.session, applications: [] }
    await user.click(screen.getByRole('button', { name: 'Save' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/not turned on for your company/)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Vocabulary
// ---------------------------------------------------------------------------

/*
  WHAT THE TERMINAL CONTROLS IS AN ACCESS POINT.

  This is the screen where the word was hardest to avoid, because everything on
  it is about a physical unit attached to a physical thing. It is also the screen
  where naming that thing a door is most costly: the reader is deciding where to
  mount hardware, and a warehouse mounting to a barrier or a school mounting to a
  locker is being told, on the page that describes their own equipment, that the
  product means something else.

  The fleet used here is named North Gate / Loading Bay / Reception -- deliberately,
  since a customer is free to call their own terminal "Front Door" and the rule
  is about OUR words, not theirs.
*/
describe('access-point vocabulary', () => {
  it('describes the fleet without calling an access point a door', async () => {
    signIn()
    renderTerminals()

    await screen.findByRole('heading', { name: 'Terminals', level: 1 })
    await screen.findByText('North Gate')
    expectNoDoorWording('The terminals list', document.body.textContent ?? '')
  })

  it('describes one terminal, and what it controls, without saying door', async () => {
    signIn()
    renderTerminals('/terminals/AT-0001')

    await screen.findByRole('heading', { name: 'North Gate', level: 1 })
    // The lifecycle copy that used to read "Records nothing and moves no door".
    expect(
      screen.getByText(/Records nothing and changes nothing at the access point/),
    ).toBeInTheDocument()
    expectNoDoorWording('The terminal detail page', document.body.textContent ?? '')
  })

  it('previews a decision without saying a door moves or a door event is written', async () => {
    const user = userEvent.setup()
    signIn()
    renderTerminals('/terminals/AT-0001')

    await screen.findByRole('heading', { name: 'North Gate', level: 1 })
    await user.click(screen.getByRole('button', { name: 'Check access' }))

    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByText(/Nothing is recorded and nothing happens at the access point/),
    ).toBeInTheDocument()
    expect(
      within(dialog).getByText(/This preview does not write an access event/),
    ).toBeInTheDocument()
    expectNoDoorWording('The check-access preview', dialog.textContent ?? '')
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
        await screen.findByRole('button', { name: 'Change feature' }),
      ).toBeInTheDocument()
      unmount()
    }
  })

  it('withholds it from a VIEWER, who can still read everything', async () => {
    signIn('VIEWER')
    renderTerminals('/terminals/AT-0001')

    await screen.findByRole('heading', { name: 'North Gate', level: 1 })
    expect(
      screen.queryByRole('button', { name: 'Change feature' }),
    ).not.toBeInTheDocument()
    // Reading is unaffected — the gate is on the write, as it is server-side.
    expect(screen.getByText('Assigned to')).toBeInTheDocument()
  })
})
