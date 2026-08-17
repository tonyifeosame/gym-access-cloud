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
import { firmwareOfferability, terminalsOffered } from './offerability'

/**
 * The firmware catalogue.
 *
 * THIS SUITE USED TO ASSERT THE OPPOSITE OF WHAT IT NOW ASSERTS, and that is the
 * point of the rewrite rather than an embarrassment to hide. It checked, in
 * several places and quite carefully, that the page said AccessLink had no
 * over-the-air update and that promoting a build sent nothing to anything. Those
 * sentences were true when they were written and became false when the heartbeat
 * began carrying `firmware_update` — at which point the tests were actively
 * holding a dangerous screen in place: a well-tested claim that a fleet-updating
 * action was inert.
 *
 * So the tests below assert the new truth in the same shape, and several of them
 * assert only on WORDS. Those are the valuable ones. The API calls are three
 * lines each and could not plausibly be wrong; the sentence that stops somebody
 * flashing a fleet by accident is easy to soften and never notice.
 */

/**
 * A build the platform would actually offer.
 *
 * DIGEST, SIZE AND AN HTTPS URL, because without all three the server withholds
 * every offer for the row. A fixture missing them would be testing the promotion
 * path against a build that can never be sent, which is exactly the trap the
 * page exists to surface.
 */
function deliverable(overrides: Partial<FirmwareVersion> = {}): FirmwareVersion {
  return makeFirmwareVersion({
    checksum_sha256: 'a'.repeat(64),
    download_url: 'https://builds.example/terminal/image.bin',
    size_bytes: 1_842_000,
    ...overrides,
  })
}

