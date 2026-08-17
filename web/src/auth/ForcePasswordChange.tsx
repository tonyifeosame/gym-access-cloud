import { useState, type FormEvent } from 'react'

import { ApiError } from '../api/client'
import * as endpoints from '../api/endpoints'
import { useSession } from '../session/useSession'

/** Mirrors models.MinPasswordLength. The server is the authority. */
const MIN_PASSWORD_LENGTH = 12

/**
 * The interstitial an operator sees when their password was chosen by somebody
 * else.
 *
 * THE SERVER REPORTS THIS AND DELIBERATELY DOES NOT ENFORCE IT. It cannot:
 * `must_change_password` marks a session whose credential is known to a third
 * party, and an account refused every request could not reach
 * /auth/password either — which is the one thing it needs to do. So the
 * enforcement lives here, in front of the console, and the server's own comment
 * says as much.
 *
 * THAT MAKES THIS THE ONE PLACE A FRONTEND GUARD IS THE MECHANISM RATHER THAN A
 * COURTESY, and it is worth being precise about what it is and is not. It is not
 * a security boundary: somebody who wanted to skip it could call the API
 * directly, and the API would let them, because they are legitimately
 * authenticated. What it stops is the ordinary case — an operator who redeemed
 * an administrator-set password and would otherwise carry on using it
 * indefinitely, with the administrator still knowing it. That case is the whole
 * of the risk, and a block in the console is the correct instrument for it.
 *
 * SO IT IS A BLOCK, NOT A BANNER. There is no "later" button. A dismissible
 * prompt is dismissed, and the credential somebody else chose stays live.
 *
 * The only way out other than changing the password is signing out, which is
 * offered — an operator who reached this screen by accident on a shared machine
 * must be able to leave without setting a password on somebody else's account.
 */
export function ForcePasswordChange() {
  const { session, logout, refresh } = useSession()

  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirmation, setConfirmation] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  const mismatch = confirmation.length > 0 && confirmation !== next
  const tooShort = next.length > 0 && next.length < MIN_PASSWORD_LENGTH
  const reusing = next.length > 0 && next === current
  const ready =
    current.length > 0 &&
    next.length >= MIN_PASSWORD_LENGTH &&
    confirmation === next &&
    !reusing

  async function onSubmit(event: FormEvent) {
    event.preventDefault()
    if (!ready) return

    setSubmitting(true)
    setError(null)

    try {
      await endpoints.changePassword(current, next)
      setCurrent('')
      setNext('')
      setConfirmation('')
      // Refetch the session: must_change_password is cleared server-side by the
      // change, and this screen is rendered from that flag. Without the refresh
      // the operator would still be looking at it.
      await refresh()
    } catch (caught) {
      setError(describeFailure(caught))
      setSubmitting(false)
    }
  }

  return (
    <div className="login">
      <form className="login__card" onSubmit={(event) => void onSubmit(event)}>
        <h1 className="login__title">Choose your own password</h1>
        <p className="login__subtitle">
          {session?.operator.email}
        </p>

        {/*
          The reason, stated. "You must change your password" with no
          explanation reads as bureaucracy and gets the shortest legal string
          typed into it; "somebody else knows this one" does not.
        */}
        <p className="login__note">
          The password you signed in with was set by somebody else — from an
          invitation or an administrative reset. They still know it. Choose one of
          your own before continuing.
        </p>

        {error ? (
          <p className="login__error" role="alert">
            {error}
          </p>
        ) : null}

        <label className="field">
          <span className="field__label">Current password</span>
          <input
            className="field__input"
            type="password"
            name="current-password"
            autoComplete="current-password"
            required
            value={current}
            onChange={(event) => setCurrent(event.target.value)}
          />
        </label>

        {/* The hint is a DESCRIPTION, not part of the name — see RedeemPage. */}
        <label className="field" htmlFor="force-new-password">
          <span className="field__label">New password</span>
          <input
            id="force-new-password"
            className="field__input"
            type="password"
            name="new-password"
            autoComplete="new-password"
            required
            value={next}
            onChange={(event) => setNext(event.target.value)}
            aria-describedby="force-password-hint"
          />
        </label>
        <p className="field__hint" id="force-password-hint">
          At least {MIN_PASSWORD_LENGTH} characters.
        </p>

        <label className="field">
          <span className="field__label">Confirm new password</span>
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

        <p className="field__error" aria-live="polite">
          {mismatch
            ? 'The two passwords do not match.'
            : reusing
              ? 'That is the password you were given. Choose a different one.'
              : tooShort
                ? `Password must be at least ${MIN_PASSWORD_LENGTH} characters.`
                : ''}
        </p>

        <button className="button button--primary" type="submit" disabled={!ready || submitting}>
          {submitting ? 'Saving…' : 'Set my password'}
        </button>

        {/* The only other way out. Someone on a shared machine must be able to
            leave without setting a password on an account that is not theirs. */}
        <button
          className="button button--quiet"
          type="button"
          disabled={submitting}
          onClick={() => void logout()}
        >
          Sign out instead
        </button>
      </form>
    </div>
  )
}

function describeFailure(caught: unknown): string {
  if (!(caught instanceof ApiError)) {
    return 'Could not reach AccessLink. Check your connection and try again.'
  }
  // 401 here means the CURRENT password was wrong, not that the session ended —
  // the API answers the same way for both, and telling them apart is the
  // difference between "retype it" and "sign in again".
  if (caught.isUnauthenticated) return 'That is not your current password.'
  if (caught.isRateLimited) return 'Too many attempts. Wait a few minutes and try again.'
  return caught.message
}
