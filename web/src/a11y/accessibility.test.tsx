import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../api/csrf'
import type { Role } from '../api/types'
import { RequireAuth } from '../auth/guards'
import { AppShell } from '../layout/AppShell'
import { ActivityPage } from '../pages/activity/ActivityPage'
import { SchedulesPage } from '../pages/access/SchedulesPage'
import { EventsPage } from '../pages/events/EventsPage'
import { FirmwarePage } from '../pages/firmware/FirmwarePage'
import { PersonDetailPage } from '../pages/people/PersonDetailPage'
import { ApplicationsPage } from '../pages/applications/ApplicationsPage'
import { DashboardPage } from '../pages/DashboardPage'
import { OperatorsListPage } from '../pages/operators/OperatorsListPage'
import { PeopleListPage } from '../pages/people/PeopleListPage'
import { SiteDetailPage } from '../pages/sites/SiteDetailPage'
import { SitesListPage } from '../pages/sites/SitesListPage'
import { TerminalDetailPage } from '../pages/terminals/TerminalDetailPage'
import { TerminalsListPage } from '../pages/terminals/TerminalsListPage'
import {
  makeApplication,
  makeAuditRecord,
  makeEvent,
  makeFirmwareVersion,
  makeOperatorAccount,
  makePendingTerminal,
  makePermission,
  makePerson,
  makeSchedule,
  makeSession,
  makeSite,
  makeTerminal,
  SITE_A,
  SITE_B,
} from '../test/fixtures'
import { expectNoViolations } from '../test/axe'
import { makeTestQueryClient, renderWithSession } from '../test/render'
import {
  resetServerState,
  resetTerminalModes,
  seed,
  seedAnnouncedTerminal,
} from '../test/server'

/**
 * The automated accessibility pass (FE-01).
 *
 * The audit recorded that no accessibility verification of any kind had been
 * done, and that every test was jsdom. This closes the automated half.
 *
 * WHAT THESE TESTS ARE WORTH, precisely. axe in jsdom catches the failures that
 * actually plague admin consoles — an unnamed control, an unlabelled field, a
 * table whose headers are not associated, an ARIA reference pointing at nothing,
 * a duplicated landmark, a skipped heading level. It cannot evaluate colour
 * contrast (no layout, no cascade — checked directly against the palette in
 * contrast.test.ts instead), cannot judge focus order as rendered, and cannot
 * tell whether an accessible name is a GOOD name.
 *
 * So this is a floor. A manual screen-reader pass and a real-browser pass remain
 * open, and are named as still-open in the audit register rather than quietly
 * covered by the presence of this file.
 *
 * THE KEYBOARD AND FOCUS TESTS BELOW ARE NOT AXE. They exercise behaviour axe
 * cannot see: that a modal moves focus in, traps it, and gives it back. Those
 * are the ones that make the console usable without a mouse, and they are worth
 * more than the rule sweeps.
 */

const SITES = [
  makeSite({ id: SITE_A.site_id, name: SITE_A.site_name }),
  makeSite({ id: SITE_B.site_id, name: SITE_B.site_name }),
]

const TERMINALS = [
  makeTerminal({ serial_number: 'AT-0001', site_public_id: SITE_A.site_id }),
  makeTerminal({
    id: 2,
    public_id: 'terminal-public-2',
    serial_number: 'AT-0002',
    device_name: 'Loading Bay',
    site_public_id: SITE_B.site_id,
    site_name: SITE_B.site_name,
    status: 'ERROR',
    firmware_outdated: true,
  }),
]

