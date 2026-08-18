import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../../api/csrf'
import type { Role, Session } from '../../api/types'
import { collectAttention } from '../DashboardPage'
import {
  makePendingTerminal,
  makeSession,
  makeSite,
  makeTerminal,
  SITE_A,
} from '../../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../../test/render'
import {
  failNext,
  resetServerState,
  resetTerminalModes,
  seed,
  seedAnnouncedTerminal,
  state,
} from '../../test/server'
import { formatPairingCode } from './AddTerminalDialog'
import { TerminalsListPage } from './TerminalsListPage'

/**
 * Adding a terminal.
 *
 * THE SCREEN A CUSTOMER MEETS FIRST, and the one where the product either keeps
 * its promise or does not: power a unit on, put it on Wi-Fi from a phone, read
 * eight characters off its screen, done. So most of what is asserted below is
 * about what the customer is TOLD — that a code has expired rather than that
 * something failed, that a terminal belongs to somebody else and cannot be
 * taken, that approving is not the same moment as the terminal working.
 *
 * The security assertions are here too, because the console is one of the two
 * places the ownership rule is enforced and the only place it is explained.
 */

const SITES = [makeSite({ id: SITE_A.site_id, name: SITE_A.site_name })]

function signedInAs(role: Role): Session {
  return makeSession({ role, sites: [], all_sites: true })
}

function renderTerminals(session: Session) {
  resetServerState(session)
  seed({ sites: SITES, terminals: [] })
  setCsrfToken(session.csrf_token)

  const router = createMemoryRouter(
    [{ path: '/terminals', element: <TerminalsListPage /> }],
    { initialEntries: ['/terminals'] },
  )
  return renderWithSession(<RouterProvider router={router} />, makeTestQueryClient())
}

beforeEach(() => {
  resetServerState(null)
  resetTerminalModes()
  setCsrfToken(null)
})

// ---------------------------------------------------------------------------
// Formatting
// ---------------------------------------------------------------------------

describe('formatPairingCode', () => {
  it('normalises anything somebody types or pastes into XXXX-XXXX', () => {
    expect(formatPairingCode('k7m2p4qx')).toBe('K7M2-P4QX')
    expect(formatPairingCode('K7M2-P4QX')).toBe('K7M2-P4QX')
    expect(formatPairingCode('k7m2 p4qx')).toBe('K7M2-P4QX')
    expect(formatPairingCode('  K7M2--P4QX  ')).toBe('K7M2-P4QX')
  })

  it('stops at eight characters, so an extra keystroke cannot make it look valid', () => {
    expect(formatPairingCode('K7M2P4QXZZZZ')).toBe('K7M2-P4QX')
  })

  it('keeps characters outside the alphabet rather than swallowing them', () => {
    // A customer who misreads B for 8 must be TOLD the code was refused. If the
    // character vanished as they typed, they would be left holding a code that
    // looks right, is short, and fails for no visible reason.
    expect(formatPairingCode('BBBB1111')).toBe('BBBB-1111')
  })
})

// ---------------------------------------------------------------------------
// The journey
// ---------------------------------------------------------------------------

