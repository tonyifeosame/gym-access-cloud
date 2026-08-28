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
  await user.type(screen.getByLabelText('Email'), overrides.email ?? 'amaka@harbourfreight.com')
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
    for (const label of ['Your name', 'Company name', 'Email', 'Password']) {
      expect(screen.getByLabelText(label)).toBeInTheDocument()
    }
    expect(screen.queryByLabelText(/slug|company id|claim code|api key|site/i)).toBeNull()
  })

  it('says plainly that this creates a company and makes them its owner', async () => {
    // Somebody who thinks they are joining a company that already exists will
    // fill this in and be confused by what they land in. The screen has to
    // settle that before the first field.
    renderAt('/register')
    await screen.findByRole('heading', { name: /create your accesslink account/i })

    // The heading names the product; the line under it says what else the form
    // does. Between them: an AccessLink account, a company, and who owns it.
    expect(screen.getByText(/sets up your company/i)).toHaveTextContent(
      /makes you its owner/i,
    )

    // And the field whose consequence is not obvious says what it does.
    expect(screen.getByText(/this is the company your team will join/i)).toBeInTheDocument()
  })

  it('keeps our vocabulary off the form', async () => {
    // "Tenant", "slug", "provisioning" and "site" are how we talk about the
    // platform, not how a customer thinks about their company. The one
    // exception is deliberate: "Main Site" is named because it is the first
    // thing they will see inside the console.
    renderAt('/register')
    await screen.findByRole('heading', { name: /create your accesslink account/i })

    const copy = document.body.textContent ?? ''
    for (const jargon of [/tenant/i, /slug/i, /provisioning/i, /operator/i, /deployment/i]) {
      expect(copy).not.toMatch(jargon)
    }
    expect(copy).toMatch(/Main Site/)
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
    expect(screen.getByText(/too short/i)).toHaveTextContent(/use at least 12 characters/i)
    expect(screen.getByLabelText('Password')).toHaveAttribute('aria-invalid', 'true')
  })

  it('turns the password hint into the correction rather than adding a second line', async () => {
    // It used to render a hint AND an error that said the same thing in
    // different words, so a short password produced two sentences about length
    // and a taller form. One element, whose text changes in place.
    const user = userEvent.setup()
    renderAt('/register')
    await screen.findByRole('heading', { name: /create your accesslink account/i })

    const mentions = () =>
      screen.getAllByText(/12 characters/i).filter((node) => node.tagName === 'P')

    expect(mentions()).toHaveLength(1)
    const hint = mentions()[0] as HTMLElement

    await user.type(screen.getByLabelText('Password'), 'short')

    // The SAME element, recoloured and reworded — not a new one below it.
    expect(mentions()).toHaveLength(1)
    expect(mentions()[0]).toBe(hint)
    expect(hint.className).toMatch(/field__hint--invalid/)

    // And it is what the input points at, so it is read out as the field's
    // description either way.
    expect(screen.getByLabelText('Password')).toHaveAttribute('aria-describedby', hint.id)
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

  it('presents signup as a control, not as a third grey link', async () => {
    // It shipped as a sentence in the muted note style, below two recovery
    // links — invisible to the one visitor on this screen who has no account.
    // It is now the card's second button: outlined, because signing in is still
    // what almost everybody is here to do.
    renderAt('/login')

    const link = await screen.findByRole('link', { name: /create an account/i })
    expect(link.className).toMatch(/\bbutton\b/)
    expect(link.className).not.toMatch(/button--primary/)

    // Sign in keeps the emphasis.
    expect(screen.getByRole('button', { name: /^sign in$/i }).className).toMatch(
      /button--primary/,
    )

    // And it is separated from the recovery links rather than sitting among
    // them: somebody with no account is not looking for a password reset.
    const recovery = screen.getByRole('link', { name: /forgotten your password/i })
    expect(recovery.closest('p')).not.toBe(link.closest('p'))
  })

  it('keeps the recovery routes reachable', async () => {
    // The signup CTA was added beside these, never in place of them: an
    // invitation whose URL was mangled in a chat client still has a code that
    // can be pasted, and somebody locked out still needs the reset.
    renderAt('/login')

    expect(await screen.findByRole('link', { name: /forgotten your password/i })).toHaveAttribute(
      'href',
      '/forgot-password',
    )
    expect(screen.getByRole('link', { name: /invitation code/i })).toHaveAttribute(
      'href',
      '/redeem',
    )
  })

  it('passes the automated accessibility sweep', async () => {
    // The screen the CTA was just added to, swept as a whole — a link dressed
    // as a button is exactly the sort of change that loses its accessible name
    // or its landmark.
    renderAt('/login')
    await screen.findByRole('button', { name: /^sign in$/i })
    await expectNoViolations()
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

describe('the two screens are one flow', () => {
  it('mirrors the sign-in card: same layout, the two actions swapped', async () => {
    // Somebody who arrives at the wrong one of these has to be able to see the
    // other immediately, and a person moving between them within a minute
    // should feel they never left. So the secondary action sits in the same
    // place, in the same style, on both.
    renderAt('/register')

    const signIn = await screen.findByRole('link', { name: /^sign in$/i })
    expect(signIn).toHaveAttribute('href', '/login')
    expect(signIn.className).toMatch(/\bbutton\b/)
    expect(signIn.className).not.toMatch(/button--primary/)

    // Create account keeps the emphasis here, exactly as Sign in does there.
    expect(screen.getByRole('button', { name: /create account/i }).className).toMatch(
      /button--primary/,
    )
  })

  it('walks from sign in to signup and back without a dead end', async () => {
    const user = userEvent.setup()
    renderAt('/login')

    await user.click(await screen.findByRole('link', { name: /create an account/i }))
    expect(
      await screen.findByRole('heading', { name: /create your accesslink account/i }),
    ).toBeInTheDocument()

    await user.click(screen.getByRole('link', { name: /^sign in$/i }))
    expect(await screen.findByRole('heading', { name: 'AccessLink' })).toBeInTheDocument()
  })
})
