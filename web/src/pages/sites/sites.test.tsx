import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { setCsrfToken } from '../../api/csrf'
import type { Role, Session } from '../../api/types'
import { keys } from '../../data/keys'
import { makeSession, makeSite, makeTerminal, SITE_A, SITE_B } from '../../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../../test/render'
import { expectNoDoorWording } from '../../test/vocabulary'
import { failNext, resetServerState, seed, state } from '../../test/server'
import { SiteDetailPage } from './SiteDetailPage'
import { SitesListPage } from './SitesListPage'

/**
 * The Sites module.
 *
 * Rendered through a real router and a real SessionProvider against the mock
 * API, so what is under test is the screen an operator actually gets --
 * including the role gates, the cache invalidation after a mutation, and the
 * handling of a credential that cannot be recovered.
 */

const SITES = [
  makeSite({ id: SITE_A.site_id, name: SITE_A.site_name, terminal_count: 2 }),
  makeSite({
    id: SITE_B.site_id,
    name: SITE_B.site_name,
    address: '9 Airport Way',
    timezone: 'Africa/Abuja',
    active: false,
    terminal_count: 0,
    // A different outage policy from its neighbour, so a list rendering one
    // value for every row cannot pass.
    offline_policy: 'DENY_ALL',
    offline_grace_minutes: 0,
  }),
]

function signIn(role: Role = 'ADMIN', overrides: Partial<Session> = {}) {
  const session = makeSession({
    role,
    operator: { id: 'operator-1', email: 'ops@example.com', full_name: 'Ops Person', role },
    ...overrides,
  })
  resetServerState(session)
  setCsrfToken(session.csrf_token)
  seed({
    sites: SITES,
    terminals: [
      makeTerminal({ serial_number: 'AT-0001', site_public_id: SITE_A.site_id }),
      makeTerminal({ id: 2, serial_number: 'AT-0002', site_public_id: SITE_A.site_id }),
    ],
  })
  return session
}

function renderSites(initialPath = '/sites', client = makeTestQueryClient()) {
  const router = createMemoryRouter(
    [
      { path: '/sites', element: <SitesListPage /> },
      { path: '/sites/:siteId', element: <SiteDetailPage /> },
    ],
    { initialEntries: [initialPath] },
  )
  return renderWithSession(<RouterProvider router={router} />, client)
}

/**
 * jsdom exposes navigator.clipboard as a getter-only property, so it has to be
 * redefined rather than assigned. Returns the spy so a test can assert what was
 * copied.
 */
function stubClipboard() {
  const writeText = vi.fn().mockResolvedValue(undefined)
  Object.defineProperty(navigator, 'clipboard', {
    value: { writeText },
    configurable: true,
  })
  return writeText
}

beforeEach(() => setCsrfToken(null))

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

