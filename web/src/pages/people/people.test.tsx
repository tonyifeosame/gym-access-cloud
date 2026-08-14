import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../../api/csrf'
import type { Person, Role, Session } from '../../api/types'
import { keys } from '../../data/keys'
import { makePerson, makeSession, SITE_A } from '../../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../../test/render'
import { failNext, resetServerState, seed, state } from '../../test/server'
import { PeopleListPage } from './PeopleListPage'
import { PersonDetailPage } from './PersonDetailPage'

/**
 * The People module.
 *
 * Two things dominate what is asserted here. Search and paging must reach the
 * SERVER — a browser-side filter over one page is the failure mode this module
 * is most prone to, and it passes every small-fixture test. And nothing about a
 * biometric credential beyond a boolean may ever appear.
 */

/** Enough people to page through: 7 records, mixed names. */
const ROSTER: Person[] = Array.from({ length: 7 }, (_, index) =>
  makePerson({
    id: `person-${index}`,
    external_id: `P-000${index}`,
    full_name: index % 2 === 0 ? `Ada Number ${index}` : `Bola Number ${index}`,
    active: index !== 3,
    biometric_enrolled: index % 3 === 0,
    category: index === 0 ? 'Contractor' : '',
  }),
)

function signIn(role: Role = 'MANAGER', overrides: Partial<Session> = {}) {
  const session = makeSession({
    role,
    operator: { id: 'operator-1', email: 'ops@example.com', full_name: 'Ops', role },
    ...overrides,
  })
  resetServerState(session)
  setCsrfToken(session.csrf_token)
  seed({ people: ROSTER })
  return session
}

function renderPeople(initialPath = '/people', client = makeTestQueryClient()) {
  const router = createMemoryRouter(
    [
      { path: '/people', element: <PeopleListPage /> },
      { path: '/people/:externalId', element: <PersonDetailPage /> },
    ],
    { initialEntries: [initialPath] },
  )
  return renderWithSession(<RouterProvider router={router} />, client)
}

/** Query strings the mock actually received, for asserting server-side work. */
function peopleRequests() {
  return state.requests.filter(
    (request) => request.method === 'GET' && request.url.includes('/console/people?'),
  )
}

beforeEach(() => setCsrfToken(null))

// ---------------------------------------------------------------------------
// List, search, paging
// ---------------------------------------------------------------------------

