import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { getCsrfToken, setCsrfToken } from '../api/csrf'
import { makePlatformCompany, makePlatformSession, makeSession } from '../test/fixtures'
import { makeTestQueryClient, renderWithQuery } from '../test/render'
import { failNext, resetServerState, seed, state } from '../test/server'
import { CompaniesPage } from './CompaniesPage'
import { CompanyDetailPage } from './CompanyDetailPage'
import { deriveSlug } from './CompanyDialogs'
import { PlatformLoginPage } from './PlatformLoginPage'
import { PlatformSessionProvider } from './PlatformSessionProvider'
import { PlatformShell } from './PlatformShell'
import type { PlatformCompany } from './types'

/**
 * Platform administration (GP-01, CON-01).
 *
 * No API created a company: `companies` had one writer, a migration, so
 * onboarding a customer meant SQL against production and there was no way to
 * rename one, suspend one or set its retention policy either.
 *
 * TWO THINGS ARE WORTH MORE THAN THE REST HERE.
 *
 *   THE TWO IDENTITIES STAY APART. An operator session must not authenticate a
 *   platform route, the CSRF tokens must not be shared, and neither signing out
 *   affects the other. Both cookies are Path=/ in the real deployment, so the
 *   browser genuinely offers each to the other's routes — nothing about the URL
 *   keeps them separate, only the code does.
 *
 *   THE SURFACE CANNOT REACH INSIDE A TENANT. Counts and nothing else. A support
 *   credential able to read every customer's roster would be the most valuable
 *   secret on the installation, and a test that pins the absence is worth having
 *   because the addition that breaks it will look helpful.
 */

const COMPANIES: PlatformCompany[] = [
  makePlatformCompany({
    id: 'company-1',
    name: 'Northwind Logistics',
    slug: 'northwind',
    operator_count: 3,
  }),
  makePlatformCompany({
    id: 'company-2',
    name: 'Meridian Schools',
    slug: 'meridian',
    // The state a half-finished onboarding leaves behind: nobody can sign in.
    operator_count: 0,
    site_count: 0,
    terminal_count: 0,
    person_count: 0,
  }),
  makePlatformCompany({
    id: 'company-3',
    name: 'Harbour Works',
    slug: 'harbour',
    active: false,
    operator_count: 2,
  }),
]

function signInAsPlatform(companies: PlatformCompany[] = COMPANIES) {
  const session = makePlatformSession()
  resetServerState(null)
  state.platformSession = session
  setCsrfToken(session.csrf_token, 'platform')
  seed({ companies })
  return session
}

function renderPlatform(path = '/platform') {
  const router = createMemoryRouter(
    [
      { path: '/platform/login', element: <PlatformLoginPage /> },
      {
        path: '/platform',
        element: <PlatformShell />,
        children: [
          { index: true, element: <CompaniesPage /> },
          { path: 'companies/:companyId', element: <CompanyDetailPage /> },
        ],
      },
      { path: '/login', element: <h1>Operator console</h1> },
    ],
    { initialEntries: [path] },
  )
  return renderWithQuery(
    <PlatformSessionProvider>
      <RouterProvider router={router} />
    </PlatformSessionProvider>,
    makeTestQueryClient(),
  )
}

beforeEach(() => {
  setCsrfToken(null)
  setCsrfToken(null, 'platform')
})

// ---------------------------------------------------------------------------
// The two identities
// ---------------------------------------------------------------------------

describe('platform administration is a separate identity', () => {
  it('does not accept an operator session', async () => {
    // An operator signed in to their console must not reach this surface by
    // navigating to it. The server refuses; the console must not pretend
    // otherwise on the way.
    const operator = makeSession()
    resetServerState(operator)
    setCsrfToken(operator.csrf_token)

    renderPlatform()

    expect(await screen.findByRole('heading', { name: 'AccessLink' })).toBeInTheDocument()
    expect(screen.getByText('Platform administration')).toBeInTheDocument()
    // Sent to the platform login, not into the companies list.
    expect(screen.queryByRole('heading', { name: 'Companies' })).not.toBeInTheDocument()
  })

  it('keeps the two CSRF tokens apart', async () => {
    // A single shared variable would mean whichever identity signed in last
    // supplied the token for the other's requests, and the failure would be a
    // 403 nobody could explain.
    const operator = makeSession()
    resetServerState(operator)
    setCsrfToken(operator.csrf_token)
    const platformSession = makePlatformSession()
    state.platformSession = platformSession
    setCsrfToken(platformSession.csrf_token, 'platform')

    expect(getCsrfToken('operator')).toBe(operator.csrf_token)
    expect(getCsrfToken('platform')).toBe(platformSession.csrf_token)
    expect(getCsrfToken('operator')).not.toBe(getCsrfToken('platform'))
  })

  it('sends the PLATFORM token on a platform write', async () => {
    const user = userEvent.setup()
    signInAsPlatform([])
    renderPlatform()

    await user.click(await screen.findByRole('button', { name: 'Create a company' }))
    await user.type(screen.getByLabelText(/Company name/), 'Trailhead Events')
    await user.click(screen.getByRole('button', { name: 'Create company' }))

    await waitFor(() => {
      const write = state.requests.find(
        (entry) => entry.method === 'POST' && entry.url.includes('/platform/companies'),
      )
      expect(write?.headers.get('X-CSRF-Token')).toBe('platform-csrf-token')
    })
  })

  it('says what this surface is, so operator credentials are not tried twice', async () => {
    resetServerState(null)
    renderPlatform()

    await screen.findByRole('heading', { name: 'AccessLink' })
    expect(screen.getByText(/It is not the operator console/i)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /the console/i })).toHaveAttribute('href', '/login')
  })
})