describe('site list', () => {
  it('shows each site with its metadata, status and terminal count', async () => {
    signIn()
    renderSites()

    expect(await screen.findByRole('link', { name: SITE_A.site_name })).toBeInTheDocument()

    const abuja = screen.getByRole('link', { name: SITE_B.site_name }).closest('tr')
    expect(within(abuja as HTMLElement).getByText('Africa/Abuja')).toBeInTheDocument()
    expect(within(abuja as HTMLElement).getByText('9 Airport Way')).toBeInTheDocument()
    expect(within(abuja as HTMLElement).getByText('Inactive')).toBeInTheDocument()

    const lagos = screen.getByRole('link', { name: SITE_A.site_name }).closest('tr')
    expect(within(lagos as HTMLElement).getByText('Active')).toBeInTheDocument()
    expect(within(lagos as HTMLElement).getByText('2')).toBeInTheDocument()
  })

  it('shows a useful empty state for a company with no sites', async () => {
    signIn()
    seed({ sites: [], terminals: [] })
    renderSites()

    expect(await screen.findByText('No sites yet')).toBeInTheDocument()
    // Two ways in: the header action and the empty state's own.
    expect(screen.getAllByRole('button', { name: 'Add a site' })).toHaveLength(2)
  })

  it('reports a failed load as an error rather than as an empty company', async () => {
    // The distinction matters: "you have no sites" and "we could not ask" look
    // identical if a failed request renders as an empty table, and only one of
    // them is a reason to go and create a site.
    signIn()
    failNext('sites-list', 500)
    renderSites()

    expect(await screen.findByRole('alert')).toHaveTextContent(/Failed to retrieve sites/)
    expect(screen.queryByText('No sites yet')).not.toBeInTheDocument()
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
  })

  it('is narrowed by the API for a site-scoped operator', async () => {
    // Filtered server-side, never here: a client-side filter would mean the
    // browser had briefly held a site the operator is not entitled to.
    signIn('MANAGER', { all_sites: false, sites: [SITE_A] })
    renderSites()

    expect(await screen.findByRole('link', { name: SITE_A.site_name })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: SITE_B.site_name })).not.toBeInTheDocument()
  })

  it('CARRIES NO KEY COLUMN, because one could never be filled', async () => {
    // `models.ConsoleSite` has no `api_key_prefix` and `consoleSiteColumns`
    // selects none, so this column rendered an em dash on every row of every
    // company, for ever. It looked populated in development only because the
    // mock invented a prefix; it no longer does.
    signIn()
    renderSites()

    await screen.findByRole('link', { name: SITE_A.site_name })
    expect(screen.queryByRole('columnheader', { name: 'Key' })).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

/**
 * SEARCH IS SERVER-SIDE, and these tests are written to fail if it stops being.
 *
 * Matching the fetched array would search whatever one response happened to
 * carry rather than the estate -- silently wrong for a customer with more
 * locations than the screen was built around, and silently right in every test
 * with two fixtures. So the assertions check the REQUEST as well as the rows:
 * a client-side filter would satisfy the second and not the first.
 */
describe('searching sites', () => {
  function sitesRequests() {
    return state.requests.filter(
      (entry) => entry.method === 'GET' && entry.url.includes('/console/sites'),
    )
  }

  it('sends the term to the API rather than filtering what it already holds', async () => {
    const user = userEvent.setup()
    signIn()
    renderSites()

    await screen.findByRole('link', { name: SITE_A.site_name })

    await user.type(screen.getByLabelText('Search sites'), 'Abuja')

    await waitFor(() =>
      expect(sitesRequests().some((entry) => entry.url.includes('q=Abuja'))).toBe(true),
    )
    await waitFor(() =>
      expect(screen.queryByRole('link', { name: SITE_A.site_name })).not.toBeInTheDocument(),
    )
    expect(screen.getByRole('link', { name: SITE_B.site_name })).toBeInTheDocument()
  })

  it('matches an address as well as a name', async () => {
    const user = userEvent.setup()
    signIn()
    renderSites()

    await screen.findByRole('link', { name: SITE_A.site_name })
    await user.type(screen.getByLabelText('Search sites'), 'Airport')

    await waitFor(() =>
      expect(screen.queryByRole('link', { name: SITE_A.site_name })).not.toBeInTheDocument(),
    )
    expect(screen.getByRole('link', { name: SITE_B.site_name })).toBeInTheDocument()
  })

  it('asks for everything when the box is empty, sending no q at all', async () => {
    signIn()
    renderSites()

    await screen.findByRole('link', { name: SITE_A.site_name })
    expect(sitesRequests()).not.toHaveLength(0)
    expect(sitesRequests().every((entry) => !entry.url.includes('q='))).toBe(true)
  })

  it('DISTINGUISHES "nothing matched" FROM "no sites yet"', async () => {
    // Telling a company with a full estate that it has no locations is the worse
    // of the two mistakes, and an empty table cannot tell them apart on its own.
    const user = userEvent.setup()
    signIn()
    renderSites()

    await screen.findByRole('link', { name: SITE_A.site_name })
    await user.type(screen.getByLabelText('Search sites'), 'nowhere at all')

    expect(await screen.findByText('No sites match that search')).toBeInTheDocument()
    expect(screen.queryByText('No sites yet')).not.toBeInTheDocument()
  })

  it('offers a way back from a search that matched nothing', async () => {
    const user = userEvent.setup()
    signIn()
    renderSites()

    await screen.findByRole('link', { name: SITE_A.site_name })
    await user.type(screen.getByLabelText('Search sites'), 'nowhere at all')
    await screen.findByText('No sites match that search')

    await user.click(screen.getByRole('button', { name: 'Clear search' }))
    expect(await screen.findByRole('link', { name: SITE_A.site_name })).toBeInTheDocument()
  })

  it('NARROWS WITHIN A GRANT AND NEVER WIDENS IT', async () => {
    // The scope predicate and the search predicate are applied in the same
    // statement server-side. A term must not surface a site the operator is not
    // entitled to, however exactly it matches.
    const user = userEvent.setup()
    signIn('MANAGER', { all_sites: false, sites: [SITE_A] })
    renderSites()

    await screen.findByRole('link', { name: SITE_A.site_name })
    await user.type(screen.getByLabelText('Search sites'), SITE_B.site_name)

    await waitFor(() =>
      expect(screen.getByText('No sites match that search')).toBeInTheDocument(),
    )
    expect(screen.queryByRole('link', { name: SITE_B.site_name })).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Role restrictions
// ---------------------------------------------------------------------------

describe('role restrictions', () => {
  it('offers no lifecycle controls below ADMIN', async () => {
    // A courtesy, not a boundary: the API refuses these regardless.
    for (const role of ['VIEWER', 'MANAGER'] as const) {
      signIn(role)
      const { unmount } = renderSites()

      await screen.findByRole('link', { name: SITE_A.site_name })
      expect(screen.queryByRole('button', { name: 'Add a site' })).not.toBeInTheDocument()
      unmount()
    }
  })

  it('offers them to an ADMIN', async () => {
    signIn('ADMIN')
    renderSites()
    expect(await screen.findByRole('button', { name: 'Add a site' })).toBeInTheDocument()
  })

  it('hides edit, rotate, deactivate and retire from a MANAGER on the detail page', async () => {
    signIn('MANAGER')
    renderSites(`/sites/${SITE_A.site_id}`)

    await screen.findByRole('heading', { name: SITE_A.site_name, level: 1 })
    for (const label of ['Edit', 'Rotate provisioning key', 'Deactivate', 'Retire']) {
      expect(screen.queryByRole('button', { name: label })).not.toBeInTheDocument()
    }
  })
})

// ---------------------------------------------------------------------------
// Detail
// ---------------------------------------------------------------------------

describe('site detail', () => {
  it('shows status, terminal count, time zone and when it was added', async () => {
    signIn()
    renderSites(`/sites/${SITE_A.site_id}`)

    await screen.findByRole('heading', { name: SITE_A.site_name, level: 1 })
    expect(screen.getByText('Active')).toBeInTheDocument()
    expect(screen.getByText('Terminals')).toBeInTheDocument()
    expect(screen.getByText('Africa/Lagos')).toBeInTheDocument()
  })

  it('says so plainly when a site is deactivated', async () => {
    signIn()
    renderSites(`/sites/${SITE_B.site_id}`)

    expect(await screen.findByText('This site is deactivated')).toBeInTheDocument()
    expect(screen.getByText(/Nothing has been deleted/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Reactivate' })).toBeInTheDocument()
  })

  it('treats another company’s site as not found, without implying it exists', async () => {
    signIn()
    renderSites('/sites/some-other-companys-site')

    expect(await screen.findByText('Site not found')).toBeInTheDocument()
    expect(screen.getByText(/does not exist, or it is not part of your company/)).toBeInTheDocument()
  })

  it('SHOWS WHAT EACH SITE DOES DURING AN OUTAGE, in the list', async () => {
    // "Which of our locations keeps opening when the network goes" is a question
    // asked about a whole estate, and answering it by opening each site in turn
    // is how it stops being asked. The columns are on every site projection, so
    // this costs no extra request.
    signIn()
    renderSites()

    const lagos = (await screen.findByText(SITE_A.site_name)).closest('tr') as HTMLElement
    expect(within(lagos).getByText('Keep working for a limited time')).toBeInTheDocument()

    const abuja = screen.getByText(SITE_B.site_name).closest('tr') as HTMLElement
    expect(within(abuja).getByText('Refuse everybody')).toBeInTheDocument()
  })

  it('explains a 403 as a scope problem rather than as a failure', async () => {
    signIn('MANAGER', { all_sites: false, sites: [SITE_A] })
    renderSites(`/sites/${SITE_B.site_id}`)

    expect(await screen.findByText('Not one of your sites')).toBeInTheDocument()
  })

  it('CARRIES NO PROVISIONING-KEY CARD, because one could never be filled', async () => {
    /*
      The card read "Not reported" on every site in every session, and spent
      thirty-nine words telling a customer that our own read endpoints do not
      return a field. `models.ConsoleSite` has no `api_key_prefix` and
      `consoleSiteColumns` selects none, so no GET carries one; `useCreateSite`
      caches `result.site` rather than the response and `useRotateSiteKey` only
      invalidates, so no cache write puts one there either.

      It looked alive in development because the test mock stored a prefix on the
      site after a create or a rotation. It no longer does -- see the note in
      test/server.ts -- which is what keeps a surface like this from being built
      against a fixture the API will not supply.
    */
    signIn()
    renderSites(`/sites/${SITE_A.site_id}`)

    await screen.findByRole('heading', { name: SITE_A.site_name, level: 1 })
    expect(screen.queryByText('Provisioning key')).not.toBeInTheDocument()
    expect(screen.queryByText(/not returned by any read endpoint/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/Not reported/i)).not.toBeInTheDocument()
    // Unchanged and non-negotiable: no GET populates a full key, and nothing on
    // the page requests one.
    expect(document.body.textContent).not.toMatch(/ats_[0-9a-f]{64}/)
  })

  it('KEEPS THE DESTRUCTIVE ACTIONS OUT OF THE PAGE HEADER', async () => {
    /*
      The header used to carry four buttons of equal weight -- Edit, Rotate
      provisioning key,
      Deactivate, Retire -- with only a fill colour separating the irreversible
      one. `.page__actions` wraps, so at some viewport widths Retire landed on a
      new row beside Edit.

      Edit is the frequent, harmless one and stays. The three that stop hardware
      working are grouped and labelled at the end of the page.
    */
    signIn()
    renderSites(`/sites/${SITE_A.site_id}`)

    await screen.findByRole('heading', { name: SITE_A.site_name, level: 1 })

    const header = document.querySelector('.page__actions') as HTMLElement
    expect(within(header).getByRole('button', { name: 'Edit' })).toBeInTheDocument()
    expect(within(header).queryByRole('button', { name: 'Retire' })).not.toBeInTheDocument()
    expect(within(header).queryByRole('button', { name: 'Rotate provisioning key' })).not.toBeInTheDocument()
    expect(within(header).queryByRole('button', { name: 'Deactivate' })).not.toBeInTheDocument()

    // All three are still reachable, together, under their own heading.
    const danger = screen.getByRole('region', { name: 'Site administration' })
    expect(within(danger).getByRole('button', { name: 'Rotate provisioning key' })).toBeInTheDocument()
    expect(within(danger).getByRole('button', { name: 'Deactivate' })).toBeInTheDocument()
    expect(within(danger).getByRole('button', { name: 'Retire' })).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Creation and the one-time credential
// ---------------------------------------------------------------------------

describe('creating a site', () => {
  it('creates it and shows the key exactly once, with a copy button', async () => {
    const user = userEvent.setup()
    const writeText = stubClipboard()

    signIn()
    const client = makeTestQueryClient()
    renderSites('/sites', client)

    await user.click(await screen.findByRole('button', { name: 'Add a site' }))
    await user.type(screen.getByLabelText(/Site name/), 'Riverside Works')
    await user.click(screen.getByRole('button', { name: 'Create site' }))

    // THE WARNING IS UNMISSABLE and comes before the value.
    expect(await screen.findByText(/shown once and cannot be recovered/i)).toBeInTheDocument()

    const key = screen.getByLabelText('Provisioning key') as HTMLInputElement
    expect(key.value).toMatch(/^ats_[0-9a-f]{64}$/)
    expect(key).toHaveAttribute('readonly')

    await user.click(screen.getByRole('button', { name: 'Copy key' }))
    expect(writeText).toHaveBeenCalledWith(key.value)
    expect(await screen.findByText('Copied to clipboard')).toBeInTheDocument()

    // Dismissal REQUIRES acknowledgement.
    const done = screen.getByRole('button', { name: 'Done' })
    expect(done).toBeDisabled()
    await user.click(screen.getByLabelText('I have stored this key somewhere safe'))
    expect(done).toBeEnabled()
    await user.click(done)

    // The list refreshed, and the key is gone from the document entirely.
    expect(await screen.findByRole('link', { name: 'Riverside Works' })).toBeInTheDocument()
    expect(document.body.textContent).not.toContain(key.value)
  })

  it('does not leave the key anywhere after the panel closes', async () => {
    // The property that matters most: not merely off-screen, but genuinely
    // gone from every place a later screen could read it.
    const user = userEvent.setup()
    stubClipboard()

    signIn()
    const client = makeTestQueryClient()
    renderSites('/sites', client)

    await user.click(await screen.findByRole('button', { name: 'Add a site' }))
    await user.type(screen.getByLabelText(/Site name/), 'Ephemeral Depot')
    await user.click(screen.getByRole('button', { name: 'Create site' }))

    const key = (screen.getByLabelText('Provisioning key') as HTMLInputElement).value
    await user.click(screen.getByLabelText('I have stored this key somewhere safe'))
    await user.click(screen.getByRole('button', { name: 'Done' }))
    await waitFor(() => expect(screen.queryByLabelText('Provisioning key')).not.toBeInTheDocument())

    // Not in the DOM.
    expect(document.body.textContent).not.toContain(key)
    // Not in either storage.
    expect(JSON.stringify(window.localStorage)).not.toContain(key)
    expect(JSON.stringify(window.sessionStorage)).not.toContain(key)
    // Not in the URL.
    expect(window.location.href).not.toContain(key)
    // And NOT IN THE QUERY CACHE, which is the one that would otherwise
    // outlive the panel and be readable from any component.
    expect(JSON.stringify(client.getQueryData(keys.sites.list('')) ?? {})).not.toContain(key)
    for (const entry of client.getQueryCache().getAll()) {
      expect(JSON.stringify(entry.state.data ?? null)).not.toContain(key)
    }
  })

  it('reports a duplicate name as a conflict the operator can act on', async () => {
    const user = userEvent.setup()
    signIn()
    renderSites()

    await user.click(await screen.findByRole('button', { name: 'Add a site' }))
    await user.type(screen.getByLabelText(/Site name/), SITE_A.site_name)
    await user.click(screen.getByRole('button', { name: 'Create site' }))

    expect(
      await screen.findByText('A site with that name already exists in your company.'),
    ).toBeInTheDocument()
    // The dialog stays open with the values intact so the name can be corrected.
    expect(screen.getByLabelText(/Site name/)).toHaveValue(SITE_A.site_name)
  })

  it('surfaces a server failure without claiming success', async () => {
    const user = userEvent.setup()
    signIn()
    failNext('create-site', 500)
    renderSites()

    await user.click(await screen.findByRole('button', { name: 'Add a site' }))
    await user.type(screen.getByLabelText(/Site name/), 'Doomed Depot')
    await user.click(screen.getByRole('button', { name: 'Create site' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/Failed to create site/)
    expect(screen.queryByLabelText('Provisioning key')).not.toBeInTheDocument()
  })

  it('validates before asking the server', async () => {
    const user = userEvent.setup()
    signIn()
    renderSites()

    await user.click(await screen.findByRole('button', { name: 'Add a site' }))
    await user.click(screen.getByRole('button', { name: 'Create site' }))

    expect(await screen.findByText(/Site name is required/)).toBeInTheDocument()
    expect(state.requests.some((r) => r.method === 'POST')).toBe(false)
  })
})

// ---------------------------------------------------------------------------
// Editing
// ---------------------------------------------------------------------------

describe('editing a site', () => {
  it('saves metadata and refreshes what is on screen', async () => {
    const user = userEvent.setup()
    signIn()
    renderSites(`/sites/${SITE_A.site_id}`)

    await user.click(await screen.findByRole('button', { name: 'Edit' }))
    const name = screen.getByLabelText(/Site name/)
    await user.clear(name)
    await user.type(name, 'Lagos Main Depot')
    await user.click(screen.getByRole('button', { name: 'Save changes' }))

    // Stale data must not survive a mutation.
    expect(
      await screen.findByRole('heading', { name: 'Lagos Main Depot', level: 1 }),
    ).toBeInTheDocument()
  })

  it('creating and editing never expose a key field', async () => {
    const user = userEvent.setup()
    signIn()
    renderSites(`/sites/${SITE_A.site_id}`)

    await user.click(await screen.findByRole('button', { name: 'Edit' }))
    expect(screen.queryByLabelText(/API key/i)).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Deactivation vs retirement
// ---------------------------------------------------------------------------

describe('deactivation and retirement are different actions', () => {
  it('deactivation is reversible and says so', async () => {
    const user = userEvent.setup()
    signIn()
    renderSites(`/sites/${SITE_A.site_id}`)

    await user.click(await screen.findByRole('button', { name: 'Deactivate' }))

    expect(screen.getByText(/stop working immediately/)).toBeInTheDocument()
    expect(screen.getByText(/This is reversible/)).toBeInTheDocument()
    // No typed confirmation: this is not the irreversible one.
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Deactivate site' }))
    expect(await screen.findByText('This site is deactivated')).toBeInTheDocument()
  })

  it('retirement states the terminal count BEFORE it happens', async () => {
    const user = userEvent.setup()
    signIn()
    renderSites(`/sites/${SITE_A.site_id}`)

    await user.click(await screen.findByRole('button', { name: 'Retire' }))

    // Scoped to the dialog: the offline-policy panel behind it also counts the
    // site's terminals, and an unscoped match would pass on the wrong element.
    const confirm = within(screen.getByRole('dialog'))
    // Site A has two terminals in the fixture.
    expect(confirm.getByText(/2 terminals/)).toBeInTheDocument()
    expect(confirm.getByText(/cannot be undone/)).toBeInTheDocument()
    expect(confirm.getByText(/deactivate it instead/)).toBeInTheDocument()
  })

  /*
    NEITHER DIALOG NAMES A DOOR.

    These two sentences are the most consequential copy in the console -- they
    are what somebody reads immediately before stopping every terminal at a
    location -- and both used to describe the effect as doors not opening. A
    school, a depot or a residential block reading that is being told what the
    product is for, at the worst possible moment. What actually stops is people
    getting in, which is true wherever the hardware is mounted.
  */
  it('describes both consequences without naming a door', async () => {
    const user = userEvent.setup()
    signIn()
    renderSites(`/sites/${SITE_A.site_id}`)

    await user.click(await screen.findByRole('button', { name: 'Deactivate' }))
    const deactivate = screen.getByRole('dialog')
    expect(
      within(deactivate).getByText(/Nobody will get in by credential/),
    ).toBeInTheDocument()
    expectNoDoorWording('The deactivate-site dialog', deactivate.textContent ?? '')
    await user.click(within(deactivate).getByRole('button', { name: 'Cancel' }))

    await user.click(screen.getByRole('button', { name: 'Retire' }))
    const retire = screen.getByRole('dialog')
    expect(
      within(retire).getByText(/stops letting anybody in immediately/),
    ).toBeInTheDocument()
    expectNoDoorWording('The retire-site dialog', retire.textContent ?? '')
  })

  it('retirement requires typing the site name', async () => {
    const user = userEvent.setup()
    signIn()
    renderSites(`/sites/${SITE_A.site_id}`)

    await user.click(await screen.findByRole('button', { name: 'Retire' }))
    const confirm = screen.getByRole('button', { name: 'Retire site' })
    expect(confirm).toBeDisabled()

    await user.type(screen.getByRole('textbox'), 'wrong name')
    expect(confirm).toBeDisabled()

    await user.clear(screen.getByRole('textbox'))
    await user.type(screen.getByRole('textbox'), SITE_A.site_name)
    expect(confirm).toBeEnabled()
  })

  it('retires the site, reports how many terminals went with it, and leaves the page', async () => {
    const user = userEvent.setup()
    signIn()
    renderSites(`/sites/${SITE_A.site_id}`)

    await user.click(await screen.findByRole('button', { name: 'Retire' }))
    await user.type(screen.getByRole('textbox'), SITE_A.site_name)
    await user.click(screen.getByRole('button', { name: 'Retire site' }))

    // Back on the list, and the site is gone from it.
    await waitFor(() =>
      expect(screen.queryByRole('link', { name: SITE_A.site_name })).not.toBeInTheDocument(),
    )
    expect(
      await screen.findByText(/retired, along with 2 terminals/),
    ).toBeInTheDocument()
  })

  it('keeps the dialog open when retirement fails', async () => {
    const user = userEvent.setup()
    signIn()
    failNext('retire-site', 500)
    renderSites(`/sites/${SITE_A.site_id}`)

    await user.click(await screen.findByRole('button', { name: 'Retire' }))
    await user.type(screen.getByRole('textbox'), SITE_A.site_name)
    await user.click(screen.getByRole('button', { name: 'Retire site' }))

    expect(await screen.findByText(/Failed to retire site/)).toBeInTheDocument()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Key rotation
// ---------------------------------------------------------------------------

describe('rotating the provisioning key', () => {
  it('warns that the old key dies immediately, then shows the new one once', async () => {
    const user = userEvent.setup()
    stubClipboard()

    signIn()
    seed({
      sites: SITES,
      // Two terminals still on the site key, as the mock models it.
      terminals: [
        makeTerminal({ serial_number: 'AT-0001', site_public_id: SITE_A.site_id, status: 'PROVISIONING' }),
        makeTerminal({ id: 2, serial_number: 'AT-0002', site_public_id: SITE_A.site_id, status: 'ONLINE' }),
      ],
    })
    renderSites(`/sites/${SITE_A.site_id}`)

    await user.click(await screen.findByRole('button', { name: 'Rotate provisioning key' }))
    expect(screen.getByText(/stops working/)).toBeInTheDocument()
    expect(screen.getByText(/no overlap period/i)).toBeInTheDocument()

    // Scoped to the dialog: the page action behind it carries the same label.
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: 'Rotate provisioning key' }),
    )

    // Scoped to the dialog: the provisioning-key card on the page behind it says
    // the same thing about the key it cannot show.
    const credential = within(screen.getByRole('dialog'))
    expect(
      await credential.findByText(/shown once and cannot be recovered/i),
    ).toBeInTheDocument()
    const key = screen.getByLabelText('Provisioning key') as HTMLInputElement
    expect(key.value).toMatch(/^ats_[0-9a-f]{64}$/)

    // legacy_terminals is surfaced, not swallowed.
    expect(screen.getByText(/1 terminal/)).toBeInTheDocument()
    expect(screen.getByText(/registered again with the new key/)).toBeInTheDocument()

    await user.click(screen.getByLabelText('I have stored this key somewhere safe'))
    await user.click(screen.getByRole('button', { name: 'Done' }))
    await waitFor(() => expect(screen.queryByLabelText('Provisioning key')).not.toBeInTheDocument())
    expect(document.body.textContent).not.toContain(key.value)
  })

  it('says plainly when no terminal was affected', async () => {
    const user = userEvent.setup()
    stubClipboard()

    signIn()
    seed({ sites: SITES, terminals: [] })
    renderSites(`/sites/${SITE_A.site_id}`)

    await user.click(await screen.findByRole('button', { name: 'Rotate provisioning key' }))
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: 'Rotate provisioning key' }),
    )

    expect(
      await screen.findByText(/No terminal at this site depends on the provisioning key/),
    ).toBeInTheDocument()
  })

  it('does not show a key when rotation fails', async () => {
    const user = userEvent.setup()
    signIn()
    failNext('rotate-key', 500)
    renderSites(`/sites/${SITE_A.site_id}`)

    await user.click(await screen.findByRole('button', { name: 'Rotate provisioning key' }))
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: 'Rotate provisioning key' }),
    )

    expect(await screen.findByText(/Failed to rotate the site key/)).toBeInTheDocument()
    expect(screen.queryByLabelText('Provisioning key')).not.toBeInTheDocument()
  })
})