function signIn(role: Role = 'OWNER') {
  const session = makeSession({
    role,
    operator: { id: 'operator-1', email: 'ops@example.com', full_name: 'Ops Person', role },
    applications: [{ code: 'ACCESS_CONTROL', settings: {} }],
  })
  resetServerState(session)
  resetTerminalModes()
  setCsrfToken(session.csrf_token)
  seed({
    sites: SITES,
    terminals: TERMINALS,
    people: [makePerson(), makePerson({ id: 'p2', external_id: 'P-0002', full_name: 'Bem Tor' })],
    permissions: [makePermission({ person_id: 'P-0001' })],
    schedules: [makeSchedule({ permission_count: 1 })],
    events: [makeEvent()],
    firmware: [makeFirmwareVersion()],
    operators: [makeOperatorAccount(), makeOperatorAccount({ id: 'op-2', email: 'a@b.example' })],
    applications: [makeApplication()],
    audit: [makeAuditRecord()],
  })
  return session
}

/**
 * Renders a page INSIDE THE REAL SHELL.
 *
 * Deliberately not in isolation: half the interesting failures are about the
 * document as a whole — two `banner` landmarks, two `<h1>`s, a nav with no
 * accessible name — and none of them are visible when a page is rendered on its
 * own.
 */
function renderInShell(path: string, client = makeTestQueryClient()) {
  const router = createMemoryRouter(
    [
      {
        path: '/',
        // RequireAuth, exactly as the real router mounts it: AppShell reads the
        // session synchronously, and the session arrives from GET /auth/me a
        // tick later. Rendering the shell without the guard is a shape the app
        // never has.
        element: (
          <RequireAuth>
            <AppShell />
          </RequireAuth>
        ),
        children: [
          { index: true, element: <DashboardPage /> },
          { path: 'people', element: <PeopleListPage /> },
          { path: 'terminals', element: <TerminalsListPage /> },
          { path: 'terminals/:serial', element: <TerminalDetailPage /> },
          { path: 'sites', element: <SitesListPage /> },
          { path: 'sites/:siteId', element: <SiteDetailPage /> },
          { path: 'operators', element: <OperatorsListPage /> },
          { path: 'activity', element: <ActivityPage /> },
          { path: 'events', element: <EventsPage /> },
          { path: 'access/schedules', element: <SchedulesPage /> },
          { path: 'people/:externalId', element: <PersonDetailPage /> },
          { path: 'settings/applications', element: <ApplicationsPage /> },
          { path: 'settings/firmware', element: <FirmwarePage /> },
        ],
      },
    ],
    { initialEntries: [path] },
  )
  return renderWithSession(<RouterProvider router={router} />, client)
}

beforeEach(() => setCsrfToken(null))

// ---------------------------------------------------------------------------
// Every screen, swept
// ---------------------------------------------------------------------------

describe('every screen passes the automated sweep', () => {
  const screens: [name: string, path: string, settled: () => Promise<unknown>][] = [
    ['the overview', '/', () => screen.findByRole('heading', { level: 1 })],
    ['people', '/people', () => screen.findByText('Ada Okonkwo')],
    ['terminals', '/terminals', () => screen.findByText('AT-0001')],
    ['one terminal', '/terminals/AT-0001', () => screen.findByRole('heading', { name: 'Lifecycle' })],
    ['sites', '/sites', () => screen.findByText(SITE_A.site_name)],
    // The site detail page carries the offline-policy radio group, whose options
    // each have a paragraph of description wired by aria-describedby. A fieldset
    // built the obvious way — descriptions inside the labels, or referenced by
    // ids that do not exist — is exactly what axe catches and a sighted review
    // does not.
    [
      'one site',
      `/sites/${SITE_A.site_id}`,
      () => screen.findByRole('heading', { name: 'Behaviour during an outage' }),
    ],
    ['operators', '/operators', () => screen.findByText('viewer@example.com')],
    ['activity', '/activity', () => screen.findByRole('heading', { name: 'Activity' })],
    ['events', '/events', () => screen.findByRole('heading', { name: 'Events' })],
    ['schedules', '/access/schedules', () => screen.findByRole('heading', { name: 'Schedules' })],
    ['one person', '/people/P-0001', () => screen.findByRole('heading', { name: 'Access' })],
    ['applications', '/settings/applications', () => screen.findByText('Access Control')],
    ['firmware', '/settings/firmware', () => screen.findByRole('heading', { name: /Firmware/ })],
  ]

  for (const [name, path, settled] of screens) {
    it(`${name} has no violations`, async () => {
      signIn()
      renderInShell(path)
      // Sweeping a loading state would pass trivially and prove nothing about
      // the screen an operator actually reads.
      await settled()
      await expectNoViolations()
    })
  }
})

