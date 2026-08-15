import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../../api/csrf'
import type { FirmwareVersion, Role, Terminal } from '../../api/types'
import { makeFirmwareVersion, makeSession, makeTerminal } from '../../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../../test/render'
import { failNext, resetServerState, seed, state } from '../../test/server'
import { FirmwarePage, groupByTarget } from './FirmwarePage'

/**
 * The firmware catalogue.
 *
 * THE ONE BELIEF THIS SCREEN EXISTS TO PREVENT: that marking a build current
 * updates anything. It does not. AccessLink has no over-the-air update, so
 * `is_current` is the value every "is this terminal outdated" report is measured
 * against and is nothing else — and somebody who publishes a build, promotes it
 * and walks away thinking the fleet is updating has been misled by a screen.
 *
 * Several tests below therefore assert only on WORDS. They are the valuable ones:
 * the API calls are three lines each and could not plausibly be wrong, while the
 * sentence that stops a rollout being imagined is easy to soften and never
 * notice.
 */

const CATALOGUE: FirmwareVersion[] = [
  makeFirmwareVersion({
    id: 1,
    version: '1.2.0',
    device_type: 'TERMINAL',
    release_channel: 'STABLE',
    is_current: true,
    created_at: '2026-06-01T00:00:00Z',
  }),
  makeFirmwareVersion({
    id: 2,
    version: '1.3.0',
    device_type: 'TERMINAL',
    release_channel: 'STABLE',
    is_current: false,
    release_notes: 'Faster wake from sleep.',
    created_at: '2026-08-01T00:00:00Z',
  }),
  // A DIFFERENT CHANNEL. "Current" is scoped per device type AND channel, so
  // this one has its own target and is untouched by promotions on STABLE.
  makeFirmwareVersion({
    id: 3,
    version: '2.0.0-beta1',
    device_type: 'TERMINAL',
    release_channel: 'BETA',
    is_current: true,
    is_mandatory: true,
    created_at: '2026-08-10T00:00:00Z',
  }),
]

const FLEET: Terminal[] = [
  makeTerminal({ id: 1, serial_number: 'AT-0001', firmware_version: '1.2.0' }),
  makeTerminal({ id: 2, public_id: 't2', serial_number: 'AT-0002', firmware_version: '1.2.0' }),
  makeTerminal({ id: 3, public_id: 't3', serial_number: 'AT-0003', firmware_version: '1.1.0' }),
  // On the beta channel, so it is measured against a different build entirely.
  makeTerminal({
    id: 4,
    public_id: 't4',
    serial_number: 'AT-0004',
    release_channel: 'BETA',
    firmware_version: '2.0.0-beta1',
  }),
]

function signIn(role: Role = 'ADMIN', firmware: FirmwareVersion[] = CATALOGUE) {
  const session = makeSession({
    role,
    operator: { id: 'operator-1', email: 'ops@example.com', full_name: 'Ops', role },
  })
  resetServerState(session)
  setCsrfToken(session.csrf_token)
  seed({ firmware, terminals: FLEET })
  return session
}

function renderFirmware() {
  const router = createMemoryRouter(
    [
      { path: '/settings/firmware', element: <FirmwarePage /> },
      { path: '/terminals', element: <p>Terminals</p> },
    ],
    { initialEntries: ['/settings/firmware'] },
  )
  return renderWithSession(<RouterProvider router={router} />, makeTestQueryClient())
}

/**
 * The catalogue row for one version.
 *
 * A version string appears TWICE on the page when it is the current build: once
 * as the row's own heading and once in the group summary ("of which 2 are
 * running 1.2.0"). That duplication is intentional — the summary is where an
 * operator reads how much of the fleet is already there — so the tests address
 * the row rather than the first match.
 */
function versionRow(version: string): HTMLElement {
  const row = screen
    .getAllByText(version)
    .map((node) => node.closest('li'))
    .find(Boolean)
  if (!row) throw new Error(`no catalogue row for ${version}`)
  return row as HTMLElement
}

beforeEach(() => setCsrfToken(null))

// ---------------------------------------------------------------------------
// Grouping, as a pure function
// ---------------------------------------------------------------------------

