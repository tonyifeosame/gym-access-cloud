import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../../api/csrf'
import {
  MULTI_PURPOSE,
  type Role,
  type Terminal,
  type TerminalCapability,
  type TerminalDetail,
  type TerminalRelease,
} from '../../api/types'
import { keys } from '../../data/keys'
import { makeSession, makeSite, makeTerminal, SITE_A } from '../../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../../test/render'
import {
  completeRelease,
  resetServerState,
  resetTerminalModes,
  revokeCredentialOf,
  seed,
  state,
} from '../../test/server'
import { releasePathFor } from './releasePath'
import { TerminalDetailPage } from './TerminalDetailPage'
import { TerminalsListPage } from './TerminalsListPage'

/**
 * Release for transfer (032).
 *
 * What the console has to get right, in order of consequence:
 *
 *   - the automated workflow is offered ONLY to a terminal whose firmware
 *     reported `terminal_release` AND whose credential the order can be keyed
 *     with; every other unit is told the path that actually works for it —
 *     a firmware update, a wipe at the unit's own console, or re-registration
 *     — and that Release anyway is not a substitute for any of them. The old
 *     copy sent the holder of an old-firmware unit to a console command that
 *     firmware does not have.
 *   - an offline unit is not told it "stops letting anyone in now": it stops
 *     when it receives the order, and keeps working under its offline policy
 *     until then
 *   - ordering needs the serial typed; forcing needs the word RELEASE typed
 *     and states, in the platform's own words, that the unit keeps working
 *     for this company's members until it reconnects or is wiped
 *   - the page follows the order: a banner while it is outstanding, Cancel
 *     and Release anyway on it, the list marks the row Releasing — and when
 *     the terminal confirms, the 404 that follows is read as COMPLETION, said
 *     so, and the page left; it was previously read as "still waiting"
 *   - a cancel or force that arrives after the terminal already confirmed is
 *     told so, not shown "Terminal not found"
 *   - Setting up is shown until the roster snapshot is acknowledged
 *   - role gating mirrors the server: ADMIN and above only
 */

const SITES = [makeSite({ id: SITE_A.site_id, name: SITE_A.site_name })]

const CAPABLE: TerminalCapability[] = [
  'wifi_provisioning',
  'wifi_recovery',
  'terminal_announce',
  'terminal_release',
]
const OLD_FIRMWARE: TerminalCapability[] = ['wifi_provisioning', 'wifi_recovery', 'terminal_announce']

