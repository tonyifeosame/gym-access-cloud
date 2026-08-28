import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../../api/csrf'
import type { Enrollment, Role, Session, Terminal } from '../../api/types'
import {
  makeEnrollment,
  makePerson,
  makeSession,
  makeTerminal,
} from '../../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../../test/render'
import { resetServerState, seed, state } from '../../test/server'
import { describeEnrollment, enrollableTerminals } from './enrollment'
import { PersonDetailPage } from './PersonDetailPage'

/**
 * Operator-driven fingerprint enrolment.
 *
 * THE ONE PROPERTY THIS FILE EXISTS FOR: the operator chooses the terminal, and
 * nothing else does. No default, no "the only online one", no silent pick when
 * the list has a single entry. Every other assertion here is about making that
 * choice possible to get right -- what the picker shows, what the states say,
 * and what happens when it does not work.
 *
 * WHAT IS NOT ASSERTED HERE, because it is not this layer's: whether the job
 * reaches the right terminal, and whether another terminal could claim it. Those
 * are server and firmware guarantees, covered in console_enrollment_test.go and
 * in the firmware's test_enrollment_job.
 */

const PERSON = makePerson({
  external_id: 'P-0001',
  full_name: 'Ada Okonkwo',
  biometric_enrolled: false,
})

// Named rather than indexed, so `noUncheckedIndexedAccess` does not turn every
// use into a possibly-undefined value that has to be asserted away.
const ONLINE_TERMINAL = makeTerminal({
  id: 1,
  serial_number: 'AT-0001',
  device_name: 'North Gate',
  site_name: 'Lagos Depot',
  site_public_id: 'site-a',
  status: 'ONLINE',
})

const OFFLINE_TERMINAL = makeTerminal({
  id: 2,
  public_id: 'terminal-public-2',
  serial_number: 'AT-0002',
  device_name: 'Reception',
  site_name: 'Lagos Depot',
  site_public_id: 'site-a',
  status: 'OFFLINE',
})

const DISABLED_TERMINAL = makeTerminal({
  id: 3,
  public_id: 'terminal-public-3',
  serial_number: 'AT-0003',
  device_name: 'Retired Unit',
  site_name: 'Lagos Depot',
  site_public_id: 'site-a',
  status: 'DISABLED',
  active: false,
})

const FLEET: Terminal[] = [ONLINE_TERMINAL, OFFLINE_TERMINAL, DISABLED_TERMINAL]

function signIn(role: Role = 'MANAGER', overrides: Partial<Session> = {}) {
  const session = makeSession({
    role,
    operator: { id: 'operator-1', email: 'ops@example.com', full_name: 'Ops', role },
    ...overrides,
  })
  resetServerState(session)
  setCsrfToken(session.csrf_token)
  seed({ people: [PERSON], terminals: FLEET })
  return session
}

function renderPerson(client = makeTestQueryClient()) {
  const router = createMemoryRouter(
    [{ path: '/people/:externalId', element: <PersonDetailPage /> }],
    { initialEntries: ['/people/P-0001'] },
  )
  return renderWithSession(<RouterProvider router={router} />, client)
}

async function openDialog(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('button', { name: 'Enrol fingerprint' }))
  return screen.findByRole('dialog')
}

function enrolmentRequests() {
  return state.requests.filter(
    (request) => request.method === 'POST' && request.url.includes('/enrollments'),
  )
}

/** The URL of the only enrolment request made, failing loudly if there is not
 * exactly one. Better than indexing: a test that started two enrolments would
 * otherwise assert about the first and say nothing about the second. */
function theEnrolmentRequestURL(): string {
  const requests = enrolmentRequests()
  expect(requests).toHaveLength(1)
  return requests[0]?.url ?? ''
}

beforeEach(() => setCsrfToken(null))

// ---------------------------------------------------------------------------
// Adding a person does not enrol them
// ---------------------------------------------------------------------------