describe('grouping the catalogue', () => {
  it('groups by the pair that "current" is actually scoped to', () => {
    // Not by version, and not into one flat list: promoting a build demotes only
    // its own device type and channel, and a flat list invites somebody to
    // believe there is one current build for everything.
    const groups = groupByTarget(CATALOGUE, FLEET)

    expect(groups.map((group) => group.key)).toEqual([
      'TERMINAL--BETA',
      'TERMINAL--STABLE',
    ])
  })

  it('counts terminals against their OWN channel, not the whole fleet', () => {
    const groups = groupByTarget(CATALOGUE, FLEET)
    const stable = groups.find((group) => group.key === 'TERMINAL--STABLE')
    const beta = groups.find((group) => group.key === 'TERMINAL--BETA')

    expect(stable?.terminalCount).toBe(3)
    expect(beta?.terminalCount).toBe(1)
  })

  it('counts how many are ALREADY on the current build', () => {
    // The number an operator reads before deciding whether an update is worth
    // doing, and where an off-by-one would hide.
    const stable = groupByTarget(CATALOGUE, FLEET).find((g) => g.key === 'TERMINAL--STABLE')
    expect(stable?.current?.version).toBe('1.2.0')
    expect(stable?.onCurrent).toBe(2)
  })

  it('reports nothing on the current build when there is no current build', () => {
    const none = groupByTarget(
      [makeFirmwareVersion({ id: 9, version: '1.4.0', is_current: false })],
      FLEET,
    )
    expect(none[0]?.current).toBeNull()
    expect(none[0]?.onCurrent).toBe(0)
  })

  it('pins the current build to the top of its group', () => {
    // It is what the group is about; hunting for a badge in a date-sorted list
    // is work the screen can do instead.
    const stable = groupByTarget(CATALOGUE, FLEET).find((g) => g.key === 'TERMINAL--STABLE')
    expect(stable?.versions[0]?.version).toBe('1.2.0')
    expect(stable?.versions[1]?.version).toBe('1.3.0')
  })
})

// ---------------------------------------------------------------------------
// What the page says it does
// ---------------------------------------------------------------------------

