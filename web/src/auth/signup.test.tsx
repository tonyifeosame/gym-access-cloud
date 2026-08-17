import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../api/csrf'
import { expectNoViolations } from '../test/axe'
import { makeTestQueryClient, renderWithSession } from '../test/render'
import { failNext, resetServerState, seed, state } from '../test/server'
import { LoginPage } from './LoginPage'
import { RegisterPage } from './RegisterPage'
import { RequireAuth } from './guards'

/**
 * Self-service signup — the first customer-facing flow that does not assume
 * somebody has already been onboarded by hand.
 *
 * WHAT IS BEING PROTECTED HERE is mostly about what the screen does NOT ask for
 * and does NOT show. A new customer has no company id, no slug, no claim code,
 * no provisioning key and has never met a platform administrator, and every one
 * of those leaking into this form would put the flow back where it started. So
 * the assertions below are as much about absence as about the happy path.
 *
 * The signup screen is rendered inside a REAL SessionProvider against the mock
 * API, so the session it adopts is the one the server actually returned rather
 * than an object injected past the code under test.
 */

/**
 * Signup, sign-in and the console behind the guard, in one router.
 *
 * The console route is what makes "signs them in" assertable: registration
 * succeeds only if the customer ends up past RequireAuth without a second
 * credential prompt.
 */
function renderAt(path: string) {
  const router = createMemoryRouter(
    [
      { path: '/register', element: <RegisterPage /> },
      { path: '/login', element: <LoginPage /> },
      {
        path: '/',
        element: (
          <RequireAuth>
            <h1>The console</h1>
          </RequireAuth>
        ),
      },
    ],
    { initialEntries: [path] },
  )
  return renderWithSession(<RouterProvider router={router} />, makeTestQueryClient())
}

async function fillSignupForm(
  user: ReturnType<typeof userEvent.setup>,
  overrides: Partial<Record<'name' | 'company' | 'email' | 'password', string>> = {},
) {
  await user.type(screen.getByLabelText('Your name'), overrides.name ?? 'Amaka Obi')
  await user.type(screen.getByLabelText('Company name'), overrides.company ?? 'Harbour Freight Ltd')
  await user.type(screen.getByLabelText('Work email'), overrides.email ?? 'amaka@harbourfreight.com')
  await user.type(screen.getByLabelText('Password'), overrides.password ?? 'correct-horse-battery')
}

beforeEach(() => {
  setCsrfToken(null)
  resetServerState(null)
})

describe('creating an account', () => {
  it('takes four fields and asks for nothing else', async () => {
    // Not a style assertion. A company id, a slug, a claim code or a
    // provisioning key on this form would each be something a brand new
    // customer cannot possibly supply, and the flow is only self-service for as
    // long as none of them appears.
    const { container } = renderAt('/register')
    await screen.findByRole('heading', { name: /create your accesslink account/i })

    expect(container.querySelectorAll('input')).toHaveLength(4)
    for (const label of ['Your name', 'Company name', 'Work email', 'Password']) {
      expect(screen.getByLabelText(label)).toBeInTheDocument()
    }
    expect(screen.queryByLabelText(/slug|company id|claim code|api key|site/i)).toBeNull()
  })

  it('passes the automated accessibility sweep', async () => {
    // The signup screen is the first thing anybody sees, and it is outside the
    // authenticated tree that the console-wide sweep covers — so it is swept
    // here instead of being the one screen nothing checks.
    renderAt('/register')
    await screen.findByRole('heading', { name: /create your accesslink account/i })
    await expectNoViolations()
  })

  it('signs the new customer straight into their own console', async () => {
    // The endpoint answers with the session body AND sets the cookie, so there
    // is nothing left to prove: sending somebody who has just chosen a password
    // to a login form to type it again is a step that only exists when signup
    // and authentication were built separately.
    const user = userEvent.setup()
    renderAt('/register')

    await fillSignupForm(user)
    await user.click(screen.getByRole('button', { name: /create account/i }))

    expect(await screen.findByRole('heading', { name: 'The console' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^sign in$/i })).toBeNull()

    // The session the console is now running on is the OWNER of the company
    // that was just created — not a viewer somewhere else's tenant.
    await waitFor(() => {
      expect(state.session?.role).toBe('OWNER')
      expect(state.session?.company.name).toBe('Harbour Freight Ltd')
    })
  })

  it('never puts a site provisioning key on screen', async () => {
    // The server creates the company's first site in the same transaction and
    // keeps only the hash of its credential, so there is no plaintext to show.
    // Asserting it here is what stops a future response shape being rendered
    // straight onto an anonymous browser's screen.
    const user = userEvent.setup()
    const { container } = renderAt('/register')

    await fillSignupForm(user)
    await user.click(screen.getByRole('button', { name: /create account/i }))
    await screen.findByRole('heading', { name: 'The console' })

    expect(container.textContent).not.toMatch(/ats_/)
    expect(document.body.textContent).not.toMatch(/ats_/)
  })

  it('sends somebody whose address is already in use to sign in instead', async () => {
    // Email is unique globally, so this is a real state a returning customer
    // reaches by accident. Repeating "already in use" without saying what to do
    // about it leaves them on a form that will never succeed.
    const user = userEvent.setup()
    seed({ registeredEmails: ['amaka@harbourfreight.com'] })
    renderAt('/register')

    await fillSignupForm(user)
    await user.click(screen.getByRole('button', { name: /create account/i }))

    const message = await screen.findByRole('alert')
    expect(message).toHaveTextContent(/already exists for that email address/i)
    expect(message).toHaveTextContent(/sign in instead/i)
    expect(screen.queryByRole('heading', { name: 'The console' })).toBeNull()
  })

  it('holds the submit until the password meets the policy, and says so', async () => {
    const user = userEvent.setup()
    renderAt('/register')

    await fillSignupForm(user, { password: 'short' })

    expect(screen.getByRole('button', { name: /create account/i })).toBeDisabled()
    // The shortfall is ANNOUNCED in the live region, not merely implied by a
    // disabled button — a control that will not respond and does not say why is
    // the worst of both.
    expect(
      screen.getByText(/password must be at least 12 characters\./i),
    ).toBeInTheDocument()
    expect(screen.getByLabelText('Password')).toHaveAttribute('aria-invalid', 'true')
  })

  it('reports a server failure as a failure, not as a rejected signup', async () => {
    // A 500 is not "your details were wrong". Telling a customer their signup
    // was refused when the store was simply unavailable sends them off editing
    // fields that were fine.
    const user = userEvent.setup()
    failNext('register', 500)
    renderAt('/register')

    await fillSignupForm(user)
    await user.click(screen.getByRole('button', { name: /create account/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/not responding/i)
    expect(screen.getByRole('button', { name: /create account/i })).toBeEnabled()
  })
})

describe('the sign-in screen', () => {
  it('offers a way to create an account', async () => {
    // The console is deployed at a single address. Without this link there is no
    // route into the product for anybody who has not already been onboarded by
    // hand, which is the gap this flow exists to close.
    renderAt('/login')

    const link = await screen.findByRole('link', { name: /create an account/i })
    expect(link).toHaveAttribute('href', '/register')
  })

  it('does not link the platform administration surface', async () => {
    // A different identity, a different table and a different cookie. A customer
    // signing up has no business being shown that it exists.
    renderAt('/login')
    await screen.findByRole('button', { name: /sign in/i })

    for (const link of screen.getAllByRole('link')) {
      expect(link.getAttribute('href')).not.toMatch(/platform/)
    }
  })
})
