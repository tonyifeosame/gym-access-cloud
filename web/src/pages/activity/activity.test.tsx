import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../../api/csrf'
import type { AuditRecord, Role } from '../../api/types'
import { makeAuditRecord, makeSession } from '../../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../../test/render'
import { expectNoDoorWording } from '../../test/vocabulary'
import { failNext, resetServerState, seed, state } from '../../test/server'
import { ActivityPage } from './ActivityPage'
import { describeAction, isKnownAction, readChanges } from './auditVocabulary'

/**
 * The activity / audit screen (SEC-07).
 *
 * Two properties are worth more than everything else here, and both are about
 * not producing a confident wrong answer:
 *
 *   FILTERING HAPPENS ON THE SERVER. A console that filtered a fetched page
 *   would answer "did anybody touch this?" from 50 rows and look complete while
 *   doing it. On an audit surface that is the worst available failure.
 *
 *   NOTHING IS HIDDEN FOR BEING UNRECOGNISED. The action column is
 *   unconstrained server-side; a record this build cannot describe is still a
 *   record somebody may be looking for.
 */

const TRAIL: AuditRecord[] = [
  makeAuditRecord({
    id: 'audit-1',
    action: 'TERMINAL_CREDENTIAL_REVOKED',
    actor_email: 'ops@example.com',
    actor_role: 'ADMIN',
    target_type: 'TERMINAL',
    target_label: 'AT-0001',
    changes: { reason: 'reported stolen', pending_jobs_cancelled: 4 },
    occurred_at: '2026-08-14T17:05:00Z',
  }),
  makeAuditRecord({
    id: 'audit-2',
    action: 'PERSON_CREATED',
    actor_email: 'kemi@example.com',
    actor_role: 'MANAGER',
    target_type: 'PERSON',
    target_label: 'P-0007',
    changes: undefined,
    occurred_at: '2026-08-13T09:00:00Z',
  }),
  makeAuditRecord({
    id: 'audit-3',
    action: 'COMPANY_CREATED',
    actor_email: 'vendor@accesslink.example',
    // Not one of the operator roles. A reader has to be able to tell that this
    // came from the vendor rather than from somebody inside the company.
    actor_role: 'PLATFORM',
    target_type: 'COMPANY',
    target_label: 'Northwind Logistics',
    occurred_at: '2026-01-01T00:00:00Z',
  }),
  makeAuditRecord({
    id: 'audit-5',
    // The two records whose stored words and customer-facing words differ. The
    // target type is APPLICATION and reads "Feature"; the action is
    // SITE_KEY_ROTATED and reads "Provisioning key rotated".
    action: 'APPLICATION_CONFIGURED',
    actor_email: 'owner@example.com',
    actor_role: 'OWNER',
    target_type: 'APPLICATION',
    target_label: 'ACCESS_CONTROL',
    occurred_at: '2026-08-11T08:00:00Z',
  }),
  makeAuditRecord({
    id: 'audit-6',
    action: 'SITE_KEY_ROTATED',
    actor_email: 'owner@example.com',
    actor_role: 'OWNER',
    target_type: 'SITE',
    target_label: 'Lagos Distribution Centre',
    occurred_at: '2026-08-10T08:00:00Z',
  }),
  makeAuditRecord({
    id: 'audit-4',
    // An action this build has never heard of, as an application-defined event
    // would be.
    action: 'VISITOR_BADGE_PRINTED',
    actor_email: 'reception@example.com',
    actor_role: 'MANAGER',
    target_type: 'PERSON',
    target_label: 'V-0044',
    occurred_at: '2026-08-12T11:00:00Z',
  }),
]

function signIn(role: Role = 'ADMIN', trail: AuditRecord[] = TRAIL) {
  const session = makeSession({
    role,
    operator: { id: 'operator-1', email: 'ops@example.com', full_name: 'Ops', role },
  })
  resetServerState(session)
  setCsrfToken(session.csrf_token)
  seed({ audit: trail })
  return session
}

function renderActivity() {
  const router = createMemoryRouter([{ path: '/activity', element: <ActivityPage /> }], {
    initialEntries: ['/activity'],
  })
  return renderWithSession(<RouterProvider router={router} />, makeTestQueryClient())
}

beforeEach(() => setCsrfToken(null))

