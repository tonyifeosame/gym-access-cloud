import { useEffect, useId, useRef, useState, type ReactNode } from 'react'

import { CopyButton } from './CopyButton'

/**
 * A secret shown once.
 *
 * THE SAME DESIGN AS THE SITE PROVISIONING KEY PANEL (pages/sites/CredentialPanel),
 * generalised so the integration-credential screens do not copy it. The rules
 * are the site panel's, and they are the whole point of having one component
 * for every credential the console ever renders:
 *
 *   - The warning is the first thing in the panel, not a footnote under the
 *     value. Somebody scanning for the thing to copy reads it on the way.
 *   - Dismissing REQUIRES A DELIBERATE ACKNOWLEDGEMENT. A plain close button
 *     would be pressed reflexively; a checkbox that must be ticked forces the
 *     sentence to be read at least once.
 *   - The CALLER mounts it in a non-dismissible dialog: no Escape, no backdrop
 *     click. Those are the two ways a dialog gets closed by accident, and here
 *     that loses a credential.
 *
 * WHAT THIS COMPONENT DOES NOT DO, and must never be changed to do: write the
 * secret to localStorage, sessionStorage, a URL, a query parameter, the React
 * Query cache, or a log. It holds it in a prop for the life of the panel. The
 * caller resets the mutation that produced it when the panel closes, which is
 * what makes the value genuinely gone rather than merely off-screen.
 */
export function SecretPanel({
  heading,
  warning,
  label,
  secret,
  prefix,
  extra,
  acknowledgement = 'I have stored this key somewhere safe',
  onDismiss,
}: {
  heading: string
  /** The consequence, in full. Rendered before the value on purpose. */
  warning: ReactNode
  /** The label on the value itself -- the console's own name for this secret. */
  label: string
  secret: string
  /** The non-secret identifier that logs and lists will show for it. */
  prefix?: string
  /** Rendered above the acknowledgement -- a rotation warning goes here. */
  extra?: ReactNode
  acknowledgement?: string
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
        {heading}
      </h2>

      <p className="credential__warning" id={warningId}>
        {warning}
      </p>

      {extra}

      <div className="credential__value">
        <label className="field__label" htmlFor={`${warningId}-secret`}>
          {label}
        </label>
        <div className="credential__row">
          {/*
            readOnly rather than disabled: a disabled input is not focusable and
            its text cannot be selected, which removes the manual fallback when
            the clipboard is unavailable.
          */}
          <input
            id={`${warningId}-secret`}
            className="field__input field__input--mono credential__input"
            value={secret}
            readOnly
            spellCheck={false}
            autoComplete="off"
            onFocus={(event) => event.currentTarget.select()}
          />
          <CopyButton value={secret} label="Copy key" onCopied={setCopied} describedBy={warningId} />
        </div>
        {prefix ? (
          <p className="field__hint">
            Identified in logs and in this console as <code>{prefix}…</code>
          </p>
        ) : null}
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
            {acknowledgement}
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
