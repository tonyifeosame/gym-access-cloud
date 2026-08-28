import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../api/csrf'
import type { Role, Session } from '../api/types'
import { RequireAuth } from '../auth/guards'
import { SiteProvider } from '../context/SiteContext'
import {
  makeAuditRecord,
  makeEvent,
  makePendingTerminal,
  makePermission,
  makePerson,
  makeSession,
  makeSite,
  makeTerminal,
  SITE_A,
  SITE_B,
} from '../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../test/render'
import { expectNoDoorWording } from '../test/vocabulary'
import { failNext, resetServerState, seed, state } from '../test/server'
import { DashboardPage, collectAttention, countFleet } from './DashboardPage'
import {
  decisionSegments,
  firmwareCompliance,
  fleetSegments,
  siteDistribution,
  startOfLocalDay,
} from './dashboard/overview'

/**
 * The overview.
 *
 * The property under test throughout is that NOTHING IS FABRICATED. Every figure
 * traces to something the API computed or to exact arithmetic over a set the API
 * returned complete, every gap is stated rather than filled, and the page never
 * implies that an enabled capability is doing anything.
 *
 * The second property, added with the redesign: THE PAGE ASKS FOR NOTHING IT MAY
 * NOT HAVE. Two of its regions sit behind a role on the server, and a console
 * that calls them anyway does not degrade gracefully — it produces a 403 on
 * every load, and in one case on a ten-second timer for as long as the tab is
 * open.
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

/*
  THE DAY'S EVENTS, STAMPED RELATIVE TO THE RUNNING CLOCK.

  The page asks for events since the READER'S midnight, so a fixture with a
  hard-coded date is either always inside the window or always outside it
  depending on when the suite runs — which is the same thing as not testing the
  boundary at all. These are built from today and yesterday so the split is real
  whenever this runs.
*/
const NOW = new Date()

function todayAt(hour: number): string {
  return new Date(NOW.getFullYear(), NOW.getMonth(), NOW.getDate(), hour, 0, 0).toISOString()
}

function yesterdayAt(hour: number): string {
  return new Date(NOW.getFullYear(), NOW.getMonth(), NOW.getDate() - 1, hour, 0, 0).toISOString()
}

/** Six today — three granted, two denied, one recorded — and two yesterday. */
const EVENTS = [
  makeEvent({
    id: 'e-1',
    decision: 'GRANTED',
    event_type: 'ACCESS_GRANTED',
    reason: 'ALLOWED',
    person_name: 'Ada Okonkwo',
    subject_external_id: 'P-0001',
    site_name: SITE_A.site_name,
    occurred_at: todayAt(11),
    recorded_at: todayAt(11),
  }),
  makeEvent({
    id: 'e-2',
    decision: 'DENIED',
    reason: 'OUTSIDE_SCHEDULE',
    person_name: 'Tunde Bakare',
    subject_external_id: 'P-0002',
    site_name: SITE_A.site_name,
    occurred_at: todayAt(10),
    recorded_at: todayAt(10),
  }),
  makeEvent({
    id: 'e-3',
    decision: 'DENIED',
    reason: 'NO_PERMISSION',
    person_name: undefined,
    subject_external_id: 'UNKNOWN-CARD-99',
    site_name: SITE_B.site_name,
    occurred_at: todayAt(9),
    recorded_at: todayAt(9),
  }),
  makeEvent({
    id: 'e-4',
    decision: 'GRANTED',
    event_type: 'ACCESS_GRANTED',
    person_name: 'Ada Okonkwo',
    subject_external_id: 'P-0001',
    site_name: SITE_A.site_name,
    occurred_at: todayAt(8),
    recorded_at: todayAt(8),
  }),
  makeEvent({
    id: 'e-5',
    decision: 'GRANTED',
    event_type: 'ACCESS_GRANTED',
    person_name: 'Ngozi Eze',
    subject_external_id: 'P-0003',
    site_name: SITE_B.site_name,
    occurred_at: todayAt(7),
    recorded_at: todayAt(7),
  }),
  makeEvent({
    id: 'e-6',
    decision: 'RECORDED',
    event_type: 'PRESENCE',
    person_name: 'Ngozi Eze',
    subject_external_id: 'P-0003',
    site_name: SITE_A.site_name,
    occurred_at: todayAt(6),
    recorded_at: todayAt(6),
  }),
  makeEvent({
    id: 'e-7',
    decision: 'GRANTED',
    event_type: 'ACCESS_GRANTED',
    person_name: 'Bisi Adewale',
    subject_external_id: 'P-0004',
    site_name: SITE_A.site_name,
    occurred_at: yesterdayAt(15),
    recorded_at: yesterdayAt(15),
  }),
  makeEvent({
    id: 'e-8',
    decision: 'DENIED',
    person_name: 'Chidi Nwosu',
    subject_external_id: 'P-0005',
    site_name: SITE_A.site_name,
    occurred_at: yesterdayAt(14),
    recorded_at: yesterdayAt(14),
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
    events: EVENTS,
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
      { path: '/sites/:siteId', element: <p>One site</p> },
      { path: '/people', element: <p>People</p> },
      { path: '/people/:externalId', element: <p>One person</p> },
      { path: '/events', element: <p>Events</p> },
      { path: '/activity', element: <p>Activity</p> },
      { path: '/settings/firmware', element: <p>Firmware</p> },
      { path: '/settings/applications', element: <p>Applications</p> },
    ],
    { initialEntries: ['/'] },
  )
  return renderWithSession(<RouterProvider router={router} />, client)
}

/** The tile carrying a given label, so a value is never matched page-wide. */
function tile(label: string): HTMLElement {
  const tiles = screen.getByRole('region', { name: 'Platform totals' })
  return within(tiles).getByText(label).closest('a, article') as HTMLElement
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

  it('counts a site-scoped fleet larger than one page in full', async () => {
    /*
      THE ONE PLACE THIS PAGE COUNTS TERMINALS ITSELF.

      Scope-wide, the tile shows the server's own rollup and cannot be wrong.
      Narrowed to one site it counts the terminal list in the browser -- and
      since D2 that list arrives from a PAGED endpoint whose default is fifty.
      A fleet of 137 reading as 50 is the failure this guards: not an error, not
      an empty state, just a wrong number that looks entirely reasonable.

      137 is deliberately larger than one default page and not a multiple of it.
    */
    signIn('MANAGER', { all_sites: false, sites: [SITE_A] })
    seed({
      terminals: Array.from({ length: 137 }, (_, index) =>
        makeTerminal({
          id: index + 1,
          public_id: `terminal-public-${index + 1}`,
          serial_number: `AT-${String(index + 1).padStart(4, '0')}`,
          site_public_id: SITE_A.site_id,
          site_name: SITE_A.site_name,
          status: index % 2 === 0 ? 'ONLINE' : 'OFFLINE',
        }),
      ),
    })
    renderDashboard()

    await screen.findByRole('region', { name: 'Platform totals' })
    // "Terminals here", not "Terminals": the tile renames itself when the view
    // is narrowed to one site, which is exactly the case being tested.
    await waitFor(() =>
      expect(within(tile('Terminals here')).getByText('137')).toBeInTheDocument(),
    )
    // 69 of the 137, so the breakdown is a second, independent witness that the
    // whole fleet was counted rather than a page of it.
    expect(within(tile('Terminals here')).getByText(/69 online/)).toBeInTheDocument()
  })
})