// ---------------------------------------------------------------------------
// The companies list
// ---------------------------------------------------------------------------

describe('the companies list', () => {
  it('lists customers with their size, not their contents', async () => {
    signInAsPlatform()
    renderPlatform()

    const row = (await screen.findByText('Northwind Logistics')).closest('tr') as HTMLElement
    expect(within(row).getByText('northwind')).toBeInTheDocument()
    expect(within(row).getByText('Active')).toBeInTheDocument()
  })

  it('SURFACES A COMPANY NOBODY CAN SIGN IN TO', async () => {
    // A company with no operator looks fine in a list and is completely
    // unusable. It is the state a half-finished onboarding leaves behind, and
    // the whole reason this is a column rather than something to remember.
    signInAsPlatform()
    renderPlatform()

    // Named twice on purpose — once in the table and once in the banner above
    // it — so the row is found by its cell rather than by the first match.
    const cells = await screen.findAllByText('Meridian Schools')
    const row = cells.map((node) => node.closest('tr')).find(Boolean) as HTMLElement
    expect(within(row).getByText('No operator yet')).toBeInTheDocument()
    expect(screen.getByText('Onboarding not finished')).toBeInTheDocument()
  })

  it('marks a suspended customer', async () => {
    signInAsPlatform()
    renderPlatform()

    const row = (await screen.findByText('Harbour Works')).closest('tr') as HTMLElement
    expect(within(row).getByText('Suspended')).toBeInTheDocument()
  })

  it('EXPOSES NOTHING FROM INSIDE A TENANT', async () => {
    // Counts, never contents. The addition that breaks this will look helpful.
    signInAsPlatform()
    renderPlatform()

    await screen.findByText('Northwind Logistics')
    const text = (document.body.textContent ?? '').toLowerCase()
    for (const forbidden of ['api_key', 'ats_', 'atd_', 'password', 'template', 'external_id']) {
      expect(text).not.toContain(forbidden)
    }
    expect(screen.getByText(/cannot open their people/i)).toBeInTheDocument()
  })

  it('reports a failed load as an error rather than as no customers', async () => {
    signInAsPlatform()
    failNext('platform-companies', 500)
    renderPlatform()

    expect(await screen.findByRole('alert')).toHaveTextContent(/Failed to retrieve companies/)
  })

  it('offers the first company from an empty state rather than a blank page', async () => {
    signInAsPlatform([])
    renderPlatform()

    expect(await screen.findByText('No companies yet')).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: 'Create a company' }).length).toBeGreaterThan(0)
  })
})

// ---------------------------------------------------------------------------
// Creating a company
// ---------------------------------------------------------------------------