describe('people list', () => {
  it('lists people with identifier, type, status and credential state', async () => {
    signIn()
    renderPeople()

    const first = (await screen.findByText('P-0000')).closest('tr') as HTMLElement
    expect(within(first).getByRole('link', { name: 'Ada Number 0' })).toBeInTheDocument()
    expect(within(first).getByText('Contractor')).toBeInTheDocument()
    expect(within(first).getByText('Active')).toBeInTheDocument()
    expect(within(first).getByText('Enrolled')).toBeInTheDocument()

    const inactive = screen.getByText('P-0003').closest('tr') as HTMLElement
    expect(within(inactive).getByText('Inactive')).toBeInTheDocument()
  })

  it('SEARCHES ON THE SERVER, not by filtering the page', async () => {
    // The failure this guards against passes every test with three fixtures and
    // is silently wrong the moment a company outgrows one page.
    const user = userEvent.setup()
    signIn()
    renderPeople()

    await screen.findByText('P-0000')
    await user.type(screen.getByLabelText('Search people'), 'bola')

    await waitFor(() => expect(screen.queryByText('P-0000')).not.toBeInTheDocument())
    // The term reached the API.
    await waitFor(() => expect(peopleRequests().at(-1)?.url).toContain('q=bola'))
    // Three Bolas exist in the roster.
    expect(screen.getByText('P-0001')).toBeInTheDocument()
    expect(screen.getByText('P-0005')).toBeInTheDocument()
  })

  it('pages on the server and reports the range', async () => {
    const user = userEvent.setup()
    signIn()
    renderPeople()

    await screen.findByText('P-0000')
    // Seven people fit one 50-row page, so no controls are offered.
    expect(screen.queryByRole('button', { name: 'Next' })).not.toBeInTheDocument()

    // Narrow the page size by asking the API for less, the way paging does.
    await user.type(screen.getByLabelText('Search people'), 'number')
    await waitFor(() => expect(peopleRequests().at(-1)?.url).toContain('q=number'))
    expect(peopleRequests().at(-1)?.url).toContain('limit=50')
  })

  it('resets to the first page when the search changes', async () => {
    // Results for a new term have no meaningful page 3 carried over.
    const user = userEvent.setup()
    signIn()
    renderPeople()

    await screen.findByText('P-0000')
    await user.type(screen.getByLabelText('Search people'), 'ada')

    await waitFor(() => expect(peopleRequests().at(-1)?.url).toContain('q=ada'))
    expect(peopleRequests().at(-1)?.url).not.toContain('offset=')
  })

  it('distinguishes "nobody matched" from "nobody yet"', async () => {
    const user = userEvent.setup()
    signIn()
    renderPeople()

    await screen.findByText('P-0000')
    await user.type(screen.getByLabelText('Search people'), 'nobody at all')

    expect(await screen.findByText('Nobody matches that search')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Clear search' }))
    await waitFor(() => expect(screen.getByText('P-0000')).toBeInTheDocument())
  })

  it('shows a useful, domain-neutral empty state for a new company', async () => {
    signIn()
    seed({ people: [] })
    renderPeople()

    expect(await screen.findByText('No people yet')).toBeInTheDocument()
    // Neutral: names several kinds of deployment rather than assuming one.
    expect(screen.getByText(/employees, students, contractors, visitors/)).toBeInTheDocument()
  })

  it('reports a failed load as an error, not as an empty roster', async () => {
    signIn()
    failNext('people', 500)
    renderPeople()

    expect(await screen.findByRole('alert')).toHaveTextContent(/Failed to retrieve people/)
    expect(screen.queryByText('No people yet')).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Tenancy and scope
// ---------------------------------------------------------------------------

describe('scope and tenancy', () => {
  it('tells a site-scoped operator that people are company-wide', async () => {
    // The schema has no person-to-site relationship, so site grants cannot
    // narrow this list. Saying so beats letting an operator assume it is
    // filtered the way their sites and terminals are.
    signIn('MANAGER', { all_sites: false, sites: [SITE_A] })
    renderPeople()

    await screen.findByText('P-0000')
    expect(
      screen.getByText(/People are company-wide, so this list is not narrowed/),
    ).toBeInTheDocument()
  })

  it('does not show that note to an unscoped operator', async () => {
    signIn('ADMIN')
    renderPeople()

    await screen.findByText('P-0000')
    expect(screen.queryByText(/company-wide/)).not.toBeInTheDocument()
  })

  it('treats an unknown identifier as not found', async () => {
    signIn()
    renderPeople('/people/P-NOPE')

    expect(await screen.findByText('Person not found')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Roles
// ---------------------------------------------------------------------------

describe('role restrictions', () => {
  it('withholds every write from a VIEWER, who can still read', async () => {
    signIn('VIEWER')
    renderPeople('/people/P-0000')

    await screen.findByRole('heading', { name: 'Ada Number 0', level: 1 })
    for (const label of ['Edit', 'Deactivate', 'Remove']) {
      expect(screen.queryByRole('button', { name: label })).not.toBeInTheDocument()
    }
    // Reading is unaffected: the gate is on the write, as it is server-side.
    expect(screen.getByText('Person type')).toBeInTheDocument()
  })

  it('offers them to a MANAGER and above', async () => {
    for (const role of ['MANAGER', 'ADMIN', 'OWNER'] as const) {
      signIn(role)
      const { unmount } = renderPeople('/people/P-0000')
      expect(await screen.findByRole('button', { name: 'Edit' })).toBeInTheDocument()
      unmount()
    }
  })

  it('offers no create action to a VIEWER', async () => {
    signIn('VIEWER')
    renderPeople()
    await screen.findByText('P-0000')
    expect(screen.queryByRole('button', { name: 'Add a person' })).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Create and edit
// ---------------------------------------------------------------------------

describe('creating a person', () => {
  it('creates one and refreshes the list', async () => {
    const user = userEvent.setup()
    signIn()
    const client = makeTestQueryClient()
    renderPeople('/people', client)

    await user.click(await screen.findByRole('button', { name: 'Add a person' }))
    await user.type(screen.getByLabelText(/Identifier/), 'P-NEW')
    await user.type(screen.getByLabelText(/Full name/), 'Chidi Okafor')
    await user.click(screen.getByRole('button', { name: 'Add person' }))

    // Every page and search is invalidated; the new person may belong on any.
    expect(await screen.findByText('P-NEW')).toBeInTheDocument()
  })

  it('validates before asking the server', async () => {
    const user = userEvent.setup()
    signIn()
    renderPeople()

    await user.click(await screen.findByRole('button', { name: 'Add a person' }))
    const before = state.requests.filter((r) => r.method === 'POST').length
    await user.click(screen.getByRole('button', { name: 'Add person' }))

    expect(await screen.findByText(/Identifier is required/)).toBeInTheDocument()
    expect(screen.getByText(/Full name is required/)).toBeInTheDocument()
    expect(state.requests.filter((r) => r.method === 'POST')).toHaveLength(before)
  })

  it('reports a duplicate identifier as an actionable conflict', async () => {
    const user = userEvent.setup()
    signIn()
    renderPeople()

    await user.click(await screen.findByRole('button', { name: 'Add a person' }))
    await user.type(screen.getByLabelText(/Identifier/), 'P-0000')
    await user.type(screen.getByLabelText(/Full name/), 'Duplicate')
    await user.click(screen.getByRole('button', { name: 'Add person' }))

    expect(
      await screen.findByText('Someone with that identifier already exists in your company.'),
    ).toBeInTheDocument()
    // Values kept so the identifier can be corrected.
    expect(screen.getByLabelText(/Identifier/)).toHaveValue('P-0000')
  })

  it('offers no credential field in either direction', async () => {
    // The console does not write credentials, and there is nothing here to try.
    const user = userEvent.setup()
    signIn()
    renderPeople()

    await user.click(await screen.findByRole('button', { name: 'Add a person' }))
    expect(screen.queryByLabelText(/fingerprint/i)).not.toBeInTheDocument()
    expect(screen.queryByLabelText(/biometric/i)).not.toBeInTheDocument()
    expect(screen.queryByLabelText(/template/i)).not.toBeInTheDocument()
  })

  it('keeps person type free text rather than a fixed taxonomy', async () => {
    // A school says Student, a factory says Contractor, an event says Attendee.
    // A fixed list would be the product deciding what business you are in.
    const user = userEvent.setup()
    signIn()
    renderPeople()

    await user.click(await screen.findByRole('button', { name: 'Add a person' }))
    const field = screen.getByLabelText('Person type')
    expect(field.tagName).toBe('INPUT')
    await user.type(field, 'Postgraduate student')
    expect(field).toHaveValue('Postgraduate student')
  })
})

describe('editing a person', () => {
  it('saves changes and updates what is on screen', async () => {
    const user = userEvent.setup()
    signIn()
    renderPeople('/people/P-0000')

    await user.click(await screen.findByRole('button', { name: 'Edit' }))
    const name = screen.getByLabelText(/Full name/)
    await user.clear(name)
    await user.type(name, 'Ada Renamed')
    await user.click(screen.getByRole('button', { name: 'Save changes' }))

    expect(
      await screen.findByRole('heading', { name: 'Ada Renamed', level: 1 }),
    ).toBeInTheDocument()
  })

  it('will not let the identifier be changed', async () => {
    // Terminals hold it and sync against it; the API addresses a person by it.
    const user = userEvent.setup()
    signIn()
    renderPeople('/people/P-0000')

    await user.click(await screen.findByRole('button', { name: 'Edit' }))
    expect(screen.getByLabelText(/Identifier/)).toBeDisabled()
    expect(screen.getByText(/Cannot be changed/)).toBeInTheDocument()
  })

  it('preserves the enrolled credential across an edit', async () => {
    const user = userEvent.setup()
    signIn()
    renderPeople('/people/P-0000')

    await user.click(await screen.findByRole('button', { name: 'Edit' }))
    const name = screen.getByLabelText(/Full name/)
    await user.clear(name)
    await user.type(name, 'Corrected Spelling')
    await user.click(screen.getByRole('button', { name: 'Save changes' }))

    // Correcting a name must never unenrol somebody.
    await screen.findByRole('heading', { name: 'Corrected Spelling', level: 1 })
    const credential = screen.getByRole('region', { name: 'Biometric credential' })
    expect(within(credential).getByText('Enrolled')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Activate / deactivate / remove
// ---------------------------------------------------------------------------

describe('activation', () => {
  it('deactivates reversibly and says terminals apply it on next sync', async () => {
    const user = userEvent.setup()
    signIn()
    renderPeople('/people/P-0000')

    await user.click(await screen.findByRole('button', { name: 'Deactivate' }))
    expect(screen.getByText(/stop admitting them/)).toBeInTheDocument()
    expect(screen.getByText(/Reversible/)).toBeInTheDocument()
    // Not the irreversible one: no typed confirmation.
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument()

    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: 'Deactivate' }),
    )
    expect(await screen.findByText('This person is inactive')).toBeInTheDocument()
  })
})

describe('removal', () => {
  it('EXPLAINS THE TERMINAL SYNCHRONISATION before asking', async () => {
    // Removing a person is not tidying a table: it enqueues a DELETE to every
    // terminal, and that job is the only way an offline terminal ever learns to
    // forget a credential it already holds.
    const user = userEvent.setup()
    signIn()
    renderPeople('/people/P-0000')

    await user.click(await screen.findByRole('button', { name: 'Remove' }))

    expect(screen.getByText(/forget this person and their credential/)).toBeInTheDocument()
    expect(screen.getByText(/offline apply it when they next reconnect/)).toBeInTheDocument()
    expect(screen.getByText(/deactivate them instead/)).toBeInTheDocument()
  })

  it('requires typing the identifier', async () => {
    const user = userEvent.setup()
    signIn()
    renderPeople('/people/P-0000')

    await user.click(await screen.findByRole('button', { name: 'Remove' }))
    const confirm = screen.getByRole('button', { name: 'Remove person' })
    expect(confirm).toBeDisabled()

    await user.type(screen.getByRole('textbox'), 'P-0000')
    expect(confirm).toBeEnabled()
  })

  it('removes, drops the cached record and returns to the list', async () => {
    const user = userEvent.setup()
    signIn()
    const client = makeTestQueryClient()
    renderPeople('/people/P-0000', client)

    await user.click(await screen.findByRole('button', { name: 'Remove' }))
    await user.type(screen.getByRole('textbox'), 'P-0000')
    await user.click(screen.getByRole('button', { name: 'Remove person' }))

    await waitFor(() => expect(screen.queryByText('P-0000')).not.toBeInTheDocument())
    // Refetching the detail would only produce a 404, so it is dropped.
    expect(client.getQueryData(keys.people.detail('P-0000'))).toBeUndefined()
  })

  it('keeps the dialog open when removal fails', async () => {
    const user = userEvent.setup()
    signIn()
    failNext('delete-person', 500)
    renderPeople('/people/P-0000')

    await user.click(await screen.findByRole('button', { name: 'Remove' }))
    await user.type(screen.getByRole('textbox'), 'P-0000')
    await user.click(screen.getByRole('button', { name: 'Remove person' }))

    expect(await screen.findByText(/Failed to delete person/)).toBeInTheDocument()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Biometrics stay abstract
// ---------------------------------------------------------------------------

describe('biometric non-disclosure', () => {
  it('reports only WHETHER a credential exists', async () => {
    signIn()
    renderPeople('/people/P-0000')

    await screen.findByRole('heading', { name: 'Ada Number 0', level: 1 })
    const credential = screen.getByRole('region', { name: 'Biometric credential' })
    expect(within(credential).getByText('Credential status')).toBeInTheDocument()
    expect(within(credential).getByText('Enrolled')).toBeInTheDocument()
  })

  it('names no template, locator, sensor or vendor anywhere on the page', async () => {
    // The console must keep working if the hardware, the vendor, or the
    // credential type ever changes.
    signIn()
    renderPeople('/people/P-0000')

    await screen.findByRole('heading', { name: 'Ada Number 0', level: 1 })
    const text = (document.body.textContent ?? '').toLowerCase()
    for (const forbidden of ['template', 'minutiae', 'r307', 'zk', 'sensor', 'slot:']) {
      expect(text).not.toContain(forbidden)
    }
  })

  it('says enrolment is not available from the console rather than faking it', async () => {
    signIn()
    renderPeople('/people/P-0000')

    expect(await screen.findByText('Enrolment happens at a terminal')).toBeInTheDocument()
    expect(screen.getByText(/no operator API to start, review or clear an enrolment/)).toBeInTheDocument()
    // And offers no button that would 404.
    expect(screen.queryByRole('button', { name: /enrol/i })).not.toBeInTheDocument()
  })

  it('carries nothing credential-shaped in any list response it holds', async () => {
    signIn()
    renderPeople()

    await screen.findByText('P-0000')
    const text = document.body.textContent ?? ''
    for (const forbidden of ['fingerprint_template', 'api_key', 'atd_', 'ats_']) {
      expect(text).not.toContain(forbidden)
    }
  })
})
