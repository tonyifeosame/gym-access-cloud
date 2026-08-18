import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../../api/csrf'
import type { Role, Session } from '../../api/types'
import { makeSession, makeSite, makeTerminal, SITE_A } from '../../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../../test/render'
import {
  advanceWifiRecovery,
  failNext,
  resetServerState,
  resetTerminalModes,
  seed,
  state,
} from '../../test/server'
import { TerminalDetailPage } from './TerminalDetailPage'

/**
 * Change Wi-Fi.
 *
 * WHAT THESE TESTS ARE PROTECTING. The console is one careless success message
 * away from telling a customer their terminal has moved to a new network when it
 * has done nothing at all — the POST only queues a command, and the terminal
 * collects it on its own schedule. So most of what is asserted below is about
 * the screen NOT saying something: not claiming the change happened before the
 * device acknowledged it, not offering the button to somebody the API would
 * refuse, and not hiding the one answer that is actually useful when the
 * terminal is offline, which is the recovery that does not need the network.
 */

const SITES = [makeSite({ id: SITE_A.site_id, name: SITE_A.site_name })]

function terminalNamed(overrides: Partial<ReturnType<typeof makeTerminal>> = {}) {
  return makeTerminal({
    serial_number: 'AT-0001',
    device_name: 'North Gate',
    site_public_id: SITE_A.site_id,
    site_name: SITE_A.site_name,
    status: 'ONLINE',
    ...overrides,
  })
}

function signIn(role: Role = 'ADMIN', overrides: Partial<Session> = {}) {
  const session = makeSession({
    role,
    operator: { id: 'operator-1', email: 'ops@example.com', full_name: 'Ops', role },
    ...overrides,
  })
  resetServerState(session)
  resetTerminalModes()
  setCsrfToken(session.csrf_token)
  seed({ sites: SITES, terminals: [terminalNamed()] })
  return session
}

function renderTerminal(client = makeTestQueryClient()) {
  const router = createMemoryRouter(
    [
      { path: '/terminals/:serial', element: <TerminalDetailPage /> },
      { path: '/terminals', element: <p>Fleet</p> },
      { path: '/sites/:siteId', element: <p>Site page</p> },
      { path: '/settings/firmware', element: <p>Firmware</p> },
    ],
    { initialEntries: ['/terminals/AT-0001'] },
  )
  return renderWithSession(<RouterProvider router={router} />, client)
}

/**
 * How long a poll-driven assertion waits.
 *
 * LONGER THAN THE DIALOG'S POLL INTERVAL, deliberately. The screen learns what
 * the terminal did by asking again, not by being told, so a test asserting on
 * that transition has to outlast one interval — and shortening the interval to
 * suit the tests would be tuning the product to the harness.
 */
const POLLED = { timeout: 6_000 }

/** Opens Terminal → Network → Change Wi-Fi and lands on the confirmation. */
async function openChangeWifi(user: ReturnType<typeof userEvent.setup>) {
  const network = await screen.findByRole('region', { name: 'Network' })
  await user.click(within(network).getByRole('button', { name: 'Change Wi-Fi' }))
  return screen.findByRole('dialog')
}

beforeEach(() => {
  resetServerState()
})

// ---------------------------------------------------------------------------
// Who sees it
// ---------------------------------------------------------------------------

