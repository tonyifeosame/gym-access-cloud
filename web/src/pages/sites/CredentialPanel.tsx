import { useEffect, useId, useRef, useState } from 'react'

import type { SiteCredential } from '../../api/types'
import { CopyButton } from '../../components/CopyButton'

/**
 * The one-time provisioning key.
 *
 * This is the only place in the console where a site API key is ever rendered,
 * and everything about it is shaped by two facts: the key registers terminals
 * and rotates their device credentials, and it cannot be recovered. If the
 * operator closes this panel without storing it, their only remedy is to rotate
 * — which invalidates the key they just failed to save, and cuts off any
 * terminal still depending on it.
 *
 * SO THE DESIGN IS DELIBERATELY OBSTRUCTIVE:
 *
 *   - The warning is the first thing in the panel, not a footnote under the
 *     value. Somebody scanning for the thing to copy reads it on the way.
 *   - Dismissing REQUIRES A DELIBERATE ACKNOWLEDGEMENT. A plain close button
 *     would be pressed reflexively; a checkbox that must be ticked forces the
 *     sentence to be read at least once.
 *   - It is not dismissible by Escape or a backdrop click. Those are the two
 *     ways a dialog gets closed by accident, and here that loses a credential.
 *
 * WHAT THIS COMPONENT DOES NOT DO, and must never be changed to do: write the
 * key to localStorage, sessionStorage, a URL, a query parameter, the React Query
 * cache, or a log. It holds it in a prop for the life of the panel. The caller
 * resets the mutation that produced it when the panel closes, which is what
 * makes the value genuinely gone rather than merely off-screen.
 */
export function CredentialPanel({
  credential,
  siteName,
  context,
  extra,
  onDismiss,
}: {
  credential: SiteCredential
  siteName: string
  /** Distinguishes a freshly created site from a rotated key. */
  context: 'created' | 'rotated'
  /** Rendered above the acknowledgement — the rotation warning goes here. */
  extra?: React.ReactNode
  onDismiss: () => void
}) {
  const [acknowledged, setAcknowledged] = useState(false)
  const [copied, setCopied] = useState(false)
  const acknowledgeId = useId()
  const warningId = useId()
  const headingRef = useRef<HTMLHeadingElement | null>(null)

  // Move focus to the warning when the panel appears. Without this a keyboard
  // user's focus is still on the button that submitted the form, several
  // elements away from a credential they have one chance to read.
  useEffect(() => {
    headingRef.current?.focus()
  }, [])

  return (
    <section
      className="credential"
      role="alertdialog"
      aria-modal="false"
      aria-labelledby={`${warningId}-heading`}
      aria-describedby={warningId}
    >
      <h2 className="credential__heading" id={`${warningId}-heading`} tabIndex={-1} ref={headingRef}>
        {context === 'created'
          ? `Provisioning key for ${siteName}`
          : `New provisioning key for ${siteName}`}
      </h2>

      <p className="credential__warning" id={warningId}>
        <strong>This key is shown once and cannot be recovered.</strong> Store it in a
        password manager now. It is the provisioning secret for this site: anyone holding
        it can register terminals here and reissue their credentials. If it is lost, the
        only remedy is to rotate — which invalidates this key too.
      </p>

      {extra}

      <div className="credential__value">
        <label className="field__label" htmlFor={`${warningId}-key`}>
          Site API key
        </label>
        <div className="credential__row">
          {/*
            readOnly rather than disabled: a disabled input is not focusable and
            its text cannot be selected, which removes the manual fallback when
            the clipboard is unavailable.
          */}
          <input
            id={`${warningId}-key`}
            className="field__input field__input--mono credential__input"
            value={credential.api_key}
            readOnly
            spellCheck={false}
            onFocus={(event) => event.currentTarget.select()}
          />
          <CopyButton
            value={credential.api_key}
            label="Copy key"
            onCopied={setCopied}
            describedBy={warningId}
          />
        </div>
        <p className="field__hint">
          Identified in logs and on this site as <code>{credential.api_key_prefix}…</code>
        </p>
      </div>

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
            I have stored this key somewhere safe
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

      {/* A nudge, not a gate: an operator may legitimately have typed it out or
          copied it by hand, so this never blocks the acknowledgement. */}
      {acknowledged || copied ? null : (
        <p className="credential__nudge">You have not copied the key yet.</p>
      )}
    </section>
  )
}
