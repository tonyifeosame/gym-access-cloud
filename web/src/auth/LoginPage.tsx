import { useState, type FormEvent } from 'react'
import { Link, Navigate, useSearchParams } from 'react-router-dom'

import { ApiError } from '../api/client'
import { useSession } from '../session/useSession'

/**
 * Sign in.
 *
 * The API answers every credential failure identically -- unknown address, wrong
 * password, disabled account, disabled company -- so this form does too. Trying
 * to be more helpful than the API would turn the login screen into an
 * account-enumeration oracle, which is exactly what that uniform 401 exists to
 * prevent.
 *
 * Two failures ARE worth distinguishing, because the operator can act on them:
 * 429 means wait (and the API says how long), and anything 5xx means the problem
 * is not their password.
 */
export function LoginPage() {
  const { status, login } = useSession()
  const [params] = useSearchParams()

  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  const next = params.get('next')
  const destination = next && next.startsWith('/') && !next.startsWith('//') ? next : '/'

  if (status === 'authenticated') {
    return <Navigate to={destination} replace />
  }

  async function onSubmit(event: FormEvent) {
    event.preventDefault()
    setError(null)
    setSubmitting(true)

    try {
      await login(email, password)
      // The session provider now holds the session; the redirect above fires on
      // the next render.
    } catch (caught) {
      setError(describeFailure(caught))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="login">
      <form className="login__card" onSubmit={(event) => void onSubmit(event)}>
        <h1 className="login__title">AccessLink</h1>
        <p className="login__subtitle">Operator console</p>

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

        {/*
          Both destinations are unauthenticated by necessity. The second is the
          one people arrive at from a link and would not otherwise find, and it
          matters that they can: an invitation whose URL was mangled in a chat
          client still has a code that can be pasted.
        */}
        <p className="login__note">
          <Link to="/forgot-password">Forgotten your password?</Link>
          {' · '}
          <Link to="/redeem">Have an invitation code?</Link>
        </p>
      </form>
    </div>
  )
}

function describeFailure(caught: unknown): string {
  if (!(caught instanceof ApiError)) {
    return 'Could not reach AccessLink. Check your connection and try again.'
  }

  if (caught.isRateLimited) {
    const wait = caught.retryAfterSeconds
    return wait
      ? `Too many attempts. Try again in ${formatWait(wait)}.`
      : 'Too many attempts. Try again shortly.'
  }

  if (caught.isUnauthenticated) {
    // The single message the API gives, and the only one that should appear:
    // saying which half was wrong would disclose whether the account exists.
    return 'Invalid email or password.'
  }

  if (caught.status >= 500) {
    return 'AccessLink is not responding. This is not a problem with your password.'
  }

  return caught.message
}

function formatWait(seconds: number): string {
  if (seconds < 60) return `${seconds} seconds`
  const minutes = Math.ceil(seconds / 60)
  return `${minutes} ${minutes === 1 ? 'minute' : 'minutes'}`
}
