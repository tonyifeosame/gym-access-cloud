import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import { setCsrfToken } from '../api/csrf'
import { PasswordInput } from '../components/PasswordInput'
import { TextField } from '../components/Form'
import { expectNoViolations } from '../test/axe'
import { renderWithQuery, renderWithSession } from '../test/render'
import { failNext, resetServerState, state } from '../test/server'
import { ForgotPasswordPage } from './ForgotPasswordPage'
import { LoginPage } from './LoginPage'
import { describeGoogleOutcome, googleSignInHref, PASSWORD_ONLY } from './providers'

/**
 * The ways in, as the login screen draws them from GET /auth/providers, and
 * the show/hide control every password field carries.
 */

function renderLogin(path = '/login') {
  const router = createMemoryRouter(
    [
      { path: '/login', element: <LoginPage /> },
      { path: '/forgot-password', element: <ForgotPasswordPage /> },
      { path: '/register', element: <h1>Create an account</h1> },
      { path: '/', element: <h1>Overview</h1> },
    ],
    { initialEntries: [path] },
  )
  return renderWithSession(<RouterProvider router={router} />)
}

beforeEach(() => {
  setCsrfToken(null)
  resetServerState(null)
})

// ---------------------------------------------------------------------------
// Sign in with Google
// ---------------------------------------------------------------------------

describe('sign in with Google', () => {
  it('is absent on a deployment that has not configured it', async () => {
    renderLogin()
    await screen.findByLabelText('Email')

    // The providers request has been answered by now; the control is not drawn.
    await waitFor(() =>
      expect(state.requests.some((r) => r.url.endsWith('/api/v1/auth/providers'))).toBe(true),
    )
    expect(screen.queryByRole('link', { name: /sign in with google/i })).not.toBeInTheDocument()
    // The password form is untouched either way.
    expect(screen.getByLabelText('Password')).toBeInTheDocument()
  })

  it('is a LINK to the API start route, carrying where to land afterwards', async () => {
    state.providers = { ...state.providers, google: { ...state.providers.google, enabled: true } }
    renderLogin('/login?next=%2Fpeople')

    const link = await screen.findByRole('link', { name: /sign in with google/i })
    // A navigation, not a fetch: the flow ends in a redirect from Google that
    // only a real navigation can receive the session cookie from.
    expect(link).toHaveAttribute('href', '/api/v1/auth/google/start?next=%2Fpeople')
    // And the password form is still there beside it.
    expect(screen.getByRole('button', { name: /^sign in$/i })).toBeInTheDocument()
  })

  it('keeps `next` inside the console when building the link', () => {
    const providers = { ...PASSWORD_ONLY, google: { ...PASSWORD_ONLY.google, enabled: true } }
    expect(googleSignInHref(providers, '//evil.example/')).toBe(
      '/api/v1/auth/google/start?next=%2F',
    )
    expect(googleSignInHref(providers, 'https://evil.example/')).toBe(
      '/api/v1/auth/google/start?next=%2F',
    )
    expect(googleSignInHref(providers, null)).toBe('/api/v1/auth/google/start?next=%2F')
    expect(googleSignInHref(providers, '/terminals?q=front')).toBe(
      '/api/v1/auth/google/start?next=%2Fterminals%3Fq%3Dfront',
    )
  })

  it('explains a sign-in that came back with an outcome code', async () => {
    renderLogin('/login?error=google_no_account')

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/no accesslink account is linked/i)
    // And says what to do instead, without naming any account.
    expect(alert).toHaveTextContent(/sign in with the email and password/i)
  })

  it('has words for every outcome the API documents, and a fallback', () => {
    for (const code of [
      'google_cancelled',
      'google_failed',
      'google_no_account',
      'google_email_unverified',
      'google_account_conflict',
      'google_state_mismatch',
    ]) {
      expect(describeGoogleOutcome(code), code).toBeTruthy()
    }
    expect(describeGoogleOutcome('google_something_newer')).toMatch(/did not complete/i)
    expect(describeGoogleOutcome(null)).toBeNull()
  })

  it('replaces the Google message with the password failure, not beside it', async () => {
    state.loginStatus = 401
    const user = userEvent.setup()
    renderLogin('/login?error=google_cancelled')

    expect(await screen.findByRole('alert')).toHaveTextContent(/cancelled/i)

    await user.type(screen.getByLabelText('Email'), 'ops@example.com')
    await user.type(screen.getByLabelText('Password'), 'wrong-password')
    await user.click(screen.getByRole('button', { name: /^sign in$/i }))

    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent(/invalid email or password/i))
    expect(screen.getAllByRole('alert')).toHaveLength(1)
  })

  it('falls back to the password form alone when providers cannot be read', async () => {
    failNext('providers', 500)
    renderLogin()

    expect(await screen.findByLabelText('Password')).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /sign in with google/i })).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: /create an account/i })).toBeInTheDocument()
  })

  it('hides the signup control where the deployment has switched signup off', async () => {
    state.providers = { ...state.providers, signup: { enabled: false } }
    renderLogin()
    await screen.findByLabelText('Email')

    await waitFor(() =>
      expect(screen.queryByRole('link', { name: /create an account/i })).not.toBeInTheDocument(),
    )
  })

  it('has no accessibility violations with the Google control drawn', async () => {
    state.providers = { ...state.providers, google: { ...state.providers.google, enabled: true } }
    const { container } = renderLogin('/login?error=google_failed')
    await screen.findByRole('link', { name: /sign in with google/i })
    await expectNoViolations(container)
  })
})

