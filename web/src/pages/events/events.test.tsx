import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../../api/csrf'
import type { FieldEvent, Role } from '../../api/types'
import { makeEvent, makeSession, makeSite, SITE_A } from '../../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../../test/render'
import { expectNoDoorWording } from '../../test/vocabulary'
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
  /*
    THE PLATFORM'S OWN ERROR, and the shape that broke the "Why" column.

    `ROSTER_CAPACITY_EXCEEDED` is written by the platform rather than by a door:
    it carries `decision: ERROR`, names no person, and sets NO reason at all. The
    column reads `reason`, so this rendered an em dash -- the most serious thing
    the platform emits arriving with less explanation than a routine refusal.
  */
  makeEvent({
    id: 'e6',
    event_type: 'ROSTER_CAPACITY_EXCEEDED',
    decision: 'ERROR',
    reason: undefined,
    person_id: undefined,
    person_name: undefined,
    subject_external_id: undefined,
    occurred_at: '2026-08-15T07:00:00Z',
    recorded_at: '2026-08-15T07:00:00Z',
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

describe('the event log', () => {
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
    expect(within(row).getByText('Time not confirmed')).toBeInTheDocument()
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
// Explaining an outcome
// ---------------------------------------------------------------------------

describe('why an event went the way it did', () => {
  it('SHOWS THE MEANING ON THE PAGE, not in a tooltip', async () => {
    /*
      The meaning used to live in a `title` attribute, which is unreachable three
      ways at once: no hover on a touch screen, no keyboard route to it, and
      inconsistent screen-reader treatment. On a phone -- where somebody is most
      likely to be standing next to the person who was just refused -- the
      explanation did not exist.
    */
    signIn()
    renderEvents()

    const row = (await screen.findByText('Ngozi Eze')).closest('tr') as HTMLElement
    expect(within(row).getByText('Outside the schedule')).toBeInTheDocument()
    expect(
      within(row).getByText(/schedule does not include this moment/i),
    ).toBeInTheDocument()
  })

  it('no longer hides the explanation in a title attribute', async () => {
    signIn()
    renderEvents()

    const row = (await screen.findByText('Ngozi Eze')).closest('tr') as HTMLElement
    const why = within(row).getByText('Outside the schedule').closest('span')
    expect(why?.closest('[title]')).toBeNull()
  })

  it('EXPLAINS AN EVENT THAT CARRIES NO REASON, which is how the platform reports its own errors', async () => {
    // `ROSTER_CAPACITY_EXCEEDED` sets no reason, so a column reading `reason`
    // alone had nothing to say about it.
    signIn()
    renderEvents()

    // Scoped to the table: "Error" is also an option in the Outcome filter.
    const table = await screen.findByRole('table')
    const row = within(table).getByText('Too many people for this terminal').closest('tr') as HTMLElement

    expect(within(row).getByText('Error')).toBeInTheDocument()
    expect(within(row).getByText(/outnumber the records it can hold/i)).toBeInTheDocument()
  })

  it('CARRIES THE REMEDY ON THE ROW, because no summary covers an error', async () => {
    // The denial summary groups REFUSALS, and this refused nobody — so it is not
    // in there, and the row is the only place its remedy can appear.
    signIn()
    renderEvents()

    const table = await screen.findByRole('table')
    const row = within(table)
      .getByText('Too many people for this terminal')
      .closest('tr') as HTMLElement

    expect(within(row).getByText(/What to do:/)).toBeInTheDocument()
    expect(within(row).getByText(/Narrow who is permitted at this terminal/i)).toBeInTheDocument()
  })

  it('does NOT repeat a denial remedy on every row, which the summary already states', async () => {
    // Forty rows of the same advice buries the rows.
    signIn()
    renderEvents()

    const table = await screen.findByRole('table')
    const row = within(table).getByText('Ngozi Eze').closest('tr') as HTMLElement
    expect(within(row).queryByText(/What to do:/)).not.toBeInTheDocument()
  })

  it('says plainly that the roster error refused nobody', async () => {
    // The distinction that stops it being read as a mass denial: nothing was
    // decided about anybody, the terminal simply stopped being updated.
    signIn()
    renderEvents()

    const table = await screen.findByRole('table')
    expect(
      within(table).getByText(/Nobody was refused by this event/i),
    ).toBeInTheDocument()
  })

  it('offers the roster error in the event-type filter it belongs to', async () => {
    // The Outcome filter has always offered "Error" while the type list omitted
    // the only thing that produces one.
    signIn()
    renderEvents()

    const types = await screen.findByLabelText(/Kind of event/)
    expect(
      within(types).getByRole('option', { name: 'Too many people for a terminal' }),
    ).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Reaching a person or a terminal from a row
// ---------------------------------------------------------------------------

describe('the links out of a row', () => {
  it('CARRY THE SHARED TABLE-LINK TREATMENT, which is where the touch-target floor lives', async () => {
    /*
      These were bare `<Link>`s, so they measured 243x20 and 260x20 at 390px --
      under the 24px WCAG 2.2 (2.5.8, AA) minimum, on roughly forty rows a page,
      and on the two columns somebody on a phone is most likely to tap.
      `table__link` is the class that carries that floor for every other table.
    */
    signIn()
    renderEvents()

    const person = await screen.findByRole('link', { name: 'Ada Okonkwo' })
    expect(person).toHaveClass('table__link')

    const row = person.closest('tr') as HTMLElement
    expect(within(row).getByRole('link', { name: 'North Gate' })).toHaveClass('table__link')
  })

  it('does not uppercase the customer\'s own site name', async () => {
    // `audit__role` uppercases and letterspaces, which suits an enum value. A
    // site name is a proper noun and was being rendered as though it were a code.
    signIn()
    renderEvents()

    const table = await screen.findByRole('table')
    const sites = within(table).getAllByText('Lagos Depot')
    expect(sites.length).toBeGreaterThan(0)
    for (const site of sites) {
      expect(site).not.toHaveClass('audit__role')
      expect(site).toHaveClass('event__site')
    }
  })
})

// ---------------------------------------------------------------------------
// Filtering
// ---------------------------------------------------------------------------

describe('filtering', () => {
  it('searches by the same field name the People screen heads its column with', async () => {
    /*
      ONE NAME FOR ONE FIELD, ACROSS SCREENS. This box said "Name or identifier"
      while People headed the same value "Member ID" and its form labelled it
      "Identifier" -- three spellings of one thing, and somebody who found a
      person on one screen had no way to know they were searching the same
      column on another. All four now read from `personVocabulary`.
    */
    signIn()
    renderEvents()

    await screen.findByText('Ada Okonkwo')
    expect(screen.getByPlaceholderText('Name or ID number')).toBeInTheDocument()
    expect(document.body.textContent ?? '').not.toMatch(/member id/i)
  })

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
      await screen.findByText('This is the event log, not the operator trail'),
    ).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Activity' })).toHaveAttribute('href', '/activity')
  })

  /*
    THIS PAGE NAMES ITSELF, and it used to name itself after a door. The note
    below the heading called this "the door log" while the page it links to
    called it the same thing from the other side, so a customer with turnstiles,
    barriers or lockers met the word twice in two clicks.
  */
  it('names itself the event log rather than the door log', async () => {
    signIn()
    renderEvents()

    await screen.findByText('This is the event log, not the operator trail')
    await screen.findByText('Ada Okonkwo')
    expectNoDoorWording('Events', document.body.textContent ?? '')
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