describe('creating a company', () => {
  it('derives a storable slug from a name a person would actually type', () => {
    // Refusing "Acme Logistics (UK)" to enforce a format the platform invented
    // is less useful than deriving one.
    expect(deriveSlug('Acme Logistics (UK)')).toBe('acme-logistics-uk')
    expect(deriveSlug('  Trailhead   Events  ')).toBe('trailhead-events')
  })

  it('creates it and goes straight to finishing the onboarding', async () => {
    const user = userEvent.setup()
    signInAsPlatform([])
    renderPlatform()

    await user.click(await screen.findByRole('button', { name: 'Create a company' }))
    await user.type(screen.getByLabelText(/Company name/), 'Trailhead Events')
    await user.click(screen.getByRole('button', { name: 'Create company' }))

    // Onboarding is NOT finished at creation, so the console goes where the
    // next step is rather than back to a list.
    expect(await screen.findByRole('heading', { name: 'Onboarding' })).toBeInTheDocument()
    expect(screen.getByText('Trailhead Events')).toBeInTheDocument()
  })

  it('says the company starts with nobody able to sign in', async () => {
    const user = userEvent.setup()
    signInAsPlatform([])
    renderPlatform()

    await user.click(await screen.findByRole('button', { name: 'Create a company' }))
    expect(screen.getByText('The next step is its first operator')).toBeInTheDocument()
  })

  it('reports a taken slug as something to change rather than as a failure', async () => {
    const user = userEvent.setup()
    signInAsPlatform()
    renderPlatform()

    await user.click(await screen.findByRole('button', { name: 'Create a company' }))
    await user.type(screen.getByLabelText(/Company name/), 'Northwind')
    await user.clear(screen.getByLabelText(/Slug/))
    await user.type(screen.getByLabelText(/Slug/), 'northwind')
    await user.click(screen.getByRole('button', { name: 'Create company' }))

    expect(await screen.findByText(/already exists on this installation/i)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Onboarding a customer end to end
// ---------------------------------------------------------------------------

describe('the onboarding checklist', () => {
  it('shows what is done, what is next, and what is the customer’s own work', async () => {
    signInAsPlatform()
    renderPlatform('/platform/companies/company-2')

    await screen.findByRole('heading', { name: 'Onboarding' })
    expect(screen.getByText('Company created')).toBeInTheDocument()
    expect(screen.getByText('First operator issued')).toBeInTheDocument()

    // The steps this surface cannot perform are listed rather than omitted. A
    // checklist that stopped at the boundary would leave whoever follows it
    // believing the job was finished.
    expect(screen.getByText('Sites created')).toBeInTheDocument()
    expect(screen.getByText('Terminals provisioned')).toBeInTheDocument()
    expect(screen.getByText('People and applications')).toBeInTheDocument()
    // A brand-new company has none of them, and each says so rather than
    // showing an empty cell that reads like a rendering failure.
    expect(screen.getAllByText('Not started').length).toBe(3)
    expect(screen.getByText(/neither console can do it on the device/i)).toBeInTheDocument()
  })

  it('ISSUES THE FIRST OWNER AS AN INVITATION, never as a password', async () => {
    // Across a company boundary, a vendor choosing a customer's owner password
    // is not a defensible fallback — so the console does not offer it, even
    // though the API would accept one.
    const user = userEvent.setup()
    signInAsPlatform()
    renderPlatform('/platform/companies/company-2')

    await user.click(await screen.findByRole('button', { name: 'Issue an invitation' }))
    expect(screen.queryByLabelText(/password/i)).not.toBeInTheDocument()
    expect(screen.getByText('You will never know their password')).toBeInTheDocument()

    await user.type(screen.getByLabelText(/Email/), 'owner@meridian.example')
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: 'Issue an invitation' }),
    )

    expect(await screen.findByText(/shown once and cannot be recovered/i)).toBeInTheDocument()
  })

  it('says this is the only account it can ever create for them', async () => {
    const user = userEvent.setup()
    signInAsPlatform()
    renderPlatform('/platform/companies/company-2')

    await user.click(await screen.findByRole('button', { name: 'Issue an invitation' }))
    expect(
      screen.getByText('This is the only account you can create for them'),
    ).toBeInTheDocument()
  })

  it('stops offering the action once the company has an operator', async () => {
    signInAsPlatform()
    renderPlatform('/platform/companies/company-1')

    await screen.findByRole('heading', { name: 'Onboarding' })
    // The server refuses with 409; offering the button anyway would produce an
    // error that means nothing to whoever pressed it.
    expect(screen.queryByRole('button', { name: 'Issue an invitation' })).not.toBeInTheDocument()
    expect(screen.getByText(/this surface cannot add another/i)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Administering a company
// ---------------------------------------------------------------------------

describe('administering a company', () => {
  it('renders NULL retention as indefinite rather than as a number nobody chose', async () => {
    signInAsPlatform()
    renderPlatform('/platform/companies/company-1')

    await screen.findByRole('heading', { name: 'Details' })
    const rows = screen.getAllByText('Indefinite')
    expect(rows.length).toBe(2)
  })

  it('refuses to offer a slug change, and says why', async () => {
    const user = userEvent.setup()
    signInAsPlatform()
    renderPlatform('/platform/companies/company-1')

    await user.click(await screen.findByRole('button', { name: 'Edit details' }))
    expect(screen.queryByLabelText(/^Slug/)).not.toBeInTheDocument()
    expect(screen.getByText('The slug cannot be changed')).toBeInTheDocument()
  })

  it('SUSPENDING STATES THAT IT SIGNS THE WHOLE CUSTOMER OUT', async () => {
    const user = userEvent.setup()
    signInAsPlatform()
    renderPlatform('/platform/companies/company-1')

    await user.click(await screen.findByRole('button', { name: 'Suspend' }))
    const dialog = screen.getByRole('dialog')
    expect(within(dialog).getByText(/signed out/i)).toBeInTheDocument()
    expect(within(dialog).getByText(/nothing is deleted/i)).toBeInTheDocument()

    // Typing the slug: this locks a paying customer out of their own console.
    const confirm = within(dialog).getByRole('button', { name: 'Suspend company' })
    expect(confirm).toBeDisabled()
    await user.type(within(dialog).getByLabelText(/type .* to confirm/i), 'northwind')
    expect(confirm).toBeEnabled()

    await user.click(confirm)
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(await screen.findByText('This company is suspended')).toBeInTheDocument()
  })

  it('offers restore rather than suspend for a suspended customer', async () => {
    signInAsPlatform()
    renderPlatform('/platform/companies/company-3')

    expect(await screen.findByRole('button', { name: 'Restore' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Suspend' })).not.toBeInTheDocument()
  })

  it('offers no way to delete a customer at all', async () => {
    signInAsPlatform()
    renderPlatform('/platform/companies/company-1')

    await screen.findByRole('heading', { name: 'Details' })
    expect(screen.queryByRole('button', { name: /delete|remove/i })).not.toBeInTheDocument()
  })
})