describe('who may change a terminal’s Wi-Fi', () => {
  // The console is not an authorization boundary — the API refuses anything this
  // wrongly permitted. What it must not do is offer a control that only produces
  // a 403, which is a worse experience than not having the control.
  it('offers the action to an administrator', async () => {
    signIn('ADMIN')
    renderTerminal()

    const network = await screen.findByRole('region', { name: 'Network' })
    expect(within(network).getByRole('button', { name: 'Change Wi-Fi' })).toBeEnabled()
  })

  it('offers it to an owner', async () => {
    signIn('OWNER')
    renderTerminal()

    const network = await screen.findByRole('region', { name: 'Network' })
    expect(within(network).getByRole('button', { name: 'Change Wi-Fi' })).toBeEnabled()
  })

  it('refuses a manager, and says who can do it instead', async () => {
    signIn('MANAGER')
    renderTerminal()

    const network = await screen.findByRole('region', { name: 'Network' })
    expect(within(network).getByRole('button', { name: 'Change Wi-Fi' })).toBeDisabled()

    // NOT JUST DISABLED. A dead control with no explanation is a support call;
    // this one names who can do it and offers the path that needs nobody.
    expect(within(network).getByText(/administrator action/i)).toBeInTheDocument()
    expect(within(network).getByText(/hold the button on the unit/i)).toBeInTheDocument()
  })

  it('refuses a viewer', async () => {
    signIn('VIEWER')
    renderTerminal()

    const network = await screen.findByRole('region', { name: 'Network' })
    expect(within(network).getByRole('button', { name: 'Change Wi-Fi' })).toBeDisabled()
  })
})

// ---------------------------------------------------------------------------
// The confirmation
// ---------------------------------------------------------------------------

describe('the confirmation', () => {
  it('says what will happen and what the customer will need', async () => {
    signIn('ADMIN')
    const user = userEvent.setup()
    renderTerminal()

    const dialog = await openChangeWifi(user)

    expect(within(dialog).getByText('Change Wi-Fi network?')).toBeInTheDocument()
    expect(
      within(dialog).getByText(
        /temporarily enter Wi-Fi setup mode.*phone or computer nearby to connect it to the new Wi-Fi/i,
      ),
    ).toBeInTheDocument()

    expect(within(dialog).getByRole('button', { name: 'Cancel' })).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Continue' })).toBeInTheDocument()
  })

  it('promises what is NOT lost, because that is the fear', async () => {
    signIn('ADMIN')
    const user = userEvent.setup()
    renderTerminal()

    const dialog = await openChangeWifi(user)
    // The firmware's recovery clears the SSID and the pre-shared key and nothing
    // else. An operator who thought this was a factory reset would never press it.
    expect(
      within(dialog).getByText(/keeps its name, its site, the people it recognises/i),
    ).toBeInTheDocument()
  })

  it('sends nothing when the operator cancels', async () => {
    signIn('ADMIN')
    const user = userEvent.setup()
    renderTerminal()

    const dialog = await openChangeWifi(user)
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(
      state.requests.some((request) => request.url.includes('/wifi-recovery')),
    ).toBe(false)
  })

  it('sends the command with the CSRF header when the operator continues', async () => {
    signIn('ADMIN')
    const user = userEvent.setup()
    renderTerminal()

    const dialog = await openChangeWifi(user)
    await user.click(within(dialog).getByRole('button', { name: 'Continue' }))

    await waitFor(() => {
      const sent = state.requests.find(
        (request) => request.method === 'POST' && request.url.includes('/wifi-recovery'),
      )
      expect(sent).toBeDefined()
      expect(sent?.headers.get('X-CSRF-Token')).toBeTruthy()
    })
  })
})

// ---------------------------------------------------------------------------
// After the command is sent
// ---------------------------------------------------------------------------

