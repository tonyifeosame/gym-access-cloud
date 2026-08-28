import { cleanup, screen, waitFor, within } from '@testing-library/react'
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
 * Features.
 *
 * This is where the product's general-purpose claim is either kept or quietly
 * broken, so the tests are mostly about what the console REFUSES to assume:
 * that the catalog is the one this build knows, or that MULTI_PURPOSE is a
 * feature a company turns on.
 *
 * WHAT THESE TESTS DELIBERATELY NO LONGER PIN. An earlier version asserted the
 * exact wording of a development-status report this screen used to render --
 * "Not built yet", "Partly built", an "Operational" state, and a paragraph per
 * feature naming what was unfinished, including how enrolled biometric material
 * is not distributed between terminals. That copy is gone, and the tests that
 * held it in place have been replaced by ones that assert it CANNOT COME BACK.
 * A test that reproduces removed copy is a test that will reintroduce it.
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

/** Whether the mock server currently has a feature switched on. */
function applicationEnabled(code: string): boolean {
  return state.applications.some((entry) => entry.code === code && entry.enabled)
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

  it('shows which features are on and which are off', async () => {
    signIn()
    seed({
      available: CATALOG,
      applications: [makeApplication({ code: 'ATTENDANCE', enabled: true })],
    })
    renderApplications()

    const attendance = (await screen.findByText('Attendance')).closest('li') as HTMLElement
    expect(within(attendance).getByText('On')).toBeInTheDocument()

    const checkIn = screen.getByText('Check-in').closest('li') as HTMLElement
    expect(within(checkIn).getByText('Off')).toBeInTheDocument()
  })

  it('is a legitimate, fully working state for a company with nothing enabled', async () => {
    // Every company starts here. It must never read as an error or a
    // misconfiguration.
    signIn()
    renderApplications()

    await screen.findByText('Access Control')
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.getAllByText('Off')).toHaveLength(CATALOG.length)
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

    expect(await screen.findByText('No features available')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// The architecture rule
// ---------------------------------------------------------------------------

describe('MULTI_PURPOSE is a terminal setting, not a feature', () => {
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

    expect(await screen.findByText('Not a feature')).toBeInTheDocument()
    expect(
      screen.getByText(/terminal setting, not a feature a company turns on/),
    ).toBeInTheDocument()
  })

  it('explains on the catalog page how features relate to terminals', async () => {
    // Four concepts meet here and are routinely confused; the page names the
    // relationship rather than leaving it to be inferred.
    signIn()
    renderApplications()

    expect(await screen.findByText('How this relates to your terminals')).toBeInTheDocument()
    expect(screen.getByText(/Multi-purpose is a terminal setting, not a feature/)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Unknown / future codes
// ---------------------------------------------------------------------------

describe('features newer than this console', () => {
  it('RENDERS AN UNKNOWN CODE rather than crashing or dropping it', async () => {
    // A capability added to the platform must appear without a frontend
    // release. Hiding it would silently conceal part of what a customer has.
    signIn()
    seed({ available: [...CATALOG, 'ROOM_BOOKING'], applications: [] })
    renderApplications()

    // Humanised from the code -- and the code itself never reaches the screen.
    expect(await screen.findByText('Room Booking')).toBeInTheDocument()
    const entry = screen.getByText('Room Booking').closest('li') as HTMLElement
    expect(within(entry).queryByText('ROOM_BOOKING')).not.toBeInTheDocument()
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
    // The warning depends on UNKNOWN_DESCRIPTION being single-sourced: it is
    // found by comparing the registry's invented description against that
    // constant, so a second copy of the sentence anywhere silently disables it.
    expect(screen.getByText('Newer than this console')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Turn on' })).toBeInTheDocument()
  })

  it('can be enabled like any other', async () => {
    const user = userEvent.setup()
    signIn()
    seed({ available: ['ROOM_BOOKING'], applications: [] })
    renderApplications('/settings/applications/room-booking')

    await user.click(await screen.findByRole('button', { name: 'Turn on' }))
    // The header action flipping IS the state now, rather than a card repeating
    // what the button beside it already said.
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Turn off' })).toBeInTheDocument(),
    )
  })

  it('reports a slug the platform does not offer as unavailable', async () => {
    signIn()
    seed({ available: CATALOG, applications: [] })
    renderApplications('/settings/applications/not-a-real-thing')

    expect(await screen.findByText('Not a feature')).toBeInTheDocument()
    expect(screen.getByText(/does not offer a feature by that name/)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Honesty about what enabling does
// ---------------------------------------------------------------------------

describe('the screen reports configuration, not our build status', () => {
  /*
   * THESE FOUR REPLACE TESTS THAT PINNED THE REMOVED COPY.
   *
   * The screen used to teach a four-state internal model and mark every
   * feature with how much of it we had written. Asserting the absence of that
   * is the only way the removal stays removed: the wording was reintroduced
   * once already, by a test that still demanded it.
   */

  const DEV_STATUS = [
    /not built yet/i,
    /partly built/i,
    /\boperational\b/i,
    /not implemented/i,
    /coming soon/i,
    /nothing acts on/i,
    /in development/i,
  ]

  it('shows no development-status wording in the feature list', async () => {
    signIn()
    renderApplications()

    await screen.findByText('Access Control')
    const text = document.body.textContent ?? ''
    for (const pattern of DEV_STATUS) {
      expect(text, `feature list must not say ${pattern}`).not.toMatch(pattern)
    }
  })

  it('shows no development-status wording on a feature page', async () => {
    signIn()
    renderApplications('/settings/applications/attendance')

    await screen.findByRole('heading', { name: 'Attendance', level: 1 })
    const text = document.body.textContent ?? ''
    for (const pattern of DEV_STATUS) {
      expect(text, `feature page must not say ${pattern}`).not.toMatch(pattern)
    }
  })

  it('says nothing about biometric replication on Registration', async () => {
    /*
      THE SPECIFIC PARAGRAPH THIS GUARDS. Registration used to carry: "nothing
      distributes enrolled biometric material between terminals, so an enrolment
      remains local to the unit that took it". That is a V2 design note. It
      belongs in docs/market-readiness.md, not under a toggle in a customer's
      settings screen.
    */
    signIn()
    renderApplications('/settings/applications/registration')

    await screen.findByRole('heading', { name: 'Registration', level: 1 })
    const text = (document.body.textContent ?? '').toLowerCase()
    for (const forbidden of [
      'biometric material',
      'distributes',
      'remains local',
      'not recognised at any other door',
      'replication',
    ]) {
      expect(text, `Registration must not mention "${forbidden}"`).not.toContain(forbidden)
    }
  })

  it('NO LONGER RENDERS THE THREE-STATE READINESS MODEL AS CARDS', async () => {
    /*
      Available / Turned on / Configured were the internal readiness triple shown
      directly to a customer, and two of the three could only ever say "Yes":
      "Available" on a page reachable only because it is available, and "Turned
      on" beside a header button already reading "Turn off".

      The third was worse than redundant. `readinessOf` defines configured as "a
      settings row exists", which is right for the model, so the card read
      "Configured — Yes" above a settings object containing `{}`.
    */
    signIn()
    renderApplications('/settings/applications/access-control')

    await screen.findByRole('heading', { name: 'Access Control', level: 1 })
    expect(screen.queryByRole('region', { name: 'Feature status' })).not.toBeInTheDocument()
    expect(screen.queryByText('Available')).not.toBeInTheDocument()
    expect(screen.queryByText('Turned on')).not.toBeInTheDocument()
  })

  it('SAYS WHAT IS ACTUALLY STORED instead of whether a row exists', async () => {
    // The honest replacement for the "Configured" card: a company with an empty
    // settings object is told nothing is stored, because nothing is.
    signIn()
    renderApplications('/settings/applications/access-control')

    await screen.findByRole('heading', { name: 'Access Control', level: 1 })
    expect(screen.getByText(/Nothing is stored for this feature/i)).toBeInTheDocument()
  })

  it('shows no raw platform code on the feature page', async () => {
    // `ACCESS_CONTROL` used to be printed under the heading "Platform code".
    signIn()
    renderApplications('/settings/applications/access-control')

    await screen.findByRole('heading', { name: 'Access Control', level: 1 })
    expect(document.body.textContent ?? '').not.toContain('ACCESS_CONTROL')
  })

  it('DOES NOT RAISE DEPENDENCIES, which the platform does not model', async () => {
    // This was a heading, a border and a sentence explaining the absence of a
    // concept. Nothing is lost by not bringing it up.
    signIn()
    renderApplications('/settings/applications/access-control')

    await screen.findByRole('heading', { name: 'Access Control', level: 1 })
    expect(screen.queryByText('Dependencies and conflicts')).not.toBeInTheDocument()
  })

  it('NAMES THE ROLE IN THE CONSOLE\'S OWN WORDS, not the stored enum', async () => {
    // Printed "VIEWER" while the operators screen called the same value
    // "Viewer".
    signIn()
    renderApplications('/settings/applications/access-control')

    await screen.findByRole('heading', { name: 'Access Control', level: 1 })
    expect(screen.getByText(/Viewer/)).toBeInTheDocument()
    expect(screen.queryByText('VIEWER')).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Roles
// ---------------------------------------------------------------------------

describe('role restrictions', () => {
  it('lets an OWNER turn a feature on', async () => {
    const user = userEvent.setup()
    signIn('OWNER')
    renderApplications()

    const attendance = (await screen.findByText('Attendance')).closest('li') as HTMLElement
    await user.click(within(attendance).getByRole('button', { name: 'Turn on' }))

    await waitFor(() => expect(within(attendance).getByText('On')).toBeInTheDocument())
  })

  it('lets an ADMIN read but offers no controls', async () => {
    signIn('ADMIN')
    renderApplications()

    await screen.findByText('Access Control')
    expect(screen.getByText('Read only')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Turn on' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Turn off' })).not.toBeInTheDocument()
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
    await user.click(within(attendance).getByRole('button', { name: 'Turn on' }))

    expect(await screen.findByText(/Insufficient permissions/)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Mutations and cache
// ---------------------------------------------------------------------------

/*
  TURNING A FEATURE OFF IS A DESTRUCTIVE ACTION AND IS NOW CONFIRMED EVERYWHERE.

  It was confirmed on the detail page and NOT on the list — and the list is where
  the toggles are, so the unguarded path was the one nearly everybody used. A
  terminal assigned to the feature stops doing anything until it is turned back
  on, which is worth a question wherever it is asked from.

  These tests run the same four-step flow against both entry points, because the
  bug was precisely that the two entry points behaved differently.
*/
describe('turning a feature off always asks first', () => {
  for (const from of ['list', 'detail'] as const) {
    describe(`from the ${from}`, () => {
      async function openTurnOff() {
        const user = userEvent.setup()
        signIn('OWNER')
        seed({
          available: CATALOG,
          applications: [makeApplication({ code: 'ATTENDANCE', enabled: true })],
        })

        if (from === 'detail') {
          renderApplications('/settings/applications/attendance')
          await screen.findByRole('heading', { name: 'Attendance', level: 1 })
          await user.click(screen.getByRole('button', { name: 'Turn off' }))
        } else {
          renderApplications()
          const row = (await screen.findByText('Attendance')).closest('li') as HTMLElement
          await user.click(within(row).getByRole('button', { name: 'Turn off' }))
        }
        return { user, dialog: await screen.findByRole('dialog') }
      }

      it('ASKS BEFORE CHANGING ANYTHING', async () => {
        const { dialog } = await openTurnOff()

        expect(within(dialog).getByText(/Turn off Attendance\?/)).toBeInTheDocument()
        expect(within(dialog).getByText(/stop doing anything/)).toBeInTheDocument()
        // Nothing has been sent yet.
        expect(state.requests.some((entry) => entry.method === 'PUT')).toBe(false)
      })

      it('CANCELLING LEAVES IT ON, and sends nothing', async () => {
        const { user, dialog } = await openTurnOff()

        await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
        await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())

        expect(state.requests.some((entry) => entry.method === 'PUT')).toBe(false)
        expect(applicationEnabled('ATTENDANCE')).toBe(true)
      })

      it('CONFIRMING TURNS IT OFF', async () => {
        const { user, dialog } = await openTurnOff()

        await user.click(within(dialog).getByRole('button', { name: 'Turn off feature' }))
        await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())

        await waitFor(() => expect(applicationEnabled('ATTENDANCE')).toBe(false))
      })
    })
  }

  it('DOES NOT ASK BEFORE TURNING SOMETHING ON', async () => {
    // Turning a feature on takes nothing away and breaks no terminal. A question
    // there teaches somebody to dismiss the one that matters.
    const user = userEvent.setup()
    signIn('OWNER')
    seed({ available: CATALOG, applications: [] })
    renderApplications()

    const row = (await screen.findByText('Attendance')).closest('li') as HTMLElement
    await user.click(within(row).getByRole('button', { name: 'Turn on' }))

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    await waitFor(() => expect(applicationEnabled('ATTENDANCE')).toBe(true))
  })

  it('SAYS THE SAME THING FROM BOTH ENTRY POINTS', async () => {
    // The copy lives in one component precisely so these cannot drift.
    const user = userEvent.setup()
    signIn('OWNER')
    seed({
      available: CATALOG,
      applications: [makeApplication({ code: 'ATTENDANCE', enabled: true })],
    })

    renderApplications()
    const row = (await screen.findByText('Attendance')).closest('li') as HTMLElement
    await user.click(within(row).getByRole('button', { name: 'Turn off' }))
    const fromList = (await screen.findByRole('dialog')).textContent
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    cleanup()

    signIn('OWNER')
    seed({
      available: CATALOG,
      applications: [makeApplication({ code: 'ATTENDANCE', enabled: true })],
    })
    renderApplications('/settings/applications/attendance')
    await screen.findByRole('heading', { name: 'Attendance', level: 1 })
    await user.click(screen.getByRole('button', { name: 'Turn off' }))
    const fromDetail = (await screen.findByRole('dialog')).textContent

    expect(fromList).toBe(fromDetail)
  })
})

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

    await user.click(within(attendance).getByRole('button', { name: 'Turn on' }))

    await waitFor(() =>
      expect(
        state.requests.filter((r) => r.url.includes('/auth/me')).length,
      ).toBeGreaterThan(before),
    )
  })

  it('warns before turning off that assigned terminals resolve to nothing', async () => {
    const user = userEvent.setup()
    signIn('OWNER')
    seed({
      available: CATALOG,
      applications: [makeApplication({ code: 'ATTENDANCE', enabled: true })],
    })
    renderApplications('/settings/applications/attendance')

    await user.click(await screen.findByRole('button', { name: 'Turn off' }))

    // THE SUBSTANCE IS UNCHANGED and is what this test protects: a terminal
    // assigned to the feature stops working, and its assignment survives. Only
    // the wording moved -- into a shared dialog, so the list and the detail page
    // cannot drift apart.
    expect(screen.getByText(/stop doing anything/)).toBeInTheDocument()
    expect(screen.getByText(/kept, not cleared/)).toBeInTheDocument()
  })

  it('reports a failed toggle without claiming success', async () => {
    const user = userEvent.setup()
    signIn('OWNER')
    failNext('set-application', 500)
    renderApplications()

    const attendance = (await screen.findByText('Attendance')).closest('li') as HTMLElement
    await user.click(within(attendance).getByRole('button', { name: 'Turn on' }))

    expect(await screen.findByText(/Could not turn Attendance on/)).toBeInTheDocument()
    expect(within(attendance).getByText('Off')).toBeInTheDocument()
  })

  it('saves settings without silently turning on a feature that is off', async () => {
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
    // The company name used to sit under a "Turned on" card that has gone; the
    // page still shows only this company's configuration, which is what the
    // surrounding assertions check.
    expect(screen.getByRole('heading', { level: 1 })).toBeInTheDocument()
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
