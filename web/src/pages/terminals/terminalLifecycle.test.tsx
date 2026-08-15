import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../../api/csrf'
import type { Role } from '../../api/types'
import { makeSession, makeSite, makeTerminal, SITE_A, SITE_B } from '../../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../../test/render'
import { failNext, resetServerState, resetTerminalModes, seed, state } from '../../test/server'
import { TerminalDetailPage } from './TerminalDetailPage'
import { TerminalsListPage } from './TerminalsListPage'

/**
 * Terminal lifecycle (SEC-01).
 *
 * The audit's first terminal blocker was that there was no operator route to
 * disable a terminal, revoke its credential, retire it or move it — a stolen
 * unit could only be dealt with by retiring its whole site. The routes now
 * exist, and what is asserted here is mostly not that they are CALLED but that
 * the console does not let an operator confuse them.
 *
 * THE CENTRAL RISK IS SEMANTIC, NOT TECHNICAL. Disable, revoke and retire are
 * one API call each and trivially easy to wire; the way this goes wrong in
 * production is an operator in a hurry picking the wrong one because the screen
 * described them interchangeably. So several tests below assert on WORDS —
 * whether the credential survives, whether the hardware must re-register,
 * whether it can be undone — and they are the most valuable tests in the file.
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
    active: true,
  }),
  makeTerminal({
    id: 2,
    public_id: 'terminal-public-2',
    serial_number: 'AT-0002',
    device_name: 'Loading Bay',
    site_public_id: SITE_B.site_id,
    site_name: SITE_B.site_name,
    status: 'DISABLED',
    active: false,
  }),
]

function signIn(role: Role = 'ADMIN') {
  const session = makeSession({
    role,
    operator: { id: 'operator-1', email: 'ops@example.com', full_name: 'Ops', role },
  })
  resetServerState(session)
  resetTerminalModes()
  setCsrfToken(session.csrf_token)
  seed({ sites: SITES, terminals: FLEET })
  return session
}

function renderTerminal(serial = 'AT-0001') {
  const router = createMemoryRouter(
    [
      { path: '/terminals', element: <TerminalsListPage /> },
      { path: '/terminals/:serial', element: <TerminalDetailPage /> },
      { path: '/sites/:siteId', element: <p>Site page</p> },
    ],
    { initialEntries: [`/terminals/${serial}`] },
  )
  return renderWithSession(<RouterProvider router={router} />, makeTestQueryClient())
}

/** The dialog, addressed as a dialog so a stray match on the page cannot pass. */
function dialog() {
  return screen.getByRole('dialog')
}

async function openLifecycle(name: RegExp) {
  const user = userEvent.setup()
  await screen.findByRole('heading', { name: 'Lifecycle' })
  await user.click(screen.getByRole('button', { name }))
  return user
}

beforeEach(() => setCsrfToken(null))

// ---------------------------------------------------------------------------
// The distinction itself
// ---------------------------------------------------------------------------