describe('what the screen says after the command is sent', () => {
  it('waits for the terminal rather than claiming the Wi-Fi changed', async () => {
    signIn('ADMIN')
    const user = userEvent.setup()
    renderTerminal()

    const dialog = await openChangeWifi(user)
    await user.click(within(dialog).getByRole('button', { name: 'Continue' }))

    expect(await within(dialog).findByText('Waiting for terminal…')).toBeInTheDocument()

    // THE ASSERTION THAT MATTERS MOST IN THIS FILE. Queuing a command is not
    // changing a network, and a screen that said so on a successful POST would
    // be reporting its own request back as the door's answer.
    expect(within(dialog).getByText(/Nothing has changed at the terminal yet/i)).toBeInTheDocument()
    expect(within(dialog).queryByText(/acknowledged/i)).not.toBeInTheDocument()
  })

  it('reports the terminal collecting the command as distinct from applying it', async () => {
    signIn('ADMIN')
    const user = userEvent.setup()
    renderTerminal()

    const dialog = await openChangeWifi(user)
    await user.click(within(dialog).getByRole('button', { name: 'Continue' }))
    await within(dialog).findByText('Waiting for terminal…')

    // The hardware polls and picks the command up. It has NOT said it applied it.
    advanceWifiRecovery('AT-0001', 'DELIVERED', { delivered_at: '2026-08-18T09:00:30Z' })

    expect(
      await within(dialog).findByText(/collected the command. Waiting for it to confirm/i, {}, POLLED),
    ).toBeInTheDocument()
    expect(within(dialog).queryByText(/acknowledged it/i)).not.toBeInTheDocument()
  })

  it('only says the terminal has the command once the device acknowledges it', async () => {
    signIn('ADMIN')
    const user = userEvent.setup()
    renderTerminal()

    const dialog = await openChangeWifi(user)
    await user.click(within(dialog).getByRole('button', { name: 'Continue' }))
    await within(dialog).findByText('Waiting for terminal…')

    advanceWifiRecovery('AT-0001', 'ACCEPTED', { acknowledged_at: '2026-08-18T09:00:45Z' })

    expect(
      await within(dialog).findByText(/The terminal acknowledged the command/i, {}, POLLED),
    ).toBeInTheDocument()
    // And the next step, which is a physical one nobody can do from here.
    expect(
      within(dialog).getByText(/connect to the setup network it displays/i),
    ).toBeInTheDocument()
  })

  it('does not claim the terminal has changed network, even when acknowledged', async () => {
    signIn('ADMIN')
    const user = userEvent.setup()
    renderTerminal()

    const dialog = await openChangeWifi(user)
    await user.click(within(dialog).getByRole('button', { name: 'Continue' }))
    await within(dialog).findByText('Waiting for terminal…')

    advanceWifiRecovery('AT-0001', 'ACCEPTED', { acknowledged_at: '2026-08-18T09:00:45Z' })
    await within(dialog).findByText(/The terminal acknowledged the command/i, {}, POLLED)

    // THE TERMINAL ACKNOWLEDGES BEFORE IT DROPS THE LINK, deliberately, so that
    // this state is reachable at all. What happens after that is past anything
    // the platform can observe, so the screen must not assert it.
    expect(within(dialog).getByText(/It should now return to Wi-Fi setup mode/i)).toBeInTheDocument()

    // And the customer must be told what "nothing happened" looks like, or they
    // will stand there hunting for a network that is never going to appear.
    expect(
      within(dialog).getByText(/If no setup network appears within a few minutes/i),
    ).toBeInTheDocument()

    // WHAT THIS NO LONGER SAYS, and why. It used to explain that the terminal
    // might be running firmware from before the feature existed and had
    // acknowledged the command without understanding it — which was true, and
    // was the only thing standing between a customer and ten wasted minutes.
    // The server now refuses to queue the command for a terminal that has not
    // reported the capability, so a customer who reaches this screen is on a
    // build that says it can do this. Keeping the sentence would be telling
    // them to suspect something the platform has already ruled out.
    expect(
      within(dialog).queryByText(/firmware from before Change Wi-Fi existed/i),
    ).not.toBeInTheDocument()
  })

  it('says so when the terminal never collected the command', async () => {
    signIn('ADMIN')
    const user = userEvent.setup()
    renderTerminal()

    const dialog = await openChangeWifi(user)
    await user.click(within(dialog).getByRole('button', { name: 'Continue' }))
    await within(dialog).findByText('Waiting for terminal…')

    advanceWifiRecovery('AT-0001', 'EXPIRED')

    expect(await within(dialog).findByText(/never collected it/i, {}, POLLED)).toBeInTheDocument()
    expect(within(dialog).getByText(/Nothing changed at the terminal/i)).toBeInTheDocument()
    // The way back that does not need the network.
    expect(within(dialog).getByText(/Hold the button on the unit/i)).toBeInTheDocument()
  })

  it('says so when the terminal reported it could not apply the command', async () => {
    signIn('ADMIN')
    const user = userEvent.setup()
    renderTerminal()

    const dialog = await openChangeWifi(user)
    await user.click(within(dialog).getByRole('button', { name: 'Continue' }))
    await within(dialog).findByText('Waiting for terminal…')

    advanceWifiRecovery('AT-0001', 'FAILED')

    expect(await within(dialog).findByText(/could not apply it/i, {}, POLLED)).toBeInTheDocument()
    expect(within(dialog).getByText(/Its Wi-Fi is unchanged/i)).toBeInTheDocument()
  })

  it('does not queue a second command when the button is pressed twice', async () => {
    signIn('ADMIN')
    const user = userEvent.setup()
    renderTerminal()

    const dialog = await openChangeWifi(user)
    await user.click(within(dialog).getByRole('button', { name: 'Continue' }))
    await within(dialog).findByText('Waiting for terminal…')

    // The dialog has moved on, so the second press is a second visit — which is
    // exactly how an anxious operator produces a duplicate.
    await user.click(within(dialog).getByRole('button', { name: 'Done' }))
    const again = await openChangeWifi(user)
    await user.click(within(again).getByRole('button', { name: 'Continue' }))

    expect(
      await within(again).findByText(/a request was already waiting/i),
    ).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Offline: the case the feature exists for
// ---------------------------------------------------------------------------

describe('a terminal that cannot be reached', () => {
  it('warns before the command is sent, on the page itself', async () => {
    signIn('ADMIN')
    seed({ terminals: [terminalNamed({ status: 'OFFLINE' })] })
    renderTerminal()

    const network = await screen.findByRole('region', { name: 'Network' })
    // THE EXACT SENTENCE THE OPERATOR NEEDS, where they are already looking.
    expect(
      within(network).getByText(
        /The terminal is offline\. Connect it to the network again or use the terminal's local Wi-Fi recovery procedure/i,
      ),
    ).toBeInTheDocument()
  })

  it('explains the local recovery when the API refuses the command', async () => {
    signIn('ADMIN')
    seed({ terminals: [terminalNamed({ status: 'OFFLINE' })] })
    const user = userEvent.setup()
    renderTerminal()

    const dialog = await openChangeWifi(user)
    // The console still asks the server: the status column is the platform's
    // last report and the server is the authority on whether it can be reached.
    await user.click(within(dialog).getByRole('button', { name: 'Continue' }))

    // THE SENTENCE, WHOLE. Matching only the first clause would also match the
    // notice's heading, and passing on the heading alone would let the actionable
    // half of the message be deleted without a test noticing.
    expect(
      await within(dialog).findByText(
        /The terminal is offline\. Connect it to the network again or use the terminal's local Wi-Fi recovery procedure\./i,
      ),
    ).toBeInTheDocument()
    expect(within(dialog).getByText(/Hold the button on the unit for five seconds/i)).toBeInTheDocument()

    // AND IT MUST NOT LOOK LIKE IT WORKED. Nothing was queued.
    expect(within(dialog).queryByText('Waiting for terminal…')).not.toBeInTheDocument()
  })

  it('refuses a terminal whose firmware has not said it can do this', async () => {
    signIn('ADMIN')

    // A unit that reports its capabilities and does not have this one. The
    // whole fleet in the field is the OTHER case -- see the test below -- and
    // both must be refused rather than queued.
    seed({ terminals: [terminalNamed({ capabilities: ['wifi_provisioning'] })] })
    const user = userEvent.setup()
    renderTerminal()

    const dialog = await openChangeWifi(user)
    await user.click(within(dialog).getByRole('button', { name: 'Continue' }))

    expect(
      await within(dialog).findByText(/cannot change Wi-Fi remotely/i),
    ).toBeInTheDocument()

    // THE REMEDY IS THE FIRMWARE, and the local recovery is what to do
    // meanwhile. Neither is "wait and try again".
    expect(within(dialog).getByText(/Update its firmware/i)).toBeInTheDocument()
    expect(
      within(dialog).getByText(/Hold the button on the unit for five seconds/i),
    ).toBeInTheDocument()

    // AND NOTHING WAS QUEUED. This is the refusal that replaced a false
    // ACCEPTED: the old behaviour queued the command, the terminal acknowledged
    // a job type it did not recognise, and this dialog reported that the
    // terminal had confirmed the request.
    expect(within(dialog).queryByText('Waiting for terminal…')).not.toBeInTheDocument()
  })

  it('refuses a terminal that has never reported what it can do', async () => {
    signIn('ADMIN')

    // ABSENT IS NOT EMPTY, and it is not consent either. Every unit built before
    // capability reporting is absent here, and every brand-new one is absent
    // until its first heartbeat -- so the answer says the platform does not
    // know, rather than claiming the terminal cannot.
    seed({ terminals: [terminalNamed({ capabilities: undefined })] })
    const user = userEvent.setup()
    renderTerminal()

    const dialog = await openChangeWifi(user)
    await user.click(within(dialog).getByRole('button', { name: 'Continue' }))

    expect(
      await within(dialog).findByText(/never reported what it can do/i),
    ).toBeInTheDocument()
    expect(within(dialog).queryByText('Waiting for terminal…')).not.toBeInTheDocument()
  })

  it('names the disabled case rather than calling it offline', async () => {
    signIn('ADMIN')
    seed({ terminals: [terminalNamed({ status: 'DISABLED', active: false })] })
    const user = userEvent.setup()
    renderTerminal()

    const dialog = await openChangeWifi(user)
    await user.click(within(dialog).getByRole('button', { name: 'Continue' }))

    // A disabled terminal is also not heartbeating, so "offline" would be true
    // and useless — it would send somebody to the door to fix something that is
    // one click away in this console.
    expect(await within(dialog).findByText(/This terminal is disabled/i)).toBeInTheDocument()
    expect(within(dialog).getByText(/Re-enable it from this page first/i)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Failure
// ---------------------------------------------------------------------------

describe('when the request itself fails', () => {
  it('says nothing was sent, and keeps the dialog open', async () => {
    signIn('ADMIN')
    const user = userEvent.setup()
    renderTerminal()

    failNext('terminal-wifi-recovery', 500)

    const dialog = await openChangeWifi(user)
    await user.click(within(dialog).getByRole('button', { name: 'Continue' }))

    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      /Failed to queue the Wi-Fi change/i,
    )
    // Closing on failure is the worst available outcome: the operator would
    // believe a door had been put into setup mode when it had not.
    expect(within(dialog).getByText(/Nothing was sent to the terminal/i)).toBeInTheDocument()
    expect(within(dialog).queryByText('Waiting for terminal…')).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// The promise the whole feature rests on
// ---------------------------------------------------------------------------

describe('Wi-Fi credentials', () => {
  it('are never asked for, and never sent', async () => {
    signIn('ADMIN')
    const user = userEvent.setup()
    renderTerminal()

    const dialog = await openChangeWifi(user)

    // NO FIELD TO TYPE A NETWORK OR A PASSWORD INTO, anywhere in the flow. The
    // network is joined at the terminal; the platform never learns the password,
    // which is a property of this dialog having nowhere to put one.
    expect(within(dialog).queryByRole('textbox')).not.toBeInTheDocument()
    expect(within(dialog).queryByLabelText(/password/i)).not.toBeInTheDocument()
    expect(within(dialog).queryByLabelText(/ssid|network name/i)).not.toBeInTheDocument()

    await user.click(within(dialog).getByRole('button', { name: 'Continue' }))
    await within(dialog).findByText('Waiting for terminal…')

    // And the request carries no body at all.
    const sent = state.requests.find(
      (request) => request.method === 'POST' && request.url.includes('/wifi-recovery'),
    )
    expect(sent).toBeDefined()
    expect(sent?.headers.get('Content-Type')).toBeNull()
  })
})
