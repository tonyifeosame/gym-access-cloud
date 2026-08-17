import { useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'

import { ApiError } from '../api/client'
import * as endpoints from '../api/endpoints'

/**
 * "I have forgotten my password."
 *
 * UNAUTHENTICATED BY NECESSITY — somebody who cannot sign in cannot sign in to
 * ask for help — and therefore a permanently exposed surface that takes an email
 * address. Everything about this screen follows from that.
 *
 * THE ANSWER IS THE SAME WHETHER OR NOT THE ACCOUNT EXISTS. The API returns 202
 * with an identical body in every case, deliberately, and this page must not be
 * more helpful than that. A screen that said "no account with that address"
 * would be an enumeration oracle for every address anybody wanted to test, and
 * it would be one this console had reintroduced after the server went to the
 * trouble of refusing to be one. So the confirmation below is rendered on
 * SUCCESS AND ON FAILURE ALIKE, and does not depend on the response at all.
 *
 * AND THE PLATFORM CANNOT ACTUALLY DELIVER ANYTHING YET. There is no
 * transactional email. The server mints a token and writes it to its own
 * operational log, which means a self-service reset completes only if somebody
 * with log access finishes it by hand. Saying so plainly is the only honest
 * option: a page that showed a confident "check your inbox" would be telling
 * every locked-out operator to wait for something that is never coming.
 */
export function ForgotPasswordPage() {
  const [email, setEmail] = useState('')
  const [submitted, setSubmitted] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [rateLimited, setRateLimited] = useState<number | null>(null)

  async function onSubmit(event: FormEvent) {
    event.preventDefault()
    setSubmitting(true)
    setRateLimited(null)

    try {
      await endpoints.requestPasswordReset(email)
    } catch (caught) {
      // 429 is the ONE failure worth surfacing, because it is about the request
      // rather than about the account: the limiter is shared with login, and an
      // operator hammering the button needs to know to stop. Every other error
      // is swallowed into the same confirmation — including a 500, because
      // reporting it would tell an attacker their probe reached the store.
      if (caught instanceof ApiError && caught.isRateLimited) {
        setRateLimited(caught.retryAfterSeconds ?? 60)
        setSubmitting(false)
        return
      }
    }

    setSubmitting(false)
    setSubmitted(true)
  }

  if (submitted) {
    return (
      <main className="login">
        <div className="login__card">
          <h1 className="login__title">Check with your administrator</h1>

          <p className="login__subtitle">
            If that address belongs to an operator account, a reset has been issued.
          </p>

          {/*
            The uncomfortable sentence, and the reason it is here rather than in
            a release note: an operator told "check your email" would wait
            indefinitely for a message this platform has no way to send.
          */}
          <p className="login__note">
            <strong>AccessLink does not send email.</strong> The reset link is recorded
            for your platform administrator, who has to pass it to you. If you have an
            administrator or owner in your own company, the fastest route is to ask
            them to issue you a reset link directly.
          </p>

          <p className="login__note">
            <Link to="/login">Back to sign in</Link>
          </p>
        </div>
      </main>
    )
  }

  return (
    <main className="login">
      <form className="login__card" onSubmit={(event) => void onSubmit(event)}>
        <h1 className="login__title">Reset your password</h1>
        <p className="login__subtitle">
          Enter the address you sign in with and we will issue a reset.
        </p>

        {rateLimited !== null ? (
          <p className="login__error" role="alert">
            Too many attempts. Try again in {formatWait(rateLimited)}.
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

        <button className="button button--primary" type="submit" disabled={submitting}>
          {submitting ? 'Requesting…' : 'Request a reset'}
        </button>

        <p className="login__note">
          <Link to="/login">Back to sign in</Link>
        </p>
      </form>
    </main>
  )
}

function formatWait(seconds: number): string {
  if (seconds < 60) return `${seconds} seconds`
  const minutes = Math.ceil(seconds / 60)
  return `${minutes} ${minutes === 1 ? 'minute' : 'minutes'}`
}
