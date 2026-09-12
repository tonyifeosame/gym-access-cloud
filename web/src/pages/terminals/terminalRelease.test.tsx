import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../../api/csrf'
import type { Role, Terminal } from '../../api/types'
import { makeSession, makeSite, makeTerminal, SITE_A } from '../../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../../test/render'
import { resetServerState, resetTerminalModes, seed, state } from '../../test/server'
import { TerminalDetailPage } from './TerminalDetailPage'
import { TerminalsListPage } from './TerminalsListPage'

/**
 * Release for transfer (032).
 *
 * What the console has to get right, in order of consequence:
 *
 *   - the automated workflow is offered ONLY to a terminal whose firmware
 *     reported `terminal_release`; every other unit gets the physical
 *     procedure and the force path, and the dialog says so before an order
 *   - ordering needs the serial typed; forcing needs the word RELEASE typed
 *     and states, in the platform's own words, that the unit keeps working
 *     for this company's members until it reconnects or is wiped
 *   - the page follows the order: a banner while it is outstanding, Cancel
 *     and Release anyway on it, and the list marks the row Releasing
 *   - Setting up is shown until the roster snapshot is acknowledged
 *   - role gating mirrors the server: ADMIN and above only
 */

const SITES = [makeSite({ id: SITE_A.site_id, name: SITE_A.site_name })]

function fleet(overrides: Partial<Terminal> = {}): Terminal[] {
  return [
    makeTerminal({
      serial_number: 'AT-0001',
      device_name: 'North Gate',
      site_public_id: SITE_A.site_id,
      site_name: SITE_A.site_name,
      status: 'ONLINE',
      active: true,
      ...overrides,
    }),
  ]
}

function signIn(role: Role = 'ADMIN', overrides: Partial<Terminal> = {}) {
  const session = makeSession({
    role,
    operator: { id: 'operator-1', email: 'ops@example.com', full_name: 'Ops', role },
  })
  resetServerState(session)
  resetTerminalModes()
  setCsrfToken(session.csrf_token)
  seed({ sites: SITES, terminals: fleet(overrides) })
  return session
}

function renderTerminal(serial = 'AT-0001') {
  const router = createMemoryRouter(
    [
      { path: '/terminals', element: <TerminalsListPage /> },
      { path: '/terminals/:serial', element: <TerminalDetailPage /> },
    ],
    { initialEntries: [`/terminals/${serial}`] },
  )
  return renderWithSession(<RouterProvider router={router} />, makeTestQueryClient())
}

function renderList() {
  const router = createMemoryRouter(
    [
      { path: '/terminals', element: <TerminalsListPage /> },
      { path: '/terminals/:serial', element: <TerminalDetailPage /> },
    ],
    { initialEntries: ['/terminals'] },
  )
  return renderWithSession(<RouterProvider router={router} />, makeTestQueryClient())
}

function dialog() {
  return screen.getByRole('dialog')
}

async function openRelease() {
  const user = userEvent.setup()
  await screen.findByRole('heading', { name: 'Lifecycle' })
  await user.click(screen.getByRole('button', { name: /^release$/i }))
  return user
}

beforeEach(() => setCsrfToken(null))

// ---------------------------------------------------------------------------
// The capability gate
// ---------------------------------------------------------------------------

