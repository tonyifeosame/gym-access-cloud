import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../../api/csrf'
import type { Role, Session, Terminal } from '../../api/types'
import { expectNoViolations } from '../../test/axe'
import { makeEnrollment, makeSession, makeTerminal } from '../../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../../test/render'
import { failNext, resetServerState, seed, state } from '../../test/server'
import { phaseOf } from './enrollment'
import { PeopleListPage } from './PeopleListPage'
import { PersonDetailPage } from './PersonDetailPage'

/**
 * Adding a person continues into enrolling them.
 *
 * THE PROPERTY UNDER TEST: the record is created FIRST, on its own request,
 * and the enrolment is a second, separately addressed request that can fail,
 * expire, be cancelled or be skipped without the person being any different
 * for it. The states an operator sees are asserted through the phase the
 * panel declares, so the wording can improve without these breaking, and the
 * wording itself is covered where it lives (enrollment.test.tsx).
 *
 * The multi-step tests wait on the real 2-second poll twice or more, so they
 * carry their own timeout: the point is that the state arrives on its own.
 */

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

const FLEET: Terminal[] = [ONLINE_TERMINAL, OFFLINE_TERMINAL]

function signIn(role: Role = 'MANAGER', overrides: Partial<Session> = {}) {
  const session = makeSession({
    role,
    operator: { id: 'operator-1', email: 'ops@example.com', full_name: 'Ops', role },
    ...overrides,
  })
  resetServerState(session)
  setCsrfToken(session.csrf_token)
  seed({ people: [], terminals: FLEET })
  return session
}

function renderPeople(client = makeTestQueryClient()) {
  const router = createMemoryRouter(
    [
      { path: '/people', element: <PeopleListPage /> },
      { path: '/people/:externalId', element: <PersonDetailPage /> },
    ],
    { initialEntries: ['/people'] },
  )
  return renderWithSession(<RouterProvider router={router} />, client)
}

/** Fills in and submits Add a person, and returns the dialog once it has
 * become the enrolment step. */
async function addPerson(user: ReturnType<typeof userEvent.setup>) {
  // An empty roster offers the action twice, in the header and in the empty
  // state; either is the same dialog.
  const [add] = await screen.findAllByRole('button', { name: 'Add a person' })
  await user.click(add as HTMLElement)
  await user.type(screen.getByLabelText(/ID number/), 'P-NEW')
  await user.type(screen.getByLabelText(/Full name/), 'Chidi Okafor')
  await user.click(screen.getByRole('button', { name: 'Add person' }))

  const dialog = await screen.findByRole('dialog', { name: /Enrol a fingerprint for Chidi Okafor/ })
  return dialog
}

function enrolmentRequests() {
  return state.requests.filter(
    (request) => request.method === 'POST' && request.url.includes('/enrollments'),
  )
}

function createRequests() {
  return state.requests.filter(
    (request) => request.method === 'POST' && request.url.endsWith('/api/v1/console/people'),
  )
}

/** The phase the outcome panel declares, or null when no panel is shown. */
function shownPhase(dialog: HTMLElement): string | null {
  return dialog.querySelector('[data-enrolment-phase]')?.getAttribute('data-enrolment-phase') ?? null
}

/** The terminal reports. The mock's stored enrolment and person are updated
 * the way the server's would be; the poll picks them up. */
function terminalReports(
  status: 'IN_PROGRESS' | 'COMPLETED' | 'FAILED' | 'EXPIRED',
  extras: { error_message?: string } = {},
) {
  const current = state.enrollments['P-NEW']
  if (!current) throw new Error('no enrolment to report on')
  const enrolled = status === 'COMPLETED'
  seed({
    enrollments: {
      'P-NEW': makeEnrollment({
        ...current,
        status,
        biometric_enrolled: enrolled,
        completed_at: status === 'IN_PROGRESS' ? undefined : '2026-08-14T17:04:00Z',
        started_at: '2026-08-14T17:01:00Z',
        ...extras,
      }),
    },
    people: state.people.map((person) =>
      person.external_id === 'P-NEW' ? { ...person, biometric_enrolled: enrolled } : person,
    ),
  })
}

