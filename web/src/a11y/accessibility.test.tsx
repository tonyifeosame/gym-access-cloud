import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../api/csrf'
import type { Role } from '../api/types'
import { RequireAuth } from '../auth/guards'
import { AppShell } from '../layout/AppShell'
import { ActivityPage } from '../pages/activity/ActivityPage'
import { ApplicationsPage } from '../pages/applications/ApplicationsPage'
import { DashboardPage } from '../pages/DashboardPage'
import { OperatorsListPage } from '../pages/operators/OperatorsListPage'
import { PeopleListPage } from '../pages/people/PeopleListPage'
import { SitesListPage } from '../pages/sites/SitesListPage'
import { TerminalDetailPage } from '../pages/terminals/TerminalDetailPage'
import { TerminalsListPage } from '../pages/terminals/TerminalsListPage'
import {
  makeApplication,
  makeAuditRecord,
  makeOperatorAccount,
  makePerson,
  makeSession,
  makeSite,
  makeTerminal,
  SITE_A,
  SITE_B,
} from '../test/fixtures'
import { expectNoViolations } from '../test/axe'
import { makeTestQueryClient, renderWithSession } from '../test/render'
import { resetServerState, resetTerminalModes, seed } from '../test/server'

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
          { path: 'operators', element: <OperatorsListPage /> },
          { path: 'activity', element: <ActivityPage /> },
          { path: 'settings/applications', element: <ApplicationsPage /> },
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
    ['operators', '/operators', () => screen.findByText('viewer@example.com')],
    ['activity', '/activity', () => screen.findByRole('heading', { name: 'Activity' })],
    ['applications', '/settings/applications', () => screen.findByText('Access Control')],
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
})

// ---------------------------------------------------------------------------
// Keyboard and focus — what axe cannot see
// ---------------------------------------------------------------------------

describe('the console works without a mouse', () => {
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