describe('the automated workflow is gated on terminal_release', () => {
  it('offers the automated release to a capable terminal and says what it does', async () => {
    signIn()
    renderTerminal()
    await openRelease()

    const d = within(dialog())
    expect(d.getByRole('heading', { name: /release north gate for transfer/i })).toBeInTheDocument()
    expect(d.getByText(/stops letting anyone in now/i)).toBeInTheDocument()
    expect(d.getByText(/erases every member and fingerprint template/i)).toBeInTheDocument()
    expect(d.getByText(/your history stays in this account/i)).toBeInTheDocument()
    expect(d.getByRole('button', { name: /^release terminal$/i })).toBeDisabled()
    // No physical procedure is shown to somebody whose unit does it itself.
    expect(d.queryByText(/usb port/i)).not.toBeInTheDocument()
  })

  it('offers only the physical procedure to a terminal that never reported it', async () => {
    signIn('ADMIN', { capabilities: ['wifi_provisioning', 'wifi_recovery', 'terminal_announce'] })
    renderTerminal()
    await openRelease()

    const d = within(dialog())
    expect(d.getByText(/cannot carry out a release on its own/i)).toBeInTheDocument()
    expect(d.getByText(/usb port/i)).toBeInTheDocument()
    expect(d.getByRole('button', { name: /order the release/i })).toBeInTheDocument()
    expect(d.queryByRole('button', { name: /^release terminal$/i })).not.toBeInTheDocument()
  })

  it('treats a terminal that has never reported capabilities as incapable', async () => {
    signIn('ADMIN', { capabilities: undefined })
    renderTerminal()
    await screen.findByRole('heading', { name: 'Lifecycle' })
    expect(screen.getByText(/has to be wiped at the unit/i)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Ordering, and what the page does afterwards
// ---------------------------------------------------------------------------

describe('ordering a release', () => {
  it('requires the serial to be typed, sends the reason, and follows the order', async () => {
    signIn()
    renderTerminal()
    const user = await openRelease()

    const confirm = within(dialog()).getByRole('button', { name: /^release terminal$/i })
    expect(confirm).toBeDisabled()
    await user.type(within(dialog()).getByLabelText(/reason/i), 'sold to the depot next door')
    await user.type(within(dialog()).getByLabelText(/type .* to confirm/i), 'AT-0001')
    expect(confirm).toBeEnabled()
    await user.click(confirm)

    // The order reached the server.
    await waitFor(() =>
      expect(
        state.requests.some(
          (entry) => entry.method === 'POST' && entry.url.endsWith('/terminals/AT-0001/release'),
        ),
      ).toBe(true),
    )

    // The banner, with both escalations, and the Release control now inert.
    await screen.findByText(/this terminal is being released for transfer/i)
    expect(screen.getAllByText(/waiting for the terminal to confirm/i).length).toBeGreaterThan(0)
    expect(screen.getByRole('button', { name: /cancel release/i })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /release anyway/i })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /releasing…/i })).toBeDisabled()
  })

  it('shows the banner for a release ordered elsewhere, and the list marks the row', async () => {
    signIn('ADMIN', {
      release: {
        state: 'ORDERED',
        ordered_at: '2026-09-12T09:00:00Z',
        ordered_by_email: 'colleague@example.com',
        order_verifiable: true,
      },
    })
    renderList()
    expect(await screen.findByText('Releasing')).toBeInTheDocument()
  })
})

describe('cancelling a release', () => {
  it('withdraws the order and warns that a started wipe cannot be undone', async () => {
    signIn()
    renderTerminal()
    const user = await openRelease()
    await user.type(within(dialog()).getByLabelText(/type .* to confirm/i), 'AT-0001')
    await user.click(within(dialog()).getByRole('button', { name: /^release terminal$/i }))
    await screen.findByText(/this terminal is being released for transfer/i)

    await user.click(screen.getByRole('button', { name: /cancel release/i }))
    expect(within(dialog()).getByText(/already started/i)).toBeInTheDocument()
    await user.click(within(dialog()).getByRole('button', { name: /cancel the release/i }))

    await waitFor(() =>
      expect(screen.queryByText(/this terminal is being released for transfer/i)).not.toBeInTheDocument(),
    )
    expect(screen.getByRole('button', { name: /^release$/i })).toBeEnabled()
  })
})

// ---------------------------------------------------------------------------
// The force
// ---------------------------------------------------------------------------

describe('releasing anyway', () => {
  it('requires the word RELEASE, states the consequence, and leaves the page', async () => {
    signIn()
    renderTerminal()
    const user = await openRelease()
    await user.type(within(dialog()).getByLabelText(/type .* to confirm/i), 'AT-0001')
    await user.click(within(dialog()).getByRole('button', { name: /^release terminal$/i }))
    await screen.findByText(/this terminal is being released for transfer/i)

    await user.click(screen.getByRole('button', { name: /release anyway/i }))
    const d = within(dialog())
    expect(d.getByText(/keeps working for your members/i)).toBeInTheDocument()
    const confirm = d.getByRole('button', { name: /^release anyway$/i })
    expect(confirm).toBeDisabled()
    await user.type(d.getByLabelText(/type .* to confirm/i), 'RELEASE')
    expect(confirm).toBeEnabled()
    await user.click(confirm)

    // The attestation is what the server requires; the fixture 400s without
    // it, so reaching the list below is the proof it was sent.
    await waitFor(() =>
      expect(state.requests.some((entry) => entry.url.endsWith('/release/force'))).toBe(true),
    )

    // Back on the fleet list, with the released unit gone from it.
    await screen.findByRole('heading', { name: /^terminals$/i, level: 1 })
    await waitFor(() => expect(screen.queryByText('AT-0001')).not.toBeInTheDocument())
  })
})

// ---------------------------------------------------------------------------
// Readiness
// ---------------------------------------------------------------------------

describe('readiness', () => {
  it('shows Setting up until the roster snapshot is acknowledged', async () => {
    signIn('ADMIN', { readiness: { state: 'SETTING_UP', job_id: 12 } })
    renderTerminal()
    expect(await screen.findByRole('heading', { name: /^setting up$/i })).toBeInTheDocument()
    expect(screen.getByText(/loading its roster from this account/i)).toBeInTheDocument()
  })

  it('marks a row Setting up on the list', async () => {
    signIn('ADMIN', { readiness: { state: 'SETTING_UP' } })
    renderList()
    expect(await screen.findByText('Setting up')).toBeInTheDocument()
  })

  it('says nothing on the list for a ready terminal', async () => {
    signIn()
    renderList()
    await screen.findByText('AT-0001')
    expect(screen.queryByText('Setting up')).not.toBeInTheDocument()
    expect(screen.queryByText('Releasing')).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Roles
// ---------------------------------------------------------------------------

describe('role gating mirrors the server', () => {
  it('disables Release for a MANAGER', async () => {
    signIn('MANAGER')
    renderTerminal()
    await screen.findByRole('heading', { name: 'Lifecycle' })
    expect(screen.getByRole('button', { name: /^release$/i })).toBeDisabled()
  })

  it('shows a VIEWER the banner without its controls', async () => {
    signIn('VIEWER', {
      release: { state: 'ORDERED', ordered_at: '2026-09-12T09:00:00Z', order_verifiable: true },
    })
    renderTerminal()
    await screen.findByText(/this terminal is being released for transfer/i)
    expect(screen.queryByRole('button', { name: /cancel release/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /release anyway/i })).not.toBeInTheDocument()
  })
})