describe('derivations behind the meters', () => {
  it('keeps zero-valued fleet states in the legend rather than dropping them', () => {
    // A legend whose rows appear and disappear as the fleet changes is one an
    // operator cannot learn the shape of, and "Reporting an error: 0" is a fact
    // worth reading.
    const segments = fleetSegments(countFleet(FLEET))
    expect(segments.map((segment) => segment.id)).toEqual([
      'online',
      'offline',
      'error',
      'updating',
      'provisioning',
      'disabled',
    ])
    expect(segments.find((segment) => segment.id === 'disabled')?.value).toBe(0)
  })

  it('tones offline as a warning and only a fault as danger', () => {
    // Overstating offline trains operators to ignore the colour that matters,
    // and the events page and the badges already agree on this.
    const segments = fleetSegments(countFleet(FLEET))
    expect(segments.find((s) => s.id === 'offline')?.tone).toBe('warning')
    expect(segments.find((s) => s.id === 'error')?.tone).toBe('danger')
  })

  it('splits the fleet across sites from the terminal list alone', () => {
    const rows = siteDistribution(SITES, FLEET)

    expect(rows.map((row) => [row.name, row.total, row.online, row.offline])).toEqual([
      [SITE_A.site_name, 2, 1, 0],
      [SITE_B.site_name, 1, 0, 1],
    ])
    // Every row adds up to itself: the split and the total come from one read.
    for (const row of rows) {
      expect(row.online + row.offline + row.other).toBe(row.total)
    }
  })

  it('keeps a site with no terminals, sorted last', () => {
    const rows = siteDistribution(
      [...SITES, makeSite({ id: 'site-c', name: 'Kano Yard' })],
      FLEET,
    )
    expect(rows.at(-1)).toMatchObject({ name: 'Kano Yard', total: 0 })
  })

  it('matches a terminal to its site by public id, never by name', () => {
    const renamed = [makeSite({ id: SITE_A.site_id, name: 'Renamed since' })]
    const rows = siteDistribution(renamed, FLEET.slice(0, 1))
    expect(rows[0]).toMatchObject({ id: SITE_A.site_id, name: 'Renamed since', total: 1 })
  })

  it('reads firmware standing off the terminal list, target and all', () => {
    // The row already carries the build the server considers current for its
    // channel, so no catalogue read is needed to name it.
    expect(firmwareCompliance(FLEET)).toEqual({
      total: 3,
      behind: 1,
      onTarget: 2,
      targets: ['1.2.0'],
    })
  })

  it('reports several targets as a count rather than picking one', () => {
    const straddling = [
      makeTerminal({ current_firmware_version: '1.2.0' }),
      makeTerminal({ id: 2, current_firmware_version: '2.0.0', release_channel: 'BETA' }),
    ]
    expect(firmwareCompliance(straddling).targets).toEqual(['1.2.0', '2.0.0'])
  })

  it('derives recorded-or-error by subtraction over a closed decision set', () => {
    const segments = decisionSegments({ total: 6, granted: 3, denied: 2 })
    expect(segments.map((segment) => [segment.id, segment.value])).toEqual([
      ['granted', 3],
      ['denied', 2],
      ['other', 1],
    ])
  })

  it('never reports a negative band when three counts race each other', () => {
    // Three separate requests: a `granted` read a moment newer than `total` is
    // a rendering artefact, not a fact about the day.
    const segments = decisionSegments({ total: 1, granted: 3, denied: 2 })
    expect(segments.find((segment) => segment.id === 'other')?.value).toBe(0)
  })

  it('starts the day at the reader’s own midnight', () => {
    const start = startOfLocalDay(new Date(2026, 7, 24, 17, 45, 12))
    expect(start.getHours()).toBe(0)
    expect(start.getMinutes()).toBe(0)
    expect(start.getDate()).toBe(24)
  })
})

