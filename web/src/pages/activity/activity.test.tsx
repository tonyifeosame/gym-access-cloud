import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../../api/csrf'
import type { AuditRecord, Role } from '../../api/types'
import { makeAuditRecord, makeSession } from '../../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../../test/render'
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
  it('says the door log is a separate stream that is not here yet', async () => {
    // Somebody investigating why a person could not get in will come here
    // first. Leaving them to conclude the trail is broken is the failure.
    signIn()
    renderActivity()

    expect(
      await screen.findByText('This is the operator trail, not the door log'),
    ).toBeInTheDocument()
    expect(screen.getByText(/does not yet expose it to the console/i)).toBeInTheDocument()
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