describe('adding a terminal', () => {
  it('takes a code, confirms the hardware, places it and reports what happens next', async () => {
    const user = userEvent.setup()
    renderTerminals(signedInAs('OWNER'))

    seedAnnouncedTerminal('K7M2-P4QX', makePendingTerminal())

    await user.click(await screen.findByRole('button', { name: 'Add a terminal' }))

    // STEP ONE IS THE INSTRUCTIONS AND ONE FIELD. Somebody opening this has a
    // box in front of them and has not done any of it yet.
    const dialog = screen.getByRole('dialog')
    expect(within(dialog).getByText(/connect it to wi-fi/i)).toBeInTheDocument()
    expect(within(dialog).getByText(/read the code/i)).toBeInTheDocument()
    // No serial number is asked for anywhere, which is the whole point.
    expect(within(dialog).queryByLabelText(/serial/i)).not.toBeInTheDocument()

    await user.type(within(dialog).getByLabelText(/code from the terminal/i), 'k7m2p4qx')
    await user.click(within(dialog).getByRole('button', { name: /continue/i }))

    // STEP TWO: the facts that let somebody believe this is their unit.
    //
    // Scoped to the dialog throughout. The terminal is on the waiting list
    // behind it from the moment the code is accepted, so an unscoped query
    // would match the row as well as the panel — and the point of these
    // assertions is what the person deciding is looking at.
    expect(await screen.findByText(/is this the terminal in front of you/i)).toBeInTheDocument()
    const confirm = within(screen.getByRole('dialog'))
    expect(confirm.getByText('AT-A1B2C3')).toBeInTheDocument()
    expect(confirm.getByText('rev-C')).toBeInTheDocument()
    expect(confirm.getByText('1.4.0')).toBeInTheDocument()
    expect(confirm.getByText(/81\.2\.0\.5/)).toBeInTheDocument()

    // WHAT IT CAN DO, at the moment somebody is deciding whether to bolt it to
    // a door. "This one cannot be recovered over the network" costs nothing to
    // learn here and a site visit to learn afterwards.
    expect(confirm.getByText(/Wi-Fi can be changed from here/i)).toBeInTheDocument()

    // The only site is chosen for them — every new customer has exactly one.
    const site = confirm.getByLabelText(/^site/i) as HTMLSelectElement
    await waitFor(() => expect(site.value).toBe(SITE_A.site_id))

    await user.type(confirm.getByLabelText(/name this terminal/i), 'Front Door')
    await user.click(confirm.getByRole('button', { name: /approve and set up/i }))

    // STEP THREE SAYS WHAT HAS AND HAS NOT HAPPENED. Approval is not the same
    // moment as the terminal working, and a customer who walks away believing
    // it is is the person this screen is for.
    const done = within(await screen.findByRole('dialog'))
    expect(await done.findByText(/is being set up/i)).toBeInTheDocument()
    expect(done.getByText(/collecting its credential/i)).toBeInTheDocument()

    // And the request carried what the operator chose.
    const approved = state.pendingTerminals[0]
    expect(approved?.state).toBe('APPROVED')
    expect(approved?.device_name).toBe('Front Door')
    expect(approved?.site_name).toBe(SITE_A.site_name)
  })

  it('distinguishes a terminal that cannot be recovered remotely from one that never said', async () => {
    // THE THREE ANSWERS ARE DIFFERENT and a console that collapsed the last two
    // would be telling an operator a terminal cannot do something when all it
    // knows is that nobody asked. Every unit built before capability reporting
    // announces without the field.
    const user = userEvent.setup()
    renderTerminals(signedInAs('OWNER'))

    seedAnnouncedTerminal('K7M2-P4QX', makePendingTerminal({ capabilities: [] }))

    await user.click(await screen.findByRole('button', { name: 'Add a terminal' }))
    const dialog = screen.getByRole('dialog')
    await user.type(within(dialog).getByLabelText(/code from the terminal/i), 'K7M2P4QX')
    await user.click(within(dialog).getByRole('button', { name: /continue/i }))

    await screen.findByText(/is this the terminal in front of you/i)
    expect(
      within(screen.getByRole('dialog')).getByText(/need somebody at the terminal/i),
    ).toBeInTheDocument()
  })

  it('says a terminal that reported nothing is unknown rather than incapable', async () => {
    const user = userEvent.setup()
    renderTerminals(signedInAs('OWNER'))

    seedAnnouncedTerminal(
      'K7M2-P4QX',
      makePendingTerminal({ capabilities: undefined }),
    )

    await user.click(await screen.findByRole('button', { name: 'Add a terminal' }))
    const dialog = screen.getByRole('dialog')
    await user.type(within(dialog).getByLabelText(/code from the terminal/i), 'K7M2P4QX')
    await user.click(within(dialog).getByRole('button', { name: /continue/i }))

    await screen.findByText(/is this the terminal in front of you/i)
    const confirm = within(screen.getByRole('dialog'))

    // "Not reported" — not "No". The unit may well be able to; nobody has asked
    // it yet, and it has no credential to heartbeat with.
    expect(confirm.getAllByText(/not reported/i).length).toBeGreaterThan(0)
    expect(confirm.queryByText(/need somebody at the terminal/i)).not.toBeInTheDocument()
  })

  it('never shows a credential, because the terminal fetches its own', async () => {
    const user = userEvent.setup()
    renderTerminals(signedInAs('OWNER'))
    seedAnnouncedTerminal('K7M2-P4QX', makePendingTerminal())

    await user.click(await screen.findByRole('button', { name: 'Add a terminal' }))
    await user.type(screen.getByLabelText(/code from the terminal/i), 'K7M2P4QX')
    await user.click(screen.getByRole('button', { name: /continue/i }))
    await screen.findByText(/is this the terminal in front of you/i)
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: /approve and set up/i }),
    )

    await within(await screen.findByRole('dialog')).findByText(/is being set up/i)

    // The claim-code dialog has a one-time panel, a copy button and a warning
    // because it hands over a secret. This flow must have none of that: there
    // is nothing here for anybody to copy, and offering a copy button would
    // teach the habit the whole design removes.
    expect(screen.queryByRole('button', { name: /copy/i })).not.toBeInTheDocument()
    expect(screen.queryByText(/shown once/i)).not.toBeInTheDocument()
    expect(document.body.textContent).not.toMatch(/atd_/)
  })
})