const CATALOGUE: FirmwareVersion[] = [
  deliverable({
    id: 1,
    version: '1.2.0',
    device_type: 'TERMINAL',
    release_channel: 'STABLE',
    is_current: true,
    created_at: '2026-06-01T00:00:00Z',
  }),
  deliverable({
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
  deliverable({
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
 * A version string appears more than once on the page when it is the current
 * build: as the row's own heading and again in the group summary. That
 * duplication is intentional — the summary is where an operator reads how much
 * of the fleet is already there — so the tests address the row rather than the
 * first match.
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
// Whether the platform would actually send a build
// ---------------------------------------------------------------------------

describe('offerability mirrors what the server will withhold', () => {
  it('accepts a build with a digest, a size and an https address', () => {
    expect(firmwareOfferability(deliverable())).toEqual({ deliverable: true, problems: [] })
  })

  it('refuses one with no checksum, because a terminal refuses an offer without one', () => {
    const result = firmwareOfferability(deliverable({ checksum_sha256: undefined }))
    expect(result.deliverable).toBe(false)
    expect(result.problems.join(' ')).toMatch(/no SHA-256 checksum/i)
  })

  it('refuses an UPPER-CASE digest, which is the mistake that looks fine', () => {
    // The server's pattern is 64 lower-case hex and the device compares exactly.
    // Folding case here would store a digest that never matches, and the symptom
    // would be every download failing verification after it had been fetched.
    const result = firmwareOfferability(deliverable({ checksum_sha256: 'A'.repeat(64) }))
    expect(result.deliverable).toBe(false)
    expect(result.problems.join(' ')).toMatch(/lower-case/i)
  })

  it('refuses a missing or zero size, which sizes the flash write', () => {
    expect(firmwareOfferability(deliverable({ size_bytes: undefined })).deliverable).toBe(false)
    expect(firmwareOfferability(deliverable({ size_bytes: 0 })).deliverable).toBe(false)
  })

  it('refuses a plaintext download address', () => {
    const result = firmwareOfferability(
      deliverable({ download_url: 'http://builds.example/image.bin' }),
    )
    expect(result.deliverable).toBe(false)
    expect(result.problems.join(' ')).toMatch(/not https/i)
  })

  it('refuses strings longer than the device’s fixed buffers', () => {
    const longUrl = `https://builds.example/${'x'.repeat(200)}.bin`
    expect(firmwareOfferability(deliverable({ download_url: longUrl })).deliverable).toBe(false)
    expect(firmwareOfferability(deliverable({ version: 'v'.repeat(30) })).deliverable).toBe(false)
  })

  it('reports EVERY problem at once rather than one per correction', () => {
    const result = firmwareOfferability(
      makeFirmwareVersion({ checksum_sha256: undefined, size_bytes: undefined, download_url: undefined }),
    )
    expect(result.problems).toHaveLength(3)
  })
})

describe('who would be offered a build', () => {
  it('narrows by device type, channel, and what a terminal already runs', () => {
    // The server's own three-part narrowing. Getting any part wrong changes a
    // number an operator reads immediately before starting a rollout.
    const offered = terminalsOffered(
      { version: '1.3.0', device_type: 'TERMINAL', release_channel: 'STABLE' },
      FLEET,
    )
    expect(offered.map((terminal) => terminal.serial_number)).toEqual([
      'AT-0001',
      'AT-0002',
      'AT-0003',
    ])
  })

  it('leaves out terminals already running the version', () => {
    const offered = terminalsOffered(
      { version: '1.2.0', device_type: 'TERMINAL', release_channel: 'STABLE' },
      FLEET,
    )
    expect(offered.map((terminal) => terminal.serial_number)).toEqual(['AT-0003'])
  })

  it('never crosses a release channel', () => {
    const offered = terminalsOffered(
      { version: '9.9.9', device_type: 'TERMINAL', release_channel: 'BETA' },
      FLEET,
    )
    expect(offered.map((terminal) => terminal.serial_number)).toEqual(['AT-0004'])
  })
})

// ---------------------------------------------------------------------------
// Grouping, as a pure function
// ---------------------------------------------------------------------------

describe('grouping the catalogue', () => {
  it('groups by the pair that "current" is actually scoped to', () => {
    // Not by version, and not into one flat list: promoting a build offers it to
    // one device type on one channel, and a flat list invites somebody to
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
    // The number an operator reads before deciding whether a rollout is worth
    // starting, and where an off-by-one would hide.
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

describe('the catalogue is honest that promoting updates hardware', () => {
  it('SAYS MAKING A BUILD CURRENT UPDATES TERMINALS, at the top of the page', async () => {
    // The single most important sentence on the screen, and the one this page
    // previously got backwards.
    signIn()
    renderFirmware()

    expect(await screen.findByText('Making a build current updates terminals')).toBeInTheDocument()
    expect(screen.getByText(/downloads it, writes it to flash and reboots/i)).toBeInTheDocument()
  })

  it('does NOT claim the platform has no over-the-air update', async () => {
    // A regression guard on the exact wording this page used to carry. It read
    // well, it was tested, and it was false.
    signIn()
    renderFirmware()

    await screen.findByText('Making a build current updates terminals')
    expect(screen.queryByText(/no over-the-air update/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/Nothing here updates a terminal/i)).not.toBeInTheDocument()
  })

  it('separates publishing from promoting, because only one of them sends anything', async () => {
    signIn()
    renderFirmware()

    await screen.findByText('Making a build current updates terminals')
    expect(screen.getByText(/nothing is offered until it is made current/i)).toBeInTheDocument()
  })

  it('says an empty catalogue makes every terminal LOOK up to date', async () => {
    // Not because they are, but because there is nothing to compare them with.
    signIn('ADMIN', [])
    renderFirmware()

    expect(await screen.findByText('No builds recorded')).toBeInTheDocument()
    expect(screen.getByText(/nothing to compare it with/i)).toBeInTheDocument()
  })

  it('says "mandatory" changes WHEN a terminal updates, not whether', async () => {
    // It used to say the field was recorded and never acted on. The device does
    // act on it — as a scheduling signal — and it does NOT relax any check,
    // which is the part that would be dangerous to imply.
    signIn()
    renderFirmware()

    await screen.findAllByText('Mandatory')
    expect(screen.getByText(/changes when, not whether/i)).toBeInTheDocument()
  })

  it('warns when a group has no current build at all', async () => {
    signIn('ADMIN', [deliverable({ id: 9, version: '1.4.0', is_current: false })])
    renderFirmware()

    expect(await screen.findByText('No current build for this combination')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// A build that cannot be sent
// ---------------------------------------------------------------------------

describe('a build the platform will never offer', () => {
  it('SAYS SO ON THE ROW, naming what is wrong with it', async () => {
    // The server withholds the offer and logs the reason where no operator can
    // read it. Without this the symptom is a fleet that silently never moves.
    signIn('ADMIN', [
      makeFirmwareVersion({ id: 7, version: '1.9.0', is_current: false, checksum_sha256: undefined }),
    ])
    renderFirmware()

    await screen.findByRole('heading', { name: 'Terminal · Stable' })
    const row = versionRow('1.9.0')
    expect(within(row).getByText('Cannot be sent')).toBeInTheDocument()
    expect(within(row).getByText(/no SHA-256 checksum/i)).toBeInTheDocument()
  })

  it('still offers to promote it rather than refusing on the platform’s behalf', async () => {
    // Refusing here would be the console inventing a rule the API does not have.
    signIn('ADMIN', [
      makeFirmwareVersion({ id: 7, version: '1.9.0', is_current: false, checksum_sha256: undefined }),
    ])
    renderFirmware()

    await screen.findByRole('heading', { name: 'Terminal · Stable' })
    expect(
      within(versionRow('1.9.0')).getByRole('button', { name: 'Make current' }),
    ).toBeInTheDocument()
  })

  it('confirms that promoting it will update nothing', async () => {
    const user = userEvent.setup()
    signIn('ADMIN', [
      makeFirmwareVersion({ id: 7, version: '1.9.0', is_current: false, checksum_sha256: undefined }),
    ])
    renderFirmware()

    await screen.findByRole('heading', { name: 'Terminal · Stable' })
    await user.click(within(versionRow('1.9.0')).getByRole('button', { name: 'Make current' }))

    const dialog = screen.getByRole('dialog')
    expect(within(dialog).getByText(/Nothing will be updated/i)).toBeInTheDocument()
    // And no typed phrase, because nothing is at risk.
    expect(within(dialog).queryByText(/to confirm/i)).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Publishing
// ---------------------------------------------------------------------------

describe('publishing a build', () => {
  it('does NOT start a rollout, and says so before and after', async () => {
    const user = userEvent.setup()
    signIn()
    renderFirmware()

    await screen.findByRole('heading', { name: 'Terminal · Stable' })
    await user.click(screen.getByRole('button', { name: 'Publish a build' }))

    const dialog = screen.getByRole('dialog')
    expect(within(dialog).getByText('This does not update anything yet')).toBeInTheDocument()

    await user.type(within(dialog).getByLabelText(/^Version/), '1.4.0')
    await user.click(within(dialog).getByRole('button', { name: 'Publish build' }))

    // The confirmation repeats it rather than saying "published".
    expect(
      await screen.findByText(/Nothing has been sent to any terminal/i),
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
    const row = versionRow('1.4.0')
    expect(within(row).getByText('Recorded')).toBeInTheDocument()
    expect(within(row).queryByText('Current target')).not.toBeInTheDocument()
  })

  it('refuses an UPPER-CASE checksum, which the platform would silently never send', async () => {
    const user = userEvent.setup()
    signIn()
    renderFirmware()

    await screen.findByRole('heading', { name: 'Terminal · Stable' })
    await user.click(screen.getByRole('button', { name: 'Publish a build' }))
    await user.type(screen.getByLabelText(/^Version/), '1.4.0')
    await user.type(screen.getByLabelText(/SHA-256/), 'A'.repeat(64))

    const before = state.requests.filter((entry) => entry.method === 'POST').length
    await user.click(screen.getByRole('button', { name: 'Publish build' }))

    // Matched on the error's own opening words: the field HINT also says
    // "64 lower-case hexadecimal characters", so a looser matcher would pass
    // whether or not the value was refused.
    expect(await screen.findByText(/A SHA-256 is 64 lower-case/i)).toBeInTheDocument()
    expect(screen.getByLabelText(/SHA-256/)).toHaveAttribute('aria-invalid', 'true')
    expect(state.requests.filter((entry) => entry.method === 'POST')).toHaveLength(before)
  })

  it('refuses a plaintext download address before the server sees it', async () => {
    const user = userEvent.setup()
    signIn()
    renderFirmware()

    await screen.findByRole('heading', { name: 'Terminal · Stable' })
    await user.click(screen.getByRole('button', { name: 'Publish a build' }))
    await user.type(screen.getByLabelText(/^Version/), '1.4.0')
    await user.type(screen.getByLabelText(/Download address/), 'http://builds.example/x.bin')
    await user.click(screen.getByRole('button', { name: 'Publish build' }))

    expect(await screen.findByText(/The download address must be https/i)).toBeInTheDocument()
  })

  it('collects the size, because the platform withholds an offer without one', async () => {
    // The field did not exist before, and its absence meant every build
    // published from this console was undeliverable.
    const user = userEvent.setup()
    signIn()
    renderFirmware()

    await screen.findByRole('heading', { name: 'Terminal · Stable' })
    await user.click(screen.getByRole('button', { name: 'Publish a build' }))
    expect(screen.getByLabelText(/File size in bytes/)).toBeInTheDocument()
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

  it('does not offer the stored location as a link', async () => {
    // Terminals fetch it; an operator should not, and a link invites somebody to
    // download a firmware image into their browser by accident.
    signIn('ADMIN', [deliverable({ id: 5, version: '1.5.0' })])
    renderFirmware()

    await screen.findByRole('heading', { name: 'Terminal · Stable' })
    expect(
      within(versionRow('1.5.0')).getByText('https://builds.example/terminal/image.bin'),
    ).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /builds\.example/ })).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Starting a rollout
// ---------------------------------------------------------------------------

describe('making a build current', () => {
  async function openPromotion(version = '1.3.0') {
    const user = userEvent.setup()
    signIn()
    renderFirmware()
    await screen.findByRole('heading', { name: 'Terminal · Stable' })
    await user.click(within(versionRow(version)).getByRole('button', { name: 'Make current' }))
    return { user, dialog: screen.getByRole('dialog') }
  }

  it('STATES THAT THIS CAN UPDATE TERMINALS, first', async () => {
    const { dialog } = await openPromotion()
    expect(within(dialog).getByText(/This can update terminals/i)).toBeInTheDocument()
    expect(
      within(dialog).getByText(/downloads the image, writes it to flash and reboots/i),
    ).toBeInTheDocument()
  })

  it('SHOWS THE AFFECTED TERMINAL COUNT, narrowed as the server narrows it', async () => {
    // Three terminals are on TERMINAL/STABLE and none of them runs 1.3.0, so all
    // three would be offered it. The beta terminal is not counted.
    const { dialog } = await openPromotion()
    expect(within(dialog).getByText(/3 terminals you can see/i)).toBeInTheDocument()
  })

  it('names the build it demotes and says a terminal is not rolled back', async () => {
    const { dialog } = await openPromotion()
    expect(within(dialog).getByText('1.2.0')).toBeInTheDocument()
    expect(within(dialog).getByText(/is not rolled back/i)).toBeInTheDocument()
    expect(within(dialog).getByText(/other channels are unaffected/i)).toBeInTheDocument()
  })

  it('says there is no undo', async () => {
    const { dialog } = await openPromotion()
    expect(within(dialog).getByText(/There is no undo/i)).toBeInTheDocument()
  })

  it('REQUIRES THE VERSION TO BE TYPED when hardware would actually change', async () => {
    // The console's strongest available signal that this is not a reporting
    // change. Reserved for the case where terminals will be offered the build.
    const { user, dialog } = await openPromotion()

    const confirm = within(dialog).getByRole('button', { name: 'Start the rollout' })
    expect(confirm).toBeDisabled()

    await user.type(within(dialog).getByLabelText(/to confirm/i), '1.3.0')
    expect(confirm).toBeEnabled()
  })

  it('moves the target and demotes the previous build', async () => {
    const { user, dialog } = await openPromotion()

    await user.type(within(dialog).getByLabelText(/to confirm/i), '1.3.0')
    await user.click(within(dialog).getByRole('button', { name: 'Start the rollout' }))

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())

    await waitFor(() =>
      expect(within(versionRow('1.3.0')).getByText('Current target')).toBeInTheDocument(),
    )
    expect(within(versionRow('1.2.0')).getByText('Recorded')).toBeInTheDocument()
  })

  it('leaves the other channel’s target alone', async () => {
    const { user, dialog } = await openPromotion()

    await user.type(within(dialog).getByLabelText(/to confirm/i), '1.3.0')
    await user.click(within(dialog).getByRole('button', { name: 'Start the rollout' }))

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(within(versionRow('2.0.0-beta1')).getByText('Current target')).toBeInTheDocument()
  })

  it('confirms afterwards that terminals will be offered it', async () => {
    const { user, dialog } = await openPromotion()

    await user.type(within(dialog).getByLabelText(/to confirm/i), '1.3.0')
    await user.click(within(dialog).getByRole('button', { name: 'Start the rollout' }))

    expect(
      await screen.findByText(/will be offered it on their next heartbeat/i),
    ).toBeInTheDocument()
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
    // change, `admin.GET("/firmware")` is ADMIN — and now that the current build
    // is the one a fleet installs, the read is as privileged as the write for a
    // stronger reason than before.
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
