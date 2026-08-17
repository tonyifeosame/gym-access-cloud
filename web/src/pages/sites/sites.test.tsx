import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { setCsrfToken } from '../../api/csrf'
import type { Role, Session } from '../../api/types'
import { keys } from '../../data/keys'
import { makeSession, makeSite, makeTerminal, SITE_A, SITE_B } from '../../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../../test/render'
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
    for (const label of ['Edit', 'Rotate key', 'Deactivate', 'Retire']) {
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

  it('never shows more than the key PREFIX', async () => {
    signIn()
    renderSites(`/sites/${SITE_A.site_id}`)

    await screen.findByRole('heading', { name: SITE_A.site_name, level: 1 })
    expect(screen.getByText('Provisioning key')).toBeInTheDocument()
    // TWO SEPARATE FACTS, and the card now keeps them apart. The KEY is
    // unrecoverable by design; the non-secret PREFIX is simply not returned by
    // any read endpoint, which is a gap in the API rather than a decision. The
    // card used to run them together as "not shown", which implied the prefix
    // was being withheld on purpose.
    expect(screen.getByText(/shown once and cannot be recovered/i)).toBeInTheDocument()
    expect(screen.getByText(/not returned by any read endpoint/i)).toBeInTheDocument()
    // No GET populates a full key, and nothing on the page requests one.
    expect(document.body.textContent).not.toMatch(/ats_[0-9a-f]{64}/)
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

    const key = screen.getByLabelText('Site API key') as HTMLInputElement
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

    const key = (screen.getByLabelText('Site API key') as HTMLInputElement).value
    await user.click(screen.getByLabelText('I have stored this key somewhere safe'))
    await user.click(screen.getByRole('button', { name: 'Done' }))
    await waitFor(() => expect(screen.queryByLabelText('Site API key')).not.toBeInTheDocument())

    // Not in the DOM.
    expect(document.body.textContent).not.toContain(key)
    // Not in either storage.
    expect(JSON.stringify(window.localStorage)).not.toContain(key)
    expect(JSON.stringify(window.sessionStorage)).not.toContain(key)
    // Not in the URL.
    expect(window.location.href).not.toContain(key)
    // And NOT IN THE QUERY CACHE, which is the one that would otherwise
    // outlive the panel and be readable from any component.
    expect(JSON.stringify(client.getQueryData(keys.sites.list()) ?? {})).not.toContain(key)
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
    expect(screen.queryByLabelText('Site API key')).not.toBeInTheDocument()
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

    await user.click(await screen.findByRole('button', { name: 'Rotate key' }))
    expect(screen.getByText(/stops working/)).toBeInTheDocument()
    expect(screen.getByText(/no overlap period/i)).toBeInTheDocument()

    // Scoped to the dialog: the page action behind it carries the same label.
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: 'Rotate key' }),
    )

    // Scoped to the dialog: the provisioning-key card on the page behind it says
    // the same thing about the key it cannot show.
    const credential = within(screen.getByRole('dialog'))
    expect(
      await credential.findByText(/shown once and cannot be recovered/i),
    ).toBeInTheDocument()
    const key = screen.getByLabelText('Site API key') as HTMLInputElement
    expect(key.value).toMatch(/^ats_[0-9a-f]{64}$/)

    // legacy_terminals is surfaced, not swallowed.
    expect(screen.getByText(/1 terminal/)).toBeInTheDocument()
    expect(screen.getByText(/re-provisioning/)).toBeInTheDocument()

    await user.click(screen.getByLabelText('I have stored this key somewhere safe'))
    await user.click(screen.getByRole('button', { name: 'Done' }))
    await waitFor(() => expect(screen.queryByLabelText('Site API key')).not.toBeInTheDocument())
    expect(document.body.textContent).not.toContain(key.value)
  })

  it('says plainly when no terminal was affected', async () => {
    const user = userEvent.setup()
    stubClipboard()

    signIn()
    seed({ sites: SITES, terminals: [] })
    renderSites(`/sites/${SITE_A.site_id}`)

    await user.click(await screen.findByRole('button', { name: 'Rotate key' }))
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: 'Rotate key' }),
    )

    expect(await screen.findByText(/No terminal at this site depends on the site key/)).toBeInTheDocument()
  })

  it('does not show a key when rotation fails', async () => {
    const user = userEvent.setup()
    signIn()
    failNext('rotate-key', 500)
    renderSites(`/sites/${SITE_A.site_id}`)

    await user.click(await screen.findByRole('button', { name: 'Rotate key' }))
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: 'Rotate key' }),
    )

    expect(await screen.findByText(/Failed to rotate the site key/)).toBeInTheDocument()
    expect(screen.queryByLabelText('Site API key')).not.toBeInTheDocument()
  })
})