describe('the catalogue is honest about doing nothing', () => {
  it('SAYS NOTHING HERE UPDATES A TERMINAL, at the top of the page', async () => {
    signIn()
    renderFirmware()

    expect(await screen.findByText('Nothing here updates a terminal')).toBeInTheDocument()
    expect(screen.getByText(/no over-the-air update/i)).toBeInTheDocument()
  })

  it('describes the current build as what the fleet is COMPARED TO', async () => {
    signIn()
    renderFirmware()

    await screen.findByText('Nothing here updates a terminal')
    expect(screen.getByText(/compared to/i)).toBeInTheDocument()
    // And marks it as a target rather than as something installed.
    expect(screen.getAllByText('Current target').length).toBeGreaterThan(0)
  })

  it('says an empty catalogue makes every terminal LOOK up to date', async () => {
    // Not because they are, but because there is nothing to compare them with —
    // and publishing the first build may mark terminals outdated the moment it
    // lands, with nothing about them having changed.
    signIn('ADMIN', [])
    renderFirmware()

    expect(await screen.findByText('No builds recorded')).toBeInTheDocument()
    expect(screen.getByText(/nothing to compare it with/i)).toBeInTheDocument()
    expect(screen.getByText(/Nothing about those terminals will have changed/i)).toBeInTheDocument()
  })

  it('says "mandatory" is recorded rather than enforced', async () => {
    // The field exists in the model and NOTHING in the platform reads it,
    // because nothing installs firmware. A badge with no caveat would imply the
    // platform acts on it.
    signIn()
    renderFirmware()

    await screen.findByText('Marked mandatory')
    expect(screen.getByText(/recorded, not enforced/i)).toBeInTheDocument()
  })

  it('warns when a group has no current build at all', async () => {
    signIn('ADMIN', [makeFirmwareVersion({ id: 9, version: '1.4.0', is_current: false })])
    renderFirmware()

    expect(await screen.findByText('No current build for this combination')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// The structure teaches the model
// ---------------------------------------------------------------------------

describe('the page mirrors how "current" is scoped', () => {
  it('shows one section per device type and channel', async () => {
    signIn()
    renderFirmware()

    expect(
      await screen.findByRole('heading', { name: 'Terminal · Stable' }),
    ).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Terminal · Beta' })).toBeInTheDocument()
  })

  it('says how many terminals a group actually describes, and that it is scoped', async () => {
    signIn()
    renderFirmware()

    const stable = (await screen.findByRole('heading', { name: 'Terminal · Stable' }))
      .closest('section') as HTMLElement
    // "you can see" — the fleet list is narrowed by site grants, and a number
    // that looks company-wide and is not is worse than no number.
    expect(within(stable).getByText(/3 terminals you can see/i)).toBeInTheDocument()
    expect(within(stable).getByText(/of which/i)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Publishing
// ---------------------------------------------------------------------------

describe('publishing a build', () => {
  it('does NOT make it the target, and says so before and after', async () => {
    const user = userEvent.setup()
    signIn()
    renderFirmware()

    await screen.findByRole('heading', { name: 'Terminal · Stable' })
    await user.click(screen.getByRole('button', { name: 'Publish a build' }))

    const dialog = screen.getByRole('dialog')
    expect(within(dialog).getByText('This does not become the target')).toBeInTheDocument()

    await user.type(within(dialog).getByLabelText(/^Version/), '1.4.0')
    await user.click(within(dialog).getByRole('button', { name: 'Publish build' }))

    // The confirmation repeats it rather than saying "published".
    expect(
      await screen.findByText(/It is not the current target until you make it one/i),
    ).toBeInTheDocument()
  })

  it('lands in the catalogue as recorded rather than current', async () => {
    const user = userEvent.setup()
    signIn()
    renderFirmware()

    await screen.findByRole('heading', { name: 'Terminal · Stable' })
    await user.click(screen.getByRole('button', { name: 'Publish a build' }))
    await user.type(screen.getByLabelText(/^Version/), '1.4.0')
    await user.click(screen.getByRole('button', { name: 'Publish build' }))

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    const row = (await screen.findByText('1.4.0')).closest('li') as HTMLElement
    expect(within(row).getByText('Recorded')).toBeInTheDocument()
    expect(within(row).queryByText('Current target')).not.toBeInTheDocument()
  })

  it('validates a checksum before asking the server', async () => {
    const user = userEvent.setup()
    signIn()
    renderFirmware()

    await screen.findByRole('heading', { name: 'Terminal · Stable' })
    await user.click(screen.getByRole('button', { name: 'Publish a build' }))
    await user.type(screen.getByLabelText(/^Version/), '1.4.0')
    await user.type(screen.getByLabelText(/SHA-256/), 'not-a-checksum')

    const before = state.requests.filter((entry) => entry.method === 'POST').length
    await user.click(screen.getByRole('button', { name: 'Publish build' }))

    expect(await screen.findByText(/64 hexadecimal characters/i)).toBeInTheDocument()
    expect(state.requests.filter((entry) => entry.method === 'POST')).toHaveLength(before)
  })

  it('turns a duplicate version into something actionable', async () => {
    const user = userEvent.setup()
    signIn()
    renderFirmware()

    await screen.findByRole('heading', { name: 'Terminal · Stable' })
    await user.click(screen.getByRole('button', { name: 'Publish a build' }))
    await user.type(screen.getByLabelText(/^Version/), '1.2.0')
    await user.click(screen.getByRole('button', { name: 'Publish build' }))

    expect(
      await screen.findByText(/already exists for this device type/i),
    ).toBeInTheDocument()
  })

  it('says the version string is what a terminal is COMPARED WITH', async () => {
    // A mismatch in formatting reads as a whole fleet being behind, which is the
    // realistic way this field goes wrong.
    const user = userEvent.setup()
    signIn()
    renderFirmware()

    await screen.findByRole('heading', { name: 'Terminal · Stable' })
    await user.click(screen.getByRole('button', { name: 'Publish a build' }))

    expect(screen.getByText(/reads as a whole fleet being behind/i)).toBeInTheDocument()
  })

  it('does not offer the stored location as a link', async () => {
    // Nothing fetches it. A link would suggest the platform does.
    signIn('ADMIN', [
      makeFirmwareVersion({ id: 5, version: '1.5.0', download_url: 'https://builds.example/1.5.0.bin' }),
    ])
    renderFirmware()

    await screen.findByRole('heading', { name: 'Terminal · Stable' })
    expect(
      within(versionRow('1.5.0')).getByText('https://builds.example/1.5.0.bin'),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('link', { name: /builds\.example/ }),
    ).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Moving the target
// ---------------------------------------------------------------------------

describe('making a build current', () => {
  it('STATES THAT NOTHING IS SENT, and names what it demotes', async () => {
    const user = userEvent.setup()
    signIn()
    renderFirmware()

    await screen.findByRole('heading', { name: 'Terminal · Stable' })
    await user.click(within(versionRow('1.3.0')).getByRole('button', { name: 'Make current' }))

    const dialog = screen.getByRole('dialog')
    expect(within(dialog).getByText(/Nothing is sent to any of them/i)).toBeInTheDocument()
    // The previous occupant of the single current slot.
    expect(within(dialog).getByText('1.2.0')).toBeInTheDocument()
    // And that other channels are untouched.
    expect(within(dialog).getByText(/other channels are unaffected/i)).toBeInTheDocument()
  })

  it('says how many terminals this changes the REPORT for', async () => {
    const user = userEvent.setup()
    signIn()
    renderFirmware()

    await screen.findByRole('heading', { name: 'Terminal · Stable' })
    await user.click(within(versionRow('1.3.0')).getByRole('button', { name: 'Make current' }))

    expect(
      within(screen.getByRole('dialog')).getByText(/3 terminals you can see/i),
    ).toBeInTheDocument()
  })

  it('moves the target and demotes the previous build', async () => {
    const user = userEvent.setup()
    signIn()
    renderFirmware()

    await screen.findByRole('heading', { name: 'Terminal · Stable' })
    await user.click(within(versionRow('1.3.0')).getByRole('button', { name: 'Make current' }))
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: 'Make it the target' }),
    )

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())

    await waitFor(() =>
      expect(within(versionRow('1.3.0')).getByText('Current target')).toBeInTheDocument(),
    )
    expect(within(versionRow('1.2.0')).getByText('Recorded')).toBeInTheDocument()
  })

  it('leaves the other channel’s target alone', async () => {
    const user = userEvent.setup()
    signIn()
    renderFirmware()

    await screen.findByRole('heading', { name: 'Terminal · Stable' })
    await user.click(within(versionRow('1.3.0')).getByRole('button', { name: 'Make current' }))
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: 'Make it the target' }),
    )

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(within(versionRow('2.0.0-beta1')).getByText('Current target')).toBeInTheDocument()
  })

  it('offers no promotion for the build that is already current', async () => {
    signIn()
    renderFirmware()

    await screen.findByRole('heading', { name: 'Terminal · Stable' })
    expect(
      within(versionRow('1.2.0')).queryByRole('button', { name: 'Make current' }),
    ).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Authorization and failure
// ---------------------------------------------------------------------------

describe('role gating mirrors the server', () => {
  it('REFUSES A MANAGER THE CATALOGUE ENTIRELY, read included', async () => {
    // Unlike Applications, where any operator may read and only an OWNER may
    // change, `admin.GET("/firmware")` is ADMIN — the catalogue's read is as
    // privileged as its writes, because the current build is the value every
    // "is this terminal outdated" report is measured against.
    //
    // So the page has no read-only state, and a MANAGER who reaches it by URL
    // gets the server's refusal reported as an error rather than as an empty
    // catalogue — which would say every terminal is up to date.
    signIn('MANAGER')
    renderFirmware()

    expect(await screen.findByRole('alert')).toHaveTextContent(/Insufficient permissions/)
    expect(screen.queryByRole('button', { name: 'Publish a build' })).not.toBeInTheDocument()
  })

  it('reports a failed load as an error rather than as an empty catalogue', async () => {
    // "No builds" and "we could not ask" mean opposite things: the first says
    // every terminal is up to date.
    signIn()
    failNext('firmware', 500)
    renderFirmware()

    expect(await screen.findByRole('alert')).toHaveTextContent(
      /Failed to retrieve firmware versions/,
    )
  })
})
