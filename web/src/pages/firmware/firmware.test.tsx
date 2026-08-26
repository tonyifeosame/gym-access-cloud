import { readFileSync } from 'node:fs'
import { join } from 'node:path'

import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../../api/csrf'
import type { FirmwareVersion, Role, Terminal } from '../../api/types'
import { makeFirmwareVersion, makeSession, makeTerminal } from '../../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../../test/render'
import { failNext, resetServerState, seed, state } from '../../test/server'
import { FirmwarePage } from './FirmwarePage'
import { firmwareOfferability, terminalsOffered } from './offerability'
import { findings, fleetStanding, groupByTarget, standingOf } from './standing'

/**
 * Firmware.
 *
 * ---------------------------------------------------------------------------
 * THIS SUITE HAS NOW BEEN WRONG TWICE, IN OPPOSITE DIRECTIONS
 * ---------------------------------------------------------------------------
 *
 * It first asserted, carefully and in several places, that AccessLink had no
 * over-the-air update and that promoting a build sent nothing to anything. True
 * when written; false from the moment the heartbeat began carrying
 * `firmware_update` — at which point the tests were holding a dangerous screen
 * in place: a well-tested claim that a fleet-updating action was inert.
 *
 * It was then rewritten to assert the opposite, and got the general case right
 * and the specific one exactly wrong. It asserted that "Make current" on ANY
 * non-current build warned that terminals would download and install it — and
 * checked that wording on an OLDER build, where it is false. A terminal refuses
 * anything not strictly newer than what it runs (`firmware_update.cpp`), so
 * nothing installs; what happens instead is that `firmware_outdated`, an exact
 * string mismatch, flips true for the whole fleet, permanently, until a newer
 * version is set.
 *
 * SO THE TESTS BELOW ARE MOSTLY ABOUT WORDS, AND THOSE ARE THE VALUABLE ONES.
 * The API calls are three lines each and could not plausibly be wrong. The
 * sentence that stops somebody putting a fleet into a permanent false alarm is
 * easy to soften and never notice.
 */

/**
 * A version the platform would actually offer.
 *
 * DIGEST, SIZE AND AN HTTPS ADDRESS, because without all three the server
 * withholds every offer for the row. A fixture missing them would be testing the
 * update path against a version that can never be sent, which is exactly the
 * trap the screen exists to surface.
 */
function deliverable(overrides: Partial<FirmwareVersion> = {}): FirmwareVersion {
  return makeFirmwareVersion({
    checksum_sha256: 'a'.repeat(64),
    download_url: 'https://builds.example/terminal/image.bin',
    size_bytes: 1_842_000,
    // NOT CURRENT BY DEFAULT, unlike the shared fixture. Every version in a
    // group claiming to be the target makes the group's `current` whichever one
    // happened to be last, and the comparisons this suite is about then read
    // backwards. Callers opt in.
    is_current: false,
    ...overrides,
  })
}

/** The fleet panel, so its legend can be addressed without the cards below. */
function fleetPanel(): HTMLElement {
  const heading = screen.getByRole('heading', { name: 'Your terminals' })
  return heading.closest('section') as HTMLElement
}

/** The update card, whose numbers repeat inside Advanced by design. */
function updateCard(): HTMLElement {
  const heading = screen.getByRole('heading', { name: 'Update available' })
  return heading.closest('section') as HTMLElement
}

/** The "Cannot be installed" notice in the main view, not the row badge. */
function cannotInstallNotice(): HTMLElement {
  const heading = screen.getByRole('heading', { name: 'Cannot be installed' })
  return heading.closest('div') as HTMLElement
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
  // A DIFFERENT CHANNEL. The target is scoped per device type AND channel, so
  // this one has its own and is untouched by changes on STABLE.
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
  makeTerminal({
    id: 3,
    public_id: 't3',
    serial_number: 'AT-0003',
    firmware_version: '1.1.0',
    firmware_outdated: true,
  }),
  // On the beta channel, so it is measured against a different version entirely.
  makeTerminal({
    id: 4,
    public_id: 't4',
    serial_number: 'AT-0004',
    release_channel: 'BETA',
    firmware_version: '2.0.0-beta1',
  }),
]

