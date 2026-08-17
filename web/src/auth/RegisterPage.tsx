import { useState, type FormEvent } from 'react'
import { Link, Navigate } from 'react-router-dom'

import { ApiError } from '../api/client'
import { useSession } from '../session/useSession'

/** Mirrors models.MinPasswordLength. The server is the authority. */
const MIN_PASSWORD_LENGTH = 12

/**
 * Create an account.
 *
 * THE FIRST SCREEN A CUSTOMER WHO HAS NEVER HEARD OF US SEES, and the whole of
 * what it may ask for is on it: their name, their company, an address and a
 * password. Everything else the platform needs — the company row, its slug, its
 * first site, the OWNER account, the session — is derived server-side in one
 * transaction. A new customer does not know what a slug is, has no company id,
 * has never met a platform administrator and has no claim code, and being asked
 * for any of those is the difference between signing up and giving up.
 *
 * WHAT THIS SCREEN DELIBERATELY DOES NOT DO:
 *
 *   - It does not offer a role. The server forces OWNER; the account that
 *     creates a company has to be able to create the others.
 *   - It does not name or show a site. One is created for them, and its
 *     PROVISIONING KEY never leaves the server — the response carries a session
 *     and nothing else. An anonymous browser must never be handed the credential
 *     that registers door hardware.
 *   - It does not reach the platform administration surface, which authenticates
 *     a different table with a different cookie and is not linked from here.
 *
 * AND IT SIGNS THEM IN. The API answers with the same session body login does
 * and sets the same cookie, so the session provider adopts it and the redirect
 * below fires on the next render. Sending somebody who has just chosen a
 * password to a login form to type it again is a step that exists only because
 * the two flows were built separately.
 */
export function RegisterPage() {
  const { status, register } = useSession()

  const [fullName, setFullName] = useState('')
  const [companyName, setCompanyName] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  // Straight into the console they now own. `replace` so the back button does
  // not return an authenticated customer to a signup form.
  if (status === 'authenticated') {
    return <Navigate to="/" replace />
  }

  const tooShort = password.length > 0 && password.length < MIN_PASSWORD_LENGTH
  const ready =
    fullName.trim().length > 0 &&
    companyName.trim().length > 0 &&
    email.trim().length > 0 &&
    password.length >= MIN_PASSWORD_LENGTH

  async function onSubmit(event: FormEvent) {
    event.preventDefault()
    if (!ready) return

    setError(null)
    setSubmitting(true)

    try {
      await register({
        full_name: fullName.trim(),
        company_name: companyName.trim(),
        email: email.trim(),
        password,
      })
      // The session provider now holds the session; the redirect above fires on
      // the next render.
    } catch (caught) {
      setError(describeFailure(caught))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    // <main> rather than a bare div, so every part of the page sits inside a
    // landmark. The console's own screens get theirs from the app shell; an
    // unauthenticated screen renders alone and has to carry its own, and axe's
    // `region` rule is what catches the difference.
    <main className="login">
      <form className="login__card" onSubmit={(event) => void onSubmit(event)}>
        <h1 className="login__title">Create your AccessLink account</h1>
        <p className="login__subtitle">
          Your company and its first site are set up for you. It takes one form.
        </p>

        {error ? (
          <p className="login__error" role="alert">
            {error}
          </p>
        ) : null}

        <label className="field">
          <span className="field__label">Your name</span>
          <input
            className="field__input"
            type="text"
            name="full-name"
            autoComplete="name"
            required
            value={fullName}
            onChange={(event) => setFullName(event.target.value)}
          />
        </label>

        <label className="field">
          <span className="field__label">Company name</span>
          <input
            className="field__input"
            type="text"
            name="organization"
            autoComplete="organization"
            required
            value={companyName}
            onChange={(event) => setCompanyName(event.target.value)}
          />
        </label>

        <label className="field">
          <span className="field__label">Work email</span>
          <input
            className="field__input"
            type="email"
            name="email"
            autoComplete="username"
            required
            value={email}
            onChange={(event) => setEmail(event.target.value)}
          />
        </label>

        {/*
          The hint sits OUTSIDE the <label> and is tied on with
          aria-describedby, for the reason set out on RedeemPage: nesting it
          would fold "At least 12 characters" into the control's accessible
          NAME, and a name and a description are different things.
        */}
        <label className="field" htmlFor="register-password">
          <span className="field__label">Password</span>
          <input
            id="register-password"
            className="field__input"
            type="password"
            name="new-password"
            autoComplete="new-password"
            required
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            aria-describedby="register-password-hint"
            aria-invalid={tooShort || undefined}
          />
        </label>
        <p className="field__hint" id="register-password-hint">
          At least {MIN_PASSWORD_LENGTH} characters.
        </p>

        {/* A live region so the shortfall is ANNOUNCED, not merely drawn. */}
        <p className="field__error" aria-live="polite">
          {tooShort ? `Password must be at least ${MIN_PASSWORD_LENGTH} characters.` : ''}
        </p>

        <button className="button button--primary" type="submit" disabled={!ready || submitting}>
          {submitting ? 'Creating your account…' : 'Create account'}
        </button>

        <p className="login__note">
          Already have an account? <Link to="/login">Sign in</Link>
        </p>
      </form>
    </main>
  )
}

function describeFailure(caught: unknown): string {
  if (!(caught instanceof ApiError)) {
    return 'Could not reach AccessLink. Check your connection and try again.'
  }

  // Branched on STATUS, never on the message: the API's error strings are
  // explicitly not stable, and each of these tells the person a different thing
  // to do next.
  switch (caught.status) {
    case 409:
      // The one case where the person is better off somewhere else entirely.
      return 'An account already exists for that email address. Sign in instead, or use "Forgotten your password?" on the sign-in page.'
    case 403:
      return 'Self-service signup is switched off on this deployment. Ask whoever runs your AccessLink installation for an account.'
    case 429:
      return caught.retryAfterSeconds
        ? `Too many attempts. Try again in ${formatWait(caught.retryAfterSeconds)}.`
        : 'Too many attempts. Try again shortly.'
    case 400:
      // The server's own words: which field it refused and why is exactly what
      // the person needs, and paraphrasing it here would mean two copies of the
      // validation rules to keep in step.
      return caught.message
    default:
      return caught.status >= 500
        ? 'AccessLink is not responding. No account has been created — try again shortly.'
        : caught.message
  }
}

function formatWait(seconds: number): string {
  if (seconds < 60) return `${seconds} seconds`
  const minutes = Math.ceil(seconds / 60)
  return `${minutes} ${minutes === 1 ? 'minute' : 'minutes'}`
}