// ---------------------------------------------------------------------------
// Forgot password, where the link arrives by email
// ---------------------------------------------------------------------------

describe('requesting a reset on a deployment that emails the link', () => {
  beforeEach(() => {
    state.providers = { ...state.providers, password_reset: { email_delivery: true } }
  })

  it('says to check the inbox, and only then', async () => {
    const user = userEvent.setup()
    renderLogin('/forgot-password')

    expect(await screen.findByText(/email you a link/i)).toBeInTheDocument()

    await user.type(screen.getByLabelText('Email'), 'ops@example.com')
    await user.click(screen.getByRole('button', { name: /request a reset/i }))

    expect(await screen.findByRole('heading', { name: /check your email/i })).toBeInTheDocument()
    expect(screen.getByText(/works once and expires/i)).toBeInTheDocument()
    expect(screen.queryByText(/does not send email/i)).not.toBeInTheDocument()
  })

  it('still answers identically whether or not the address exists', async () => {
    const user = userEvent.setup()
    renderLogin('/forgot-password')

    await user.type(await screen.findByLabelText('Email'), 'nobody@example.com')
    await user.click(screen.getByRole('button', { name: /request a reset/i }))
    const first = (await screen.findByRole('heading', { level: 1 })).textContent

    resetServerState(null)
    state.providers = { ...state.providers, password_reset: { email_delivery: true } }
    renderLogin('/forgot-password')
    const emails = await screen.findAllByLabelText('Email')
    await user.type(emails[emails.length - 1] as HTMLElement, 'ops@example.com')
    const buttons = screen.getAllByRole('button', { name: /request a reset/i })
    await user.click(buttons[buttons.length - 1] as HTMLElement)

    const second = (await screen.findAllByRole('heading', { level: 1 })).pop()?.textContent
    expect(second).toBe(first)
  })

  it('does not promise an email where the deployment cannot send one', async () => {
    state.providers = { ...state.providers, password_reset: { email_delivery: false } }
    const user = userEvent.setup()
    renderLogin('/forgot-password')

    await user.type(await screen.findByLabelText('Email'), 'ops@example.com')
    await user.click(screen.getByRole('button', { name: /request a reset/i }))

    expect(await screen.findByText(/does not send email/i)).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: /check your email/i })).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// The show/hide control
// ---------------------------------------------------------------------------

describe('the password show/hide control', () => {
  it('reveals and hides the value without changing anything else about the field', async () => {
    const user = userEvent.setup()
    renderWithQuery(
      <label>
        Password
        <PasswordInput name="password" autoComplete="current-password" defaultValue="hunter2!" />
      </label>,
    )

    const input = screen.getByLabelText('Password')
    const toggle = screen.getByRole('button', { name: /show password/i })
    expect(input).toHaveAttribute('type', 'password')
    expect(toggle).toHaveAttribute('aria-pressed', 'false')
    expect(toggle).toHaveAttribute('type', 'button')
    expect(toggle).toHaveAttribute('aria-controls', input.id)

    await user.click(toggle)
    expect(input).toHaveAttribute('type', 'text')
    expect(input).toHaveValue('hunter2!')
    expect(input).toHaveAttribute('autocomplete', 'current-password')
    expect(input).toHaveAttribute('name', 'password')
    expect(screen.getByRole('button', { name: /hide password/i })).toHaveAttribute(
      'aria-pressed',
      'true',
    )

    await user.click(screen.getByRole('button', { name: /hide password/i }))
    expect(input).toHaveAttribute('type', 'password')
  })

  it('is reachable by keyboard and does not submit the form', async () => {
    const user = userEvent.setup()
    let submitted = 0
    renderWithQuery(
      <form
        onSubmit={(event) => {
          event.preventDefault()
          submitted += 1
        }}
      >
        <label>
          Password
          <PasswordInput name="password" />
        </label>
        <button type="submit">Go</button>
      </form>,
    )

    const input = screen.getByLabelText('Password')
    await user.click(input)
    await user.keyboard('secret')
    await user.tab()
    expect(screen.getByRole('button', { name: /show password/i })).toHaveFocus()
    await user.keyboard('{Enter}')
    expect(input).toHaveAttribute('type', 'text')
    expect(submitted).toBe(0)
  })

  it('is what TextField renders for type="password"', async () => {
    const user = userEvent.setup()
    renderWithQuery(
      <TextField label="New password" type="password" value="abc" onChange={() => {}} />,
    )

    expect(screen.getByLabelText('New password')).toHaveAttribute('type', 'password')
    await user.click(screen.getByRole('button', { name: /show password/i }))
    expect(screen.getByLabelText('New password')).toHaveAttribute('type', 'text')
  })

  it('is on the sign-in screen and passes axe with the value revealed', async () => {
    const user = userEvent.setup()
    const { container } = renderLogin()

    await user.type(await screen.findByLabelText('Password'), 'a-long-enough-password')
    await user.click(screen.getByRole('button', { name: /show password/i }))
    expect(screen.getByLabelText('Password')).toHaveAttribute('type', 'text')
    await expectNoViolations(container)
  })
})