beforeEach(() => setCsrfToken(null))

describe('adding a person leads into enrolling them', () => {
  it('creates the person first, then offers the terminal picker for them', async () => {
    const user = userEvent.setup()
    signIn()
    renderPeople()

    const dialog = await addPerson(user)

    // The person exists before any enrolment is mentioned, from ONE create
    // request; nothing about enrolment rode along with it.
    expect(createRequests()).toHaveLength(1)
    expect(state.people.map((p) => p.external_id)).toContain('P-NEW')
    expect(enrolmentRequests()).toHaveLength(0)

    // The picker, with the eligible fleet, and nothing preselected.
    expect(await within(dialog).findByRole('radio', { name: /North Gate/ })).not.toBeChecked()
    expect(within(dialog).getByRole('radio', { name: /Reception/ })).not.toBeChecked()
    expect(within(dialog).getByRole('button', { name: 'Skip for now' })).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Start enrolment' })).toBeInTheDocument()
    expect(shownPhase(dialog)).toBeNull()
  })

  it('can be skipped, leaving the person added and not enrolled', async () => {
    const user = userEvent.setup()
    signIn()
    renderPeople()

    const dialog = await addPerson(user)
    await within(dialog).findByRole('radio', { name: /North Gate/ })
    await user.click(within(dialog).getByRole('button', { name: 'Skip for now' }))

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(enrolmentRequests()).toHaveLength(0)
    expect(state.people.find((p) => p.external_id === 'P-NEW')?.biometric_enrolled).toBe(false)
    // And the list shows them, unenrolled.
    expect(await screen.findByText('P-NEW')).toBeInTheDocument()
  })

  it('runs an enrolment to success: queued, then scanning, then enrolled', async () => {
    const user = userEvent.setup()
    signIn()
    renderPeople()

    const dialog = await addPerson(user)
    await user.click(await within(dialog).findByRole('radio', { name: /North Gate/ }))
    await user.click(within(dialog).getByRole('button', { name: 'Start enrolment' }))

    // Addressed to the chosen terminal, and to nobody else.
    await waitFor(() => expect(enrolmentRequests()).toHaveLength(1))
    expect(enrolmentRequests()[0]?.url).toContain('/terminals/AT-0001/enrollments')

    // Queued: the terminal has not picked it up yet.
    await waitFor(() => expect(shownPhase(dialog)).toBe('queued'))
    expect(within(dialog).getByRole('button', { name: 'Cancel enrolment' })).toBeInTheDocument()
    expect(within(dialog).queryByRole('radio')).toBeNull()

    // The terminal fetches the job and shows the prompt.
    terminalReports('IN_PROGRESS')
    await waitFor(() => expect(shownPhase(dialog)).toBe('scanning'), { timeout: 6000 })
    expect(within(dialog).getByText(/place the same finger on the reader/i)).toBeInTheDocument()

    // The finger is captured.
    terminalReports('COMPLETED')
    await waitFor(() => expect(shownPhase(dialog)).toBe('succeeded'), { timeout: 6000 })
    expect(within(dialog).getByRole('button', { name: 'Done' })).toBeInTheDocument()
    expect(within(dialog).queryByRole('button', { name: 'Cancel enrolment' })).toBeNull()

    await user.click(within(dialog).getByRole('button', { name: 'Done' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  }, 20_000)

  it('reports a failure in the terminal words, keeps the person, and offers a retry', async () => {
    const user = userEvent.setup()
    signIn()
    renderPeople()

    const dialog = await addPerson(user)
    await user.click(await within(dialog).findByRole('radio', { name: /North Gate/ }))
    await user.click(within(dialog).getByRole('button', { name: 'Start enrolment' }))
    await waitFor(() => expect(shownPhase(dialog)).toBe('queued'))

    terminalReports('FAILED', { error_message: 'the fingerprint reader could not complete the capture' })
    await waitFor(() => expect(shownPhase(dialog)).toBe('failed'), { timeout: 6000 })
    expect(
      within(dialog).getByText(/the fingerprint reader could not complete the capture/),
    ).toBeInTheDocument()

    // The person is exactly as they were: present, and not enrolled.
    expect(state.people.find((p) => p.external_id === 'P-NEW')?.biometric_enrolled).toBe(false)

    // Retry: the picker is back with the whole fleet, and a different door
    // can be chosen. Nothing is preselected -- not even the one that failed.
    const retryPicker = await within(dialog).findByRole('radio', { name: /Reception/ })
    expect(retryPicker).not.toBeChecked()
    expect(within(dialog).getByRole('radio', { name: /North Gate/ })).not.toBeChecked()
    await user.click(retryPicker)
    await user.click(within(dialog).getByRole('button', { name: 'Try again' }))

    await waitFor(() => expect(enrolmentRequests()).toHaveLength(2))
    expect(enrolmentRequests()[1]?.url).toContain('/terminals/AT-0002/enrollments')
    await waitFor(() => expect(shownPhase(dialog)).toBe('queued'))
  }, 20_000)

  it('reports a finger that is already enrolled as a failure to retry, not a success', async () => {
    const user = userEvent.setup()
    signIn()
    renderPeople()

    const dialog = await addPerson(user)
    await user.click(await within(dialog).findByRole('radio', { name: /North Gate/ }))
    await user.click(within(dialog).getByRole('button', { name: 'Start enrolment' }))
    await waitFor(() => expect(shownPhase(dialog)).toBe('queued'))

    // The terminal refuses a finger that already resolves to somebody else.
    // It arrives as a FAILED enrolment carrying the terminal's own words; the
    // console reports those and does not call anybody enrolled.
    terminalReports('FAILED', { error_message: 'that finger is already enrolled at slot 7' })
    await waitFor(() => expect(shownPhase(dialog)).toBe('failed'), { timeout: 6000 })
    expect(within(dialog).getByText(/already enrolled at slot 7/)).toBeInTheDocument()
    expect(within(dialog).queryByText(/Fingerprint enrolled/)).toBeNull()
    expect(state.people.find((p) => p.external_id === 'P-NEW')?.biometric_enrolled).toBe(false)
    expect(within(dialog).getByRole('button', { name: 'Try again' })).toBeInTheDocument()
  }, 20_000)

  it('cancels a live enrolment and says no fingerprint was recorded', async () => {
    const user = userEvent.setup()
    signIn()
    renderPeople()

    const dialog = await addPerson(user)
    await user.click(await within(dialog).findByRole('radio', { name: /North Gate/ }))
    await user.click(within(dialog).getByRole('button', { name: 'Start enrolment' }))
    await waitFor(() => expect(shownPhase(dialog)).toBe('queued'))

    await user.click(within(dialog).getByRole('button', { name: 'Cancel enrolment' }))

    await waitFor(() => expect(shownPhase(dialog)).toBe('cancelled'))
    expect(within(dialog).getByText(/no fingerprint was recorded/i)).toBeInTheDocument()
    expect(state.people.find((p) => p.external_id === 'P-NEW')?.biometric_enrolled).toBe(false)
    // The picker is offered again, and so is leaving.
    expect(await within(dialog).findByRole('radio', { name: /North Gate/ })).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Skip for now' })).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Try again' })).toBeInTheDocument()
  })

  it('says so when no terminal is available, and keeps the person', async () => {
    const user = userEvent.setup()
    signIn()
    // A fleet with nothing that can enrol: one disabled, one never provisioned.
    seed({
      terminals: [
        makeTerminal({ serial_number: 'AT-0003', device_name: 'Off', status: 'DISABLED', active: false }),
        makeTerminal({ id: 4, public_id: 'tp-4', serial_number: 'AT-0004', status: 'PROVISIONING' }),
      ],
    })
    renderPeople()

    const dialog = await addPerson(user)

    expect(await within(dialog).findByText(/No terminal can run an enrolment/i)).toBeInTheDocument()
    expect(within(dialog).getByText(/The person has been kept/i)).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Start enrolment' })).toBeDisabled()
    expect(within(dialog).queryByRole('radio')).toBeNull()
    expect(state.people.map((p) => p.external_id)).toContain('P-NEW')
    expect(enrolmentRequests()).toHaveLength(0)

    await user.click(within(dialog).getByRole('button', { name: 'Skip for now' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('reports a terminal the server refuses without a half-started state', async () => {
    const user = userEvent.setup()
    signIn()
    renderPeople()

    const dialog = await addPerson(user)
    await user.click(await within(dialog).findByRole('radio', { name: /North Gate/ }))
    // The terminal was disabled between the list loading and the click.
    failNext('start-enrolment', 409)
    await user.click(within(dialog).getByRole('button', { name: 'Start enrolment' }))

    expect(await within(dialog).findByRole('alert')).toBeInTheDocument()
    expect(shownPhase(dialog)).toBeNull()
    expect(within(dialog).getByRole('button', { name: 'Start enrolment' })).toBeInTheDocument()
    expect(state.enrollments['P-NEW']).toBeUndefined()
  })

  it('offers an offline terminal, marked, and queues against it', async () => {
    const user = userEvent.setup()
    signIn()
    renderPeople()

    const dialog = await addPerson(user)
    const offline = await within(dialog).findByRole('radio', { name: /Reception/ })
    expect(within(dialog).getByText(/will wait until it reconnects/i)).toBeInTheDocument()
    await user.click(offline)
    await user.click(within(dialog).getByRole('button', { name: 'Start enrolment' }))

    await waitFor(() => expect(shownPhase(dialog)).toBe('queued'))
    expect(within(dialog).getByText(/That terminal is offline/i)).toBeInTheDocument()
  })

  it('leaves a running enrolment running when the dialog is closed, and shows it on the person page', async () => {
    const user = userEvent.setup()
    signIn()
    const client = makeTestQueryClient({ gcTime: 60_000 })
    renderPeople(client)

    const dialog = await addPerson(user)
    await user.click(await within(dialog).findByRole('radio', { name: /North Gate/ }))
    await user.click(within(dialog).getByRole('button', { name: 'Start enrolment' }))
    await waitFor(() => expect(shownPhase(dialog)).toBe('queued'))

    await user.click(within(dialog).getByRole('button', { name: 'Leave it running' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())

    // Still live on the server; nothing was cancelled by closing.
    expect(state.enrollments['P-NEW']?.status).toBe('PENDING')

    // The person's own page picks it up with the same workflow.
    await user.click(await screen.findByRole('link', { name: 'Chidi Okafor' }))
    expect(await screen.findByText(/Waiting for North Gate/i)).toBeInTheDocument()
  })

  it('has no accessibility violations at the enrolment step', async () => {
    const user = userEvent.setup()
    signIn()
    const { container } = renderPeople()

    const dialog = await addPerson(user)
    await within(dialog).findByRole('radio', { name: /North Gate/ })
    await expectNoViolations(container)
  })
})

describe('phaseOf', () => {
  it('maps every server status to one of the six states', () => {
    expect(phaseOf(null)).toBe('choose')
    expect(phaseOf(makeEnrollment({ status: 'PENDING' }))).toBe('queued')
    expect(phaseOf(makeEnrollment({ status: 'IN_PROGRESS' }))).toBe('scanning')
    expect(phaseOf(makeEnrollment({ status: 'COMPLETED' }))).toBe('succeeded')
    expect(phaseOf(makeEnrollment({ status: 'FAILED' }))).toBe('failed')
    expect(phaseOf(makeEnrollment({ status: 'EXPIRED' }))).toBe('failed')
    expect(phaseOf(makeEnrollment({ status: 'CANCELLED' }))).toBe('cancelled')
  })

  it('treats a state it does not know as one to retry from, never as success', () => {
    expect(phaseOf({ ...makeEnrollment(), status: 'SOMETHING_NEW' as never })).toBe('failed')
  })
})
