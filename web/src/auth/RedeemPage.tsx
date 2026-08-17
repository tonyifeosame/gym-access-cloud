import { useEffect, useState, type FormEvent } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'

import { ApiError } from '../api/client'
import * as endpoints from '../api/endpoints'

/** Mirrors models.MinPasswordLength. The server is the authority. */
const MIN_PASSWORD_LENGTH = 12

/**
 * Setting a password from an invitation or a reset link.
 *
 * UNAUTHENTICATED, because the token IS the authorisation: somebody redeeming an
 * invitation has never had a password and somebody redeeming a reset has
 * forgotten theirs.
 *
 * THE TOKEN ARRIVES IN THE QUERY STRING, AND THE FIRST THING THIS PAGE DOES IS
 * TAKE IT BACK OUT. That is not decoration. A secret in a URL leaks in ways the
 * person holding it never sees: into browser history, into a Referer header on
 * any outbound navigation, into a screenshot, onto the screen behind somebody
 * typing a password in an open-plan office, and into any session-recording or
 * analytics script that logs locations. `history.replaceState` on arrival closes
 * most of that at the cost of nothing — the value is already in component state
 * by then, and it was never going to be readable again anyway.
 *
 * It cannot be avoided entirely: a link is the only thing the recipient can be
 * sent, and they have nothing else to authenticate with. So the exposure is
 * contained rather than pretended away, and a code can be pasted instead for
 * anybody who prefers to receive it out of band.
 *
 * REDEMPTION DOES NOT SIGN ANYBODY IN. The API answers 204, not a session, and
 * this page sends them to the login screen. That is the right shape: the new
 * credential gets exercised once, immediately, while the person who set it is
 * still there to notice if it did not work.
 *
 * THE THREE FAILURES ARE REPORTED DISTINCTLY — unknown, expired, already used —
 * branched on STATUS rather than on the message, because the API's strings are
 * explicitly not stable. That is not an enumeration risk: whoever is holding the
 * token already holds the secret, and "this link has expired" is the difference
 * between asking for a new one and concluding the product is broken.
 */
export function RedeemPage() {
  const [params] = useSearchParams()
  const navigate = useNavigate()

  const [token, setToken] = useState(() => params.get('token') ?? '')
  const [password, setPassword] = useState('')
  const [confirmation, setConfirmation] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const [done, setDone] = useState(false)

  // Strip the secret from the address bar. replaceState rather than a router
  // navigation so no history entry is created that still carries it.
  useEffect(() => {
    if (!params.get('token')) return
    window.history.replaceState(null, '', window.location.pathname)
  }, [params])

  const arrivedWithLink = token.length > 0

  const mismatch = confirmation.length > 0 && confirmation !== password
  const tooShort = password.length > 0 && password.length < MIN_PASSWORD_LENGTH
  const ready =
    token.trim().length > 0 && password.length >= MIN_PASSWORD_LENGTH && confirmation === password

  async function onSubmit(event: FormEvent) {
    event.preventDefault()
    if (!ready) return

    setSubmitting(true)
    setError(null)

    try {
      await endpoints.redeemCredential(token.trim(), password)
      setDone(true)
      // Nothing is kept once it has been spent.
      setToken('')
      setPassword('')
      setConfirmation('')
    } catch (caught) {
      setError(describeFailure(caught))
    } finally {
      setSubmitting(false)
    }
  }

  if (done) {
    return (
      <main className="login">
        <div className="login__card">
          <h1 className="login__title">Password set</h1>
          <p className="login__subtitle">
            Sign in with your new password. Any other sessions on this account have
            been signed out.
          </p>
          <button
            className="button button--primary"
            type="button"
            onClick={() => navigate('/login', { replace: true })}
          >
            Go to sign in
          </button>
        </div>
      </main>
    )
  }

  return (
    <main className="login">
      <form className="login__card" onSubmit={(event) => void onSubmit(event)}>
        <h1 className="login__title">Set your password</h1>
        <p className="login__subtitle">
          {arrivedWithLink
            ? 'Choose a password for your AccessLink account.'
            : 'Paste the code you were sent, then choose a password.'}
        </p>

        {error ? (
          <p className="login__error" role="alert">
            {error}
          </p>
        ) : null}

        {/*
          Shown only when the page was NOT opened from a link. Rendering a
          populated field containing the token would put the secret back on
          screen, which is the thing the URL strip above just prevented.
        */}
        {!arrivedWithLink ? (
          <label className="field">
            <span className="field__label">Code</span>
            <input
              className="field__input field__input--mono"
              type="text"
              name="token"
              autoComplete="off"
              spellCheck={false}
              required
              value={token}
              onChange={(event) => setToken(event.target.value)}
            />
          </label>
        ) : null}

        {/*
          The hint sits OUTSIDE the <label> and is tied on with
          aria-describedby. Nesting it would fold "At least 12 characters" into
          the control's accessible NAME, so a screen reader would announce the
          field as "New password At least 12 characters" — a name and a
          description are different things and conflating them is the classic
          way this markup goes wrong.
        */}
        <label className="field" htmlFor="redeem-password">
          <span className="field__label">New password</span>
          <input
            id="redeem-password"
            className="field__input"
            type="password"
            name="new-password"
            autoComplete="new-password"
            required
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            aria-describedby="redeem-password-hint"
          />
        </label>
        <p className="field__hint" id="redeem-password-hint">
          At least {MIN_PASSWORD_LENGTH} characters.
        </p>

        <label className="field">
          <span className="field__label">Confirm password</span>
          <input
            className="field__input"
            type="password"
            name="confirm-password"
            autoComplete="new-password"
            required
            value={confirmation}
            onChange={(event) => setConfirmation(event.target.value)}
            aria-invalid={mismatch || undefined}
          />
        </label>

        {/* A live region so the mismatch is ANNOUNCED, not merely drawn. */}
        <p className="field__error" aria-live="polite">
          {mismatch
            ? 'The two passwords do not match.'
            : tooShort
              ? `Password must be at least ${MIN_PASSWORD_LENGTH} characters.`
              : ''}
        </p>

        <button className="button button--primary" type="submit" disabled={!ready || submitting}>
          {submitting ? 'Setting…' : 'Set password'}
        </button>

        <p className="login__note">
          <Link to="/login">Back to sign in</Link>
        </p>
      </form>
    </main>
  )
}

function describeFailure(caught: unknown): string {
  if (!(caught instanceof ApiError)) {
    return 'Could not reach AccessLink. Check your connection and try again.'
  }

  // Branched on status, never on the message: the API's error strings are
  // explicitly not stable, and each of these tells the person a different thing
  // to do next.
  switch (caught.status) {
    case 404:
      return 'That link is not valid. Ask whoever sent it to issue a new one — a newer link may have replaced it.'
    case 410:
      return 'That link has expired. Ask whoever sent it to issue a new one.'
    case 409:
      return 'That link has already been used. If it was not you, tell your administrator immediately.'
    case 429:
      return 'Too many attempts. Wait a few minutes and try again.'
    case 400:
      return caught.message
    default:
      return caught.status >= 500
        ? 'AccessLink is not responding. Your link has not been used — try again shortly.'
        : caught.message
  }
}