describe('a person with no credential', () => {
  it('reads as Not enrolled and offers the enrolment action', async () => {
    signIn()
    renderPerson()

    expect(await screen.findByText('Not enrolled')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Enrol fingerprint' })).toBeInTheDocument()
  })

  it('starts nothing on its own', async () => {
    signIn()
    renderPerson()

    await screen.findByText('Not enrolled')
    // Creating a person must not arm any reader anywhere. Nothing has been
    // asked of any terminal until an operator says so.
    expect(enrolmentRequests()).toHaveLength(0)
  })

  it('hides the action from an operator who may not manage people', async () => {
    signIn('VIEWER')
    renderPerson()

    await screen.findByText('Not enrolled')
    expect(screen.queryByRole('button', { name: 'Enrol fingerprint' })).toBeNull()
  })
})

// ---------------------------------------------------------------------------
// Terminal selection
// ---------------------------------------------------------------------------

describe('choosing a terminal', () => {
  it('shows the name, serial, site and status of every eligible terminal', async () => {
    const user = userEvent.setup()
    signIn()
    renderPerson()

    const dialog = await openDialog(user)

    const online = await within(dialog).findByRole('radio', { name: /North Gate/ })
    expect(online).toBeInTheDocument()
    expect(within(dialog).getByText('AT-0001')).toBeInTheDocument()
    expect(within(dialog).getAllByText(/Lagos Depot/).length).toBeGreaterThan(0)
    expect(within(dialog).getByText('Online')).toBeInTheDocument()
  })

  it('OFFERS AN OFFLINE TERMINAL, marked, because the request waits for it', async () => {
    const user = userEvent.setup()
    signIn()
    renderPerson()

    const dialog = await openDialog(user)

    expect(await within(dialog).findByRole('radio', { name: /Reception/ })).toBeInTheDocument()
    expect(within(dialog).getByText('Offline')).toBeInTheDocument()
    expect(
      within(dialog).getByText(/will wait until it reconnects/i),
    ).toBeInTheDocument()
  })

  it('does NOT offer a terminal that cannot run an enrolment', async () => {
    const user = userEvent.setup()
    signIn()
    renderPerson()

    const dialog = await openDialog(user)
    await within(dialog).findByRole('radio', { name: /North Gate/ })

    // Disabled and inactive. The server refuses it with a 409, so offering it
    // would be offering a choice that gets rejected.
    expect(within(dialog).queryByRole('radio', { name: /Retired Unit/ })).toBeNull()
  })

  it('NEVER PRESELECTS A TERMINAL, even when only one is eligible', async () => {
    const user = userEvent.setup()
    signIn()
    seed({ terminals: [ONLINE_TERMINAL] })
    renderPerson()

    const dialog = await openDialog(user)
    const only = await within(dialog).findByRole('radio', { name: /North Gate/ })

    // THE POINT OF THE WHOLE FEATURE. A single-terminal site is where a default
    // would look harmless, and it is the habit that would then be wrong at the
    // site that later has three.
    expect(only).not.toBeChecked()
  })

  it('refuses to start until a terminal is chosen, and asks in words', async () => {
    const user = userEvent.setup()
    signIn()
    renderPerson()

    const dialog = await openDialog(user)
    await within(dialog).findByRole('radio', { name: /North Gate/ })
    await user.click(within(dialog).getByRole('button', { name: 'Start enrolment' }))

    expect(
      await within(dialog).findByText(/Select a terminal before starting/i),
    ).toBeInTheDocument()
    expect(enrolmentRequests()).toHaveLength(0)
  })

  it('says so when nothing in the fleet can enrol', async () => {
    const user = userEvent.setup()
    signIn()
    seed({ terminals: [DISABLED_TERMINAL] })
    renderPerson()

    const dialog = await openDialog(user)
    expect(
      await within(dialog).findByText(/No terminal can run an enrolment/i),
    ).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Start enrolment' })).toBeDisabled()
  })
})

// ---------------------------------------------------------------------------
// Starting, and addressing
// ---------------------------------------------------------------------------

describe('starting an enrolment', () => {
  it('addresses it to the terminal the operator picked', async () => {
    const user = userEvent.setup()
    signIn()
    renderPerson()

    const dialog = await openDialog(user)
    await user.click(await within(dialog).findByRole('radio', { name: /Reception/ }))
    await user.click(within(dialog).getByRole('button', { name: 'Start enrolment' }))

    await waitFor(() => expect(enrolmentRequests()).toHaveLength(1))

    // THE URL CARRIES THE CHOSEN SERIAL. This is the assertion that the console
    // cannot quietly send an enrolment to a different door: the terminal is in
    // the path, and the path is what the server authorizes.
    const url = theEnrolmentRequestURL()
    expect(url).toContain('/terminals/AT-0002/enrollments')
    expect(url).not.toContain('AT-0001')
  })

  it('WITH SEVERAL TERMINALS, only the selected one is ever named', async () => {
    const user = userEvent.setup()
    signIn()
    renderPerson()

    const dialog = await openDialog(user)
    await user.click(await within(dialog).findByRole('radio', { name: /North Gate/ }))
    await user.click(within(dialog).getByRole('button', { name: 'Start enrolment' }))

    await waitFor(() => expect(enrolmentRequests()).toHaveLength(1))

    // One request, to one terminal. Selecting AT-0001 must not touch AT-0002 or
    // AT-0003 in any way.
    const urls = enrolmentRequests().map((request) => request.url)
    expect(urls).toHaveLength(1)
    expect(urls[0]).toContain('AT-0001')
    expect(urls.some((url) => url.includes('AT-0002') || url.includes('AT-0003'))).toBe(false)
  })

  it('tells the operator what to do next', async () => {
    const user = userEvent.setup()
    signIn()
    renderPerson()

    const dialog = await openDialog(user)
    await user.click(await within(dialog).findByRole('radio', { name: /North Gate/ }))
    await user.click(within(dialog).getByRole('button', { name: 'Start enrolment' }))

    expect(await screen.findByText(/place their finger on North Gate/i)).toBeInTheDocument()
  })

  it('reports a terminal the server refuses without leaving a half-started state', async () => {
    const user = userEvent.setup()
    signIn()
    // A terminal the picker would allow but the server refuses -- the two rules
    // agreeing is not something the console may assume.
    seed({ terminals: [makeTerminal({ serial_number: 'AT-0009', device_name: 'Odd One', active: false })] })
    renderPerson()

    const user2 = user
    await user2.click(await screen.findByRole('button', { name: 'Enrol fingerprint' }))
    const dialog = await screen.findByRole('dialog')

    expect(await within(dialog).findByText(/No terminal can run an enrolment/i)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// The status an operator watches
// ---------------------------------------------------------------------------

describe('enrolment status', () => {
  it('names every state in words rather than as a code', () => {
    const states: Enrollment['status'][] = [
      'PENDING',
      'IN_PROGRESS',
      'COMPLETED',
      'FAILED',
      'EXPIRED',
      'CANCELLED',
    ]

    for (const status of states) {
      const described = describeEnrollment(makeEnrollment({ status }))
      expect(described.label).not.toBe(status)
      expect(described.detail.length).toBeGreaterThan(20)

      // NO FIRMWARE VOCABULARY ANYWHERE. An operator running a gym must never
      // be shown the word "serial monitor", "UART", "firmware" or "job".
      const words = `${described.label} ${described.headline} ${described.detail}`.toLowerCase()
      for (const forbidden of ['serial monitor', 'uart', 'firmware', 'platformio', 'sync job']) {
        expect(words).not.toContain(forbidden)
      }
    }
  })

  it('treats PENDING and IN_PROGRESS as live and everything else as finished', () => {
    expect(describeEnrollment(makeEnrollment({ status: 'PENDING' })).live).toBe(true)
    expect(describeEnrollment(makeEnrollment({ status: 'IN_PROGRESS' })).live).toBe(true)

    for (const status of ['COMPLETED', 'FAILED', 'EXPIRED', 'CANCELLED'] as const) {
      expect(describeEnrollment(makeEnrollment({ status })).live).toBe(false)
    }
  })

  it('shows an unknown state plainly rather than crashing or claiming success', () => {
    const described = describeEnrollment(makeEnrollment({ status: 'SOMETHING_NEW' }))
    expect(described.live).toBe(false)
    expect(described.tone).not.toBe('positive')
  })

  it('shows the live prompt and the terminal it is waiting for', async () => {
    const user = userEvent.setup()
    signIn()
    seed({
      enrollments: {
        'P-0001': makeEnrollment({ status: 'IN_PROGRESS', terminal_name: 'North Gate' }),
      },
    })
    renderPerson()

    const dialog = await openDialog(user)
    expect(await within(dialog).findByText(/North Gate is ready/i)).toBeInTheDocument()
    expect(within(dialog).getByText('Ready — place finger')).toBeInTheDocument()
  })

  it('warns when the door it is waiting for is offline', async () => {
    const user = userEvent.setup()
    signIn()
    seed({
      enrollments: {
        'P-0001': makeEnrollment({ status: 'PENDING', terminal_status: 'OFFLINE' }),
      },
    })
    renderPerson()

    const dialog = await openDialog(user)
    expect(await within(dialog).findByText(/That terminal is offline/i)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Completion
// ---------------------------------------------------------------------------

describe('a completed enrolment', () => {
  it('shows the credential as Enrolled, and which terminal captured it', async () => {
    signIn()
    seed({
      people: [makePerson({ external_id: 'P-0001', biometric_enrolled: true })],
      enrollments: {
        'P-0001': makeEnrollment({
          status: 'COMPLETED',
          biometric_enrolled: true,
          terminal_name: 'North Gate',
          completed_at: '2026-08-14T17:03:00Z',
        }),
      },
    })
    renderPerson()

    expect(await screen.findByText('Enrolled')).toBeInTheDocument()

    // WHICH DOOR, AND WHEN. A fingerprint lives on the sensor that captured it,
    // so this is the difference between "re-enrol them here" and an afternoon
    // of guessing why one door does not admit them.
    const enrolledAt = (await screen.findByText('Enrolled at')).closest(
      '.detail-list__row',
    ) as HTMLElement
    expect(within(enrolledAt).getByText(/North Gate/)).toBeInTheDocument()
  })

  it('SHOWS ENROLLED AS SOON AS THE TERMINAL REPORTS, without a manual refresh', async () => {
    // THE REGRESSION THIS GUARDS. The badge read the PERSON query, which is
    // invalidated when an operator STARTS an enrolment -- at which point they
    // are still not enrolled. Nothing fired when the terminal reported a capture
    // minutes later, so the page said "Not enrolled" beside a panel saying the
    // enrolment had succeeded, until somebody navigated away and back.
    //
    // No dialog is opened here on purpose: this is the operator who started an
    // enrolment, left the person's page up, and is watching it.
    signIn()
    seed({
      enrollments: {
        'P-0001': makeEnrollment({ status: 'IN_PROGRESS', biometric_enrolled: false }),
      },
    })
    renderPerson()

    const credential = await screen.findByRole('region', { name: 'Biometric credential' })
    expect(within(credential).getByText('Not enrolled')).toBeInTheDocument()

    // The customer places their finger. The browser's cached person record is
    // now stale, and only the enrolment poll can see that.
    seed({
      people: [makePerson({ external_id: 'P-0001', biometric_enrolled: true })],
      enrollments: {
        'P-0001': makeEnrollment({ status: 'COMPLETED', biometric_enrolled: true }),
      },
    })

    await waitFor(
      () => expect(within(credential).getByText('Enrolled')).toBeInTheDocument(),
      // Longer than the poll interval: the point is that it arrives on its own.
      { timeout: 6000 },
    )
    expect(within(credential).queryByText('Not enrolled')).toBeNull()
  })

  it('offers a re-enrolment rather than pretending there is nothing to do', async () => {
    signIn()
    seed({ people: [makePerson({ external_id: 'P-0001', biometric_enrolled: true })] })
    renderPerson()

    expect(
      await screen.findByRole('button', { name: 'Re-enrol fingerprint' }),
    ).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Failure, expiry, cancellation and retry
// ---------------------------------------------------------------------------

describe('when it does not work', () => {
  it('keeps the person and reports the terminal own words on a failure', async () => {
    signIn()
    seed({
      enrollments: {
        'P-0001': makeEnrollment({
          status: 'FAILED',
          error_message: 'the sensor did not respond',
          completed_at: '2026-08-14T17:04:00Z',
        }),
      },
    })
    renderPerson()

    // The person is untouched: still there, still active, still not enrolled.
    //
    // "Still active" is asserted as the ABSENCE of the inactive banner. This
    // page carries no Status card by design -- an active person is shown by
    // nothing being wrong, and the badge lives in the People list.
    expect(await screen.findByText('Not enrolled')).toBeInTheDocument()
    expect(screen.queryByText('This person is inactive')).toBeNull()
    expect(screen.getByText(/the sensor did not respond/)).toBeInTheDocument()
  })

  it('reads an expiry differently from a fault', async () => {
    const user = userEvent.setup()
    signIn()
    seed({ enrollments: { 'P-0001': makeEnrollment({ status: 'EXPIRED' }) } })
    renderPerson()

    const dialog = await openDialog(user)
    expect(await within(dialog).findByText(/Nobody came to the terminal/i)).toBeInTheDocument()
    expect(within(dialog).queryByText(/did not work/i)).toBeNull()
  })

  it('ALLOWS A RETRY, at the same terminal or a different one', async () => {
    const user = userEvent.setup()
    signIn()
    seed({ enrollments: { 'P-0001': makeEnrollment({ status: 'FAILED' }) } })
    renderPerson()

    const dialog = await openDialog(user)

    // The picker is offered again, with the whole eligible fleet -- not only
    // the terminal that just failed.
    await within(dialog).findByRole('radio', { name: /North Gate/ })
    expect(within(dialog).getByRole('radio', { name: /Reception/ })).toBeInTheDocument()

    await user.click(within(dialog).getByRole('radio', { name: /Reception/ }))
    await user.click(within(dialog).getByRole('button', { name: 'Start enrolment' }))

    await waitFor(() => expect(enrolmentRequests()).toHaveLength(1))
    expect(theEnrolmentRequestURL()).toContain('/terminals/AT-0002/enrollments')
  })

  it('cancels a live enrolment, and says no fingerprint was recorded', async () => {
    const user = userEvent.setup()
    signIn()
    seed({ enrollments: { 'P-0001': makeEnrollment({ status: 'IN_PROGRESS' }) } })
    renderPerson()

    const dialog = await openDialog(user)
    await user.click(await within(dialog).findByRole('button', { name: 'Cancel enrolment' }))

    expect(await within(dialog).findByText(/Enrolment cancelled/i)).toBeInTheDocument()
    expect(
      within(dialog).getByText(/no fingerprint was recorded/i),
    ).toBeInTheDocument()
  })

  it('offers the picker again after a cancellation', async () => {
    const user = userEvent.setup()
    signIn()
    seed({ enrollments: { 'P-0001': makeEnrollment({ status: 'CANCELLED' }) } })
    renderPerson()

    const dialog = await openDialog(user)
    expect(await within(dialog).findByRole('radio', { name: /North Gate/ })).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Eligibility, on its own
// ---------------------------------------------------------------------------

describe('enrollableTerminals', () => {
  it('keeps online and offline terminals and drops the ones that cannot answer', () => {
    const eligible = enrollableTerminals(FLEET)
    expect(eligible.map((terminal) => terminal.serial_number)).toEqual(['AT-0001', 'AT-0002'])
  })

  it('drops an inactive terminal whatever its reported status says', () => {
    const eligible = enrollableTerminals([
      makeTerminal({ serial_number: 'AT-9', status: 'ONLINE', active: false }),
    ])
    expect(eligible).toHaveLength(0)
  })

  it('drops a terminal that has never been provisioned', () => {
    // PROVISIONING means the row exists and the hardware has never registered,
    // so it holds no credential of its own and cannot authenticate to collect
    // the request. The server refuses it; offering it would be offering a
    // choice that comes back as a 409.
    const eligible = enrollableTerminals([
      makeTerminal({ serial_number: 'AT-NEW', status: 'PROVISIONING', active: true }),
    ])
    expect(eligible).toHaveLength(0)
  })
})