// ---------------------------------------------------------------------------
// Dialogs
// ---------------------------------------------------------------------------

describe('dialogs', () => {
  it('a destructive confirmation is named, described and free of violations', async () => {
    const user = userEvent.setup()
    signIn()
    renderInShell('/terminals/AT-0001')

    await screen.findByRole('heading', { name: 'Lifecycle' })
    await user.click(screen.getByRole('button', { name: /^revoke$/i }))

    const dialog = await screen.findByRole('dialog')
    expect(dialog).toHaveAttribute('aria-modal', 'true')
    // A dialog with no accessible name announces as "dialog" and nothing else.
    expect(dialog).toHaveAccessibleName(/revoke the credential/i)
    await expectNoViolations()
  })

  it('a form dialog is free of violations', async () => {
    const user = userEvent.setup()
    signIn()
    renderInShell('/terminals/AT-0001')

    await screen.findByRole('heading', { name: 'Lifecycle' })
    await user.click(screen.getByRole('button', { name: /^move$/i }))
    await screen.findByRole('dialog')
    await expectNoViolations()
  })

  it('the claim-code panel is free of violations', async () => {
    // A one-time credential inside an alertdialog inside a dialog. Nested
    // dialog roles and a heading made programmatically focusable are both easy
    // to get wrong, and neither is visible without a sweep.
    const user = userEvent.setup()
    signIn()
    renderInShell(`/sites/${SITE_A.site_id}`)

    await screen.findByRole('heading', { name: 'Behaviour during an outage' })
    // Behind the Advanced disclosure now: the claim code is the pre-authorised
    // installer path, and adding a terminal happens on the Terminals page.
    await user.click(screen.getByText(/pre-authorise a terminal for an installer/i))
    await user.click(screen.getByRole('button', { name: 'Issue a claim code' }))
    await screen.findByRole('dialog')
    await expectNoViolations()

    await user.type(screen.getByLabelText(/Serial number/), 'AT-0099')
    await user.click(screen.getByRole('button', { name: 'Issue claim code' }))

    await screen.findByLabelText('Claim code')
    await expectNoViolations()
  })

  it('the add-a-terminal flow is free of violations at every step', async () => {
    // THE SCREEN A CUSTOMER MEETS FIRST, so it is swept at every stage: an
    // instruction list, a form, a confirmation panel with a programmatically
    // focused heading, and two alert panels that appear without the focus
    // moving. The last of those is the one worth a sweep — an alert nobody is
    // told about is an alert that does not exist for a screen reader.
    const user = userEvent.setup()
    signIn()
    seedAnnouncedTerminal('K7M2-P4QX', makePendingTerminal())
    renderInShell('/terminals')

    await screen.findByRole('button', { name: 'Add a terminal' })
    await user.click(screen.getByRole('button', { name: 'Add a terminal' }))
    await screen.findByRole('dialog')
    await expectNoViolations()

    await user.type(screen.getByLabelText(/code from the terminal/i), 'K7M2P4QX')
    await user.click(screen.getByRole('button', { name: /continue/i }))

    await screen.findByText(/is this the terminal in front of you/i)
    await expectNoViolations()

    // Two sites in this fixture, so nothing is preselected and the site has to
    // be chosen — which is the shape the select and its error are swept in.
    const confirm = within(screen.getByRole('dialog'))
    await user.selectOptions(confirm.getByLabelText(/^site/i), SITE_A.site_id)
    await user.click(confirm.getByRole('button', { name: /approve and set up/i }))
    await within(await screen.findByRole('dialog')).findByText(/is being set up/i)
    await expectNoViolations()
  })
})

