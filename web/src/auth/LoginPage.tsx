import { useState, type FormEvent } from 'react'
import { Link, Navigate, useSearchParams } from 'react-router-dom'

import { ApiError } from '../api/client'
import { PasswordInput } from '../components/PasswordInput'
import { useSession } from '../session/useSession'
import { describeGoogleOutcome, googleSignInHref, useAuthProviders } from './providers'

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
  const providers = useAuthProviders()

  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  // A Google sign-in that did not complete comes back here with `?error=`, a
  // stable code from the API. Read once into state so it survives the URL
  // being cleaned and so a later password failure replaces it rather than
  // stacking two messages.
  const [error, setError] = useState<string | null>(() =>
    describeGoogleOutcome(params.get('error')),
  )
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
    // <main> rather than a bare div, matching the signup screen: an
    // unauthenticated page renders outside the app shell, so it has to carry its
    // own landmark or nothing on it is inside one. Markup only — nothing about
    // signing in changes.
    <main className="login">
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
          <PasswordInput
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
          SIGN IN WITH GOOGLE, only where the deployment has it configured.

          A LINK, NOT A BUTTON WITH A HANDLER. The flow is a chain of redirects
          — to Google, back to the API, back here — and only a real navigation
          carries the session cookie the API sets at the end of it. It leaves
          the password form exactly as it is: Google is another way to prove
          who you are to an account that exists, not a replacement for the
          form, and an operator whose company never set it up never sees this.
        */}
        {providers.google.enabled ? (
          <>
            <p className="login__divider">or</p>
            <a className="button login__secondary login__google" href={googleSignInHref(providers, next)}>
              <GoogleMark />
              Sign in with Google
            </a>
          </>
        ) : null}

        {/*
          Every destination here is unauthenticated by necessity. The redemption
          link is the one people arrive at from a message and would not otherwise
          find, and it matters that they can: an invitation whose URL was mangled
          in a chat client still has a code that can be pasted.
        */}
        <p className="login__note">
          <Link to="/forgot-password">Forgotten your password?</Link>
          {' · '}
          <Link to="/redeem">Have an invitation code?</Link>
        </p>

        {/*
          NEW CUSTOMERS, AND A CONTROL RATHER THAN A LINE OF PROSE.

          This was a sentence in the muted note style, sitting third in a stack
          of small grey links below the button — which is to say it was invisible
          to the one person on this screen who has no account at all. Somebody
          with no account is not scanning for a password reset; they are looking
          for a way in, and if they do not find it here there is no other route
          into the product.

          So it is a button, and the SECOND button on the card: outlined rather
          than filled, because signing in is still the action almost every
          visitor wants. Same control, same size, same rhythm as everything else
          on the card — an alternative, not a competitor.
        */}
        {providers.signup.enabled ? (
          <>
            <p className="login__divider">New to AccessLink?</p>

            <Link className="button login__secondary" to="/register">
              Create an account
            </Link>
          </>
        ) : null}
      </form>
    </main>
  )
}

/*
  Google's "G", drawn inline so the CSP stays at 'self' and nothing is fetched
  from a third party on the login screen. Decorative: the link's text is its
  name.
*/
function GoogleMark() {
  return (
    <svg className="login__google-mark" aria-hidden="true" width="18" height="18" viewBox="0 0 48 48">
      <path fill="#EA4335" d="M24 9.5c3.5 0 6.6 1.2 9 3.6l6.8-6.8C35.7 2.5 30.2 0 24 0 14.6 0 6.5 5.4 2.6 13.3l7.9 6.1C12.4 13.6 17.7 9.5 24 9.5z" />
      <path fill="#4285F4" d="M46.5 24.5c0-1.6-.1-3.1-.4-4.5H24v9h12.7c-.6 3-2.3 5.5-4.8 7.2l7.7 6c4.5-4.2 6.9-10.3 6.9-17.7z" />
      <path fill="#FBBC05" d="M10.5 28.6A14.5 14.5 0 0 1 9.7 24c0-1.6.3-3.1.8-4.6l-7.9-6.1A24 24 0 0 0 0 24c0 3.9.9 7.5 2.6 10.7l7.9-6.1z" />
      <path fill="#34A853" d="M24 48c6.2 0 11.5-2 15.3-5.6l-7.7-6c-2.1 1.4-4.7 2.2-7.6 2.2-6.3 0-11.6-4.1-13.5-9.9l-7.9 6.1C6.5 42.6 14.6 48 24 48z" />
    </svg>
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
