import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../../api/csrf'
import type { OperatorAccount, Role, Session } from '../../api/types'
import { makeOperatorAccount, makeSession, makeSite, SITE_A, SITE_B } from '../../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../../test/render'
import { failNext, resetServerState, seed, state } from '../../test/server'
import { OperatorDetailPage } from './OperatorDetailPage'
import { OperatorsListPage } from './OperatorsListPage'

/**
 * The Operators module.
 *
 * The rules being protected here are the ones that keep a company from locking
 * itself out or quietly escalating privilege: who may manage whom, what an
 * operator may not do to their own account, and the fact that an EMPTY set of
 * site restrictions means every site rather than none.
 */

const SITES = [
  makeSite({ id: SITE_A.site_id, name: SITE_A.site_name }),
  makeSite({ id: SITE_B.site_id, name: SITE_B.site_name }),
]

const SELF_ID = 'operator-1'

const ROSTER: OperatorAccount[] = [
  makeOperatorAccount({
    id: SELF_ID,
    email: 'ops@example.com',
    full_name: 'Ops Person',
    role: 'ADMIN',
    all_sites: true,
    sites: [],
  }),
  makeOperatorAccount({
    id: 'operator-viewer',
    email: 'viewer@example.com',
    full_name: 'Sam Viewer',
    role: 'VIEWER',
    all_sites: false,
    sites: [SITE_A],
  }),
  makeOperatorAccount({
    id: 'operator-unscoped',
    email: 'manager@example.com',
    full_name: 'Kemi Manager',
    role: 'MANAGER',
    all_sites: true,
    sites: [],
  }),
  makeOperatorAccount({
    id: 'operator-owner',
    email: 'owner@example.com',
    full_name: 'Tobi Owner',
    role: 'OWNER',
    all_sites: true,
    sites: [],
  }),
]

function signIn(role: Role = 'ADMIN', overrides: Partial<Session> = {}) {
  const session = makeSession({
    role,
    operator: { id: SELF_ID, email: 'ops@example.com', full_name: 'Ops Person', role },
    ...overrides,
  })
  resetServerState(session)
  setCsrfToken(session.csrf_token)
  seed({
    sites: SITES,
    operators: ROSTER.map((operator) =>
      operator.id === SELF_ID ? { ...operator, role } : operator,
    ),
  })
  return session
}

function renderOperators(initialPath = '/operators', client = makeTestQueryClient()) {
  const router = createMemoryRouter(
    [
      { path: '/operators', element: <OperatorsListPage /> },
      { path: '/operators/:operatorId', element: <OperatorDetailPage /> },
      { path: '/sites/:siteId', element: <p>Site page</p> },
      { path: '/people', element: <p>People page</p> },
    ],
    { initialEntries: [initialPath] },
  )
  return renderWithSession(<RouterProvider router={router} />, client)
}

beforeEach(() => setCsrfToken(null))

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

describe('operator list', () => {
  it('lists accounts with role, status and site access', async () => {
    signIn()
    renderOperators()

    const viewer = (await screen.findByText('viewer@example.com')).closest('tr') as HTMLElement
    expect(within(viewer).getByText('Viewer')).toBeInTheDocument()
    expect(within(viewer).getByText('Active')).toBeInTheDocument()
    expect(within(viewer).getByText('1 site')).toBeInTheDocument()
  })

  it('renders an EMPTY grant set as "All sites", never as none', async () => {
    // The one state in this product whose two readings are exact opposites.
    signIn()
    renderOperators()

    const manager = (await screen.findByText('manager@example.com')).closest('tr') as HTMLElement
    expect(within(manager).getByText('All sites')).toBeInTheDocument()
    expect(within(manager).queryByText('0')).not.toBeInTheDocument()
  })

  it('distinguishes "all sites by role" from "all sites by default"', async () => {
    signIn()
    renderOperators()

    const owner = (await screen.findByText('owner@example.com')).closest('tr') as HTMLElement
    expect(within(owner).getByText('All sites (by role)')).toBeInTheDocument()
  })

  it('marks the signed-in operator’s own row', async () => {
    signIn()
    renderOperators()

    const self = (await screen.findByText('ops@example.com')).closest('tr') as HTMLElement
    expect(within(self).getByText('You')).toBeInTheDocument()
  })

  it('separates operators from the people terminals recognise', async () => {
    signIn()
    renderOperators()

    await screen.findByText('ops@example.com')
    expect(screen.getByText(/Operators administer AccessLink/)).toBeInTheDocument()
  })

  it('reports a failed load as an error rather than an empty console', async () => {
    signIn()
    failNext('operators-list', 500)
    renderOperators()

    expect(await screen.findByRole('alert')).toHaveTextContent(/Failed to retrieve operators/)
  })
})