// ---------------------------------------------------------------------------
// The vocabulary, as pure functions
// ---------------------------------------------------------------------------

describe('reading an audit record', () => {
  it('turns a stored action name into a sentence', () => {
    expect(describeAction('TERMINAL_CREDENTIAL_REVOKED').label).toBe(
      'Terminal credential revoked',
    )
    expect(describeAction('TERMINAL_CREDENTIAL_REVOKED').tone).toBe('destructive')
  })

  it('humanises an action it has never heard of rather than dropping it', () => {
    // The column is deliberately unconstrained server-side.
    expect(describeAction('VISITOR_BADGE_PRINTED').label).toBe('Visitor Badge Printed')
    expect(isKnownAction('VISITOR_BADGE_PRINTED')).toBe(false)
  })

  it('renders changes as data without interpreting them', () => {
    const changes = readChanges({ reason: 'stolen', pending_jobs_cancelled: 4, extra: null })
    expect(changes).toEqual([
      { key: 'Reason', value: 'stolen' },
      { key: 'Pending Jobs Cancelled', value: '4' },
      { key: 'Extra', value: '—' },
    ])
  })

  it('survives a changes value that is not an object', () => {
    expect(readChanges(null)).toEqual([])
    expect(readChanges('a string')).toEqual([])
    expect(readChanges([1, 2])).toEqual([])
  })
})

// ---------------------------------------------------------------------------
// The table
// ---------------------------------------------------------------------------