function signIn(
  role: Role = 'ADMIN',
  firmware: FirmwareVersion[] = CATALOGUE,
  fleet: Terminal[] = FLEET,
) {
  const session = makeSession({
    role,
    operator: { id: 'operator-1', email: 'ops@example.com', full_name: 'Ops', role },
  })
  resetServerState(session)
  setCsrfToken(session.csrf_token)
  seed({ firmware, terminals: fleet })
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

// ---------------------------------------------------------------------------
// Reaching the two halves of the screen
// ---------------------------------------------------------------------------

/**
 * The Advanced disclosure, as an element.
 *
 * TESTS ASSERT CONTAINMENT RATHER THAN ABSENCE, throughout. A closed `<details>`
 * still has its children in the DOM, and jsdom does not apply the user-agent
 * rule that hides them — so `queryBy...` finds things inside it and an
 * `not.toBeInTheDocument()` assertion about the catalogue would pass whether or
 * not the catalogue had actually been demoted. Asking "is this node inside the
 * disclosure" is the question that has an honest answer here, and it is also
 * the one that stays true when somebody changes how the disclosure is drawn.
 */
function advanced(): HTMLElement {
  // BY SELECTOR, because the phrase is deliberately repeated in the main view:
  // the notices that send somebody in here name it exactly as it is labelled,
  // which is the point. Only one of those is the disclosure itself.
  const summary = screen.getByText('Advanced: all versions', { selector: 'summary' })
  const details = summary.closest('details')
  if (!details) throw new Error('the advanced disclosure is not a <details>')
  return details as HTMLElement
}

/** Whether a node lives behind the disclosure rather than in the main view. */
function isAdvanced(node: Element | null): boolean {
  return node !== null && advanced().contains(node)
}

/** Opens it, so the catalogue can be interacted with. */
function openAdvanced(): void {
  advanced().setAttribute('open', '')
}

/**
 * The catalogue row for one version.
 *
 * A version string appears more than once when it is the target: as the row's
 * own heading, again in the group summary, and possibly in the update card
 * above. That duplication is intentional, so this addresses rows specifically.
 */
function versionRow(version: string): HTMLElement {
  const row = within(advanced())
    .getAllByText(version)
    .map((node) => node.closest('li.rule'))
    .find(Boolean)
  if (!row) throw new Error(`no catalogue row for ${version}`)
  return row as HTMLElement
}

beforeEach(() => setCsrfToken(null))

// ---------------------------------------------------------------------------
// Where the fleet stands — the question the screen is now organised around
// ---------------------------------------------------------------------------

describe('fleet standing', () => {
  it('SPLITS "hasn’t reported" OUT OF "behind", which the API cannot do', () => {
    /*
      `database/firmware.go` computes firmware_outdated as TRUE when a terminal
      has never reported a version. Defensible server-side — it is certainly not
      known to be current — and a bad thing to show a customer, because "1
      terminal needs an update" sends somebody looking for an update that does
      not exist when a terminal has simply not been switched on yet.
    */
    const standing = fleetStanding([
      makeTerminal({ id: 1, firmware_version: '1.2.0', firmware_outdated: false }),
      makeTerminal({ id: 2, firmware_version: '1.1.0', firmware_outdated: true }),
      // Never checked in. The server marks it outdated; the customer must not
      // read that as an update waiting.
      makeTerminal({ id: 3, firmware_version: '', firmware_outdated: true }),
    ])

    expect(standing).toEqual({
      upToDate: 1,
      updateAvailable: 1,
      neverReported: 1,
      total: 3,
    })
  })

  it('does not let the flag override an absent version', () => {
    // The order of the branches is the whole fix: `firmware_version` is read
    // first, whatever `firmware_outdated` says about the same row.
    expect(
      fleetStanding([makeTerminal({ id: 1, firmware_version: '', firmware_outdated: true })]),
    ).toMatchObject({ updateAvailable: 0, neverReported: 1 })
  })

  it('counts an empty fleet as nothing rather than as anything', () => {
    expect(fleetStanding([])).toEqual({
      upToDate: 0,
      updateAvailable: 0,
      neverReported: 0,
      total: 0,
    })
  })

  it('SHOWS THE STANDING BEFORE THE CATALOGUE, which is the whole reordering', async () => {
    signIn()
    renderFirmware()

    const fleetHeading = await screen.findByRole('heading', { name: 'Your terminals' })
    // Not behind the disclosure, and physically ahead of it in the document.
    expect(isAdvanced(fleetHeading)).toBe(false)
    expect(
      fleetHeading.compareDocumentPosition(advanced()) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
  })

  it('names all three states on the screen, including the good news at zero', async () => {
    signIn()
    renderFirmware()

    await screen.findByRole('heading', { name: 'Your terminals' })
    // Scoped to the panel: "Update available" is deliberately also the heading
    // of the action card below, which is the point of using one word for one
    // thing.
    const legend = within(fleetPanel())
    expect(legend.getByText('Up to date')).toBeInTheDocument()
    expect(legend.getByText('Update available')).toBeInTheDocument()
    expect(legend.getByText('Hasn’t reported a version yet')).toBeInTheDocument()
  })

  it('says there is nothing to keep up to date when there are no terminals', async () => {
    signIn('ADMIN', CATALOGUE, [])
    renderFirmware()

    expect(await screen.findByText(/No terminals yet/i)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// What the customer should actually do
// ---------------------------------------------------------------------------

describe('findings', () => {
  const terminals = [
    makeTerminal({ id: 1, firmware_version: '1.2.0' }),
    makeTerminal({ id: 2, public_id: 't2', firmware_version: '1.2.0' }),
  ]

  function groupsFor(versions: FirmwareVersion[], fleet = terminals) {
    return groupByTarget(versions, fleet)
  }

  it('OFFERS THE NEWEST NEWER DELIVERABLE VERSION, and counts who would get it', () => {
    const found = findings(
      groupsFor([
        deliverable({ id: 1, version: '1.2.0', is_current: true, created_at: '2026-06-01T00:00:00Z' }),
        deliverable({ id: 2, version: '1.3.0', created_at: '2026-07-01T00:00:00Z' }),
        deliverable({ id: 3, version: '1.4.0', created_at: '2026-08-01T00:00:00Z' }),
      ]),
      { known: true, terminals },
    )

    expect(found).toHaveLength(1)
    expect(found).toMatchObject([{ kind: 'UPDATE', wouldReach: 2 }])
    // The NEWEST of the two candidates, not merely the first one found.
    expect(found.map((finding) => ('version' in finding ? finding.version.version : null))).toEqual([
      '1.4.0',
    ])
  })

  it('NEVER OFFERS AN OLDER VERSION AS AN UPDATE', () => {
    /*
      The defect this whole change exists for. An older version installs on
      nothing — the device refuses anything not strictly newer — and presenting
      it as an update is how a fleet ends up in a permanent false alarm.
    */
    const found = findings(
      groupsFor([
        deliverable({ id: 1, version: '1.2.0', is_current: true, created_at: '2026-06-01T00:00:00Z' }),
        deliverable({ id: 2, version: '1.1.0', created_at: '2026-03-01T00:00:00Z' }),
      ]),
      { known: true, terminals },
    )

    expect(found).toEqual([])
  })

  it('reports a newer version that cannot be installed rather than dropping it', () => {
    const found = findings(
      groupsFor([
        deliverable({ id: 1, version: '1.2.0', is_current: true, created_at: '2026-06-01T00:00:00Z' }),
        makeFirmwareVersion({
          id: 2,
          version: '1.4.0',
          is_current: false,
          created_at: '2026-08-01T00:00:00Z',
          checksum_sha256: undefined,
        }),
      ]),
      { known: true, terminals },
    )

    expect(found).toMatchObject([{ kind: 'UPDATE_BLOCKED' }])
  })

  it('reports a TARGET that cannot be installed, because the fleet is then frozen', () => {
    const found = findings(
      groupsFor([
        makeFirmwareVersion({ id: 1, version: '1.2.0', is_current: true, checksum_sha256: undefined }),
      ]),
      { known: true, terminals },
    )

    expect(found).toMatchObject([{ kind: 'TARGET_BLOCKED' }])
  })

  it('says so when terminals exist and no version is their target', () => {
    const found = findings(
      groupsFor([deliverable({ id: 1, version: '1.4.0', is_current: false })]),
      { known: true, terminals },
    )

    expect(found).toMatchObject([{ kind: 'NO_TARGET', versionCount: 1 }])
  })

  it('IGNORES GROUPS NOBODY HAS HARDWARE ON', () => {
    // A catalogue combination with no terminals is not a fault and not an
    // opportunity. A panel explaining that it affects nothing is a panel about
    // nothing, and two of them used to occupy half the page.
    const found = findings(
      groupsFor([
        deliverable({ id: 1, version: '9.0.0', device_type: 'READER', release_channel: 'CANARY' }),
      ]),
      { known: true, terminals },
    )

    expect(found).toEqual([])
  })

  it('offers nothing when the update would reach nobody', () => {
    const onNewest = [makeTerminal({ id: 1, firmware_version: '1.4.0' })]
    const found = findings(
      groupsFor(
        [
          deliverable({ id: 1, version: '1.2.0', is_current: true, created_at: '2026-06-01T00:00:00Z' }),
          deliverable({ id: 2, version: '1.4.0', created_at: '2026-08-01T00:00:00Z' }),
        ],
        onNewest,
      ),
      { known: true, terminals: onNewest },
    )

    expect(found.some((finding) => finding.kind === 'UPDATE')).toBe(false)
  })

  it('SAYS NOTHING AT ALL WHEN THE FLEET IS UNKNOWN', () => {
    // Every finding carries or implies a count, and a count derived from a list
    // that failed to load is the exact failure this screen has been bitten by.
    expect(findings(groupsFor(CATALOGUE), { known: false, reason: 'unavailable' })).toEqual([])
    expect(findings(groupsFor(CATALOGUE), { known: false, reason: 'loading' })).toEqual([])
  })
})

// ---------------------------------------------------------------------------
// The update card
// ---------------------------------------------------------------------------

describe('an available update', () => {
  it('IS THE PRIMARY ACTION, in the main view and not behind the disclosure', async () => {
    signIn()
    renderFirmware()

    const button = await screen.findByRole('button', { name: /^Update 3 terminals to 1\.3\.0$/ })
    expect(isAdvanced(button)).toBe(false)
    expect(button).toHaveClass('button--primary')
  })

  it('COUNTS THE TERMINALS IT WOULD REACH, narrowed as the server narrows it', async () => {
    // Three terminals are on TERMINAL/STABLE and none runs 1.3.0, so all three
    // would be offered it. The beta terminal is on another channel entirely.
    signIn()
    renderFirmware()

    await screen.findByRole('heading', { name: 'Update available' })
    const card = within(updateCard())
    expect(card.getByRole('button', { name: /Update 3 terminals to 1\.3\.0/ })).toBeInTheDocument()
    expect(card.getByText(/3 terminals you can see/)).toBeInTheDocument()
  })

  it('says what happens if the update is applied', async () => {
    signIn()
    renderFirmware()

    await screen.findByRole('heading', { name: 'Update available' })
    const card = within(updateCard())
    expect(card.getByText('If you update:')).toBeInTheDocument()
    expect(card.getByText(/installs it and restarts once/i)).toBeInTheDocument()
    expect(card.getByText(/at its next check-in/i)).toBeInTheDocument()
  })

  it('SAYS WHAT HAPPENS IF THEY DO NOTHING, which the old screen never did', async () => {
    // "Nothing" is the honest answer and worth printing. A screen that only
    // describes the consequences of acting reads as one that is pressing you to.
    signIn()
    renderFirmware()

    await screen.findByRole('heading', { name: 'Update available' })
    const card = within(updateCard())
    expect(card.getByText('If you don’t:')).toBeInTheDocument()
    expect(card.getByText(/go on working/i)).toBeInTheDocument()
  })

  it('carries the release notes, so the customer knows what they are taking', async () => {
    signIn()
    renderFirmware()

    await screen.findByRole('heading', { name: 'Update available' })
    expect(within(updateCard()).getByText('Faster wake from sleep.')).toBeInTheDocument()
  })

  it('NEVER SHOWS AN OLDER VERSION AS AN UPDATE', async () => {
    signIn('ADMIN', [
      deliverable({ id: 1, version: '1.2.0', is_current: true, created_at: '2026-06-01T00:00:00Z' }),
      deliverable({ id: 2, version: '1.1.0', created_at: '2026-03-01T00:00:00Z' }),
    ])
    renderFirmware()

    await screen.findByRole('heading', { name: 'Your terminals' })
    expect(screen.queryByRole('heading', { name: 'Update available' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^Update .* to 1\.1\.0$/ })).not.toBeInTheDocument()
  })

  it('is not offered to an operator who may not change the target', async () => {
    // The route itself is ADMIN, so this is defence in depth rather than a state
    // an operator can reach — but the guard is asserted where it is written.
    signIn()
    renderFirmware()
    expect(
      await screen.findByRole('button', { name: /^Update 3 terminals to 1\.3\.0$/ }),
    ).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Nothing to do
// ---------------------------------------------------------------------------

describe('when every terminal is up to date', () => {
  const allCurrent = [
    deliverable({ id: 1, version: '1.2.0', is_current: true, created_at: '2026-06-01T00:00:00Z' }),
  ]
  const settled = [
    makeTerminal({ id: 1, firmware_version: '1.2.0' }),
    makeTerminal({ id: 2, public_id: 't2', firmware_version: '1.2.0' }),
  ]

  it('OFFERS NO ACTION CARD, because there is no action', async () => {
    signIn('ADMIN', allCurrent, settled)
    renderFirmware()

    await screen.findByRole('heading', { name: 'Your terminals' })
    expect(screen.queryByRole('heading', { name: 'Update available' })).not.toBeInTheDocument()
  })

  it('SHOWS NO WARNING ABOUT UPDATING TERMINALS', async () => {
    /*
      The 91-word amber banner used to render whenever the catalogue was
      non-empty — including here, where there is nothing to press. At 390px it
      was 280px, a third of the first screen, cautioning about an action the page
      was not offering. A caution attached to no action is one people learn to
      scroll past.
    */
    signIn('ADMIN', allCurrent, settled)
    renderFirmware()

    await screen.findByRole('heading', { name: 'Your terminals' })
    expect(screen.queryByText(/Making a build current updates terminals/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/writes it to flash and reboots/i)).not.toBeInTheDocument()
  })

  it('still reaches the catalogue, which has simply stopped leading', async () => {
    signIn('ADMIN', allCurrent, settled)
    renderFirmware()

    await screen.findByRole('heading', { name: 'Your terminals' })
    expect(
      screen.getByText('Advanced: all versions', { selector: 'summary' }),
    ).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Cannot be installed
// ---------------------------------------------------------------------------

describe('a version the platform will never send', () => {
  it('IS REPORTED IN THE MAIN VIEW when it is the target, naming the field to fix', async () => {
    /*
      The server withholds the offer and logs the reason where no operator can
      read it. With the TARGET undeliverable nothing is being offered to anybody
      and the fleet is frozen — the single most important fault this screen can
      report, and it must not be behind a disclosure.
    */
    signIn('ADMIN', [
      makeFirmwareVersion({ id: 7, version: '1.9.0', is_current: true, checksum_sha256: undefined }),
    ])
    renderFirmware()

    const heading = await screen.findByRole('heading', { name: 'Cannot be installed' })
    expect(isAdvanced(heading)).toBe(false)
    const notice = within(cannotInstallNotice())
    expect(notice.getByText(/no SHA-256 checksum/i)).toBeInTheDocument()
    expect(notice.getByText(/will not send it to any of them/i)).toBeInTheDocument()
  })

  it('IS REPORTED WHEN IT IS THE NEWER VERSION, rather than vanishing', async () => {
    // Otherwise the update a customer is entitled to expect simply disappears
    // from the screen with no explanation.
    signIn('ADMIN', [
      deliverable({ id: 1, version: '1.2.0', is_current: true, created_at: '2026-06-01T00:00:00Z' }),
      makeFirmwareVersion({
        id: 2,
        version: '1.5.0',
        is_current: false,
        created_at: '2026-08-20T00:00:00Z',
        checksum_sha256: undefined,
      }),
    ])
    renderFirmware()

    const heading = await screen.findByRole('heading', { name: 'Cannot be installed' })
    expect(isAdvanced(heading)).toBe(false)
    const notice = within(cannotInstallNotice())
    expect(notice.getByText(/will not send it to anything/i)).toBeInTheDocument()
    expect(notice.getByText(/no SHA-256 checksum/i)).toBeInTheDocument()
  })

  it('lists EVERY reason at once rather than one per correction', async () => {
    signIn('ADMIN', [
      makeFirmwareVersion({
        id: 7,
        version: '1.9.0',
        is_current: true,
        checksum_sha256: undefined,
        download_url: undefined,
        size_bytes: undefined,
      }),
    ])
    renderFirmware()

    await screen.findByRole('heading', { name: 'Cannot be installed' })
    const notice = within(cannotInstallNotice())
    expect(notice.getByText(/no SHA-256 checksum/i)).toBeInTheDocument()
    expect(notice.getByText(/no file size/i)).toBeInTheDocument()
    expect(notice.getByText(/no download address/i)).toBeInTheDocument()
  })

  it('still badges the row inside Advanced', async () => {
    signIn('ADMIN', [
      deliverable({ id: 1, version: '1.2.0', is_current: true, created_at: '2026-06-01T00:00:00Z' }),
      makeFirmwareVersion({
        id: 2,
        version: '1.5.0',
        is_current: false,
        created_at: '2026-08-20T00:00:00Z',
        checksum_sha256: undefined,
      }),
    ])
    renderFirmware()

    await screen.findByRole('heading', { name: 'Your terminals' })
    openAdvanced()
    const row = versionRow('1.5.0')
    expect(within(row).getByText('Update available')).toBeInTheDocument()
    expect(within(row).getByText('Cannot be installed')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Advanced
// ---------------------------------------------------------------------------

describe('Advanced: all versions', () => {
  it('HOLDS THE CATALOGUE, including groups no terminal is on', async () => {
    signIn()
    renderFirmware()

    await screen.findByRole('heading', { name: 'Your terminals' })
    expect(isAdvanced(screen.getByRole('heading', { name: 'Terminal · Stable' }))).toBe(true)
    expect(isAdvanced(screen.getByRole('heading', { name: 'Terminal · Beta' }))).toBe(true)
  })

  it('KEEPS AN EMPTY GROUP OUT OF THE MAIN VIEW ENTIRELY', async () => {
    /*
      Two of a typical page's three panels described combinations the customer
      owned no hardware for, and together they were half the page. They are still
      here, in full, one press away.
    */
    signIn('ADMIN', [
      ...CATALOGUE,
      deliverable({ id: 9, version: '9.0.0', device_type: 'READER', release_channel: 'CANARY' }),
    ])
    renderFirmware()

    await screen.findByRole('heading', { name: 'Your terminals' })
    const empty = screen.getByRole('heading', { name: 'Reader · Canary' })
    expect(isAdvanced(empty)).toBe(true)
  })

  it('PUTS PUBLISHING BEHIND IT, capability and permission unchanged', async () => {
    signIn()
    renderFirmware()

    await screen.findByRole('heading', { name: 'Your terminals' })
    const publish = screen.getByRole('button', { name: 'Publish a version' })
    expect(isAdvanced(publish)).toBe(true)
  })

  it('repeats the caution, because the same action is available inside', async () => {
    signIn()
    renderFirmware()

    await screen.findByRole('heading', { name: 'Your terminals' })
    expect(
      within(advanced()).getByText(/installs it and restarts once, without anybody visiting/i),
    ).toBeInTheDocument()
  })

  it('offers "Set as target version" rather than a second primary action', async () => {
    signIn()
    renderFirmware()

    await screen.findByRole('heading', { name: 'Your terminals' })
    openAdvanced()

    const button = within(versionRow('1.3.0')).getByRole('button', {
      name: 'Set as target version',
    })
    // Never `button--danger` in here: the genuine update is the primary action
    // above, and three competing destructive buttons was the old failure.
    expect(button).not.toHaveClass('button--danger')
    expect(button).not.toHaveClass('button--primary')
  })

  it('offers nothing for the version already in use', async () => {
    signIn()
    renderFirmware()

    await screen.findByRole('heading', { name: 'Your terminals' })
    openAdvanced()
    expect(
      within(versionRow('1.2.0')).queryByRole('button', { name: 'Set as target version' }),
    ).not.toBeInTheDocument()
  })

  it('says an empty catalogue makes every terminal LOOK up to date', async () => {
    signIn('ADMIN', [])
    renderFirmware()

    await screen.findByRole('heading', { name: 'Your terminals' })
    expect(screen.getByText('No versions recorded')).toBeInTheDocument()
    expect(screen.getByText(/nothing to compare it with/i)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// The downgrade, which is the reason for all of this
// ---------------------------------------------------------------------------

describe('setting an older version as the target', () => {
  const withOlder = [
    deliverable({ id: 1, version: '1.2.0', is_current: true, created_at: '2026-06-01T00:00:00Z' }),
    deliverable({ id: 2, version: '1.1.0', created_at: '2026-03-01T00:00:00Z' }),
  ]

  async function openOlder() {
    const user = userEvent.setup()
    signIn('ADMIN', withOlder)
    renderFirmware()
    await screen.findByRole('heading', { name: 'Your terminals' })
    openAdvanced()
    await user.click(
      within(versionRow('1.1.0')).getByRole('button', { name: 'Set as target version' }),
    )
    return { user, dialog: await screen.findByRole('dialog') }
  }

  it('IS REACHABLE ONLY UNDER ADVANCED', async () => {
    signIn('ADMIN', withOlder)
    renderFirmware()

    await screen.findByRole('heading', { name: 'Your terminals' })
    openAdvanced()
    const button = within(versionRow('1.1.0')).getByRole('button', {
      name: 'Set as target version',
    })
    expect(isAdvanced(button)).toBe(true)
  })

  it('is marked on its own row as installing nothing', async () => {
    signIn('ADMIN', withOlder)
    renderFirmware()

    await screen.findByRole('heading', { name: 'Your terminals' })
    openAdvanced()
    const row = versionRow('1.1.0')
    expect(within(row).getByText('Older than the version in use')).toBeInTheDocument()
    expect(within(row).getByText(/refuse firmware older than what they already run/i)).toBeInTheDocument()
  })

  it('SAYS NOTHING WILL BE INSTALLED, which is the truth the old copy inverted', async () => {
    /*
      This dialog previously showed the upgrade copy word for word: "3 terminals
      will be offered it… downloads the image, writes it to flash and reboots."
      A terminal refuses anything not strictly newer than what it runs
      (`firmware_update.cpp` returns kUpToDate), so nothing installs, ever.
    */
    const { dialog } = await openOlder()

    expect(within(dialog).getByText(/Nothing will be installed/i)).toBeInTheDocument()
    expect(
      within(dialog).getByText(/refuses firmware older than what it already runs/i),
    ).toBeInTheDocument()
    expect(within(dialog).queryByText(/installs it and restarts once/i)).not.toBeInTheDocument()
  })

  it('NAMES THE REAL CONSEQUENCE: a fleet that reports as behind and cannot stop', async () => {
    // `firmware_outdated` is an exact string mismatch, so every terminal flips
    // to "update available" against a version none of them will ever accept.
    const { dialog } = await openOlder()

    expect(within(dialog).getByText(/What does change is the report/i)).toBeInTheDocument()
    expect(
      within(dialog).getByText(/cannot leave until a newer version is set as the target/i),
    ).toBeInTheDocument()
  })

  it('DOES NOT ASK FOR A TYPED VERSION, because no hardware changes', async () => {
    // Asking for one where nothing installs is how operators learn to type
    // phrases without reading them, which costs the protection where it counts.
    const { dialog } = await openOlder()

    expect(within(dialog).queryByLabelText(/to confirm/i)).not.toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Set as target version' })).toBeEnabled()
  })

  it('still allows it, rather than inventing a rule the platform does not have', async () => {
    const { user, dialog } = await openOlder()

    await user.click(within(dialog).getByRole('button', { name: 'Set as target version' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(
      await screen.findByText(/older than what your terminals run, so none of them will install it/i),
    ).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Starting a real update
// ---------------------------------------------------------------------------

describe('starting an update', () => {
  async function openUpdate() {
    const user = userEvent.setup()
    signIn()
    renderFirmware()
    await user.click(
      await screen.findByRole('button', { name: /^Update 3 terminals to 1\.3\.0$/ }),
    )
    return { user, dialog: await screen.findByRole('dialog') }
  }

  it('STATES THAT THIS CAN UPDATE TERMINALS, first', async () => {
    const { dialog } = await openUpdate()
    expect(within(dialog).getByText(/This can update terminals/i)).toBeInTheDocument()
    expect(within(dialog).getByText(/installs it and restarts once/i)).toBeInTheDocument()
  })

  it('shows the affected count, narrowed as the server narrows it', async () => {
    const { dialog } = await openUpdate()
    expect(within(dialog).getByText(/3 terminals you can see/i)).toBeInTheDocument()
  })

  it('names the version it replaces and says a terminal is not rolled back', async () => {
    const { dialog } = await openUpdate()
    expect(within(dialog).getByText('1.2.0')).toBeInTheDocument()
    expect(within(dialog).getByText(/is not rolled back/i)).toBeInTheDocument()
    expect(within(dialog).getByText(/other channels are unaffected/i)).toBeInTheDocument()
  })

  it('says there is no undo', async () => {
    const { dialog } = await openUpdate()
    expect(within(dialog).getByText(/There is no undo/i)).toBeInTheDocument()
  })

  it('REQUIRES THE VERSION TO BE TYPED when hardware would actually change', async () => {
    const { user, dialog } = await openUpdate()

    const confirm = within(dialog).getByRole('button', { name: 'Start the update' })
    expect(confirm).toBeDisabled()

    await user.type(within(dialog).getByLabelText(/to confirm/i), '1.3.0')
    expect(confirm).toBeEnabled()
  })

  it('moves the target and demotes the previous version', async () => {
    const { user, dialog } = await openUpdate()

    await user.type(within(dialog).getByLabelText(/to confirm/i), '1.3.0')
    await user.click(within(dialog).getByRole('button', { name: 'Start the update' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())

    openAdvanced()
    await waitFor(() =>
      expect(within(versionRow('1.3.0')).getByText('In use now')).toBeInTheDocument(),
    )
    expect(within(versionRow('1.2.0')).getByText('Older than the version in use')).toBeInTheDocument()
  })

  it('leaves the other channel’s target alone', async () => {
    const { user, dialog } = await openUpdate()

    await user.type(within(dialog).getByLabelText(/to confirm/i), '1.3.0')
    await user.click(within(dialog).getByRole('button', { name: 'Start the update' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())

    openAdvanced()
    expect(within(versionRow('2.0.0-beta1')).getByText('In use now')).toBeInTheDocument()
  })

  it('confirms afterwards in the customer’s words', async () => {
    const { user, dialog } = await openUpdate()

    await user.type(within(dialog).getByLabelText(/to confirm/i), '1.3.0')
    await user.click(within(dialog).getByRole('button', { name: 'Start the update' }))

    expect(
      await screen.findByText(/will be offered it at their next check-in/i),
    ).toBeInTheDocument()
  })

  it('DOES NOT CALL AN EMPTY COMBINATION "already running it"', async () => {
    /*
      "Nobody is here" and "everybody already has it" are different facts, and
      the dialog printed the second for both — telling somebody that a
      combination with zero terminals was all already running the version.
    */
    const user = userEvent.setup()
    signIn('ADMIN', [
      deliverable({ id: 1, version: '1.2.0', is_current: true, created_at: '2026-06-01T00:00:00Z' }),
      deliverable({
        id: 9,
        version: '9.0.0',
        device_type: 'READER',
        release_channel: 'CANARY',
        created_at: '2026-08-01T00:00:00Z',
      }),
    ])
    renderFirmware()

    await screen.findByRole('heading', { name: 'Your terminals' })
    openAdvanced()
    await user.click(
      within(versionRow('9.0.0')).getByRole('button', { name: 'Set as target version' }),
    )

    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByText(/No terminal you can see is on this hardware type and channel at all/i),
    ).toBeInTheDocument()
    expect(within(dialog).queryByText(/already running/i)).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// When the terminal list cannot be read
// ---------------------------------------------------------------------------

/**
 * Every count on this screen comes from a SECOND request, and its error state
 * was once unchecked — so an empty terminal list meant "nothing is affected" and
 * "we could not find out" at the same time, and the page chose the first. With
 * the fleet endpoint down it told an operator that a build nothing was running
 * would reach nobody, AND removed the typed-phrase safeguard, because that
 * safeguard was armed by `affected > 0`.
 */
describe('when the terminal list cannot be read', () => {
  function signInWithoutFleet(firmware: FirmwareVersion[] = CATALOGUE) {
    const session = signIn('ADMIN', firmware)
    failNext('terminals-list', 500)
    return session
  }

  it('SAYS SO INSTEAD OF DRAWING A METER OF ZEROES', async () => {
    // An empty bar reads "0 update available" — the good news, on no evidence.
    signInWithoutFleet()
    renderFirmware()

    expect(
      await screen.findByText(/The list of terminals could not be loaded/i),
    ).toBeInTheDocument()
    expect(screen.queryByText('Up to date')).not.toBeInTheDocument()
  })

  it('OFFERS NO UPDATE IT CANNOT DESCRIBE', async () => {
    signInWithoutFleet()
    renderFirmware()

    await screen.findByText(/The list of terminals could not be loaded/i)
    expect(screen.queryByRole('heading', { name: 'Update available' })).not.toBeInTheDocument()
  })

  it('REFUSES A TARGET CHANGE IT CANNOT DESCRIBE', async () => {
    signInWithoutFleet()
    renderFirmware()

    await screen.findByText(/The list of terminals could not be loaded/i)
    openAdvanced()
    const button = within(versionRow('1.3.0')).getByRole('button', {
      name: 'Set as target version',
    })
    expect(button).toBeDisabled()
    expect(
      within(versionRow('1.3.0')).getByText(/Unavailable while terminal numbers cannot be read/i),
    ).toBeInTheDocument()
  })

  it('does not claim a deliverable version would reach nobody', async () => {
    signInWithoutFleet()
    renderFirmware()

    await screen.findByText(/The list of terminals could not be loaded/i)
    openAdvanced()
    expect(
      within(versionRow('1.3.0')).getByText(/How many terminals this would reach is unavailable/i),
    ).toBeInTheDocument()
  })

  it('KEEPS THE CATALOGUE ITSELF USABLE, because only the counts are missing', async () => {
    signInWithoutFleet()
    renderFirmware()

    await screen.findByText(/The list of terminals could not be loaded/i)
    openAdvanced()
    expect(within(versionRow('1.2.0')).getByText('In use now')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Publish a version' })).toBeEnabled()
  })
})

// ---------------------------------------------------------------------------
// Publishing — unchanged capability, new home
// ---------------------------------------------------------------------------

describe('publishing a version', () => {
  async function openPublish() {
    const user = userEvent.setup()
    signIn()
    renderFirmware()
    await screen.findByRole('heading', { name: 'Your terminals' })
    openAdvanced()
    await user.click(screen.getByRole('button', { name: 'Publish a version' }))
    return { user, dialog: await screen.findByRole('dialog') }
  }

  it('does NOT start an update, and says so before and after', async () => {
    const { user, dialog } = await openPublish()

    expect(within(dialog).getByText('This does not update anything yet')).toBeInTheDocument()

    await user.type(within(dialog).getByLabelText(/^Version/), '1.4.0')
    await user.click(within(dialog).getByRole('button', { name: 'Publish version' }))

    expect(
      await screen.findByText(/Nothing has been sent to any terminal/i),
    ).toBeInTheDocument()
  })

  it('lands in the catalogue as recorded rather than as the target', async () => {
    const { user, dialog } = await openPublish()

    await user.type(within(dialog).getByLabelText(/^Version/), '1.4.0')
    await user.click(within(dialog).getByRole('button', { name: 'Publish version' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())

    openAdvanced()
    await waitFor(() => expect(versionRow('1.4.0')).toBeInTheDocument())
    // Still the old target: publishing changed nothing about what is offered.
    expect(within(versionRow('1.2.0')).getByText('In use now')).toBeInTheDocument()
  })

  it('refuses an UPPER-CASE checksum, which the platform would silently never send', async () => {
    const { user, dialog } = await openPublish()

    await user.type(within(dialog).getByLabelText(/^Version/), '1.4.0')
    await user.type(within(dialog).getByLabelText(/SHA-256/), 'A'.repeat(64))
    await user.click(within(dialog).getByRole('button', { name: 'Publish version' }))

    // ON THE HALF THE HINT DOES NOT ALSO SAY. The field's hint carries "64
    // lower-case hexadecimal characters" too, and asserting on that would pass
    // against a form that never validated anything.
    expect(
      await screen.findByText(/The platform and the terminal compare it exactly/i),
    ).toBeInTheDocument()
  })

  it('refuses a plaintext download address before the server sees it', async () => {
    const { user, dialog } = await openPublish()

    await user.type(within(dialog).getByLabelText(/^Version/), '1.4.0')
    await user.type(within(dialog).getByLabelText(/Download address/), 'http://builds.example/x.bin')
    await user.click(within(dialog).getByRole('button', { name: 'Publish version' }))

    expect(
      await screen.findByText(/The download address must be https/i),
    ).toBeInTheDocument()
  })

  it('collects the size, because the platform withholds an offer without one', async () => {
    const { user, dialog } = await openPublish()

    await user.type(within(dialog).getByLabelText(/^Version/), '1.4.0')
    await user.type(within(dialog).getByLabelText(/File size/), '1842000')
    await user.click(within(dialog).getByRole('button', { name: 'Publish version' }))

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    const published = state.requests.find((entry) => entry.method === 'POST' && /firmware/.test(entry.url))
    expect(published).toBeDefined()
  })

  it('turns a duplicate version into something actionable', async () => {
    const { user, dialog } = await openPublish()

    await user.type(within(dialog).getByLabelText(/^Version/), '1.2.0')
    await user.click(within(dialog).getByRole('button', { name: 'Publish version' }))

    expect(await screen.findByText(/That version already exists/i)).toBeInTheDocument()
  })

  it('PINS ITS ACTIONS IN THE DIALOG FOOTER, outside the scrolling body', async () => {
    // Seven fields with a hint apiece put the submit 400px below the fold at
    // 1440x900 and 680px below at 390 when it lived at the end of the form.
    const { dialog } = await openPublish()

    const submit = within(dialog).getByRole('button', { name: 'Publish version' })
    expect(submit.closest('.dialog__footer')).not.toBeNull()
    expect(submit.closest('form')).toBeNull()
  })

  it('still submits the form it is no longer inside', async () => {
    const { user, dialog } = await openPublish()

    const form = dialog.querySelector('form')
    const submit = within(dialog).getByRole('button', { name: 'Publish version' })
    expect(submit).toHaveAttribute('form', form?.id)

    await user.type(within(dialog).getByLabelText(/^Version/), '1.4.0')
    await user.click(submit)
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('does not offer the stored location as a link', async () => {
    // Terminals fetch it; offering it as something to click invites somebody to
    // download a firmware image into their browser by accident.
    signIn()
    renderFirmware()

    await screen.findByRole('heading', { name: 'Your terminals' })
    openAdvanced()
    const row = versionRow('1.2.0')
    expect(
      within(row).queryByRole('link', { name: /builds\.example/ }),
    ).not.toBeInTheDocument()
    expect(within(row).getByText('https://builds.example/terminal/image.bin')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// The verification material
// ---------------------------------------------------------------------------

describe('the verification material is kept, not deleted', () => {
  it('KEEPS THE DIGEST AND THE ADDRESS BEHIND THEIR OWN DISCLOSURE', async () => {
    signIn()
    renderFirmware()

    await screen.findByRole('heading', { name: 'Your terminals' })
    openAdvanced()
    const row = versionRow('1.2.0')
    const details = within(row).getByText('Image details').closest('details')

    expect(details).not.toBeNull()
    expect(details?.contains(within(row).getByText('a'.repeat(64)))).toBe(true)
  })
})

// ---------------------------------------------------------------------------
// Pure modules
// ---------------------------------------------------------------------------

describe('offerability mirrors what the server will withhold', () => {
  it('accepts a version with a digest, a size and an https address', () => {
    expect(firmwareOfferability(deliverable())).toEqual({ deliverable: true, problems: [] })
  })

  it('refuses one with no checksum, because a terminal refuses an offer without one', () => {
    const result = firmwareOfferability(deliverable({ checksum_sha256: undefined }))
    expect(result.deliverable).toBe(false)
    expect(result.problems[0]).toMatch(/SHA-256/)
  })

  it('refuses an UPPER-CASE digest, which is the mistake that looks fine', () => {
    const result = firmwareOfferability(deliverable({ checksum_sha256: 'A'.repeat(64) }))
    expect(result.deliverable).toBe(false)
    expect(result.problems[0]).toMatch(/lower-case/)
  })

  it('refuses a missing or zero size, which sizes the flash write', () => {
    expect(firmwareOfferability(deliverable({ size_bytes: 0 })).deliverable).toBe(false)
    expect(firmwareOfferability(deliverable({ size_bytes: undefined })).deliverable).toBe(false)
  })

  it('refuses a plaintext download address', () => {
    const result = firmwareOfferability(
      deliverable({ download_url: 'http://builds.example/image.bin' }),
    )
    expect(result.deliverable).toBe(false)
    expect(result.problems[0]).toMatch(/https/)
  })

  it('refuses strings longer than the device’s fixed buffers', () => {
    const long = `https://builds.example/${'x'.repeat(120)}.bin`
    expect(firmwareOfferability(deliverable({ download_url: long })).deliverable).toBe(false)
    expect(firmwareOfferability(deliverable({ version: 'v'.repeat(30) })).deliverable).toBe(false)
  })

  it('reports EVERY problem at once rather than one per correction', () => {
    const result = firmwareOfferability(
      makeFirmwareVersion({
        checksum_sha256: undefined,
        download_url: undefined,
        size_bytes: undefined,
      }),
    )
    expect(result.problems).toHaveLength(3)
  })
})

describe('who would be offered a version', () => {
  it('narrows by device type, channel, and what a terminal already runs', () => {
    const version = { version: '1.3.0', device_type: 'TERMINAL', release_channel: 'STABLE' }
    expect(terminalsOffered(version, FLEET).map((t) => t.serial_number)).toEqual([
      'AT-0001',
      'AT-0002',
      'AT-0003',
    ])
  })

  it('leaves out terminals already running the version', () => {
    const version = { version: '1.2.0', device_type: 'TERMINAL', release_channel: 'STABLE' }
    expect(terminalsOffered(version, FLEET).map((t) => t.serial_number)).toEqual(['AT-0003'])
  })

  it('never crosses a release channel', () => {
    const version = { version: '3.0.0', device_type: 'TERMINAL', release_channel: 'BETA' }
    expect(terminalsOffered(version, FLEET).map((t) => t.serial_number)).toEqual(['AT-0004'])
  })
})

describe('grouping the catalogue', () => {
  it('groups by the pair the target is actually scoped to', () => {
    const groups = groupByTarget(CATALOGUE, FLEET)
    expect(groups.map((group) => group.key)).toEqual(['TERMINAL--STABLE', 'TERMINAL--BETA'])
  })

  it('PUTS THE GROUP WITH THE MOST TERMINALS FIRST', () => {
    // This once sorted alphabetically on a machine name, so a combination with
    // no hardware outranked the one carrying the fleet.
    const groups = groupByTarget(CATALOGUE, FLEET)
    expect(groups.map((group) => group.terminalCount)).toEqual([3, 1])
  })

  it('counts how many are ALREADY on the current version', () => {
    const groups = groupByTarget(CATALOGUE, FLEET)
    expect(groups.map((group) => group.onCurrent)).toEqual([2, 1])
  })

  it('reports nothing on the current version when there is no target', () => {
    const groups = groupByTarget([deliverable({ id: 1, version: '1.4.0' })], FLEET)
    expect(groups.map((group) => group.current)).toEqual([null])
    expect(groups.map((group) => group.onCurrent)).toEqual([0])
  })

  it('pins the version in use to the top of its group', () => {
    const groups = groupByTarget(CATALOGUE, FLEET)
    expect(groups.map((group) => group.versions.map((version) => version.version))).toEqual([
      ['1.2.0', '1.3.0'],
      ['2.0.0-beta1'],
    ])
  })
})

describe('how a version stands against the target', () => {
  const current = deliverable({ id: 1, version: '1.2.0', is_current: true, created_at: '2026-06-01T00:00:00Z' })

  it('distinguishes newer from older by publication date', () => {
    expect(standingOf(deliverable({ id: 2, created_at: '2026-08-01T00:00:00Z' }), current)).toBe('NEWER')
    expect(standingOf(deliverable({ id: 3, created_at: '2026-03-01T00:00:00Z' }), current)).toBe('OLDER')
  })

  it('makes no comparison when there is no target to compare against', () => {
    // "Older" would be inventing a comparison against nothing.
    expect(standingOf(deliverable({ id: 2 }), null)).toBe('UNCOMPARED')
  })

  it('calls the target itself current', () => {
    expect(standingOf(current, current)).toBe('CURRENT')
  })
})

// ---------------------------------------------------------------------------
// The screen's own states
// ---------------------------------------------------------------------------

describe('the page keeps its identity while it loads', () => {
  it('RENDERS THE HEADING BEFORE THE CATALOGUE ARRIVES', async () => {
    // This once returned a bare spinner, so the document had no `<h1>` at all
    // while the catalogue loaded — an axe `page-has-heading-one` violation.
    signIn()
    renderFirmware()

    expect(screen.getByRole('heading', { name: 'Firmware', level: 1 })).toBeInTheDocument()
    await screen.findByRole('heading', { name: 'Your terminals' })
  })

  it('keeps it when the catalogue fails to load', async () => {
    signIn()
    failNext('firmware', 500)
    renderFirmware()

    await screen.findByRole('alert')
    expect(screen.getByRole('heading', { name: 'Firmware', level: 1 })).toBeInTheDocument()
  })
})

describe('role gating mirrors the server', () => {
  it('REFUSES A MANAGER THE CATALOGUE ENTIRELY, read included', async () => {
    signIn('MANAGER')
    renderFirmware()

    expect(await screen.findByRole('alert')).toHaveTextContent(/Insufficient permissions/)
    expect(screen.queryByRole('button', { name: 'Publish a version' })).not.toBeInTheDocument()
  })

  it('reports a failed load as an error rather than as an empty catalogue', async () => {
    // "No versions" and "we could not ask" mean opposite things: the first says
    // every terminal is up to date.
    signIn()
    failNext('firmware', 500)
    renderFirmware()

    expect(await screen.findByRole('alert')).toHaveTextContent(
      /Failed to retrieve firmware versions/,
    )
  })
})

// ---------------------------------------------------------------------------
// Terminology
// ---------------------------------------------------------------------------

describe('the screen speaks the console’s own vocabulary', () => {
  it('SAYS VERSION, NOT BUILD, in the customer-facing half', async () => {
    signIn()
    renderFirmware()

    await screen.findByRole('heading', { name: 'Your terminals' })

    // Everything outside the disclosure, which is what an ordinary visit reads.
    const page = document.querySelector('.page') as HTMLElement
    const main = page.cloneNode(true) as HTMLElement
    main.querySelector('details')?.remove()
    const text = (main.textContent ?? '').toLowerCase()

    for (const jargon of ['build', 'catalogue', 'heartbeat', 'flash', 'over the air', 'make current']) {
      expect(text, `the main view must not say "${jargon}"`).not.toContain(jargon)
    }
    expect(text).toContain('version')
    expect(text).toContain('check-in')
  })

  it('badges a version in the words the rest of the console uses', async () => {
    signIn()
    renderFirmware()

    await screen.findByRole('heading', { name: 'Your terminals' })
    openAdvanced()
    expect(within(versionRow('1.2.0')).getByText('In use now')).toBeInTheDocument()
    expect(within(versionRow('1.3.0')).getByText('Update available')).toBeInTheDocument()
  })

  it('calls a mandatory version "Install sooner" and says what that means', async () => {
    // It used to be recorded and never acted on. The device acts on it — as a
    // scheduling signal — and it relaxes no check, which is the part that would
    // be dangerous to imply.
    signIn()
    renderFirmware()

    await screen.findByRole('heading', { name: 'Your terminals' })
    openAdvanced()
    const row = versionRow('2.0.0-beta1')
    expect(within(row).getByText('Install sooner')).toBeInTheDocument()
    expect(within(row).getByText(/changes when, not whether/i)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// The focus ring on a disclosure (F8)
// ---------------------------------------------------------------------------

describe('a disclosure focuses like every other control', () => {
  it('IS IN THE CONSOLE’S OWN :focus-visible RULE', () => {
    /*
      A <summary> is natively focusable and is not an <a>, a <button> or a
      [tabindex], so it fell through the selector list and drew the browser's own
      1px outline while every control beside it drew the 2px accent ring.
      Measured in Chrome on this screen, where four disclosures sit in the tab
      order directly before the primary action.

      READ FROM THE STYLESHEET THAT SHIPS, as contrast.test.ts reads the palette:
      a copy of the rule here would keep passing after somebody edited the real
      one, which is the change this exists to catch.
    */
    const css = readFileSync(join(process.cwd(), 'src', 'styles', 'primitives.css'), 'utf8')
    const rule = css.slice(css.indexOf('a:focus-visible'), css.indexOf('/* --- page ---'))

    expect(rule).toContain('summary:focus-visible')
    expect(rule).toMatch(/outline:\s*2px solid var\(--accent\)/)
  })
})