// ---------------------------------------------------------------------------
// Self-protection
// ---------------------------------------------------------------------------

describe('an operator cannot escalate or lock out via their own account', () => {
  it('offers no role change, deactivation or removal on your own account', async () => {
    // Not a courtesy: the sole OWNER demoting themselves would leave nobody able
    // to manage operators and no way back that does not involve the database.
    signIn('OWNER')
    renderOperators(`/operators/${SELF_ID}`)

    await screen.findByRole('heading', { name: 'Ops Person', level: 1 })
    expect(screen.queryByRole('button', { name: 'Change role' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Deactivate' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Remove' })).not.toBeInTheDocument()
  })

  it('explains why, rather than leaving a gap', async () => {
    signIn('OWNER')
    renderOperators(`/operators/${SELF_ID}`)

    expect(await screen.findByText('This is your own account')).toBeInTheDocument()
    expect(screen.getByText(/stops the last administrator locking everybody out/)).toBeInTheDocument()
  })

  it('still allows site access and password changes on your own account', async () => {
    // Neither can lock a company out, and both are legitimate self-service.
    signIn('OWNER')
    renderOperators(`/operators/${SELF_ID}`)

    expect(await screen.findByRole('button', { name: 'Site access' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Reset password' })).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Role hierarchy
// ---------------------------------------------------------------------------

describe('role hierarchy', () => {
  it('stops an ADMIN touching an OWNER at all', async () => {
    // Otherwise ADMIN is a synonym for OWNER one request later.
    signIn('ADMIN')
    renderOperators('/operators/operator-owner')

    await screen.findByRole('heading', { name: 'Tobi Owner', level: 1 })
    expect(screen.getByText('You cannot change this account')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Change role' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Remove' })).not.toBeInTheDocument()
  })

  it('lets an OWNER manage another OWNER', async () => {
    signIn('OWNER')
    renderOperators('/operators/operator-owner')

    expect(await screen.findByRole('button', { name: 'Change role' })).toBeInTheDocument()
  })

  it('offers an ADMIN no OWNER option when creating', async () => {
    const user = userEvent.setup()
    signIn('ADMIN')
    renderOperators()

    await user.click(await screen.findByRole('button', { name: 'Add an operator' }))
    const select = screen.getByLabelText(/Role/)
    const values = within(select).getAllByRole('option').map((o) => (o as HTMLOptionElement).value)

    expect(values).toEqual(['VIEWER', 'MANAGER', 'ADMIN'])
    expect(values).not.toContain('OWNER')
  })

  it('offers an OWNER every role', async () => {
    const user = userEvent.setup()
    signIn('OWNER')
    renderOperators()

    await user.click(await screen.findByRole('button', { name: 'Add an operator' }))
    const select = screen.getByLabelText(/Role/)
    const values = within(select).getAllByRole('option').map((o) => (o as HTMLOptionElement).value)
    expect(values).toContain('OWNER')
  })
})

// ---------------------------------------------------------------------------
// Creating
// ---------------------------------------------------------------------------

/**
 * Switches the form to the "I will choose the password" path.
 *
 * The DEFAULT is now an invitation (PPL-02), so every test that is about the
 * password field has to ask for it. That the helper is needed at all is the
 * point: choosing a password somebody else will know is no longer what happens
 * by pressing through the form.
 */
async function chooseDirectPassword(user: ReturnType<typeof userEvent.setup>) {
  await user.selectOptions(screen.getByLabelText(/How they get their password/), 'PASSWORD')
}

describe('creating an operator', () => {
  it('creates one and refreshes the list', async () => {
    const user = userEvent.setup()
    signIn()
    renderOperators()

    await user.click(await screen.findByRole('button', { name: 'Add an operator' }))
    await user.type(screen.getByLabelText(/Email/), 'new@example.com')
    await user.type(screen.getByLabelText(/Full name/), 'New Operator')
    await chooseDirectPassword(user)
    await user.type(screen.getByLabelText(/Initial password/), 'a-long-enough-password')
    await user.click(screen.getByRole('button', { name: 'Create operator' }))

    expect(await screen.findByText('new@example.com')).toBeInTheDocument()
  })

  it('enforces the platform’s minimum password length before asking the server', async () => {
    const user = userEvent.setup()
    signIn()
    renderOperators()

    await user.click(await screen.findByRole('button', { name: 'Add an operator' }))
    await user.type(screen.getByLabelText(/Email/), 'short@example.com')
    await user.type(screen.getByLabelText(/Full name/), 'Short Password')
    await chooseDirectPassword(user)
    await user.type(screen.getByLabelText(/Initial password/), 'tooshort')

    const before = state.requests.filter((r) => r.method === 'POST').length
    await user.click(screen.getByRole('button', { name: 'Create operator' }))

    expect(await screen.findByText(/at least 12 characters/)).toBeInTheDocument()
    expect(state.requests.filter((r) => r.method === 'POST')).toHaveLength(before)
  })

  it('DEFAULTS TO AN INVITATION, so the creator never learns the password', async () => {
    // The whole of PPL-02 at the point it is decided. Before the handover
    // mechanism existed, choosing somebody else's password was the only path,
    // and the realistic consequence was an administrator who knew it
    // indefinitely with nothing recording that they did.
    const user = userEvent.setup()
    signIn()
    renderOperators()

    await user.click(await screen.findByRole('button', { name: 'Add an operator' }))

    // No password field at all until the other path is deliberately chosen.
    expect(screen.queryByLabelText(/Initial password/)).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Create and invite' })).toBeInTheDocument()
  })

  it('issues a one-time link and refuses to close until it is acknowledged', async () => {
    const user = userEvent.setup()
    signIn()
    renderOperators()

    await user.click(await screen.findByRole('button', { name: 'Add an operator' }))
    await user.type(screen.getByLabelText(/Email/), 'invited@example.com')
    await user.type(screen.getByLabelText(/Full name/), 'Invited Operator')
    await user.click(screen.getByRole('button', { name: 'Create and invite' }))

    // The dialog STAYS OPEN on success. Closing would discard a credential that
    // cannot be read back, turning a successful action into one the operator has
    // to repeat.
    expect(await screen.findByText(/shown once and cannot be recovered/i)).toBeInTheDocument()

    const done = screen.getByRole('button', { name: 'Done' })
    expect(done).toBeDisabled()

    await user.click(screen.getByLabelText(/I have copied this link/i))
    expect(done).toBeEnabled()
  })

  it('says plainly that the platform does not deliver the link', async () => {
    // There is no transactional email. Somebody has to send it, and doing that
    // carelessly — or not at all — is the realistic failure.
    const user = userEvent.setup()
    signIn()
    renderOperators()

    await user.click(await screen.findByRole('button', { name: 'Add an operator' }))
    expect(screen.getByText(/AccessLink does not have email/i)).toBeInTheDocument()
  })

  it('warns that a chosen password must be handed over out of band', async () => {
    const user = userEvent.setup()
    signIn()
    renderOperators()

    await user.click(await screen.findByRole('button', { name: 'Add an operator' }))
    await chooseDirectPassword(user)
    expect(screen.getByText(/cannot show it to you again/i)).toBeInTheDocument()
  })

  it('WARNS THAT AN EMPTY SITE SELECTION GRANTS EVERY SITE', async () => {
    const user = userEvent.setup()
    signIn()
    renderOperators()

    await user.click(await screen.findByRole('button', { name: 'Add an operator' }))
    // Default role is VIEWER, which is scoped by grants.
    expect(await screen.findByText('This operator will reach every site')).toBeInTheDocument()

    await user.click(screen.getByLabelText(SITE_A.site_name))
    await waitFor(() =>
      expect(screen.queryByText('This operator will reach every site')).not.toBeInTheDocument(),
    )
  })

  it('says grants are not used for a role that ignores them', async () => {
    const user = userEvent.setup()
    signIn()
    renderOperators()

    await user.click(await screen.findByRole('button', { name: 'Add an operator' }))
    await user.selectOptions(screen.getByLabelText(/Role/), 'ADMIN')

    expect(screen.getByText(/Not used for this role/)).toBeInTheDocument()
    expect(screen.getByLabelText(SITE_A.site_name)).toBeDisabled()
  })

  it('reports a duplicate email as an actionable conflict', async () => {
    const user = userEvent.setup()
    signIn()
    renderOperators()

    await user.click(await screen.findByRole('button', { name: 'Add an operator' }))
    await user.type(screen.getByLabelText(/Email/), 'viewer@example.com')
    await user.type(screen.getByLabelText(/Full name/), 'Duplicate')
    await chooseDirectPassword(user)
    await user.type(screen.getByLabelText(/Initial password/), 'a-long-enough-password')
    await user.click(screen.getByRole('button', { name: 'Create operator' }))

    expect(
      await screen.findByText('That email address is already in use in your company.'),
    ).toBeInTheDocument()
  })

  it('validates the email format client-side', async () => {
    const user = userEvent.setup()
    signIn()
    renderOperators()

    await user.click(await screen.findByRole('button', { name: 'Add an operator' }))
    await user.type(screen.getByLabelText(/Email/), 'not-an-email')
    await user.type(screen.getByLabelText(/Full name/), 'Bad Email')
    await chooseDirectPassword(user)
    await user.type(screen.getByLabelText(/Initial password/), 'a-long-enough-password')
    await user.click(screen.getByRole('button', { name: 'Create operator' }))

    expect(await screen.findByText(/does not look like an email address/)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Site grants
// ---------------------------------------------------------------------------

describe('site access', () => {
  it('shows a restricted operator’s sites as links', async () => {
    signIn()
    renderOperators('/operators/operator-viewer')

    await screen.findByRole('heading', { name: 'Sam Viewer', level: 1 })
    expect(screen.getByText(/Restricted to 1 site/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: SITE_A.site_name })).toHaveAttribute(
      'href',
      `/sites/${SITE_A.site_id}`,
    )
  })

  it('spells out that no restrictions means every site', async () => {
    signIn()
    renderOperators('/operators/operator-unscoped')

    await screen.findByRole('heading', { name: 'Kemi Manager', level: 1 })
    expect(screen.getByText(/reaches/)).toBeInTheDocument()
    expect(screen.getByText(/That is what an empty set of site restrictions means/)).toBeInTheDocument()
  })

  it('notes that people are not narrowed by site restrictions', async () => {
    // Grants bound sites, terminals and site settings. People are company-wide.
    signIn()
    renderOperators('/operators/operator-viewer')

    await screen.findByRole('heading', { name: 'Sam Viewer', level: 1 })
    expect(screen.getByText(/People are company-wide/)).toBeInTheDocument()
  })

  it('warns in the editor that clearing the selection grants everything', async () => {
    const user = userEvent.setup()
    signIn()
    renderOperators('/operators/operator-viewer')

    await user.click(await screen.findByRole('button', { name: 'Site access' }))
    expect(screen.getByLabelText(SITE_A.site_name)).toBeChecked()

    await user.click(screen.getByLabelText(SITE_A.site_name))
    expect(await screen.findByText('This grants every site')).toBeInTheDocument()
  })

  it('saves a narrowed selection', async () => {
    const user = userEvent.setup()
    signIn()
    renderOperators('/operators/operator-viewer')

    await user.click(await screen.findByRole('button', { name: 'Site access' }))
    await user.click(screen.getByLabelText(SITE_B.site_name))
    await user.click(screen.getByRole('button', { name: 'Save site access' }))

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(await screen.findByText(/Restricted to 2 sites/)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Role, password, activation, removal
// ---------------------------------------------------------------------------

describe('changing an account', () => {
  it('warns that a role change signs the operator out', async () => {
    const user = userEvent.setup()
    signIn()
    renderOperators('/operators/operator-viewer')

    await user.click(await screen.findByRole('button', { name: 'Change role' }))
    await user.selectOptions(screen.getByLabelText(/Role/), 'MANAGER')

    expect(screen.getByText('This signs them out')).toBeInTheDocument()
    // Scoped: the page action behind the dialog carries the same label.
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: 'Change role' }),
    )

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(await screen.findByText('Manager')).toBeInTheDocument()
  })

  it('warns that promoting past grants makes them stop applying', async () => {
    // And that they are kept, and would apply again on demotion -- the latent
    // surprise recorded as MR-016.
    const user = userEvent.setup()
    signIn()
    renderOperators('/operators/operator-viewer')

    await user.click(await screen.findByRole('button', { name: 'Change role' }))
    await user.selectOptions(screen.getByLabelText(/Role/), 'ADMIN')

    expect(screen.getByText('Their site restrictions stop applying')).toBeInTheDocument()
    expect(screen.getByText(/would apply again if the account were later/)).toBeInTheDocument()
  })

  it('resets a password, warning that it signs them out everywhere', async () => {
    const user = userEvent.setup()
    signIn()
    renderOperators('/operators/operator-viewer')

    await user.click(await screen.findByRole('button', { name: 'Reset password' }))
    expect(screen.getByText('This signs them out everywhere')).toBeInTheDocument()

    const confirm = within(screen.getByRole('dialog')).getByRole('button', {
      name: 'Reset password',
    })
    await user.type(screen.getByLabelText(/New password/), 'short')
    expect(confirm).toBeDisabled()

    await user.clear(screen.getByLabelText(/New password/))
    await user.type(screen.getByLabelText(/New password/), 'a-long-enough-password')
    expect(confirm).toBeEnabled()
    await user.click(confirm)

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('deactivates reversibly and says so', async () => {
    const user = userEvent.setup()
    signIn()
    renderOperators('/operators/operator-viewer')

    await user.click(await screen.findByRole('button', { name: 'Deactivate' }))
    expect(screen.getByText(/signed out everywhere immediately/)).toBeInTheDocument()
    expect(screen.getByText(/Reversible/)).toBeInTheDocument()

    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: 'Deactivate account' }),
    )
    expect(await screen.findByText('This account is deactivated')).toBeInTheDocument()
  })

  it('requires typing the email to remove an account', async () => {
    const user = userEvent.setup()
    signIn()
    renderOperators('/operators/operator-viewer')

    await user.click(await screen.findByRole('button', { name: 'Remove' }))
    expect(screen.getByText(/deactivate the account instead/)).toBeInTheDocument()

    const confirm = screen.getByRole('button', { name: 'Remove operator' })
    expect(confirm).toBeDisabled()
    await user.type(screen.getByRole('textbox'), 'viewer@example.com')
    expect(confirm).toBeEnabled()

    await user.click(confirm)
    await waitFor(() =>
      expect(screen.queryByText('viewer@example.com')).not.toBeInTheDocument(),
    )
  })

  it('keeps the dialog open when a change fails', async () => {
    const user = userEvent.setup()
    signIn()
    failNext('update-operator', 500)
    renderOperators('/operators/operator-viewer')

    await user.click(await screen.findByRole('button', { name: 'Change role' }))
    await user.selectOptions(screen.getByLabelText(/Role/), 'MANAGER')
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: 'Change role' }),
    )

    expect(await screen.findByRole('alert')).toHaveTextContent(/Failed to update operator/)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Non-disclosure
// ---------------------------------------------------------------------------

describe('nothing secret is disclosed', () => {
  it('shows no hash, token or key on the list or the detail', async () => {
    signIn()
    renderOperators()
    await screen.findByText('ops@example.com')
    let text = document.body.textContent ?? ''
    for (const forbidden of ['password_hash', 'token_hash', 'csrf', 'api_key', 'ats_', 'atd_']) {
      expect(text.toLowerCase()).not.toContain(forbidden)
    }

    renderOperators('/operators/operator-viewer')
    await screen.findByRole('heading', { name: 'Sam Viewer', level: 1 })
    text = document.body.textContent ?? ''
    for (const forbidden of ['password_hash', 'token_hash', 'api_key']) {
      expect(text.toLowerCase()).not.toContain(forbidden)
    }
  })

  it('never echoes a password back after it has been submitted', async () => {
    const user = userEvent.setup()
    signIn()
    renderOperators()

    await user.click(await screen.findByRole('button', { name: 'Add an operator' }))
    await user.type(screen.getByLabelText(/Email/), 'echo@example.com')
    await user.type(screen.getByLabelText(/Full name/), 'Echo Test')
    await chooseDirectPassword(user)
    await user.type(screen.getByLabelText(/Initial password/), 'a-long-enough-password')
    await user.click(screen.getByRole('button', { name: 'Create operator' }))

    await screen.findByText('echo@example.com')
    expect(document.body.textContent).not.toContain('a-long-enough-password')
  })
})
