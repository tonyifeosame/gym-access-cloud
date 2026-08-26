import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { delay, http } from 'msw'
import { createMemoryRouter, MemoryRouter, RouterProvider } from 'react-router-dom'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../../api/csrf'
import type { OperatorAccount, Role, Session } from '../../api/types'
import { makeOperatorAccount, makeSession, makeSite, SITE_A, SITE_B } from '../../test/fixtures'
import { makeTestQueryClient, renderWithSession } from '../../test/render'
import { useSession } from '../../session/useSession'
import { failNext, resetServerState, seed, server, state } from '../../test/server'
import { ResetPasswordDialog } from './OperatorDialogs'
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

  it('LEADS WITH THE SAME WORDS FOR BOTH KINDS OF "all sites"', async () => {
    /*
      The two unrestricted cases used to read "All sites (by role)" and a bare
      "All sites", and the first was muted while the second was not — two strings
      for one reach, told apart partly by a grey. Colour is not available to every
      reader, and neither is the knack of spotting which phrasing this is.

      Both now begin with the same two words and put the reason beside them in
      plain language. The DISTINCTION IS STILL MADE, because restricting an
      administrator does nothing while restricting a manager does; it is just no
      longer smuggled in as a tone.
    */
    signIn('ADMIN')
    renderOperators()

    const owner = (await screen.findByRole('link', { name: 'Tobi Owner' })).closest('tr')
    const manager = screen.getByRole('link', { name: 'Kemi Manager' }).closest('tr')

    expect(within(owner as HTMLElement).getByText(/All sites/)).toBeInTheDocument()
    expect(within(owner as HTMLElement).getByText('(by role)')).toBeInTheDocument()

    expect(within(manager as HTMLElement).getByText(/All sites/)).toBeInTheDocument()
    expect(within(manager as HTMLElement).getByText('(not restricted)')).toBeInTheDocument()

    // Never a bare "0": empty grants mean unrestricted, and the opposite reading
    // is the dangerous one.
    expect(within(manager as HTMLElement).queryByText('0')).not.toBeInTheDocument()
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

  it('OFFERS NEITHER A PASSWORD RESET NOR SITE ACCESS ON YOUR OWN ACCOUNT', async () => {
    /*
      THIS REVERSES WHAT THIS TEST USED TO ASSERT, deliberately, and the reason is
      not security — neither control can lock a company out, which is why the
      server permits both and why the three genuine self-protections above are
      untouched. It is that neither meant anything here.

      RESET PASSWORD was contradicted by the page's own notice forty pixels
      below it, which says your own password is changed from Settings. Pressing
      it opened a dialog written about a colleague: "they choose their own
      password", "you never learn it". An administrator was told they would never
      learn their own password.

      SITE ACCESS could never do anything. This page is ADMIN-gated, so your own
      account is always an administrator's or an owner's, and both roles reach
      every site regardless of what is stored. The dialog opened and said exactly
      that — a control whose entire content was an explanation of its own
      inertness.
    */
    signIn('OWNER')
    renderOperators(`/operators/${SELF_ID}`)

    await screen.findByRole('heading', { name: 'Ops Person', level: 1 })
    expect(screen.queryByRole('button', { name: 'Reset password' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Send invitation' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Site access' })).not.toBeInTheDocument()
  })

  it('sends you to Settings for your own password, rather than naming it', async () => {
    signIn('OWNER')
    renderOperators(`/operators/${SELF_ID}`)

    await screen.findByText('This is your own account')
    const settings = screen.getByRole('link', { name: 'Settings' })
    expect(settings).toHaveAttribute('href', '/settings')
  })

  it('KEEPS BOTH CONTROLS ON SOMEBODY ELSE, which is who they were built for', async () => {
    // The point is that these are administrator-to-colleague operations, not
    // that they are dangerous. On another account they are unchanged.
    signIn('OWNER')
    renderOperators('/operators/operator-unscoped')

    expect(await screen.findByRole('button', { name: 'Site access' })).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /Reset password|Send invitation/ }),
    ).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// The reset dialog's audience
// ---------------------------------------------------------------------------

/**
 * Mounts a dialog only once the session has arrived.
 *
 * These two tests render ResetPasswordDialog DIRECTLY, which is the point of
 * them: the guard has to hold for a call site that is not the detail page. The
 * dialog reads an authenticated session on its first render, and
 * `renderWithSession` mounts its children before GET /auth/me resolves -- so
 * without this the component throws before it can decide anything.
 */
function WhenSignedIn({ children }: { children: ReactNode }) {
  const { session } = useSession()
  return session ? <>{children}</> : null
}

describe('the password reset dialog is written about somebody else', () => {
  it('REFUSES TO ADDRESS THE SIGNED-IN OPERATOR AS A COLLEAGUE', async () => {
    /*
      Every line of that dialog describes an administrator handing a credential
      to somebody else: "they choose their own password", "you never learn it",
      "you only pass the link on", "the audit trail records that you set it".
      Pointed at your own account, all of it is false.

      The detail page no longer offers it there, so this state is not reachable
      through the UI. THIS IS THE SECOND LOCK, and it is worth having because
      copy that is only correct for one audience should not depend on every
      future caller remembering which. Rendered directly, as a new call site
      would, the dialog declines and points at Settings.
    */
    signIn('OWNER')
    const self = ROSTER.find((operator) => operator.id === SELF_ID)!
    renderWithSession(
      <MemoryRouter>
        <WhenSignedIn>
          <ResetPasswordDialog open operator={self} onClose={() => {}} />
        </WhenSignedIn>
      </MemoryRouter>,
    )

    expect(await screen.findByText('This is your own account')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Settings' })).toHaveAttribute('href', '/settings')

    // None of the colleague-facing language, and no way to act.
    expect(screen.queryByText(/you only pass the link on/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/You never learn it/i)).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Issue a reset link/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Set password/ })).not.toBeInTheDocument()
  })

  it('keeps every word of it for somebody else', async () => {
    signIn('OWNER')
    const other = ROSTER.find((operator) => operator.id === 'operator-viewer')!
    renderWithSession(
      <MemoryRouter>
        <WhenSignedIn>
          <ResetPasswordDialog open operator={other} onClose={() => {}} />
        </WhenSignedIn>
      </MemoryRouter>,
    )

    expect(await screen.findByText(/you only pass the link on/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Issue a reset link' })).toBeInTheDocument()
    // The security consequence, untouched.
    expect(screen.getByText(/This signs them out everywhere/i)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// The site picker says nothing until it knows something
// ---------------------------------------------------------------------------

describe('the site picker does not guess while it is loading', () => {
  it('DOES NOT CLAIM THE COMPANY HAS NO SITES WHILE THE REQUEST IS IN FLIGHT', async () => {
    /*
      `empty` is a statement of FACT about the customer's account, and the group
      used to make it whenever `options` was short — which includes every moment
      before the sites request resolved. A company with sites was told "Your
      company has no sites yet", with no spinner, next to a hint reading
      "Narrowed to 1 site": two contradictory claims in one dialog.

      Held open here by a request that never resolves, which is the state a slow
      connection produces.
    */
    const user = userEvent.setup()
    signIn('ADMIN')
    // Held open rather than merely slow: the assertion is about the state
    // BEFORE resolution, and a race against a real delay is a flaky test.
    server.use(http.get('*/api/v1/console/sites', async () => { await delay('infinite') }))
    renderOperators()

    await user.click(await screen.findByRole('button', { name: 'Add an operator' }))

    expect(await screen.findByText('Loading…')).toBeInTheDocument()
    expect(screen.queryByText(/no sites yet/i)).not.toBeInTheDocument()
  })

  it('says so plainly once the request comes back empty', async () => {
    const user = userEvent.setup()
    signIn('ADMIN')
    seed({ sites: [] })
    renderOperators()

    await user.click(await screen.findByRole('button', { name: 'Add an operator' }))

    expect(await screen.findByText(/Your company has no sites yet/i)).toBeInTheDocument()
    expect(screen.queryByText('Loading…')).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Where the destructive actions live
// ---------------------------------------------------------------------------

describe('the actions that stop an account working', () => {
  it('ARE NOT IN THE PAGE HEADER, so they cannot wrap into it', async () => {
    /*
      At 390px the header's five buttons wrapped into two rows with Deactivate
      and Remove alone on the second — Remove in the destructive fill, at the
      x-position Change role had held on the row above. Reaching for the leftmost
      button of a row you have already used once is how an account gets removed
      instead of re-roled.
    */
    signIn('OWNER')
    renderOperators('/operators/operator-viewer')

    await screen.findByRole('heading', { name: 'Sam Viewer', level: 1 })

    const header = document.querySelector('.page__actions') as HTMLElement
    expect(within(header).queryByRole('button', { name: 'Deactivate' })).not.toBeInTheDocument()
    expect(within(header).queryByRole('button', { name: 'Remove' })).not.toBeInTheDocument()
    // The everyday ones stay where they were.
    expect(within(header).getByRole('button', { name: 'Change role' })).toBeInTheDocument()
    expect(within(header).getByRole('button', { name: 'Site access' })).toBeInTheDocument()
  })

  it('are grouped under their own heading, with Remove still destructive', async () => {
    signIn('OWNER')
    renderOperators('/operators/operator-viewer')

    await screen.findByRole('heading', { name: 'Sam Viewer', level: 1 })
    const zone = screen.getByRole('region', { name: 'Account administration' })

    expect(within(zone).getByRole('button', { name: 'Deactivate' })).toBeInTheDocument()
    const remove = within(zone).getByRole('button', { name: 'Remove' })
    expect(remove).toHaveClass('button--danger')
  })

  it('KEEPS EVERY PERMISSION GATE THAT GOVERNED THEM IN THE HEADER', async () => {
    // Moving a control must not widen who may reach it. An ADMIN may not touch
    // an OWNER at all, and nobody may deactivate or remove themselves.
    signIn('ADMIN')
    const admin = renderOperators('/operators/operator-owner')
    await screen.findByRole('heading', { name: 'Tobi Owner', level: 1 })
    expect(
      screen.queryByRole('region', { name: 'Account administration' }),
    ).not.toBeInTheDocument()
    admin.unmount()

    signIn('OWNER')
    renderOperators(`/operators/${SELF_ID}`)
    await screen.findByRole('heading', { name: 'Ops Person', level: 1 })
    expect(
      screen.queryByRole('region', { name: 'Account administration' }),
    ).not.toBeInTheDocument()
  })

  it('still requires the email address to remove', async () => {
    // Unchanged by the move, and non-negotiable.
    const user = userEvent.setup()
    signIn('OWNER')
    renderOperators('/operators/operator-viewer')

    await screen.findByRole('heading', { name: 'Sam Viewer', level: 1 })
    const zone = screen.getByRole('region', { name: 'Account administration' })
    await user.click(within(zone).getByRole('button', { name: 'Remove' }))

    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('viewer@example.com')).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Remove operator' })).toBeDisabled()
  })
})

// ---------------------------------------------------------------------------
// The account creation date
// ---------------------------------------------------------------------------

describe('the account creation date', () => {
  it('SHOWS NO TIME OF DAY, which was a timezone artifact rather than a fact', async () => {
    // The value is whatever moment the row was written, read back in the
    // viewer's zone: a midnight-UTC creation rendered as "1:00 AM" one zone
    // east. Nobody created that account at one in the morning.
    signIn('OWNER')
    renderOperators('/operators/operator-viewer')

    await screen.findByRole('heading', { name: 'Sam Viewer', level: 1 })
    const card = screen.getByText('Account created').closest('.card') as HTMLElement
    const shown = within(card).getByText(/\d{4}/)

    expect(shown.textContent).not.toMatch(/\d{1,2}:\d{2}/)
    expect(shown.textContent).not.toMatch(/AM|PM/i)
    // The full instant is still carried for anyone correlating a log.
    expect(shown).toHaveAttribute('datetime')
    expect(shown).toHaveAttribute('title')
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

  it('SAYS THE LINK IS SHOWN ONCE AND THAT YOU ARE THE ONE SENDING IT', async () => {
    /*
      The dialog used to carry a whole InfoNote restating the option description
      immediately above it. It is one of the two blocks that pushed this form to
      872px in a 900px viewport and put its submit button below the fold on open.

      WHAT MAY NOT BE LOST IS THE CONSEQUENCE, and it has not been: that the
      administrator never learns the password, and that the link is shown once
      and travels by a channel they choose. Both are on the option that carries
      the decision, which is where somebody choosing between the two reads them.
    */
    const user = userEvent.setup()
    signIn('ADMIN')
    renderOperators()

    await user.click(await screen.findByRole('button', { name: 'Add an operator' }))

    expect(screen.getByText(/shown once here for you to send/i)).toBeInTheDocument()
    expect(screen.getByText(/You never learn it/i)).toBeInTheDocument()
  })

  it('warns that a chosen password must be handed over out of band', async () => {
    // Shorter than it was, and the two facts that matter are both still here:
    // the administrator delivers it, and it cannot be read back.
    const user = userEvent.setup()
    signIn('ADMIN')
    renderOperators()

    await user.click(await screen.findByRole('button', { name: 'Add an operator' }))
    await user.selectOptions(
      screen.getByLabelText(/How they get their password/),
      'PASSWORD',
    )

    expect(screen.getByText(/You give it to them yourself/i)).toBeInTheDocument()
    expect(screen.getByText(/cannot be shown again/i)).toBeInTheDocument()
    // And the consequence of choosing this path, which is not trimmed.
    expect(screen.getByText(/You will know their credential/i)).toBeInTheDocument()
  })

  it('WARNS THAT AN EMPTY SITE SELECTION GRANTS EVERY SITE', async () => {
    const user = userEvent.setup()
    signIn()
    renderOperators()

    await user.click(await screen.findByRole('button', { name: 'Add an operator' }))
    // Default role is VIEWER, which is scoped by grants.
    expect(await screen.findByText('This operator will reach all sites')).toBeInTheDocument()

    await user.click(screen.getByLabelText(SITE_A.site_name))
    await waitFor(() =>
      expect(screen.queryByText('This operator will reach all sites')).not.toBeInTheDocument(),
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

  it('DEFAULTS to a reset link, so the administrator never learns the password', async () => {
    // SEC-10. Before the handover mechanism existed, the only answer to "they
    // are locked out" was an administrator typing a new password and reading it
    // out — which leaves them knowing a colleague's credential indefinitely.
    const user = userEvent.setup()
    signIn()
    renderOperators('/operators/operator-viewer')

    await user.click(await screen.findByRole('button', { name: 'Reset password' }))

    // No password field until the other path is deliberately chosen.
    expect(screen.queryByLabelText(/New password/)).not.toBeInTheDocument()
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: /issue a reset link/i }),
    )

    expect(await screen.findByText(/shown once and cannot be recovered/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Done' })).toBeDisabled()
  })

  it('still allows setting a password directly, and says what that costs', async () => {
    const user = userEvent.setup()
    signIn()
    renderOperators('/operators/operator-viewer')

    await user.click(await screen.findByRole('button', { name: 'Reset password' }))
    await user.selectOptions(screen.getByLabelText(/How they get back in/), 'PASSWORD')

    expect(screen.getByText('This signs them out everywhere')).toBeInTheDocument()

    const confirm = within(screen.getByRole('dialog')).getByRole('button', {
      name: 'Set password',
    })
    await user.type(screen.getByLabelText(/New password/), 'short')
    expect(confirm).toBeDisabled()

    await user.clear(screen.getByLabelText(/New password/))
    await user.type(screen.getByLabelText(/New password/), 'a-long-enough-password')
    expect(confirm).toBeEnabled()
    await user.click(confirm)

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('offers an INVITATION rather than a reset for an account never signed in to', async () => {
    // The server refuses an invitation for an account that has signed in, and
    // audits the two differently. Choosing between them from the account's own
    // state is what stops the operator meeting a 409 that means nothing to them.
    const user = userEvent.setup()
    signIn()
    seed({
      operators: ROSTER.map((operator) =>
        operator.id === 'operator-viewer' ? { ...operator, last_login_at: undefined } : operator,
      ),
    })
    renderOperators('/operators/operator-viewer')

    expect(await screen.findByText('This account has never been used')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Reset password' })).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Send invitation' }))
    expect(screen.getByText(/Any earlier invitation stops working/i)).toBeInTheDocument()

    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: /issue an invitation/i }),
    )
    expect(await screen.findByText(/shown once and cannot be recovered/i)).toBeInTheDocument()
  })

  it('never leaves a minted link in the page after the panel is dismissed', async () => {
    // Same contract as a site provisioning key: it lives in one panel and is
    // gone when that panel closes.
    const user = userEvent.setup()
    signIn()
    renderOperators('/operators/operator-viewer')

    await user.click(await screen.findByRole('button', { name: 'Reset password' }))
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: /issue a reset link/i }),
    )
    await screen.findByText(/shown once and cannot be recovered/i)

    await user.click(screen.getByLabelText(/I have copied this link/i))
    await user.click(screen.getByRole('button', { name: 'Done' }))

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    // The token the mock issues for a reset. Nothing on the page may still hold it.
    expect(document.body.textContent).not.toContain('f9e8d7c6')
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