describe('attention items', () => {
  const fleet = countFleet(FLEET)

  it('reports only what the platform actually said', () => {
    const items = collectAttention({
      fleet,
      terminals: FLEET,
      applicationCount: 1,
      peopleTotal: 5,
    })
    const ids = items.map((item) => item.id)

    expect(ids).toContain('terminals-error')
    expect(ids).toContain('terminals-offline')
    expect(ids).toContain('terminals-never-reported')
    expect(ids).toContain('terminals-outdated')
    // Nothing invented: no application or people prompt when both are fine.
    expect(ids).not.toContain('no-features')
    expect(ids).not.toContain('no-people')
  })

  it('orders faults above things that are merely incomplete', () => {
    const items = collectAttention({
      fleet,
      terminals: FLEET,
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
      applicationCount: 1,
      peopleTotal: 1,
    })
    expect(items).toEqual([])
  })

  /*
    P0-3. THE STEP THE CHECKLIST STOPPED SHORT OF.

    Absence of permission is not permission: a person with no rule reaches
    nothing. So a customer who added a terminal and a roster and watched this
    list go quiet had a deployment that refused everybody, with nothing on
    screen having said so — and the very next thing they did was walk to a
    terminal and be denied.
  */
  it('raises the ACCESS step once there are people but no rules', () => {
    const healthy = [makeTerminal({ status: 'ONLINE' })]
    const items = collectAttention({
      fleet: countFleet(healthy),
      terminals: healthy,
      applicationCount: 1,
      peopleTotal: 4,
      peopleWithoutAccess: 4,
    })

    const access = items.find((item) => item.id === 'people-without-access')
    expect(access).toBeDefined()
    // The whole roster, so it is said as the plain fact it is.
    expect(access?.title).toBe('Nobody can get in yet')
    expect(access?.action).toBe('Grant access')
    expect(access?.href).toBe('/people')
    // It is the last thing between them and a working deployment.
    expect(access?.primary).toBe(true)
  })

  it('counts, rather than rounding to “nobody”, when only some lack rules', () => {
    const healthy = [makeTerminal({ status: 'ONLINE' })]
    const items = collectAttention({
      fleet: countFleet(healthy),
      terminals: healthy,
      applicationCount: 1,
      peopleTotal: 10,
      peopleWithoutAccess: 3,
    })

    const access = items.find((item) => item.id === 'people-without-access')
    expect(access?.title).toBe('3 people have no access')
    // Not the primary action: most of the roster is already working.
    expect(access?.primary).toBe(false)
  })

  it('says “person” for one, because the console is read by people', () => {
    const healthy = [makeTerminal({ status: 'ONLINE' })]
    const items = collectAttention({
      fleet: countFleet(healthy),
      terminals: healthy,
      applicationCount: 1,
      peopleTotal: 10,
      peopleWithoutAccess: 1,
    })
    expect(
      items.find((item) => item.id === 'people-without-access')?.title,
    ).toBe('1 person has no access')
  })

  it('drops the step once everybody has a rule', () => {
    const healthy = [makeTerminal({ status: 'ONLINE' })]
    expect(
      collectAttention({
        fleet: countFleet(healthy),
        terminals: healthy,
        applicationCount: 2,
        peopleTotal: 10,
        peopleWithoutAccess: 0,
      }),
    ).toEqual([])
  })

  it('does NOT raise it while the roster is still empty', () => {
    // "Nobody has access" is not a useful thing to say about nobody. Adding
    // people is the step that comes first, and it has its own item.
    const healthy = [makeTerminal({ status: 'ONLINE' })]
    const ids = collectAttention({
      fleet: countFleet(healthy),
      terminals: healthy,
      applicationCount: 1,
      peopleTotal: 0,
      peopleWithoutAccess: 0,
    }).map((item) => item.id)

    expect(ids).toContain('no-people')
    expect(ids).not.toContain('people-without-access')
  })

  it('says nothing while the count has not arrived, rather than assuming zero', () => {
    // The item is a claim about the customer's configuration. Making it from a
    // figure that has not loaded would raise and retract it on every visit.
    const healthy = [makeTerminal({ status: 'ONLINE' })]
    const ids = collectAttention({
      fleet: countFleet(healthy),
      terminals: healthy,
      applicationCount: 1,
      peopleTotal: 4,
      peopleWithoutAccess: undefined,
    }).map((item) => item.id)

    expect(ids).not.toContain('people-without-access')
  })

  /*
    THE WHOLE JOURNEY, IN ORDER, for the customer signup actually creates: a
    company with one site, no terminals, no features, nobody, and nothing
    granted. Each step has to appear, and they have to appear in the order
    somebody would do them.
  */
  it('walks a brand-new customer from empty to ready, in order', () => {
    const empty = collectAttention({
      fleet: countFleet([]),
      terminals: [],
      applicationCount: 0,
      peopleTotal: 0,
      peopleWithoutAccess: 0,
    }).map((item) => item.id)
    expect(empty).toEqual(['no-terminals', 'no-features', 'no-people'])

    // A terminal paired, still nothing else.
    const paired = [makeTerminal({ status: 'ONLINE' })]
    const withTerminal = collectAttention({
      fleet: countFleet(paired),
      terminals: paired,
      applicationCount: 0,
      peopleTotal: 0,
      peopleWithoutAccess: 0,
    }).map((item) => item.id)
    expect(withTerminal).toEqual(['no-features', 'no-people'])

    // People added — and now the step that used to be missing.
    const withPeople = collectAttention({
      fleet: countFleet(paired),
      terminals: paired,
      applicationCount: 0,
      peopleTotal: 3,
      peopleWithoutAccess: 3,
    }).map((item) => item.id)
    expect(withPeople).toEqual(['no-features', 'people-without-access'])

    // Access granted. Nothing left to do.
    expect(
      collectAttention({
        fleet: countFleet(paired),
        terminals: paired,
        applicationCount: 1,
        peopleTotal: 3,
        peopleWithoutAccess: 0,
      }),
    ).toEqual([])
  })

  /*
    INDUSTRY-NEUTRAL. The same sentence is read by a gym, an office, a school, a
    warehouse and a factory, so it names none of them — and names no door, no
    shift and no badge either.
  */
  it('describes access without naming an industry', () => {
    const healthy = [makeTerminal({ status: 'ONLINE' })]
    const access = collectAttention({
      fleet: countFleet(healthy),
      terminals: healthy,
      applicationCount: 1,
      peopleTotal: 4,
      peopleWithoutAccess: 4,
    }).find((item) => item.id === 'people-without-access')

    const text = `${access?.title} ${access?.detail}`.toLowerCase()
    for (const word of [
      'gym',
      'member',
      'student',
      'pupil',
      'employee',
      'patient',
      'guest',
      'workout',
      'class',
      'shift',
    ]) {
      expect(text).not.toContain(word)
    }
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

    // "Your context" was removed: it restated the company name the header
    // above already carries and the scope the site selector already shows.
    expect(screen.queryByRole('region', { name: 'Your context' })).not.toBeInTheDocument()
  })

  it('shows totals the API actually computes', async () => {
    signIn()
    renderDashboard()

    await screen.findByRole('region', { name: 'Platform totals' })

    // Three terminals, two people, six events today, two of them denied. The
    // denied tile carries its denominator in the figure -- "2 of 6" -- rather
    // than repeating the count in the description beneath it.
    await waitFor(() => expect(within(tile('Terminals')).getByText('3')).toBeInTheDocument())
    expect(within(tile('Terminals')).getByText(/1 online/)).toBeInTheDocument()
    await waitFor(() => expect(within(tile('People')).getByText('2')).toBeInTheDocument())
    await waitFor(() => expect(within(tile('Events today')).getByText('6')).toBeInTheDocument())
    await waitFor(() =>
      expect(within(tile('Denied today')).getByText('2 of 6')).toBeInTheDocument(),
    )
    expect(within(tile('Denied today')).getByText('events today')).toBeInTheDocument()
  })

  it('reads the people total from the envelope rather than counting a page', async () => {
    // limit=1 fetches one row; `total` is the server's count over the roster.
    signIn()
    seed({
      people: Array.from({ length: 120 }, (_, i) =>
        makePerson({ id: `p${i}`, external_id: `P-${i}` }),
      ),
    })
    renderDashboard()

    await screen.findByRole('region', { name: 'Platform totals' })
    await waitFor(() => expect(within(tile('People')).getByText('120')).toBeInTheDocument())
  })

  it('breaks down terminal health from the server’s own rollup', async () => {
    signIn()
    renderDashboard()

    const health = await screen.findByRole('region', { name: /Terminal health/ })
    expect(await within(health).findByText('Online')).toBeInTheDocument()
    expect(within(health).getByText('Reporting an error')).toBeInTheDocument()
    // Zero-valued states stay in the legend rather than vanishing.
    expect(within(health).getByText('Disabled')).toBeInTheDocument()
    expect(within(health).getByText(/behind on firmware/)).toBeInTheDocument()
    expect(within(health).getByText(/never reported in/)).toBeInTheDocument()
  })

  it('counts the day’s decisions from the server, each as its own exact total', async () => {
    signIn()
    renderDashboard()

    const today = await screen.findByRole('region', { name: 'Today at your access points' })
    await waitFor(() => expect(within(today).getByText('6')).toBeInTheDocument())

    const granted = within(today).getByText('Granted').closest('li') as HTMLElement
    expect(within(granted).getByText('3')).toBeInTheDocument()
    const denied = within(today).getByText('Denied').closest('li') as HTMLElement
    expect(within(denied).getByText('2')).toBeInTheDocument()
    // RECORDED and ERROR are derived by subtraction rather than fetched, and
    // labelled as both because the page cannot tell which without asking.
    const other = within(today).getByText('Other outcomes').closest('li') as HTMLElement
    expect(within(other).getByText('1')).toBeInTheDocument()
  })

  it('bounds the day at the reader’s midnight, excluding yesterday', async () => {
    signIn()
    renderDashboard()

    // Eight events are seeded; two of them happened yesterday and must not be
    // counted, which is the whole point of sending `from`.
    const today = await screen.findByRole('region', { name: 'Today at your access points' })
    await waitFor(() => expect(within(today).getByText('6')).toBeInTheDocument())
    expect(within(today).queryByText('8')).not.toBeInTheDocument()
    expect(within(today).getByText(/since midnight, your time/)).toBeInTheDocument()
  })

  it('shows the event log, most recent first', async () => {
    signIn()
    renderDashboard()

    const activity = await screen.findByRole('region', { name: 'Recent access activity' })
    // Ada is seeded twice today and nobody else is repeated.
    expect(await within(activity).findAllByText('Ada Okonkwo')).toHaveLength(2)
    expect(within(activity).getByText('Tunde Bakare')).toBeInTheDocument()

    // Newest first, and the window is the trail's head rather than today's:
    // the two events from yesterday are still the seventh and eighth rows.
    const who = within(activity)
      .getAllByRole('row')
      .slice(1)
      .map((row) => (row.querySelector('[data-label="Who"]')?.textContent ?? '').trim())
    expect(who[0]).toBe('Ada Okonkwo')
    expect(who.at(-1)).toBe('Chidi Nwosu')
    // An unmatched presentation keeps the identifier the terminal read and is
    // marked, rather than looking like a name that failed to load.
    expect(within(activity).getByText('UNKNOWN-CARD-99')).toBeInTheDocument()
    expect(within(activity).getByText('Not recognised')).toBeInTheDocument()
  })

  it('spreads the fleet across sites', async () => {
    signIn()
    renderDashboard()

    const sites = await screen.findByRole('region', { name: 'Sites' })
    const lagos = (await within(sites).findByText(SITE_A.site_name)).closest(
      'li',
    ) as HTMLElement
    expect(within(lagos).getByText(/2 terminals/)).toBeInTheDocument()
    expect(within(lagos).getByText(/1 online/)).toBeInTheDocument()
  })

  it('reports firmware standing without reading the catalogue', async () => {
    signIn()
    renderDashboard()

    const firmware = await screen.findByRole('region', { name: 'Firmware' })
    /*
      THREE SEGMENTS, FROM THE SAME FUNCTION THE FIRMWARE SCREEN USES.

      This was "On the current build" and "Behind", both read straight off
      `firmware_outdated` -- which is TRUE for a terminal that has never
      reported a version at all. So a unit registered this morning and not yet
      switched on was reported to the customer as needing an update. Sharing
      `fleetStanding` is also what stops this panel and the Firmware screen
      disagreeing about the same fleet one click apart.
    */
    expect(await within(firmware).findByText('Up to date')).toBeInTheDocument()
    expect(within(firmware).getByText('Update available')).toBeInTheDocument()
    expect(within(firmware).getByText('Hasn’t reported a version yet')).toBeInTheDocument()
    expect(within(firmware).getByText(/Current version: 1\.2\.0/)).toBeInTheDocument()

    // The number came off the terminal list. Nothing asked /console/firmware.
    expect(
      state.requests.filter((request) => request.url.includes('/console/firmware')),
    ).toHaveLength(0)
  })

  /*
    THE CHECKLIST HAS TO BE ONE THE READER CAN FINISH.

    Every item used to render its action for every role, so a VIEWER opening the
    console for the first time was handed three things to do and could do none of
    them. Two led to a page where the button is simply absent; "View features"
    led to /settings/applications, which is RequireRole minimum="ADMIN" and
    redirects to the forbidden page. Being sent somewhere you are not allowed, on
    the first screen of the product, reads as a broken account.

    Asserted per role against an EMPTY company, because that is the state a real
    first-time reader is in and the one that raises all three onboarding items at
    once. What must hold in every case: the item is still there, and the action
    is offered only to somebody who can complete it.
  */
  function emptyCompany(role: Role) {
    const session = makeSession({
      role,
      operator: { id: 'operator-1', email: 'ops@example.com', full_name: 'Ops Person', role },
      applications: [],
    })
    resetServerState(session)
    setCsrfToken(session.csrf_token)
    seed({
      sites: SITES,
      terminals: [],
      people: [makePerson({ external_id: 'P-1' }), makePerson({ id: 'p2', external_id: 'P-2' })],
      events: [],
    })
    return session
  }

  async function attentionActions() {
    const region = await screen.findByRole('region', { name: 'Needs your attention' })
    await within(region).findByText(/Add your first terminal/)
    return {
      region,
      links: within(region)
        .queryAllByRole('link')
        .map((link) => `${link.textContent} → ${link.getAttribute('href')}`),
    }
  }

  it('offers an OWNER every onboarding action', async () => {
    emptyCompany('OWNER')
    renderDashboard()

    const { links } = await attentionActions()
    expect(links).toEqual(
      expect.arrayContaining([
        'Add a terminal → /terminals',
        'View features → /settings/applications',
        'Grant access → /people',
      ]),
    )
  })

  it('withholds the ADMIN-only actions from a MANAGER, and says who can', async () => {
    emptyCompany('MANAGER')
    renderDashboard()

    const { region, links } = await attentionActions()

    // Adding a terminal is `addTerminals` (ADMIN); the Features screen is an
    // ADMIN route. Neither is offered, and neither is silently dropped.
    expect(links).not.toContain('Add a terminal → /terminals')
    expect(links).not.toContain('View features → /settings/applications')
    expect(
      within(region).getByText('Adding a terminal needs an administrator or owner.'),
    ).toBeInTheDocument()
    expect(
      within(region).getByText('Turning a feature on needs an administrator or owner.'),
    ).toBeInTheDocument()

    // THE STEP ITSELF IS STILL THERE. A manager who cannot add the terminal is
    // often the person who will go and find somebody who can.
    expect(within(region).getByText(/Add your first terminal/)).toBeInTheDocument()

    // And what a MANAGER genuinely can do is still offered: `manageAccess`.
    expect(links).toContain('Grant access → /people')
  })

  it('offers a VIEWER no action at all, and never a link to a page that would refuse them', async () => {
    emptyCompany('VIEWER')
    renderDashboard()

    const { region, links } = await attentionActions()

    expect(links).toEqual([])
    // The one that used to land on the forbidden page.
    expect(
      within(region).queryByRole('link', { name: 'View features' }),
    ).not.toBeInTheDocument()

    for (const sentence of [
      'Adding a terminal needs an administrator or owner.',
      'Turning a feature on needs an administrator or owner.',
      'Granting access needs a manager or above.',
    ]) {
      expect(within(region).getByText(sentence)).toBeInTheDocument()
    }
  })

  it('never links any role to a route their role cannot open', async () => {
    // The rule stated once rather than per item: /settings/applications and
    // /settings/firmware are ADMIN routes, so no lower role may be sent to one
    // from here. A new ADMIN-gated destination added to this list without a
    // `requiresRole` fails this.
    const ADMIN_ONLY = ['/settings/applications', '/settings/firmware']

    for (const role of ['MANAGER', 'VIEWER'] as const) {
      emptyCompany(role)
      const { unmount } = renderDashboard()
      const { links } = await attentionActions()
      for (const link of links) {
        for (const route of ADMIN_ONLY) {
          expect(link, `${role} was offered ${link}`).not.toContain(route)
        }
      }
      unmount()
    }
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

  /*
   * THREE, THEN A BUTTON.
   *
   * The default fixture produces four: a fault, an offline unit, one that never
   * connected and one behind on software. A real deployment produces five or
   * six, and rendered in full they filled the first screen — somebody opening
   * the console met six problems of equal weight before a single figure about
   * their business.
   */
  /*
    P0-3, END TO END. The unit tests above prove `collectAttention` raises the
    step; this proves the page actually asks the server for the figure and
    renders it. Without the request there is no item, and the unit tests would
    still pass.
  */
  it('asks the server who has no access, and offers the step', async () => {
    signIn('ADMIN')
    // A HEALTHY FLEET, so the access step is not pushed behind "View 2 more" by
    // the fixture's faults — the attention list shows three and orders faults
    // first, deliberately. The state under test is the one a customer reaches by
    // pairing a terminal, adding a roster and stopping: two people, no rules.
    seed({ terminals: [makeTerminal({ status: 'ONLINE' })] })
    renderDashboard()

    const attention = await screen.findByRole('region', { name: 'Needs your attention' })
    await within(attention).findByText('Nobody can get in yet')
    expect(
      within(attention).getByRole('link', { name: 'Grant access' }),
    ).toHaveAttribute('href', '/people')

    // The figure came from the server rather than from anything counted here.
    expect(
      state.requests.filter((request) => request.url.includes('/console/onboarding')).length,
    ).toBeGreaterThan(0)
  })

  it('drops the step once everybody on the roster has a rule', async () => {
    signIn('ADMIN')
    seed({
      permissions: [
        makePermission({ id: 'perm-1', person_id: 'P-1' }),
        makePermission({ id: 'perm-2', person_id: 'P-2' }),
      ],
    })
    renderDashboard()

    const attention = await screen.findByRole('region', { name: 'Needs your attention' })
    await within(attention).findByText(/terminal/i)
    expect(within(attention).queryByText('Nobody can get in yet')).not.toBeInTheDocument()
    expect(within(attention).queryByText(/no access/i)).not.toBeInTheDocument()
  })

  /*
    THE END OF THE JOURNEY, which the checklist used to stop one step short of.

    A customer who has added a terminal, added people and granted access has
    finished configuring and has never seen the product do anything. The list
    went quiet at exactly that moment, which reads as "done" when what has been
    reached is "should work".

    A HEALTHY, FULLY CONFIGURED COMPANY is therefore the fixture here: one online
    terminal, everybody granted, and no events. Every other item has to be silent
    or the assertion is measuring the wrong thing.
  */
  function readyButUnproven(role: Role = 'ADMIN') {
    const session = makeSession({
      role,
      operator: { id: 'operator-1', email: 'ops@example.com', full_name: 'Ops Person', role },
      applications: [{ code: 'ACCESS_CONTROL', settings: {} }],
    })
    resetServerState(session)
    setCsrfToken(session.csrf_token)
    seed({
      sites: SITES,
      terminals: [makeTerminal({ serial_number: 'AT-0001', status: 'ONLINE' })],
      people: [makePerson({ external_id: 'P-1' })],
      permissions: [makePermission({ id: 'perm-1', person_id: 'P-1' })],
      events: [],
    })
    return session
  }

  it('offers a way to CHECK access once everything is configured and nothing has happened', async () => {
    readyButUnproven()
    renderDashboard()

    const attention = await screen.findByRole('region', { name: 'Needs your attention' })
    expect(await within(attention).findByText('Check that access works')).toBeInTheDocument()

    // It sends them to the terminal, where the check already lives, and names
    // the control they are looking for rather than duplicating it here.
    const action = within(attention).getByRole('link', { name: 'Check access' })
    expect(action).toHaveAttribute('href', '/terminals/AT-0001')
    expect(action).not.toHaveClass('button--primary')
    expect(within(attention).getByText(/choose Check access/)).toBeInTheDocument()

    // IT CLAIMS NOTHING HAPPENED. The item exists because nothing has.
    expect(within(attention).getByText(/nothing has been recorded yet/i)).toBeInTheDocument()
    const text = attention.textContent ?? ''
    expect(text).not.toMatch(/\bdoors?\b/i)
    expect(text).toMatch(/access point/)
  })

  it('retires it for good once a single event has been recorded', async () => {
    readyButUnproven()
    seed({ events: [makeEvent({ id: 'first', occurred_at: todayAt(9) })] })
    renderDashboard()

    /*
      WAIT FOR THE EVENT TO BE ON SCREEN before asserting the absence. Asserting
      it straight after the region appears passes for the wrong reason: the
      trail has not arrived yet, so the item is absent because the figure is
      unknown rather than because it is non-zero. This test was written that way
      first and survived inverting the condition it exists to protect.
    */
    const activity = await screen.findByRole('region', { name: 'Recent access activity' })
    await within(activity).findByText('Ada Okonkwo')

    // The events themselves are the acknowledgement; nothing else is needed.
    expect(screen.queryByText('Check that access works')).not.toBeInTheDocument()
    expect(
      screen.queryByRole('region', { name: 'Needs your attention' }),
    ).not.toBeInTheDocument()
  })

  it('does not raise it while somebody still has no access', async () => {
    // The step that actually blocks people comes first; two invitations at once
    // is how a checklist stops being read.
    readyButUnproven()
    seed({ permissions: [] })
    renderDashboard()

    const attention = await screen.findByRole('region', { name: 'Needs your attention' })
    await within(attention).findByText('Nobody can get in yet')
    expect(within(attention).queryByText('Check that access works')).not.toBeInTheDocument()
  })

  it('does not raise it for a company with no terminal to check at', async () => {
    readyButUnproven()
    seed({ terminals: [] })
    renderDashboard()

    const attention = await screen.findByRole('region', { name: 'Needs your attention' })
    await within(attention).findByText('Add your first terminal')
    expect(within(attention).queryByText('Check that access works')).not.toBeInTheDocument()
  })

  it('withholds the check from a VIEWER, who cannot run it', async () => {
    // `configureTerminals` is MANAGER, which is what the button on the terminal
    // page requires. A viewer is told the step exists and who can take it.
    readyButUnproven('VIEWER')
    renderDashboard()

    const attention = await screen.findByRole('region', { name: 'Needs your attention' })
    expect(await within(attention).findByText('Check that access works')).toBeInTheDocument()
    expect(
      within(attention).queryByRole('link', { name: 'Check access' }),
    ).not.toBeInTheDocument()
    expect(
      within(attention).getByText('Checking access needs a manager or above.'),
    ).toBeInTheDocument()
  })

  it('offers the check to a MANAGER, who can', async () => {
    readyButUnproven('MANAGER')
    renderDashboard()

    const attention = await screen.findByRole('region', { name: 'Needs your attention' })
    expect(
      await within(attention).findByRole('link', { name: 'Check access' }),
    ).toHaveAttribute('href', '/terminals/AT-0001')
  })

  it('shows only the first three, and says how many are left', async () => {
    // A waiting terminal on top of the fixture's three faults, so there is
    // reliably something to hide.
    signIn('MANAGER')
    seed({ pendingTerminals: [makePendingTerminal()] })
    renderDashboard()

    const attention = await screen.findByRole('region', { name: 'Needs your attention' })
    await within(attention).findByText(/waiting to be set up/)

    expect(within(attention).getAllByRole('listitem')).toHaveLength(3)
    // The count is on the control, so a short list is still honest about it.
    const more = within(attention).getByRole('button', { name: /^View \d+ more$/ })
    expect(more).toHaveAttribute('aria-expanded', 'false')
    // The fourth item, by severity order, is not on screen yet.
    expect(within(attention).queryByText(/never reported in/)).not.toBeInTheDocument()
  })

  it('reveals the rest without losing any of them', async () => {
    // NOTHING IS DROPPED, only deferred: every item the platform reported is
    // one click away, and the control returns.
    const user = userEvent.setup()
    signIn('MANAGER')
    seed({ pendingTerminals: [makePendingTerminal()] })
    renderDashboard()

    const attention = await screen.findByRole('region', { name: 'Needs your attention' })
    await within(attention).findByText(/waiting to be set up/)

    const shown = within(attention).getAllByRole('listitem').length
    await user.click(within(attention).getByRole('button', { name: /^View \d+ more$/ }))

    expect(within(attention).getByText(/never reported in/)).toBeInTheDocument()
    expect(within(attention).getAllByRole('listitem').length).toBeGreaterThan(shown)

    const fewer = within(attention).getByRole('button', { name: 'Show fewer' })
    expect(fewer).toHaveAttribute('aria-expanded', 'true')
    await user.click(fewer)
    expect(within(attention).getAllByRole('listitem')).toHaveLength(3)
  })

  it('makes setting up a waiting terminal the primary action', async () => {
    /*
      THE ONE ITEM WITH A PERSON WAITING AT A DOOR. Every other row here is
      read-then-decide; this one blocks somebody standing at the hardware. It
      used to be a text link indistinguishable from the dozen others on the
      page.
    */
    /*
      AN ADMIN, AND THE ROLE IS PART OF THE ASSERTION. This read `signIn('MANAGER')`
      because seeing the waiting list is `viewPendingTerminals` (MANAGER) — but
      APPROVING is ADMIN (`canApprove` in PendingTerminals), so the primary button
      was being offered to the one role that cannot press it. The item is still
      raised for a manager; what they get instead is covered below.
    */
    signIn('ADMIN')
    seed({ pendingTerminals: [makePendingTerminal()] })
    renderDashboard()

    const attention = await screen.findByRole('region', { name: 'Needs your attention' })
    const action = await within(attention).findByRole('link', { name: 'Set up terminals' })
    expect(action).toHaveClass('button--primary')
    expect(action).toHaveAttribute('href', '/terminals')

    // And it is the first thing in the list, not the fourth.
    const [first] = within(attention).getAllByRole('listitem')
    expect(within(first as HTMLElement).getByText(/waiting to be set up/)).toBeInTheDocument()
  })

  it('tells a MANAGER a terminal is waiting without offering to set it up', async () => {
    // The half of the previous test that used to be wrong. A manager sees the
    // waiting list and cannot approve from it, so the item is raised and the
    // button is not — they are usually the person who goes and finds somebody.
    signIn('MANAGER')
    seed({ pendingTerminals: [makePendingTerminal()] })
    renderDashboard()

    const attention = await screen.findByRole('region', { name: 'Needs your attention' })
    expect(await within(attention).findByText(/waiting to be set up/)).toBeInTheDocument()
    expect(
      within(attention).queryByRole('link', { name: 'Set up terminals' }),
    ).not.toBeInTheDocument()
    expect(
      within(attention).getByText('Setting a terminal up needs an administrator or owner.'),
    ).toBeInTheDocument()
  })

  it('does not raise a deliberately deactivated site as a problem', async () => {
    /*
      A DEACTIVATED SITE IS SOMEBODY'S OWN DECISION. Reporting a customer's
      setting back to them as something needing attention is how an alert list
      stops being read. It stays visible in the Sites panel, which is where a
      state belongs.
    */
    signIn()
    seed({ sites: [makeSite({ id: SITE_A.site_id, name: SITE_A.site_name, active: false })] })
    renderDashboard()

    const attention = await screen.findByRole('region', { name: 'Needs your attention' })
    await within(attention).findByText(/1 terminal reporting a fault/)
    expect(within(attention).queryByText(/deactivated/i)).not.toBeInTheDocument()

    const sites = await screen.findByRole('region', { name: 'Sites' })
    expect(within(sites).getByText('Deactivated')).toBeInTheDocument()
  })

  it('shows no attention section for a healthy deployment', async () => {
    signIn('ADMIN', { applications: [{ code: 'ATTENDANCE', settings: {} }] })
    seed({
      terminals: [makeTerminal({ status: 'ONLINE' })],
      people: [makePerson()],
    })
    renderDashboard()

    await screen.findByRole('region', { name: 'Platform totals' })
    await waitFor(() =>
      expect(screen.queryByRole('region', { name: 'Needs your attention' })).not.toBeInTheDocument(),
    )
  })

  it('still fills the screen when nothing at all is wrong', async () => {
    // THE REGRESSION THIS GUARDS. The attention list is the only conditional
    // region, and it used to be the only dense one — so the healthiest
    // deployment got the emptiest dashboard, which is exactly backwards.
    signIn('ADMIN', { applications: [{ code: 'ATTENDANCE', settings: {} }] })
    seed({
      terminals: [makeTerminal({ status: 'ONLINE' })],
      people: [makePerson()],
    })
    renderDashboard()

    for (const region of [
      'Platform totals',
      'Terminal health',
      'Today at your access points',
      'Recent access activity',
      'Sites',
      'Firmware',
      'Features',
    ]) {
      expect(
        await screen.findByRole('region', { name: region }),
        `the overview lost its "${region}" region`,
      ).toBeInTheDocument()
    }
  })
})

// ---------------------------------------------------------------------------
// Roles
// ---------------------------------------------------------------------------

describe('the dashboard asks for nothing it may not have', () => {
  it('never calls the announcements endpoint as a VIEWER', async () => {
    /*
      THE 403 LOOP. `GET /console/terminal-announcements` is MANAGER on the
      server and the hook behind it polls every ten seconds, so an ungated call
      meant a rejected request on load and another six times a minute for the
      life of the tab — for a count that was never going to arrive.
    */
    signIn('VIEWER')
    renderDashboard()

    await screen.findByRole('region', { name: 'Platform totals' })
    await waitFor(() =>
      expect(within(tile('People')).getByText('2')).toBeInTheDocument(),
    )

    expect(
      state.requests.filter((request) => request.url.includes('terminal-announcements')),
    ).toHaveLength(0)
  })

  it('tells a VIEWER the waiting count is above their role instead of showing a zero', async () => {
    signIn('VIEWER')
    renderDashboard()

    await screen.findByRole('region', { name: 'Platform totals' })
    const waiting = tile('Awaiting setup')
    expect(within(waiting).getByText('—')).toBeInTheDocument()
    expect(within(waiting).getByText(/visible to managers and above/)).toBeInTheDocument()
    expect(within(waiting).queryByText('0')).not.toBeInTheDocument()
  })

  it('counts terminals waiting to be set up for a manager', async () => {
    signIn('MANAGER')
    seed({ pendingTerminals: [makePendingTerminal()] })
    renderDashboard()

    await screen.findByRole('region', { name: 'Platform totals' })
    await waitFor(() =>
      expect(within(tile('Awaiting setup')).getByText('1')).toBeInTheDocument(),
    )
  })

  it('omits the audit card below ADMIN rather than showing it locked', async () => {
    signIn('MANAGER')
    renderDashboard()

    await screen.findByRole('region', { name: 'Platform totals' })
    await waitFor(() =>
      expect(screen.queryByRole('region', { name: 'Recent changes here' })).not.toBeInTheDocument(),
    )
    expect(
      state.requests.filter((request) => request.url.includes('/console/audit')),
    ).toHaveLength(0)
  })

  it('shows recent operator changes to an ADMIN', async () => {
    signIn('ADMIN')
    seed({ audit: [makeAuditRecord({ action: 'TERMINAL_DISABLED', target_label: 'AT-0001' })] })
    renderDashboard()

    const changes = await screen.findByRole('region', { name: 'Recent changes here' })
    /*
      THE AUDIT VOCABULARY'S WORDS, NOT A HUMANISED CODE.

      This panel called `humaniseCode` on the stored action, so the same record
      read "Terminal Disabled" here and "Terminal disabled" on Activity, "Person
      Created" here and "Person added" there — and "Site Key Rotated" here while
      every other surface calls that credential a provisioning key. One helper
      decides these names; this panel now reads it.
    */
    expect(await within(changes).findByText(/Terminal disabled — AT-0001/)).toBeInTheDocument()
    expect(within(changes).getByText(/ops@example\.com/)).toBeInTheDocument()
  })

  it('NAMES THE SITE CREDENTIAL AS THE REST OF THE CONSOLE NAMES IT', async () => {
    // `SITE_KEY_ROTATED` humanised to "Site Key Rotated" — a phrase the product
    // uses nowhere else. The button that does it, the confirmation, the panel
    // that follows and the Activity trail all say "provisioning key".
    signIn()
    seed({ audit: [makeAuditRecord({ id: 'a9', action: 'SITE_KEY_ROTATED', target_label: 'Lagos Depot' })] })
    renderDashboard()

    const changes = await screen.findByRole('region', { name: 'Recent changes here' })
    expect(
      await within(changes).findByText(/Provisioning key rotated — Lagos Depot/),
    ).toBeInTheDocument()
    expect(within(changes).queryByText(/Site Key Rotated/)).not.toBeInTheDocument()
  })

  it('still renders an action this build has never heard of', async () => {
    // `describeAction` falls back to the same humanisation this used to call,
    // so an application-defined event is humanised rather than dropped.
    signIn()
    seed({ audit: [makeAuditRecord({ id: 'a8', action: 'VISITOR_BADGE_PRINTED', target_label: 'V-0044' })] })
    renderDashboard()

    const changes = await screen.findByRole('region', { name: 'Recent changes here' })
    expect(await within(changes).findByText(/Visitor Badge Printed — V-0044/)).toBeInTheDocument()
  })

  it('hides the firmware catalogue link from a manager but keeps the figures', async () => {
    signIn('MANAGER')
    renderDashboard()

    const firmware = await screen.findByRole('region', { name: 'Firmware' })
    expect(await within(firmware).findByText('Up to date')).toBeInTheDocument()
    expect(within(firmware).queryByRole('link', { name: 'All versions' })).not.toBeInTheDocument()
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

    await screen.findByRole('region', { name: 'Platform totals' })
    const peopleTile = tile('People')
    expect(within(peopleTile).getByText('—')).toBeInTheDocument()
    expect(within(peopleTile).queryByText('0')).not.toBeInTheDocument()
  })

  it('names the features a company uses without reporting on our build', async () => {
    /*
      WHAT THIS TEST USED TO DEMAND. It asserted a warning headed "These are not
      running yet", explaining that the platform does not evaluate these
      workflows and that no attendance is being calculated. Accurate, and a
      development status report on the first screen of a paid product — the same
      content removed from Features itself, and the last copy of it in the
      console. It belongs to docs/market-readiness.md now.

      The feature NAMES stay: what a company uses AccessLink for is a fact about
      that company, and it is the reason the panel exists.
    */
    signIn('ADMIN', { applications: [{ code: 'ATTENDANCE', settings: {} }] })
    renderDashboard()

    const features = await screen.findByRole('region', { name: 'Features' })
    expect(within(features).getByText('Attendance')).toBeInTheDocument()

    const text = document.body.textContent ?? ''
    for (const pattern of [
      /not running yet/i,
      /no attendance is being calculated/i,
      /does not yet evaluate/i,
      /not built/i,
      /\boperational\b/i,
    ]) {
      expect(text, `the overview must not say ${pattern}`).not.toMatch(pattern)
    }
  })

  it('shows the event log the console can actually reach', async () => {
    /*
      THE CLAIM THIS REPLACES. This panel used to say there was no
      operator-facing feed for door events and no audit trail of changes made
      here, and that both were planned. Both had shipped:
      `GET /api/v1/console/events` is mounted at VIEWER and `/console/audit` at
      ADMIN, and each has its own entry in the navigation the operator is
      looking at while reading the panel.

      A dashboard whose stated reason for being empty is visibly false is worse
      than an empty dashboard, so the assertion is inverted rather than deleted.
    */
    signIn()
    renderDashboard()

    const activity = await screen.findByRole('region', { name: 'Recent access activity' })
    expect((await within(activity).findAllByText('Ada Okonkwo')).length).toBeGreaterThan(0)
    expect(
      within(activity).queryByText(/not available in this console/i),
    ).not.toBeInTheDocument()
    expect(within(activity).getByRole('link', { name: 'All events' })).toHaveAttribute(
      'href',
      '/events',
    )
  })

  it('says the access points have been quiet rather than showing an empty table', async () => {
    signIn()
    seed({ events: [] })
    renderDashboard()

    const activity = await screen.findByRole('region', { name: 'Recent access activity' })
    expect(await within(activity).findByText('Nothing at your access points yet')).toBeInTheDocument()
  })

  it('marks a terminal whose clock was not believable', async () => {
    signIn()
    seed({
      events: [
        makeEvent({
          id: 'untrusted',
          occurred_at: todayAt(9),
          recorded_at: todayAt(9),
          occurred_at_trusted: false,
        }),
      ],
    })
    renderDashboard()

    const activity = await screen.findByRole('region', { name: 'Recent access activity' })
    // Word for word what the events page says, so the two surfaces cannot read
    // as two different conditions.
    expect(
      await within(activity).findByText('Time not confirmed'),
    ).toBeInTheDocument()
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

  it('reports a failed event count as an error rather than a quiet day', async () => {
    // "No events today" and "we could not ask" are different statements, and on
    // an access-control dashboard the first one is the alarming one to make by
    // accident.
    signIn()
    failNext('events', 500)
    renderDashboard()

    const today = await screen.findByRole('region', { name: 'Today at your access points' })
    await waitFor(() =>
      expect(within(today).getByRole('alert')).toHaveTextContent(/Failed to retrieve events/),
    )
  })

  it('treats a company with nothing configured as a working state', async () => {
    signIn('ADMIN', { applications: [] })
    seed({ sites: [], terminals: [], people: [], events: [] })
    renderDashboard()

    await screen.findByRole('region', { name: 'Platform totals' })

    // Said in two places, deliberately: the attention list offers it as the
    // next step, and the Features panel explains that having none is fine.
    const attention = await screen.findByRole('region', { name: 'Needs your attention' })
    expect(within(attention).getByText('No features turned on')).toBeInTheDocument()

    const features = await screen.findByRole('region', { name: 'Features' })
    expect(within(features).getByText(/No features turned on for/)).toBeInTheDocument()
    expect(within(features).getByText(/normal starting state/)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Scope
// ---------------------------------------------------------------------------

describe('site scope', () => {
  /*
   * SCOPE IS READ OFF THE FIGURES NOW, NOT OFF A PANEL DESCRIBING IT.
   *
   * These three used to assert against "Your context", a card restating the
   * company name in the header above it and the scope the site selector in the
   * top bar already showed. Removing it did not remove the behaviour: a
   * narrowed dashboard still says so on the tile whose figure changed, and
   * still refuses to imply the one figure that cannot be narrowed.
   */

  it('marks the fleet figure as narrowed for a scoped operator', async () => {
    signIn('MANAGER', { all_sites: false, sites: [SITE_A] })
    renderDashboard()

    await screen.findByRole('region', { name: 'Platform totals' })
    expect(tile('Terminals here')).toBeInTheDocument()
  })

  it('does not imply the roster is narrowed, because it cannot be', async () => {
    // The schema has no person-to-site relationship, so People is company-wide
    // whatever the selector says. Saying so on the tile is the honest form.
    signIn('MANAGER', { all_sites: false, sites: [SITE_A] })
    renderDashboard()

    await screen.findByRole('region', { name: 'Platform totals' })
    expect(within(tile('People')).getByText('company-wide')).toBeInTheDocument()
  })

  it('leaves the figures unnarrowed for an operator who reaches everything', async () => {
    signIn()
    renderDashboard()

    await screen.findByRole('region', { name: 'Platform totals' })
    expect(tile('Terminals')).toBeInTheDocument()
    expect(within(tile('People')).getByText('on the roster')).toBeInTheDocument()
  })

  it('remembers a single-site operator’s only site as the selection', async () => {
    // SiteProvider defaults a scoped operator to their one site rather than to
    // an "all sites" view the API would not honour. Visible here because the
    // fleet tile renames itself the moment the view is narrowed to one site,
    // and nobody touched the selector to make that happen.
    signIn('MANAGER', { all_sites: false, sites: [SITE_A] })
    renderDashboard()

    await screen.findByRole('region', { name: 'Platform totals' })
    expect(tile('Terminals here')).toBeInTheDocument()
  })

  it('narrows the event log server-side when the selector is narrowed', async () => {
    /*
      A FieldEvent carries a site NAME and no id, so the console cannot narrow
      the trail itself — asking the server with `site_id` is the only honest
      way, and doing it in the browser would be filtering on an editable,
      non-unique string.
    */
    signIn('MANAGER', { all_sites: false, sites: [SITE_A] })
    renderDashboard()

    await screen.findByRole('region', { name: 'Platform totals' })
    await waitFor(() =>
      expect(
        state.requests.filter(
          (request) =>
            request.url.includes('/console/events') &&
            request.url.includes(`site_id=${SITE_A.site_id}`),
        ).length,
      ).toBeGreaterThan(0),
    )

    // Four events at Lagos Depot today, three of them granted.
    const today = await screen.findByRole('region', { name: 'Today at your access points' })
    await waitFor(() => expect(within(today).getByText('4')).toBeInTheDocument())
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

  /*
    THE PLACE A TERMINAL STANDS AT IS AN ACCESS POINT, NOT A DOOR.

    This screen carried the word three times -- the day's heading, the empty
    state of the event log and the sentence under it -- and each one told a
    school, a warehouse or a residential block that the product was built for
    somebody else. The attention list is checked from the pure function as well
    as from the rendered page, because two of its items (a terminal waiting to
    be approved, a terminal offline) only appear on a screen that happens to
    have those terminals.
  */
  it('calls the place a terminal stands at an access point, never a door', async () => {
    const waiting = [makeTerminal({ status: 'OFFLINE' })]
    const attention = collectAttention({
      fleet: countFleet(waiting),
      terminals: waiting,
      applicationCount: 0,
      peopleTotal: 2,
      pendingTerminals: 1,
      peopleWithoutAccess: 2,
    })
    expect(attention.length).toBeGreaterThan(0)
    expectNoDoorWording(
      'The overview attention list',
      attention.map((item) => `${item.title} ${item.detail} ${item.action ?? ''}`).join(' '),
    )

    signIn('ADMIN', { applications: [{ code: 'ACCESS_CONTROL', settings: {} }] })
    renderDashboard()

    // The heading that used to say "Today at the door".
    await screen.findByRole('region', { name: 'Today at your access points' })
    await screen.findByRole('region', { name: 'Recent access activity' })
    expectNoDoorWording('The overview', document.body.textContent ?? '')
  })

  it('says an access point has been quiet without naming a door', async () => {
    signIn()
    seed({ events: [] })
    renderDashboard()

    const activity = await screen.findByRole('region', { name: 'Recent access activity' })
    expect(
      await within(activity).findByText('Nothing at your access points yet'),
    ).toBeInTheDocument()
    expect(
      within(activity).getByText(/presents themselves at an access point/),
    ).toBeInTheDocument()
    expectNoDoorWording('The overview empty state', activity.textContent ?? '')
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