function fleet(overrides: Partial<Terminal> = {}): Terminal[] {
  return [
    makeTerminal({
      serial_number: 'AT-0001',
      device_name: 'North Gate',
      site_public_id: SITE_A.site_id,
      site_name: SITE_A.site_name,
      status: 'ONLINE',
      active: true,
      firmware_outdated: false,
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
  await screen.findByRole('heading', { name: 'Actions' })
  await user.click(screen.getByRole('button', { name: /^release$/i }))
  return user
}

/** Orders the release through the dialog and waits for the banner. */
async function orderRelease(serial = 'AT-0001') {
  const user = await openRelease()
  await user.type(within(dialog()).getByLabelText(/type .* to confirm/i), serial)
  await user.click(within(dialog()).getByRole('button', { name: /^(release terminal|order the release)$/i }))
  await screen.findByText(/this terminal is being released for transfer/i)
  return user
}

beforeEach(() => setCsrfToken(null))

// ---------------------------------------------------------------------------
// The path resolver
// ---------------------------------------------------------------------------

describe('releasePathFor', () => {
  const capable: TerminalDetail = {
    ...makeTerminal({ capabilities: CAPABLE }),
    application_mode: MULTI_PURPOSE,
    effective_applications: [],
  }
  const old: TerminalDetail = { ...capable, capabilities: OLD_FIRMWARE }
  const revoked = (t: TerminalDetail): TerminalDetail => ({
    ...t,
    health: {
      pending_jobs: 0,
      failed_jobs: 0,
      credential_active: false,
      offline_policy: 'DENY_ALL',
      offline_grace_minutes: 0,
    },
  })
  const facts = (over: Partial<TerminalRelease>): TerminalRelease => ({
    serial_number: 'AT-0001',
    state: '',
    terminal_capable: true,
    order_verifiable: false,
    ...over,
  })

  it('offers the automated path only to capable firmware with a live credential', () => {
    expect(releasePathFor(capable, undefined)).toBe('automated')
    expect(releasePathFor(old, undefined)).toBe('update-firmware')
    expect(releasePathFor(revoked(capable), undefined)).toBe('wipe-at-unit')
    expect(releasePathFor(revoked(old), undefined)).toBe('no-remote-path')
  })

  it('reads a missing capability list as incapable and a missing health block as credentialed', () => {
    expect(releasePathFor({ ...capable, capabilities: undefined }, undefined)).toBe('update-firmware')
  })

  it('lets order_verifiable overrule the detail once an order exists', () => {
    // The order was keyed with no credential: unverifiable, whatever the detail said.
    expect(releasePathFor(capable, facts({ state: 'ORDERED', order_verifiable: false }))).toBe(
      'wipe-at-unit',
    )
    // Before an order there is no MAC, so a false there means nothing yet.
    expect(releasePathFor(capable, facts({ state: '', order_verifiable: false }))).toBe('automated')
    // The release facts' capability is the server's reading and wins over the row.
    expect(
      releasePathFor(old, facts({ state: 'ORDERED', terminal_capable: true, order_verifiable: true })),
    ).toBe('automated')
  })
})

// ---------------------------------------------------------------------------
// The capability gate
// ---------------------------------------------------------------------------

describe('the automated workflow is gated on terminal_release', () => {
  it('offers the automated release to a capable terminal and says what it does', async () => {
    signIn('ADMIN', { capabilities: CAPABLE })
    renderTerminal()
    await openRelease()

    const d = within(dialog())
    expect(d.getByRole('heading', { name: /release north gate for transfer/i })).toBeInTheDocument()
    expect(d.getByText(/the moment it receives the release order/i)).toBeInTheDocument()
    expect(d.getByText(/erases every member and fingerprint template/i)).toBeInTheDocument()
    expect(d.getByText(/your history stays in this account/i)).toBeInTheDocument()
    expect(d.getByRole('button', { name: /^release terminal$/i })).toBeDisabled()
    // No console procedure is shown to somebody whose unit does it itself.
    expect(d.queryByText(/usb port/i)).not.toBeInTheDocument()
    expect(d.queryByText(/update its firmware/i)).not.toBeInTheDocument()
  })

  it('sends an old-firmware unit to a firmware update, never to a console command', async () => {
    signIn('ADMIN', { capabilities: OLD_FIRMWARE, firmware_outdated: true })
    renderTerminal()
    await openRelease()

    const d = within(dialog())
    expect(d.getByText(/cannot carry out a release/i)).toBeInTheDocument()
    expect(d.getByText(/update its firmware first/i)).toBeInTheDocument()
    expect(d.getByRole('link', { name: /^firmware$/i })).toHaveAttribute('href', '/settings/firmware')
    // The order is held for the unit and delivered once it can act on it.
    expect(d.getByText(/holds the order for it/i)).toBeInTheDocument()
    // Release anyway is on the same page and reads like the shortcut. It is not.
    expect(d.getByText(/release anyway is not a substitute/i)).toBeInTheDocument()
    // The old copy: a command this firmware does not have.
    expect(d.queryByText(/usb port/i)).not.toBeInTheDocument()
    expect(d.queryByText(/type release/i)).not.toBeInTheDocument()
    expect(d.getByRole('button', { name: /order the release/i })).toBeInTheDocument()
    expect(d.queryByRole('button', { name: /^release terminal$/i })).not.toBeInTheDocument()
  })

  it('tells the holder of a unit already on the current build that a capable build must be published', async () => {
    signIn('ADMIN', { capabilities: OLD_FIRMWARE, firmware_outdated: false })
    renderTerminal()
    await openRelease()
    expect(within(dialog()).getByText(/has to be published/i)).toBeInTheDocument()
  })

  it('treats a terminal that has never reported capabilities as incapable', async () => {
    signIn('ADMIN', { capabilities: undefined })
    renderTerminal()
    await screen.findByRole('heading', { name: 'Actions' })
    expect(screen.getByText(/needs a firmware update first/i)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// A credential the order cannot be keyed with
// ---------------------------------------------------------------------------

describe('a terminal without a credential is never offered the automated flow', () => {
  it('gives capable firmware the wipe at its own console, then Release anyway', async () => {
    signIn('ADMIN', { capabilities: CAPABLE })
    revokeCredentialOf('AT-0001')
    renderTerminal()
    const user = await openRelease()

    const d = within(dialog())
    expect(d.getByText(/no credential/i)).toBeInTheDocument()
    expect(d.getByText(/usb port/i)).toBeInTheDocument()
    expect(d.queryByText(/waiting for the terminal/i)).not.toBeInTheDocument()

    await user.type(d.getByLabelText(/type .* to confirm/i), 'AT-0001')
    await user.click(d.getByRole('button', { name: /order the release/i }))

    // The banner reads the server's order_verifiable, not the dialog's guess.
    await screen.findByText(/this terminal is being released for transfer/i)
    expect(screen.getByText(/the order cannot reach it/i)).toBeInTheDocument()
    expect(screen.queryByText(/waiting for the terminal to confirm/i)).not.toBeInTheDocument()
  })

  it('refuses to order anything for old firmware with no credential, and says what to do instead', async () => {
    signIn('ADMIN', { capabilities: OLD_FIRMWARE })
    revokeCredentialOf('AT-0001')
    renderTerminal()
    const user = await openRelease()

    const d = within(dialog())
    expect(d.getByText(/neither told to wipe nor updated/i)).toBeInTheDocument()
    expect(d.getByText(/claim code/i)).toBeInTheDocument()
    expect(d.getByText(/this cannot be confirmed yet/i)).toBeInTheDocument()
    await user.type(d.getByLabelText(/type .* to confirm/i), 'AT-0001')
    expect(d.getByRole('button', { name: /order the release/i })).toBeDisabled()
  })
})

// ---------------------------------------------------------------------------
// Ordering, and what the page does afterwards
// ---------------------------------------------------------------------------

describe('ordering a release', () => {
  it('requires the serial to be typed, sends the reason, and follows the order', async () => {
    signIn('ADMIN', { capabilities: CAPABLE })
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
    await waitFor(() => expect(screen.getByRole('button', { name: /release anyway/i })).toBeEnabled())
    expect(screen.getByRole('button', { name: /releasing…/i })).toBeDisabled()
  })

  it('does not tell the holder of an offline unit that it stops now', async () => {
    signIn('ADMIN', { capabilities: CAPABLE, status: 'OFFLINE' })
    renderTerminal()
    const user = await openRelease()

    const d = within(dialog())
    expect(d.getByText(/nothing changes at the door yet/i)).toBeInTheDocument()
    expect(d.getByText(/keeps working under its site.s offline policy/i)).toBeInTheDocument()
    expect(d.queryByText(/stops letting anyone in now/i)).not.toBeInTheDocument()

    await user.type(d.getByLabelText(/type .* to confirm/i), 'AT-0001')
    await user.click(d.getByRole('button', { name: /^release terminal$/i }))
    await screen.findByText(/this terminal is being released for transfer/i)
    expect(screen.getByText(/it is offline right now/i)).toBeInTheDocument()
  })

  it('holds the order for an old-firmware unit and says so on the banner', async () => {
    signIn('ADMIN', { capabilities: OLD_FIRMWARE, firmware_outdated: true })
    renderTerminal()
    await orderRelease()

    expect(screen.getByText(/the order is being held for it/i)).toBeInTheDocument()
    expect(screen.getByText(/release anyway does not wipe the unit/i)).toBeInTheDocument()
    expect(screen.queryByText(/usb console/i)).not.toBeInTheDocument()
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

// ---------------------------------------------------------------------------
// The end of the release
// ---------------------------------------------------------------------------

describe('when the terminal confirms', () => {
  it('reads the 404 as completion, says so, and leaves the page', async () => {
    signIn('ADMIN', { capabilities: CAPABLE })
    const { queryClient } = renderTerminal()
    await orderRelease()

    // The terminal executes the order and sends its receipt; the row is gone.
    // The poll that was answering ORDERED now answers 404.
    completeRelease('AT-0001')
    await queryClient.refetchQueries({ queryKey: keys.terminals.release('AT-0001') })

    expect(await screen.findByText(/north gate has been released/i)).toBeInTheDocument()
    expect(screen.getByText(/the terminal confirmed the wipe/i)).toBeInTheDocument()
    // Back on the fleet list, with the released unit gone from it.
    await screen.findByRole('heading', { name: /^terminals$/i, level: 1 })
    await waitFor(() => expect(screen.queryByText('AT-0001')).not.toBeInTheDocument())
    expect(screen.queryByText(/terminal not found/i)).not.toBeInTheDocument()
    // The banner is gone with the page; the order's own toast may still be fading.
    expect(screen.queryByText(/this terminal is being released for transfer/i)).not.toBeInTheDocument()
  })

  it('tells a force that arrives too late that the release already completed', async () => {
    signIn('ADMIN', { capabilities: CAPABLE })
    renderTerminal()
    const user = await orderRelease()
    await waitFor(() => expect(screen.getByRole('button', { name: /release anyway/i })).toBeEnabled())
    await user.click(screen.getByRole('button', { name: /release anyway/i }))
    await user.type(within(dialog()).getByLabelText(/type .* to confirm/i), 'RELEASE')

    completeRelease('AT-0001')
    await user.click(within(dialog()).getByRole('button', { name: /^release anyway$/i }))

    expect(await screen.findByText(/had already been released/i)).toBeInTheDocument()
    expect(screen.queryByText(/terminal not found/i)).not.toBeInTheDocument()
    await screen.findByRole('heading', { name: /^terminals$/i, level: 1 })
  })

  it('tells a cancel that arrives too late the same thing', async () => {
    signIn('ADMIN', { capabilities: CAPABLE })
    renderTerminal()
    const user = await orderRelease()
    await user.click(screen.getByRole('button', { name: /cancel release/i }))

    completeRelease('AT-0001')
    await user.click(within(dialog()).getByRole('button', { name: /cancel the release/i }))

    expect(await screen.findByText(/before the cancellation reached it/i)).toBeInTheDocument()
    expect(screen.queryByText(/terminal not found/i)).not.toBeInTheDocument()
    await screen.findByRole('heading', { name: /^terminals$/i, level: 1 })
  })

  it('names a completed release on the not-found page a fresh load lands on', async () => {
    signIn()
    renderTerminal('AT-9999')
    expect(await screen.findByText(/released to another owner/i)).toBeInTheDocument()
  })
})

describe('cancelling a release', () => {
  it('withdraws the order and warns that a started wipe cannot be undone', async () => {
    signIn('ADMIN', { capabilities: CAPABLE })
    renderTerminal()
    const user = await orderRelease()

    await user.click(screen.getByRole('button', { name: /cancel release/i }))
    expect(within(dialog()).getByText(/not yet acted/i)).toBeInTheDocument()
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
    signIn('ADMIN', { capabilities: CAPABLE })
    renderTerminal()
    const user = await orderRelease()

    await waitFor(() => expect(screen.getByRole('button', { name: /release anyway/i })).toBeEnabled())
    await user.click(screen.getByRole('button', { name: /release anyway/i }))
    const d = within(dialog())
    expect(d.getByText(/keeps working for your members/i)).toBeInTheDocument()
    // The two consequences the server's sentence leaves out.
    expect(d.getByText(/never uploaded/i)).toBeInTheDocument()
    expect(d.getByText('AT-0001')).toBeInTheDocument()
    const confirm = d.getByRole('button', { name: /^release anyway$/i })
    expect(confirm).toBeDisabled()
    await user.type(d.getByLabelText(/type .* to confirm/i), 'RELEASE')
    expect(confirm).toBeEnabled()
    await user.click(confirm)

    // The attestation is what the server requires; the fixture 400s without
    // it, and 409s unless the request names the very order the page showed,
    // so reaching the list below is the proof that both were sent.
    await waitFor(() =>
      expect(state.requests.some((entry) => entry.url.endsWith('/release/force'))).toBe(true),
    )

    // Back on the fleet list, with the released unit gone from it.
    await screen.findByRole('heading', { name: /^terminals$/i, level: 1 })
    await waitFor(() => expect(screen.queryByText('AT-0001')).not.toBeInTheDocument())
  })

  it('tells the holder of an old-firmware unit that forcing does not wipe it', async () => {
    signIn('ADMIN', { capabilities: OLD_FIRMWARE, firmware_outdated: true })
    renderTerminal()
    const user = await orderRelease()
    await waitFor(() => expect(screen.getByRole('button', { name: /release anyway/i })).toBeEnabled())
    await user.click(screen.getByRole('button', { name: /release anyway/i }))
    const d = within(dialog())
    expect(d.getByText(/does not wipe it/i)).toBeInTheDocument()
    expect(d.getByText(/updating its firmware first/i)).toBeInTheDocument()
    expect(d.queryByText(/will find the order and erase itself/i)).not.toBeInTheDocument()
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
    await screen.findByRole('heading', { name: 'Actions' })
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
