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
 *     creates a company has to be able to create the others. The word "owner"
 *     appears once, in the subtitle, as plain English about who they will be.
 *   - It does not ask about a site, and it never shows one's PROVISIONING KEY.
 *     A site is created for them and the key never leaves the server — the
 *     response carries a session and nothing else. An anonymous browser must
 *     never be handed the credential that registers door hardware. The site is
 *     MENTIONED, by the name they will see inside the console, because meeting
 *     "Main Site" here is better than wondering where it came from.
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
        {/*
          WHAT THE FIRST TWO LINES HAVE TO GET ACROSS, in the customer's words
          rather than ours: this creates an account AND sets up their company,
          and they are the person who will own it. Somebody who thinks they are
          joining a company that already exists here will fill this in and be
          confused by what they land in.

          "Tenant", "slug", "operator" and "provisioning" are absent
          deliberately, here and everywhere else on this screen. They are our
          vocabulary; the customer meets the product first and the words for it
          afterwards, inside the console.

          The brand is named once, in the heading. Repeating it in the line
          directly under it reads as a template rather than as a sentence.
        */}
        <h1 className="login__title">Create your AccessLink account</h1>
        <p className="login__subtitle">
          It also sets up your company and makes you its owner.
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

        {/*
          The hint says what this field DOES, because it is the one field on the
          form whose consequence is not obvious: a name typed here becomes the
          company everybody else will be invited into. Tied on with
          aria-describedby rather than nested, for the reason given below.
        */}
        <label className="field" htmlFor="register-company">
          <span className="field__label">Company name</span>
          <input
            id="register-company"
            className="field__input"
            type="text"
            name="organization"
            autoComplete="organization"
            required
            value={companyName}
            onChange={(event) => setCompanyName(event.target.value)}
            aria-describedby="register-company-hint"
          />
        </label>
        <p className="field__hint" id="register-company-hint">
          This is the company your team will join. You can rename it later.
        </p>

        <label className="field">
          {/* "Email", matching the sign-in screen. The same person reads both,
              often minutes apart, and two names for one thing is a needless
              moment of doubt about whether they are the same thing. */}
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

        {/*
          The hint sits OUTSIDE the <label> and is tied on with
          aria-describedby, for the reason set out on RedeemPage: nesting it
          would fold "Use at least 12 characters" into the control's accessible
          NAME, and a name and a description are different things.

          ONE LINE, WHICH BECOMES THE CORRECTION. It was a hint plus a separate
          error that said the same thing in different words, so a short password
          produced two sentences about length and a taller form. Keeping one
          element means the guidance turns into the complaint in place: nothing
          moves, nothing is repeated, and because the element is always present
          and its text changes, the live region announces it properly.
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
        <p
          className={`field__hint${tooShort ? ' field__hint--invalid' : ''}`}
          id="register-password-hint"
          aria-live="polite"
        >
          {tooShort
            ? `That is too short — use at least ${MIN_PASSWORD_LENGTH} characters.`
            : `Use at least ${MIN_PASSWORD_LENGTH} characters.`}
        </p>

        <button className="button button--primary" type="submit" disabled={!ready || submitting}>
          {submitting ? 'Creating your account…' : 'Create account'}
        </button>

        {/*
          What happens the moment they press it, in one sentence.
          "Main Site" is named ON PURPOSE and is the only piece of product
          vocabulary on this screen: it is the first thing they will see inside
          the console, and meeting it here — with permission to rename it — is
          better than wondering where it came from.
        */}
        <p className="login__note">
          You will be signed in straight away, with a first location called
          &ldquo;Main Site&rdquo; to get you started.
        </p>

        {/*
          The mirror of the sign-in card, which offers "Create an account" in
          exactly this position and style. Same card, two actions, swapped.
        */}
        <p className="login__divider">Already have an account?</p>

        <Link className="button login__secondary" to="/login">
          Sign in
        </Link>
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
      // No "deployment", no "self-service", no "instance". Whoever hits this is
      // being told to go and ask a person, so the sentence has to say which
      // person rather than describe how the software is configured.
      return 'New accounts cannot be created here. Ask whoever set up AccessLink for your organisation to invite you.'
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