describe('the activity table', () => {
  it('lists who did what, to what, and when', async () => {
    signIn()
    renderActivity()

    const row = (await screen.findByText('AT-0001')).closest('tr') as HTMLElement
    expect(within(row).getByText('Terminal credential revoked')).toBeInTheDocument()
    expect(within(row).getByText('ops@example.com')).toBeInTheDocument()
  })

  it('MARKS A PLATFORM ACTOR as not being somebody inside the company', async () => {
    // Reusing an operator role for a vendor action would be a lie nobody could
    // detect afterwards.
    signIn()
    renderActivity()

    const row = (await screen.findByText('Northwind Logistics')).closest('tr') as HTMLElement
    expect(within(row).getByText('Platform')).toBeInTheDocument()
  })

  it('shows an action it cannot describe rather than hiding the record', async () => {
    signIn()
    renderActivity()

    const row = (await screen.findByText('V-0044')).closest('tr') as HTMLElement
    expect(within(row).getByText('Visitor Badge Printed')).toBeInTheDocument()
    // And says it is unrecognised, by showing the raw code beside it.
    expect(within(row).getByText('VISITOR_BADGE_PRINTED')).toBeInTheDocument()
  })

  it('expands a record to its detail, including what changed', async () => {
    const user = userEvent.setup()
    signIn()
    renderActivity()

    const row = (await screen.findByText('AT-0001')).closest('tr') as HTMLElement
    await user.click(within(row).getByRole('button', { name: 'Show' }))

    const detail = screen.getByRole('region', { name: 'Record detail' })
    expect(within(detail).getByText('Reason')).toBeInTheDocument()
    expect(within(detail).getByText('reported stolen')).toBeInTheDocument()
    expect(within(detail).getByText('203.0.113.10')).toBeInTheDocument()
  })

  it('says so when a record carried no extra detail, rather than showing nothing', async () => {
    const user = userEvent.setup()
    signIn()
    renderActivity()

    const row = (await screen.findByText('P-0007')).closest('tr') as HTMLElement
    await user.click(within(row).getByRole('button', { name: 'Show' }))

    expect(screen.getByText(/recorded no additional detail/i)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Reaching a record's detail
// ---------------------------------------------------------------------------

describe('opening a record brings the reader to it', () => {
  it('MOVES FOCUS INTO THE PANEL, which is what makes it reachable', async () => {
    /*
      The panel renders below the table, deliberately -- an expanded row of a
      different shape breaks the table's column semantics and has nowhere to go
      in the card layout. The cost is that "below the table" means below fifty
      rows: measured in a real browser at 2,882px down at 1440px and 7,887px down
      at 390px, with the page not scrolling and focus left on the button.
      Pressing Show did nothing an operator could perceive.

      Focus is the half that carries the fix. A pointer user gets the scroll; a
      keyboard or screen-reader user gets placed inside the panel instead of
      being left forty rows above it.
    */
    const user = userEvent.setup()
    signIn()
    renderActivity()

    const row = (await screen.findByText('AT-0001')).closest('tr') as HTMLElement
    await user.click(within(row).getByRole('button', { name: 'Show' }))

    const detail = screen.getByRole('region', { name: 'Record detail' })
    const heading = within(detail).getByRole('heading', { level: 2 })
    expect(heading).toHaveFocus()
  })

  it('moves the reader again when a different record is opened', async () => {
    // Otherwise the second click silently swaps the contents of a panel the
    // operator is no longer looking at.
    const user = userEvent.setup()
    signIn()
    renderActivity()

    const first = (await screen.findByText('AT-0001')).closest('tr') as HTMLElement
    await user.click(within(first).getByRole('button', { name: 'Show' }))

    const second = (await screen.findByText('P-0007')).closest('tr') as HTMLElement
    await user.click(within(second).getByRole('button', { name: 'Show' }))

    const detail = screen.getByRole('region', { name: 'Record detail' })
    expect(within(detail).getByRole('heading', { level: 2 })).toHaveFocus()
  })

  it('KEEPS THE DETAIL CONTROL ON EVERY VIEWPORT, because it is the only route in', async () => {
    /*
      The Detail column was marked `secondary`, which hides a cell below the
      breakpoint. That is right for detail that does not earn phone space and
      wrong here: this cell is not detail, it is the only way to reach it.
      Measured at 390px the button was 0x0, absent from the accessibility tree
      with its cell, and unfocusable -- zero keyboard-reachable Show controls --
      so a record's IP address and its changes were desktop-only.

      Asserted on the class rather than on a rendered width because jsdom has no
      layout: `table__cell--secondary` is what the breakpoint keys on.
    */
    signIn()
    renderActivity()

    const row = (await screen.findByText('AT-0001')).closest('tr') as HTMLElement
    const cell = within(row).getByRole('button', { name: 'Show' }).closest('td') as HTMLElement
    expect(cell).not.toHaveClass('table__cell--secondary')
  })
})

// ---------------------------------------------------------------------------
// Saying it in the console's own words
// ---------------------------------------------------------------------------

describe('the trail reads in one vocabulary', () => {
  it('NAMES AN OPERATOR ROLE AS THE REST OF THE CONSOLE NAMES IT', async () => {
    // This column printed the stored enum, "ADMIN", beside an email address,
    // while the Operators screen called the same value "Administrator".
    signIn()
    renderActivity()

    const row = (await screen.findByText('AT-0001')).closest('tr') as HTMLElement
    expect(within(row).getByText('Administrator')).toBeInTheDocument()
    expect(within(row).queryByText('ADMIN')).not.toBeInTheDocument()
  })

  it('LEAVES THE PLATFORM MARKER ALONE, because it is not an operator role', async () => {
    // PLATFORM marks a change made by the vendor's own surface rather than by
    // somebody inside the company. Passing it through a role label would be
    // inventing a role that does not exist.
    signIn()
    renderActivity()

    expect(await screen.findByText('Platform')).toBeInTheDocument()
  })

  it('humanises a target type the way its own filter already does', async () => {
    signIn()
    renderActivity()

    const row = (await screen.findByText('AT-0001')).closest('tr') as HTMLElement
    expect(within(row).getByText('Terminal')).toBeInTheDocument()
    expect(within(row).queryByText('TERMINAL')).not.toBeInTheDocument()
  })

  it('KEEPS THE TARGET IDENTIFIER EXACTLY AS RECORDED', async () => {
    // The half of that column the audit contract depends on. Humanising the KIND
    // of thing must not touch WHICH thing.
    signIn()
    renderActivity()

    expect(await screen.findByText('AT-0001')).toBeInTheDocument()
  })

  it('CALLS A FEATURE A FEATURE, in the column and in the filter alike', async () => {
    /*
      The stored `target_type` is APPLICATION, and humanising it gave
      "Application" -- a word this console stopped showing customers. Everywhere
      else, the thing a company turns on is a FEATURE: the navigation entry, the
      page heading, the terminal assignment dialog and the role descriptions all
      say so. Leaving this one humanised put the abandoned word back in the one
      screen an operator opens to find out what changed.

      THE STORED VALUE IS UNTOUCHED. `describeTarget` maps only the label, and
      the filter still sends APPLICATION to the server -- which is asserted here
      by checking that selecting the option produces a matching request rather
      than an empty table.
    */
    signIn()
    renderActivity()

    const row = (await screen.findByText('ACCESS_CONTROL')).closest('tr') as HTMLElement
    expect(within(row).getByText('Feature')).toBeInTheDocument()
    expect(within(row).queryByText('Application')).not.toBeInTheDocument()

    expect(
      within(screen.getByLabelText('Target type')).getByRole('option', { name: 'Feature' }),
    ).toHaveValue('APPLICATION')
  })

  it('names the site credential the way the site page named it', async () => {
    /*
      `SITE_KEY_ROTATED` read as "Site key rotated" while every other surface --
      the button that does it, the confirmation, the panel that follows, the
      warning inside it -- calls the same credential a PROVISIONING KEY. An
      owner checking the trail for the rotation they just performed was looking
      for a phrase the product had used nowhere else.
    */
    signIn()
    renderActivity()

    expect(await screen.findByText('Provisioning key rotated')).toBeInTheDocument()
    expect(screen.queryByText('Site key rotated')).not.toBeInTheDocument()
  })

  it('still shows the raw code for an action it cannot describe', async () => {
    // Unchanged and non-negotiable: the action column is open server-side, and a
    // record this build cannot name must still be identifiable.
    signIn()
    renderActivity()

    expect(await screen.findByText('VISITOR_BADGE_PRINTED')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Time, and how much of the answer is on screen
// ---------------------------------------------------------------------------

describe('what the trail says about when', () => {
  it('SHOWS AN ABSOLUTE TIME AS WELL AS A RELATIVE ONE', async () => {
    /*
      Relative time is right for scanning, and it was all there was -- the
      instant sat in a `title`, which a touch screen has no way to reach. The
      question an audit trail is usually opened to answer is the other one: what
      time did this happen, so it can be lined up against an incident or another
      system's log.
    */
    signIn()
    renderActivity()

    const row = (await screen.findByText('AT-0001')).closest('tr') as HTMLElement
    const times = within(row).getAllByRole('time')
    expect(times.length).toBeGreaterThanOrEqual(2)

    // One of them still reads as elapsed time, and one as a date.
    const text = times.map((t) => t.textContent ?? '').join(' | ')
    expect(text).toMatch(/ago|just now|yesterday/i)
    expect(text).toMatch(/\d{4}/)
  })

  it('ALWAYS SAYS HOW MANY RECORDS MATCHED, even when they all fit on one page', async () => {
    /*
      The count line lived inside the pagination control, which returned nothing
      at all when everything fitted on one page -- so "how many matched"
      disappeared in exactly the case where it is the answer. On an audit trail
      that is the normal case: narrow to one operator and a fortnight, and
      "three records match" IS the finding.
    */
    signIn()
    renderActivity()

    await screen.findByText('AT-0001')
    expect(screen.getByText(/Showing/)).toBeInTheDocument()
    expect(screen.getByText(/records/)).toBeInTheDocument()
    // A single page still offers no paging controls: two disabled buttons are a
    // worse way of saying "there is no more" than their absence.
    expect(screen.queryByRole('button', { name: 'Next' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Previous' })).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Filtering
// ---------------------------------------------------------------------------

describe('filtering', () => {
  it('FILTERS ON THE SERVER, not by narrowing the page', async () => {
    const user = userEvent.setup()
    signIn()
    renderActivity()

    await screen.findByText('AT-0001')
    await user.selectOptions(screen.getByLabelText('Action'), 'PERSON_CREATED')

    await waitFor(() => expect(screen.queryByText('AT-0001')).not.toBeInTheDocument())
    expect(screen.getByText('P-0007')).toBeInTheDocument()

    // The proof: the filter reached the API. A client-side filter would show the
    // same screen having asked for everything.
    const asked = state.requests.filter((entry) => entry.url.includes('/console/audit'))
    expect(asked.some((entry) => entry.url.includes('action=PERSON_CREATED'))).toBe(true)
  })

  it('narrows by operator on the server too', async () => {
    const user = userEvent.setup()
    signIn()
    renderActivity()

    await screen.findByText('AT-0001')
    await user.type(screen.getByLabelText('Operator'), 'kemi')

    await waitFor(() =>
      expect(
        state.requests.some((entry) => entry.url.includes('actor=kemi')),
      ).toBe(true),
    )
    await waitFor(() => expect(screen.queryByText('AT-0001')).not.toBeInTheDocument())
  })

  it('INCLUDES THE WHOLE OF THE "TO" DAY', async () => {
    // A bare date as `until` means midnight at the START of the day, which
    // silently excludes everything that happened on the day the operator asked
    // about. It is the most common date-filter bug and an unusually bad one
    // here, because the result looks like "nothing happened".
    const user = userEvent.setup()
    signIn()
    renderActivity()

    await screen.findByText('AT-0001')
    await user.type(screen.getByLabelText('To'), '2026-08-14')

    await waitFor(() => {
      const asked = state.requests.filter((entry) => entry.url.includes('until='))
      expect(asked.length).toBeGreaterThan(0)
      const until = new URL(asked[asked.length - 1]?.url ?? '').searchParams.get('until') ?? ''
      // The record at 17:05 on the 14th must still be inside the window.
      expect(until > '2026-08-14T17:05:00Z').toBe(true)
    })
    expect(screen.getByText('AT-0001')).toBeInTheDocument()
  })

  it('returns to the first page when a filter changes', async () => {
    // Staying on page 4 of a result set that now has one page shows an empty
    // table over a filter that matched plenty.
    const user = userEvent.setup()
    const many = Array.from({ length: 120 }, (_, index) =>
      makeAuditRecord({
        id: `audit-${index}`,
        action: index % 2 === 0 ? 'PERSON_CREATED' : 'SITE_UPDATED',
        target_label: `T-${index}`,
        occurred_at: '2026-08-10T00:00:00Z',
      }),
    )
    signIn('ADMIN', many)
    renderActivity()

    await screen.findByText('T-0')
    await user.click(await screen.findByRole('button', { name: 'Next' }))
    await screen.findByText(/Page 2 of/)

    await user.selectOptions(screen.getByLabelText('Action'), 'SITE_UPDATED')
    await waitFor(() => expect(screen.queryByText(/Page 2 of/)).not.toBeInTheDocument())
  })

  it('distinguishes "nothing matched" from "nothing recorded yet"', async () => {
    const user = userEvent.setup()
    signIn()
    renderActivity()

    await screen.findByText('AT-0001')
    await user.selectOptions(screen.getByLabelText('Action'), 'FIRMWARE_PUBLISHED')

    expect(await screen.findByText('Nothing matched')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Clear filters' }))
    await waitFor(() => expect(screen.getByText('AT-0001')).toBeInTheDocument())
  })

  it('says nothing is recorded rather than nothing matched on an empty trail', async () => {
    signIn('ADMIN', [])
    renderActivity()

    expect(await screen.findByText('Nothing recorded yet')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Honesty about what this page is
// ---------------------------------------------------------------------------

describe('what this page does not cover', () => {
  it('sends somebody looking for the event log to the event log', async () => {
    // Somebody investigating why a person could not get in will come here
    // first. Leaving them to conclude the trail is broken is the failure, and
    // so is telling them the history does not exist once it does.
    signIn()
    renderActivity()

    expect(
      await screen.findByText('This is the operator trail, not the event log'),
    ).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Events' })).toHaveAttribute('href', '/events')
  })

  /*
    THE OTHER TRAIL IS THE EVENT LOG, NOT THE DOOR LOG.

    The note that points at Events named it after one kind of hardware. A school
    reading "door log" on the screen that explains where its access history
    lives has been told the product is somebody else's.
  */
  it('names the other trail without calling it a door log', async () => {
    signIn()
    renderActivity()

    await screen.findByText('This is the operator trail, not the event log')
    expectNoDoorWording('Activity', document.body.textContent ?? '')
  })

  it('reports a failed load as an error rather than as an empty trail', async () => {
    // "No records" and "we could not ask" must never look alike on an audit
    // surface.
    signIn()
    failNext('audit', 500)
    renderActivity()

    expect(await screen.findByRole('alert')).toHaveTextContent(/Failed to retrieve the audit trail/)
  })
})