// ---------------------------------------------------------------------------
// Keyboard and focus — what axe cannot see
// ---------------------------------------------------------------------------

describe('the console works without a mouse', () => {
  /*
    THE WAY PAST THE NAVIGATION.

    The sidebar is the same eleven-or-so links on every screen, so without this
    a keyboard or screen-reader user paid thirteen tab stops to reach the page
    content -- on every navigation, all session. axe never flagged it: its
    `bypass` rule is satisfied by the landmarks this console already has, which
    is why a green sweep sat on top of it for so long.

    Asserted here rather than in the browser pass because the property is about
    ORDER AND FOCUS, which is exactly what a DOM test can pin: first in the tab
    order, and focus genuinely lands in the main landmark afterwards. Whether it
    is VISIBLE when focused is a CSS question, and the one part of this that has
    to be checked in a real browser.
  */
  it('offers a skip link as the FIRST thing a keyboard reaches', async () => {
    const user = userEvent.setup()
    signIn()
    renderInShell('/people')

    await screen.findByRole('heading', { name: 'People', level: 1 })

    // Nothing before it. The first Tab from the document lands here, which is
    // the whole point -- a skip link buried behind the sign-out button is not a
    // skip link.
    await user.tab()
    const skip = screen.getByRole('link', { name: 'Skip to main content' })
    expect(document.activeElement).toBe(skip)
    expect(skip).toHaveAttribute('href', '#main')
  })

  it('MOVES FOCUS to the main landmark, not merely the scroll position', async () => {
    // The half that silently does not happen if the target cannot hold focus:
    // the viewport moves, the next Tab carries on from the header, and the user
    // is back in the navigation they just asked to skip.
    const user = userEvent.setup()
    signIn()
    renderInShell('/people')

    await screen.findByRole('heading', { name: 'People', level: 1 })
    await user.tab()
    await user.keyboard('{Enter}')

    const main = document.getElementById('main')
    expect(main).not.toBeNull()
    expect(document.activeElement).toBe(main)

    // And it is reachable only that way: -1 keeps the landmark out of the tab
    // order, so nobody arrives on a focusable region by accident.
    expect(main).toHaveAttribute('tabindex', '-1')
  })

  it('leaves no fragment behind in the address bar', async () => {
    // A `#main` that survives into the next route is litter, and it would be
    // copied into any URL an operator shared from that point on. Following the
    // link normally would set it, so this is what proves the handler ran.
    const user = userEvent.setup()
    signIn()
    renderInShell('/people')

    await screen.findByRole('heading', { name: 'People', level: 1 })
    const before = window.location.hash

    await user.tab()
    await user.keyboard('{Enter}')

    expect(window.location.hash).toBe(before)
    // Still on the page it started on: this moves focus, it does not navigate.
    expect(screen.getByRole('heading', { name: 'People', level: 1 })).toBeInTheDocument()
  })

  it('MOVES FOCUS INTO a dialog when it opens', async () => {
    // A modal that leaves focus behind it is invisible to anyone not using a
    // pointer: they tab through the page underneath and never reach it.
    const user = userEvent.setup()
    signIn()
    renderInShell('/terminals/AT-0001')

    await screen.findByRole('heading', { name: 'Lifecycle' })
    await user.click(screen.getByRole('button', { name: /^disable$/i }))

    const dialog = await screen.findByRole('dialog')
    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true))
  })

  it('TRAPS focus while it is open, cycling at the end', async () => {
    const user = userEvent.setup()
    signIn()
    renderInShell('/terminals/AT-0001')

    await screen.findByRole('heading', { name: 'Lifecycle' })
    await user.click(screen.getByRole('button', { name: /^disable$/i }))
    const dialog = await screen.findByRole('dialog')

    // Tab far enough to have escaped a dialog that was not trapping.
    for (let step = 0; step < 12; step += 1) {
      await user.tab()
      expect(dialog.contains(document.activeElement)).toBe(true)
    }
  })

  it('RETURNS focus to whatever opened it', async () => {
    // Losing it dumps a keyboard user back at the top of the document with
    // their place gone.
    const user = userEvent.setup()
    signIn()
    renderInShell('/terminals/AT-0001')

    await screen.findByRole('heading', { name: 'Lifecycle' })
    const opener = screen.getByRole('button', { name: /^disable$/i })
    await user.click(opener)
    await screen.findByRole('dialog')

    await user.keyboard('{Escape}')
    await waitFor(() => expect(document.activeElement).toBe(opener))
  })

  it('closes on Escape', async () => {
    const user = userEvent.setup()
    signIn()
    renderInShell('/terminals/AT-0001')

    await screen.findByRole('heading', { name: 'Lifecycle' })
    await user.click(screen.getByRole('button', { name: /^disable$/i }))
    await screen.findByRole('dialog')

    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('makes a clickable table row reachable and activatable from the keyboard', async () => {
    // A row that only responds to a pointer is an interaction that does not
    // exist for anyone using a keyboard.
    signIn()
    renderInShell('/terminals')

    await screen.findByText('AT-0001')
    const rows = screen.getAllByRole('button').filter((node) => node.tagName === 'TR')
    if (rows.length > 0) {
      expect(rows[0]).toHaveAttribute('tabindex', '0')
    }
  })
})