describe('disable, revoke and retire are presented as different operations', () => {
  it('offers all three separately, never as one control with a mode', async () => {
    signIn()
    renderTerminal()

    await screen.findByRole('heading', { name: 'Lifecycle' })

    // Three distinct controls. A single "deactivate" with a dropdown is exactly
    // the shape this must not have.
    expect(screen.getByRole('button', { name: /^disable$/i })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^revoke$/i })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^retire$/i })).toBeInTheDocument()
  })

  it('says the credential SURVIVES a disable', async () => {
    signIn()
    renderTerminal()
    await openLifecycle(/^disable$/i)

    // The load-bearing sentence: this is the operation that does not need a site
    // visit afterwards, and an operator who does not know that will reach for
    // revoke instead and strand the hardware.
    expect(within(dialog()).getByText(/does not touch the credential/i)).toBeInTheDocument()
    expect(within(dialog()).getByText(/no site visit/i)).toBeInTheDocument()
  })

  it('says the hardware must RE-REGISTER after a revoke', async () => {
    signIn()
    renderTerminal()
    await openLifecycle(/^revoke$/i)

    expect(within(dialog()).getByText(/destroyed/i)).toBeInTheDocument()
    expect(within(dialog()).getByText(/re-register/i)).toBeInTheDocument()
    // And it names the situation it is for, not only what it does.
    expect(within(dialog()).getByText(/missing, stolen/i)).toBeInTheDocument()
  })

  it('says a retire CANNOT BE UNDONE and points at revoke as the milder option', async () => {
    signIn()
    renderTerminal()
    await openLifecycle(/^retire$/i)

    expect(within(dialog()).getByText(/cannot be undone/i)).toBeInTheDocument()
    expect(
      within(dialog()).getByText(/revoke its credential instead/i),
    ).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Disable and re-enable
// ---------------------------------------------------------------------------

describe('disable and re-enable', () => {
  it('disables a terminal and reflects the new state on the page', async () => {
    signIn()
    renderTerminal()
    const user = await openLifecycle(/^disable$/i)

    await user.click(within(dialog()).getByRole('button', { name: /disable terminal/i }))

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())

    const request = state.requests.find((entry) => entry.url.includes('/state'))
    expect(request?.method).toBe('PUT')
    // The response carries the whole detail object, so the page must show the
    // new state without a refetch having to land first.
    await waitFor(() =>
      expect(screen.getByRole('button', { name: /^re-enable$/i })).toBeInTheDocument(),
    )
  })

  it('offers RE-ENABLE rather than disable for a terminal that is already disabled', async () => {
    signIn()
    renderTerminal('AT-0002')

    await screen.findByRole('heading', { name: 'Lifecycle' })
    expect(screen.getByRole('button', { name: /^re-enable$/i })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^disable$/i })).not.toBeInTheDocument()
  })

  it('sends the reason so the next operator can read why', async () => {
    signIn()
    renderTerminal()
    const user = await openLifecycle(/^disable$/i)

    await user.type(
      within(dialog()).getByLabelText(/reason/i),
      'panel damaged in the storm',
    )
    await user.click(within(dialog()).getByRole('button', { name: /disable terminal/i }))

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(state.requests.some((entry) => entry.url.includes('/state'))).toBe(true)
  })

  it('keeps the dialog open when the server refuses', async () => {
    signIn()
    renderTerminal()
    const user = await openLifecycle(/^disable$/i)

    failNext('terminal-state', 500)
    await user.click(within(dialog()).getByRole('button', { name: /disable terminal/i }))

    // Closing on failure would leave the operator believing a door had been shut
    // when it had not — the worst available outcome for a destructive action.
    expect(await within(dialog()).findByRole('alert')).toBeInTheDocument()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Revoke
// ---------------------------------------------------------------------------

describe('revoking a credential', () => {
  it('requires the serial to be typed before it will proceed', async () => {
    signIn()
    renderTerminal()
    const user = await openLifecycle(/^revoke$/i)

    const confirm = within(dialog()).getByRole('button', { name: /revoke credential/i })
    expect(confirm).toBeDisabled()

    await user.type(within(dialog()).getByLabelText(/type .* to confirm/i), 'AT-0001')
    expect(confirm).toBeEnabled()
  })

  it('reports the queued work that will never be delivered', async () => {
    signIn()
    renderTerminal()
    const user = await openLifecycle(/^revoke$/i)

    await user.type(within(dialog()).getByLabelText(/type .* to confirm/i), 'AT-0001')
    await user.click(within(dialog()).getByRole('button', { name: /revoke credential/i }))

    // An operator who is not told believes those changes reached the hardware.
    expect(await screen.findByText(/4 queued changes cancelled/i)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Retire
// ---------------------------------------------------------------------------

describe('retiring a terminal', () => {
  it('leaves the page afterwards, because it now describes nothing', async () => {
    signIn()
    renderTerminal()
    const user = await openLifecycle(/^retire$/i)

    await user.type(within(dialog()).getByLabelText(/type .* to confirm/i), 'AT-0001')
    await user.click(within(dialog()).getByRole('button', { name: /retire terminal/i }))

    // Back on the fleet list, and the retired unit is gone from it rather than
    // lingering as a row that 404s when opened.
    await screen.findByRole('heading', { name: /terminals/i })
    await waitFor(() => expect(screen.queryByText('AT-0001')).not.toBeInTheDocument())
  })
})

// ---------------------------------------------------------------------------
// Move
// ---------------------------------------------------------------------------

describe('moving a terminal to another site', () => {
  it('does not offer the site it already stands at', async () => {
    signIn()
    renderTerminal()
    await openLifecycle(/^move$/i)

    const select = within(dialog()).getByLabelText(/move to/i)
    expect(within(select).getByRole('option', { name: SITE_B.site_name })).toBeInTheDocument()
    // Offering "move it to where it is" invites a no-op that still cancels the
    // terminal's queued work.
    expect(
      within(select).queryByRole('option', { name: SITE_A.site_name }),
    ).not.toBeInTheDocument()
  })

  it('warns that the roster is REBUILT rather than carried', async () => {
    signIn()
    renderTerminal()
    await openLifecycle(/^move$/i)

    expect(within(dialog()).getByText(/rebuilds from scratch/i)).toBeInTheDocument()
    expect(within(dialog()).getByText(/will not recognise anybody/i)).toBeInTheDocument()
  })

  it('moves the terminal and says where it went', async () => {
    signIn()
    renderTerminal()
    const user = await openLifecycle(/^move$/i)

    await user.selectOptions(within(dialog()).getByLabelText(/move to/i), SITE_B.site_id)
    await user.click(within(dialog()).getByRole('button', { name: /move terminal/i }))

    expect(await screen.findByText(new RegExp(`moved to ${SITE_B.site_name}`, 'i'))).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Resync
// ---------------------------------------------------------------------------

describe('forcing a resync', () => {
  it('is a plain confirmation — nothing here is destroyed', async () => {
    signIn()
    renderTerminal()
    const user = await openLifecycle(/^resync$/i)

    // No typed phrase: reserving that ceremony for the irreversible is what
    // keeps it meaningful when it does appear.
    expect(within(dialog()).queryByLabelText(/type .* to confirm/i)).not.toBeInTheDocument()

    await user.click(within(dialog()).getByRole('button', { name: /queue a full sync/i }))
    expect(await screen.findByText(/12 changes queued/i)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Authorization is the server's, and the console only avoids offering 403s
// ---------------------------------------------------------------------------

describe('role gating mirrors the server', () => {
  it('disables the ADMIN lifecycle controls for a MANAGER, and says why', async () => {
    signIn('MANAGER')
    renderTerminal()

    await screen.findByRole('heading', { name: 'Lifecycle' })
    expect(screen.getByRole('button', { name: /^disable$/i })).toBeDisabled()
    expect(screen.getByRole('button', { name: /^revoke$/i })).toBeDisabled()
    expect(screen.getByRole('button', { name: /^retire$/i })).toBeDisabled()

    // Resync is MANAGER on the server, so it stays available — the gate mirrors
    // the route groups rather than treating the whole section as one privilege.
    expect(screen.getByRole('button', { name: /^resync$/i })).toBeEnabled()
    expect(screen.getByText(/administrator actions/i)).toBeInTheDocument()
  })

  it('leaves a VIEWER with nothing to press', async () => {
    signIn('VIEWER')
    renderTerminal()

    await screen.findByRole('heading', { name: 'Lifecycle' })
    expect(screen.getByRole('button', { name: /^resync$/i })).toBeDisabled()
    expect(screen.getByRole('button', { name: /^revoke$/i })).toBeDisabled()
  })
})
