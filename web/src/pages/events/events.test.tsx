import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../../api/csrf'
import type { FieldEvent, Role } from '../../api/types'
import { makeEvent, makeSession, makeSite, SITE_A } from '../../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../../test/render'
import { failNext, resetServerState, seed, state } from '../../test/server'
import { EventsPage } from './EventsPage'

/**
 * The door log (SEC-08).
 *
 * The only access-log route was authenticated with the site provisioning key —
 * a secret that lives on hardware bolted to a wall and that a browser must never
 * hold — so an operator could not see who had been let in, who had been refused,
 * or why. This is that history.
 *
 * WHAT IS ASSERTED HERE is mostly about not producing a confident wrong answer:
 * filters reach the server, a presentation that matched nobody is still shown,
 * two divergent timestamps are both surfaced, and a refusal explains itself
 * rather than printing a code.
 */

const EVENTS: FieldEvent[] = [
  makeEvent({
    id: 'e1',
    decision: 'DENIED',
    reason: 'NO_PERMISSION',
    person_name: 'Ada Okonkwo',
    subject_external_id: 'P-0001',
  }),
  makeEvent({
    id: 'e2',
    event_type: 'ACCESS_GRANTED',
    decision: 'GRANTED',
    reason: 'ALLOWED',
    person_name: 'Bem Tor',
    subject_external_id: 'P-0002',
    occurred_at: '2026-08-15T09:00:00Z',
    recorded_at: '2026-08-15T09:00:00Z',
  }),
  makeEvent({
    id: 'e3',
    decision: 'DENIED',
    reason: 'OUTSIDE_SCHEDULE',
    person_name: 'Ngozi Eze',
    subject_external_id: 'P-0003',
  }),
  // A presentation that matched NOBODY. The more interesting half of a security
  // trail, and the one a naive implementation filters out for having no person.
  makeEvent({
    id: 'e4',
    decision: 'DENIED',
    reason: 'PERSON_UNKNOWN',
    person_id: undefined,
    person_name: undefined,
    subject_external_id: 'UNKNOWN-CARD-99',
  }),
  // Buffered offline and uploaded hours later, with a clock the terminal could
  // not vouch for.
  makeEvent({
    id: 'e5',
    decision: 'GRANTED',
    reason: 'ALLOWED',
    person_name: 'Yusuf Bello',
    subject_external_id: 'P-0005',
    occurred_at: '2026-08-15T02:00:00Z',
    recorded_at: '2026-08-15T11:00:00Z',
    occurred_at_trusted: false,
  }),
]

function signIn(role: Role = 'VIEWER', events: FieldEvent[] = EVENTS) {
  const session = makeSession({
    role,
    operator: { id: 'operator-1', email: 'ops@example.com', full_name: 'Ops', role },
  })
  resetServerState(session)
  setCsrfToken(session.csrf_token)
  seed({ sites: [makeSite({ id: SITE_A.site_id, name: SITE_A.site_name })], events })
  return session
}

function renderEvents() {
  const router = createMemoryRouter(
    [
      { path: '/events', element: <EventsPage /> },
      { path: '/activity', element: <p>Activity</p> },
      { path: '/people/:externalId', element: <p>Person</p> },
      { path: '/terminals/:serial', element: <p>Terminal</p> },
    ],
    { initialEntries: ['/events'] },
  )
  return renderWithSession(<RouterProvider router={router} />, makeTestQueryClient())
}

beforeEach(() => setCsrfToken(null))

// ---------------------------------------------------------------------------
// The table
// ---------------------------------------------------------------------------

