import { useEffect, useId, useRef, useState } from 'react'

import type { CredentialToken } from '../api/types'
import { CopyButton } from './CopyButton'
import { Timestamp } from './Timestamp'

/**
 * A one-time invitation or password-reset link.
 *
 * THE TOKEN IS A CREDENTIAL, and everything here follows from that plus one
 * more fact: THE PLATFORM DOES NOT DELIVER IT. There is no transactional email,
 * so the administrator who minted this link is the delivery mechanism. If they
 * close this panel without copying it, the link is gone and they must issue
 * another one.
 *
 * So this borrows the site-credential panel's deliberately obstructive shape —
 * warning first, explicit acknowledgement, no accidental dismissal — for the
 * same reason: the cost of a reflexive close is a credential nobody has.
 *
 * WHY A LINK AND NOT JUST THE TOKEN. The token alone is what the API takes, and
 * it is offered below for an administrator who would rather dictate it over the
 * phone. But the thing an operator can actually be sent is a URL, and building
 * it here — rather than leaving each caller to concatenate one — is what keeps
 * the redemption route in a single place.
 *
 * THE URL CARRIES A SECRET, AND THAT IS A REAL TRADE, not an oversight. It is
 * how every invitation flow works, because the recipient has nothing else to
 * authenticate with. The exposure is contained at the other end: the redemption
 * page strips the token from the address bar on arrival, so it does not linger
 * in browser history, in a Referer header, or on the screen behind whoever is
 * setting their password. See RedeemPage.
 *
 * WHAT THIS MUST NEVER BE CHANGED TO DO: write the token to localStorage,
 * sessionStorage, the React Query cache, or a log. It lives in a prop for the
 * life of this panel, and the caller resets the mutation that produced it when
 * the panel closes.
 */

/** The console's redemption route. One definition, used to build every link. */
export const REDEEM_PATH = '/redeem'

/**
 * The link an operator is sent.
 *
 * `origin` is a parameter rather than read from `window` inside the component so
 * that this is a pure function a test can assert on, and so a deployment serving
 * the console from a path prefix has somewhere to say so.
 */
export function redeemUrl(token: string, origin: string): string {
  return `${origin.replace(/\/+$/, '')}${REDEEM_PATH}?token=${encodeURIComponent(token)}`
}

export function HandoverLinkPanel({
  token,
  operatorName,
  delivery,
  onDismiss,
}: {
  token: CredentialToken
  /** Who the link is for. Named so it cannot be sent to the wrong person. */
  operatorName: string
  /** The server's own words about delivery. Shown rather than paraphrased. */
  delivery?: string
  onDismiss: () => void
}) {
  const [acknowledged, setAcknowledged] = useState(false)
  const [copied, setCopied] = useState(false)
  const acknowledgeId = useId()
  const warningId = useId()
  const headingRef = useRef<HTMLHeadingElement | null>(null)

  // Focus the heading when the panel appears. Without this a keyboard user is
  // still on the button that submitted the form, several elements away from a
  // credential they have one chance to read.
  useEffect(() => {
    headingRef.current?.focus()
  }, [])

  const invitation = token.purpose === 'INVITE'
  const url = redeemUrl(token.token, window.location.origin)

  return (
    <section
      className="credential"
      role="alertdialog"
      aria-modal="false"
      aria-labelledby={`${warningId}-heading`}
      aria-describedby={warningId}
    >
      <h2 className="credential__heading" id={`${warningId}-heading`} tabIndex={-1} ref={headingRef}>
        {invitation ? 'Invitation link' : 'Password reset link'} for {operatorName}
      </h2>

      <p className="credential__warning" id={warningId}>
        <strong>This link is shown once and cannot be recovered.</strong>{' '}
        {delivery ??
          'Send it to them over a channel you trust; the platform does not deliver it.'}{' '}
        Anyone holding this link can set the password on that account, so treat it
        as the credential it is.
      </p>

      <div className="credential__value">
        <label className="field__label" htmlFor={`${warningId}-link`}>
          Link to send
        </label>
        <div className="credential__row">
          {/* readOnly rather than disabled: a disabled input cannot be focused or
              selected, which removes the manual fallback when the clipboard is
              unavailable. */}
          <input
            id={`${warningId}-link`}
            className="field__input field__input--mono credential__input"
            value={url}
            readOnly
            spellCheck={false}
            onFocus={(event) => event.currentTarget.select()}
          />
          <CopyButton value={url} label="Copy link" onCopied={setCopied} describedBy={warningId} />
        </div>
        <p className="field__hint">
          Expires <Timestamp value={token.expires_at} relative />. Issuing another
          link for this account invalidates this one.
        </p>
      </div>

      {/*
        The bare token, for an administrator who would rather read it out than
        send a URL. Secondary on purpose — the link is what most people need —
        but present, because dictating a token over the phone is a legitimate
        out-of-band channel and inventing a worse one is what happens otherwise.
      */}
      <details className="credential__alternate">
        <summary>Send the code instead of a link</summary>
        <div className="credential__row">
          <input
            className="field__input field__input--mono credential__input"
            value={token.token}
            readOnly
            aria-label="Redemption code"
            spellCheck={false}
            onFocus={(event) => event.currentTarget.select()}
          />
          <CopyButton value={token.token} label="Copy code" />
        </div>
        <p className="field__hint">
          They enter this at <code>{REDEEM_PATH}</code> on this console.
        </p>
      </details>

      <div className="credential__acknowledge">
        <div className="checkbox">
          <input
            id={acknowledgeId}
            type="checkbox"
            className="checkbox__input"
            checked={acknowledged}
            onChange={(event) => setAcknowledged(event.target.checked)}
          />
          <label className="checkbox__label" htmlFor={acknowledgeId}>
            I have copied this link and will send it to {operatorName}
          </label>
        </div>

        <button
          type="button"
          className="button button--primary"
          onClick={onDismiss}
          disabled={!acknowledged}
        >
          Done
        </button>
      </div>

      {/* A nudge, not a gate: they may have copied it by hand. */}
      {acknowledged || copied ? null : (
        <p className="credential__nudge">You have not copied the link yet.</p>
      )}
    </section>
  )
}