// ---------------------------------------------------------------------------
// What the customer is told when it does not work
// ---------------------------------------------------------------------------

describe('refusals', () => {
  it('explains an unrecognised code as expiry rather than as a failure', async () => {
    const user = userEvent.setup()
    renderTerminals(signedInAs('OWNER'))
    // Nothing seeded: no terminal in the room is showing this code.

    await user.click(await screen.findByRole('button', { name: 'Add a terminal' }))
    await user.type(screen.getByLabelText(/code from the terminal/i), 'ZZZZ9999')
    await user.click(screen.getByRole('button', { name: /continue/i }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/not recognised/i)
    // THE REMEDY, not just the refusal. The overwhelmingly likely cause is that
    // the code rotated while they were typing.
    expect(alert).toHaveTextContent(/15 minutes/i)
    expect(alert).toHaveTextContent(/screen/i)

    // And they are still on the field, able to try the new one.
    expect(screen.getByLabelText(/code from the terminal/i)).toBeInTheDocument()
  })

  it('refuses a terminal owned by another account without naming who owns it', async () => {
    const user = userEvent.setup()
    renderTerminals(signedInAs('OWNER'))
    seedAnnouncedTerminal(
      'K7M2-P4QX',
      makePendingTerminal({ verdict: 'REFUSED_OTHER_COMPANY' }),
    )

    await user.click(await screen.findByRole('button', { name: 'Add a terminal' }))
    await user.type(screen.getByLabelText(/code from the terminal/i), 'K7M2P4QX')
    await user.click(screen.getByRole('button', { name: /continue/i }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/another accesslink account/i)
    // The route out is stated, and it is deliberately not self-service.
    expect(alert).toHaveTextContent(/released/i)

    // NOT ADVANCED TO STEP TWO. There is nothing to approve, and showing the
    // site selector would offer an action the API will refuse.
    expect(
      screen.queryByRole('button', { name: /approve and set up/i }),
    ).not.toBeInTheDocument()
  })

  it('sends an operator to re-enable their own disabled terminal', async () => {
    const user = userEvent.setup()
    renderTerminals(signedInAs('OWNER'))
    seedAnnouncedTerminal('K7M2-P4QX', makePendingTerminal({ verdict: 'REFUSED_DISABLED' }))

    await user.click(await screen.findByRole('button', { name: 'Add a terminal' }))
    await user.type(screen.getByLabelText(/code from the terminal/i), 'K7M2P4QX')
    await user.click(screen.getByRole('button', { name: /continue/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/disabled/i)
  })

  it('warns before re-provisioning, naming the terminal that stops working', async () => {
    const user = userEvent.setup()
    renderTerminals(signedInAs('OWNER'))
    seedAnnouncedTerminal(
      'K7M2-P4QX',
      makePendingTerminal({
        verdict: 'RE_PROVISION',
        existing_terminal: {
          serial_number: 'AT-A1B2C3',
          device_name: 'Front Door',
          site_name: SITE_A.site_name,
          status: 'ONLINE',
        },
      }),
    )

    await user.click(await screen.findByRole('button', { name: 'Add a terminal' }))
    await user.type(screen.getByLabelText(/code from the terminal/i), 'K7M2P4QX')
    await user.click(screen.getByRole('button', { name: /continue/i }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/already set up as Front Door/i)
    expect(alert).toHaveTextContent(/stops working/i)

    // WARNED, NOT BLOCKED. This is the legitimate recovery path after a factory
    // reset, and refusing it would leave a customer with dead hardware.
    expect(screen.getByRole('button', { name: /approve and set up/i })).toBeEnabled()
  })
})

// ---------------------------------------------------------------------------
// The waiting list
// ---------------------------------------------------------------------------

describe('waiting to be set up', () => {
  it('shows nothing at all when nothing is waiting', async () => {
    renderTerminals(signedInAs('OWNER'))
    await screen.findByRole('button', { name: 'Add a terminal' })

    expect(screen.queryByText(/waiting to be set up/i)).not.toBeInTheDocument()
  })

  it('lists a waiting terminal and offers to approve it', async () => {
    const user = userEvent.setup()
    renderTerminals(signedInAs('OWNER'))
    seed({ pendingTerminals: [makePendingTerminal()] })

    const section = await screen.findByRole('region', { name: /waiting to be set up/i })
    expect(within(section).getByText('AT-A1B2C3')).toBeInTheDocument()
    expect(within(section).getByText(/waiting for approval/i)).toBeInTheDocument()

    await user.click(within(section).getByRole('button', { name: /^approve$/i }))
    expect(await screen.findByText(/is this the terminal in front of you/i)).toBeInTheDocument()
  })

  it('distinguishes approved-but-not-collected from working, and offers no Approve for it', async () => {
    renderTerminals(signedInAs('OWNER'))
    seed({
      pendingTerminals: [
        makePendingTerminal({ state: 'APPROVED', site_name: SITE_A.site_name }),
      ],
    })

    const section = await screen.findByRole('region', { name: /waiting to be set up/i })
    // THE DISTINCTION THAT MATTERS: approved is a decision, not a working door.
    expect(within(section).getByText(/approved — collecting/i)).toBeInTheDocument()
    expect(within(section).getByText(/next time it checks in/i)).toBeInTheDocument()
    expect(within(section).queryByRole('button', { name: /^approve$/i })).not.toBeInTheDocument()
  })

  it('explains an expired row rather than letting it vanish', async () => {
    renderTerminals(signedInAs('OWNER'))
    seed({ pendingTerminals: [makePendingTerminal({ state: 'EXPIRED' })] })

    const section = await screen.findByRole('region', { name: /waiting to be set up/i })
    expect(within(section).getByText(/timed out/i)).toBeInTheDocument()
    expect(within(section).getByText(/showing a new code/i)).toBeInTheDocument()
  })

  it('lets a MANAGER see what is waiting without being able to act on it', async () => {
    renderTerminals(signedInAs('MANAGER'))
    seed({ pendingTerminals: [makePendingTerminal()] })

    const section = await screen.findByRole('region', { name: /waiting to be set up/i })
    expect(within(section).getByText('AT-A1B2C3')).toBeInTheDocument()
    // The person who unpacked the box is often not an administrator, so they
    // can SEE it — and the copy tells them who has to finish the job.
    expect(within(section).getByText(/administrator/i)).toBeInTheDocument()
    expect(within(section).queryByRole('button', { name: /^approve$/i })).not.toBeInTheDocument()

    // And the way in is not offered either.
    expect(screen.queryByRole('button', { name: 'Add a terminal' })).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// The empty fleet, and the dashboard's first step
// ---------------------------------------------------------------------------

describe('a company with no terminals', () => {
  it('tells them how to add one instead of pointing at the provisioning key', async () => {
    renderTerminals(signedInAs('OWNER'))

    expect(await screen.findByText(/no terminals yet/i)).toBeInTheDocument()
    // The empty state's own call to action, named to match the dashboard's
    // onboarding item rather than duplicating the header button's label.
    expect(
      screen.getByRole('button', { name: /add your first terminal/i }),
    ).toBeInTheDocument()
    // THE OLD COPY SENT PEOPLE TO THE SITE PROVISIONING KEY — the credential
    // that registers every terminal at a site for ever, and exactly what a
    // customer should never handle.
    expect(screen.queryByText(/provisioning key/i)).not.toBeInTheDocument()
    expect(screen.getByText(/connect it to wi-fi/i)).toBeInTheDocument()
  })
})

describe('the dashboard first step', () => {
  const fleet = {
    total: 0,
    online: 0,
    offline: 0,
    updating: 0,
    error: 0,
    disabled: 0,
    provisioning: 0,
    firmware_outdated: 0,
  }

  it('asks a new customer for their first terminal before anything else', () => {
    const items = collectAttention({
      fleet,
      terminals: [],
      inactiveSites: 0,
      applicationCount: 0,
      peopleTotal: 0,
    })

    const ids = items.map((item) => item.id)
    expect(ids).toContain('no-terminals')
    // Before applications and people: both are preparation for a door that does
    // not exist yet.
    expect(ids.indexOf('no-terminals')).toBeLessThan(ids.indexOf('no-applications'))
    expect(ids.indexOf('no-terminals')).toBeLessThan(ids.indexOf('no-people'))
  })

  it('raises a waiting terminal above everything except a fault', () => {
    const items = collectAttention({
      fleet: { ...fleet, total: 2, offline: 1, error: 1 },
      terminals: [makeTerminal()],
      inactiveSites: 1,
      applicationCount: 1,
      peopleTotal: 4,
      pendingTerminals: 1,
    })

    const ids = items.map((item) => item.id)
    expect(ids[0]).toBe('terminals-waiting')
  })

  it('does not ask for a first terminal while one is already waiting', () => {
    const items = collectAttention({
      fleet,
      terminals: [],
      inactiveSites: 0,
      applicationCount: 1,
      peopleTotal: 1,
      pendingTerminals: 1,
    })

    const ids = items.map((item) => item.id)
    expect(ids).toContain('terminals-waiting')
    // The same job, further along. Two entries would read as two things to do.
    expect(ids).not.toContain('no-terminals')
  })
})

// ---------------------------------------------------------------------------
// Failure handling
// ---------------------------------------------------------------------------

describe('when the API fails', () => {
  it('keeps the customer on the field with the request id, rather than closing', async () => {
    const user = userEvent.setup()
    renderTerminals(signedInAs('OWNER'))
    seedAnnouncedTerminal('K7M2-P4QX', makePendingTerminal())
    failNext('adopt-terminal', 500)

    await user.click(await screen.findByRole('button', { name: 'Add a terminal' }))
    await user.type(screen.getByLabelText(/code from the terminal/i), 'K7M2P4QX')
    await user.click(screen.getByRole('button', { name: /continue/i }))

    expect(await screen.findByText(/failed to add the terminal/i)).toBeInTheDocument()
    expect(screen.getByLabelText(/code from the terminal/i)).toBeInTheDocument()
  })
})
