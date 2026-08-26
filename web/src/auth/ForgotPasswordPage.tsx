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
          {/*
            THE HEADING NAMES A NEXT STEP RATHER THAN A PERSON.

            It read "Check with your administrator", which is the one thing that
            is not true for the reader most likely to be here. Self-service
            signup creates a company with exactly ONE account — its owner — so
            for a great many people on this screen "your administrator" is
            themselves, and the page sent them to nobody.
          */}
          <h1 className="login__title">How to get back in</h1>

          {/*
            UNCHANGED, AND IT MUST STAY UNCHANGED. This sentence is rendered on
            success and on failure alike and does not depend on the response, so
            that a stranger typing addresses learns nothing about which of them
            exist. Everything below is generic advice for the same reason: none
            of it varies with who the address belongs to.
          */}
          <p className="login__subtitle">
            If that address belongs to an operator account, a reset has been issued.
          </p>

          {/*
            The uncomfortable sentence, kept because it is true and because an
            operator told "check your email" would wait indefinitely for a
            message this platform has no way to send. What follows it is now two
            real routes rather than one that assumes a colleague.
          */}
          <p className="login__note">
            <strong>AccessLink does not send email</strong>, so the link has to reach
            you another way. There are two, depending on your company:
          </p>

          <ul className="login__routes">
            <li>
              <strong>Somebody else runs your account with you.</strong> Ask an owner
              or administrator in your company to issue you a reset link — they can do
              it from Operators in their console, and it is the fastest route.
            </li>
            <li>
              {/*
                THE ROUTE THAT DID NOT EXIST. A sole owner has nobody to ask, and
                before the platform gained a recovery route the honest answer was
                that they were locked out for good. Support can now issue this
                link for a company that has only one owner or administrator.
              */}
              <strong>You are the only owner or administrator.</strong> Contact
              AccessLink support. They can issue a single-use link for the one account
              that administers your company, which is exactly this case — nobody else
              can do it for you.
            </li>
          </ul>

          {/*
            THE ADVICE THAT PREVENTS A SECOND OCCURRENCE, and the only sentence
            here aimed at the reader's future rather than their present. It costs
            one line and it is the difference between needing support once and
            needing them every time.
          */}
          <p className="login__note">
            Once you are back in, adding a second owner or administrator means you can
            always reset each other without waiting for anybody.
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