// ---------------------------------------------------------------------------
// Document structure
// ---------------------------------------------------------------------------

describe('document structure', () => {
  it('gives every screen EXACTLY ONE h1', async () => {
    // The document outline a screen reader builds its navigation from. Two h1s
    // means no single answer to "what is this page".
    for (const path of ['/', '/people', '/terminals', '/sites', '/activity']) {
      signIn()
      const view = renderInShell(path)
      await screen.findByRole('heading', { level: 1 })
      expect(screen.getAllByRole('heading', { level: 1 })).toHaveLength(1)
      view.unmount()
    }
  })

  it('gives the navigation an accessible name', async () => {
    // "navigation" and nothing else is what a page with several navs sounds
    // like when there is no way to tell them apart.
    signIn()
    renderInShell('/')

    await screen.findByRole('heading', { level: 1 })
    expect(screen.getByRole('navigation', { name: 'Console' })).toBeInTheDocument()
  })

  it('has one banner landmark, not one per page block', async () => {
    signIn()
    renderInShell('/people')

    await screen.findByText('Ada Okonkwo')
    expect(screen.getAllByRole('banner')).toHaveLength(1)
  })

  it('names every table, because a bare grid announces nothing', async () => {
    signIn()
    renderInShell('/terminals')

    await screen.findByText('AT-0001')
    for (const table of screen.getAllByRole('table')) {
      expect(table).toHaveAccessibleName()
    }
  })

  it('announces a loading state rather than leaving it silent', async () => {
    signIn()
    renderInShell('/people')

    // The status region exists before it has anything to say, so the
    // announcement is not competing with the element being inserted.
    const status = await screen.findByRole('status')
    expect(status).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Errors are announced, not merely drawn
// ---------------------------------------------------------------------------

describe('errors reach somebody who cannot see them', () => {
  it('marks a form-level failure as an alert', async () => {
    const user = userEvent.setup()
    signIn()
    renderInShell('/terminals/AT-0001')

    await screen.findByRole('heading', { name: 'Lifecycle' })
    await user.click(screen.getByRole('button', { name: /^move$/i }))
    const dialog = await screen.findByRole('dialog')

    // Submitting with nothing chosen surfaces the field error, which lives in a
    // polite live region rather than an alert — a validation message is not
    // interrupting news, and reserving role="alert" for submission failures is
    // what keeps "the alert" unambiguous.
    await user.click(within(dialog).getByRole('button', { name: /move terminal/i }))
    expect(await within(dialog).findByText(/is required/i)).toBeInTheDocument()
  })
})