describe('the door log', () => {
  it('shows who, where, what and why', async () => {
    signIn()
    renderEvents()

    const row = (await screen.findByText('Ada Okonkwo')).closest('tr') as HTMLElement
    expect(within(row).getByText('Denied')).toBeInTheDocument()
    expect(within(row).getByText('North Gate')).toBeInTheDocument()
    expect(within(row).getByText('No rule covers this')).toBeInTheDocument()
  })

  it('SHOWS A PRESENTATION THAT MATCHED NOBODY, marked as unrecognised', async () => {
    // The most interesting half of a security trail. `person_id` is empty and
    // the identifier the terminal actually read is kept, so an unknown card at
    // 3am is visible rather than dropped for having no person to attach to.
    signIn()
    renderEvents()

    const row = (await screen.findByText('UNKNOWN-CARD-99')).closest('tr') as HTMLElement
    expect(within(row).getByText('Not recognised')).toBeInTheDocument()
  })

  it('SURFACES A DIVERGENT REPORT TIME rather than presenting one instant', async () => {
    // A terminal buffers while offline, so an event that arrived just now may
    // have happened hours ago. Showing only one of the two cannot explain that
    // to the person reading it.
    signIn()
    renderEvents()

    const row = (await screen.findByText('Yusuf Bello')).closest('tr') as HTMLElement
    expect(within(row).getByText(/reported/i)).toBeInTheDocument()
  })

  it('says when the terminal’s own clock could not be vouched for', async () => {
    // A terminal that has never reached NTP sends nothing and the server stamps
    // its arrival. Presenting that as an unqualified door time would be a quiet
    // lie.
    signIn()
    renderEvents()

    const row = (await screen.findByText('Yusuf Bello')).closest('tr') as HTMLElement
    expect(within(row).getByText('Terminal clock unverified')).toBeInTheDocument()
  })

  it('EXPLAINS the refusals on the page, grouped by reason', async () => {
    // Six people refused for "no rule covers this" is a configuration mistake;
    // six refused for "outside the schedule" is a rota mistake. The remedies
    // are different, and neither is guessable from the code.
    signIn()
    renderEvents()

    const summary = await screen.findByRole('region', {
      name: 'Why people were refused on this page',
    })
    expect(within(summary).getByText(/Grant them access at this terminal/i)).toBeInTheDocument()
    expect(within(summary).getByText(/Check the schedule’s windows/i)).toBeInTheDocument()
  })

  it('does not show the denial summary when nothing was refused', async () => {
    signIn('VIEWER', [EVENTS[1] as FieldEvent])
    renderEvents()

    await screen.findByText('Bem Tor')
    expect(
      screen.queryByRole('region', { name: 'Why people were refused on this page' }),
    ).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Filtering
// ---------------------------------------------------------------------------

describe('filtering', () => {
  it('FILTERS ON THE SERVER, not by narrowing the page', async () => {
    const user = userEvent.setup()
    signIn()
    renderEvents()

    await screen.findByText('Ada Okonkwo')
    await user.selectOptions(screen.getByLabelText('Outcome'), 'GRANTED')

    await waitFor(() => expect(screen.queryByText('Ada Okonkwo')).not.toBeInTheDocument())
    // The proof: the filter reached the API. A client-side filter would show the
    // same screen having asked for everything.
    expect(
      state.requests.some((entry) => entry.url.includes('decision=GRANTED')),
    ).toBe(true)
  })

  it('INCLUDES THE WHOLE OF THE "TO" DAY', async () => {
    // A bare date means midnight at the START of the day, silently excluding
    // everything that happened on the day being asked about — and the answer
    // looks like "nothing happened".
    const user = userEvent.setup()
    signIn()
    renderEvents()

    await screen.findByText('Ada Okonkwo')
    await user.type(screen.getByLabelText('To'), '2026-08-15')

    await waitFor(() => {
      const asked = state.requests.filter((entry) => entry.url.includes('to='))
      expect(asked.length).toBeGreaterThan(0)
      const to = new URL(asked[asked.length - 1]?.url ?? '').searchParams.get('to') ?? ''
      // The 11:00 event must still be inside the window.
      expect(to > '2026-08-15T11:00:00Z').toBe(true)
    })
  })

  it('returns to the first page when a filter changes', async () => {
    const user = userEvent.setup()
    const many = Array.from({ length: 120 }, (_, index) =>
      makeEvent({
        id: `bulk-${index}`,
        subject_external_id: `P-${index}`,
        person_name: `Person ${index}`,
        decision: index % 2 === 0 ? 'DENIED' : 'GRANTED',
      }),
    )
    signIn('VIEWER', many)
    renderEvents()

    await screen.findByText('Person 0')
    await user.click(await screen.findByRole('button', { name: 'Next' }))
    await screen.findByText(/Page 2 of/)

    await user.selectOptions(screen.getByLabelText('Outcome'), 'GRANTED')
    await waitFor(() => expect(screen.queryByText(/Page 2 of/)).not.toBeInTheDocument())
  })

  it('distinguishes "nothing matched" from "nothing recorded"', async () => {
    const user = userEvent.setup()
    signIn()
    renderEvents()

    await screen.findByText('Ada Okonkwo')
    await user.selectOptions(screen.getByLabelText('Kind of event'), 'TAMPER')
    expect(await screen.findByText('Nothing matched')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Clear filters' }))
    await waitFor(() => expect(screen.getByText('Ada Okonkwo')).toBeInTheDocument())
  })

  it('says nothing is recorded rather than nothing matched on an empty trail', async () => {
    signIn('VIEWER', [])
    renderEvents()

    expect(await screen.findByText('Nothing recorded yet')).toBeInTheDocument()
    // And explains why a terminal's events may arrive late.
    expect(screen.getByText(/uploads what it buffered when it reconnects/i)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// What this page is not
// ---------------------------------------------------------------------------

describe('the two trails are kept apart', () => {
  it('sends somebody looking for operator changes to Activity', async () => {
    signIn()
    renderEvents()

    expect(
      await screen.findByText('This is the door log, not the operator trail'),
    ).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Activity' })).toHaveAttribute('href', '/activity')
  })

  it('is readable by a VIEWER, unlike the audit trail', async () => {
    // "Why was she refused" is a question somebody at a front desk has to be
    // able to answer without an administrator.
    signIn('VIEWER')
    renderEvents()

    expect(await screen.findByText('Ada Okonkwo')).toBeInTheDocument()
  })

  it('reports a failed load as an error rather than as an empty trail', async () => {
    signIn()
    failNext('events', 500)
    renderEvents()

    expect(await screen.findByRole('alert')).toHaveTextContent(/Failed to retrieve events/)
  })
})
