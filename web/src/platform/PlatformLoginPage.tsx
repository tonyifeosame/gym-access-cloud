import { useState, type FormEvent } from 'react'
import { Link, Navigate } from 'react-router-dom'

import { ApiError } from '../api/client'
import { usePlatformSession } from './PlatformSessionProvider'

/**
 * Signing in to platform administration.
 *
 * VISIBLY A DIFFERENT SURFACE from the operator console, and that is a safety
 * property rather than branding. Somebody who arrives here with tenant operator
 * credentials will try them, fail, and need to understand why — "wrong password"
 * would send them round the same loop. So the page says what this is and links
 * to the console they probably want.
 *
 * Every credential failure answers identically, as the API does: unknown
 * address, wrong password, disabled account. Being more helpful here would turn
 * the vendor's own administration login into an enumeration oracle, which is a
 * worse place to have one than the tenant console.
 */
export function PlatformLoginPage() {
  const { status, login } = usePlatformSession()

  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  if (status === 'authenticated') {
    return <Navigate to="/platform" replace />
  }

  async function onSubmit(event: FormEvent) {
    event.preventDefault()
    setError(null)
    setSubmitting(true)
    try {
      await login(email, password)
    } catch (caught) {
      setError(describeFailure(caught))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <main className="login">
      <form className="login__card" onSubmit={(event) => void onSubmit(event)}>
        <h1 className="login__title">AccessLink</h1>
        <p className="login__subtitle">Platform administration</p>

        <p className="login__note">
          This is the surface that creates and administers <strong>customer
          companies</strong>. It is not the operator console — if you sign in to
          run a company&apos;s doors, people or terminals, you want{' '}
          <Link to="/login">the console</Link> instead.
        </p>

        {error ? (
          <p className="login__error" role="alert">
            {error}
          </p>
        ) : null}

        <label className="field">
          <span className="field__label">Email</span>
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

        <label className="field">
          <span className="field__label">Password</span>
          <input
            className="field__input"
            type="password"
            name="password"
            autoComplete="current-password"
            required
            value={password}
            onChange={(event) => setPassword(event.target.value)}
          />
        </label>

        <button className="button button--primary" type="submit" disabled={submitting}>
          {submitting ? 'Signing in…' : 'Sign in'}
        </button>
      </form>
    </main>
  )
}

function describeFailure(caught: unknown): string {
  if (!(caught instanceof ApiError)) {
    return 'Could not reach AccessLink. Check your connection and try again.'
  }
  if (caught.isRateLimited) {
    const wait = caught.retryAfterSeconds
    return wait
      ? `Too many attempts. Try again in ${wait < 60 ? `${wait} seconds` : `${Math.ceil(wait / 60)} minutes`}.`
      : 'Too many attempts. Try again shortly.'
  }
  if (caught.isUnauthenticated) {
    // Deliberately says nothing about which half was wrong, and nothing about
    // whether a platform account with that address exists.
    return 'Invalid email or password. Platform administration is a separate account from any operator console login.'
  }
  if (caught.status >= 500) {
    return 'AccessLink is not responding. This is not a problem with your password.'
  }
  return caught.message
}
