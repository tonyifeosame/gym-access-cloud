import { useEffect, useId, useState, type ReactNode } from 'react'

import { ApiError } from '../api/client'
import { Dialog } from './Dialog'

/**
 * Confirmation before something that cannot be quietly undone.
 *
 * THE CONSEQUENCE IS THE POINT, not the ceremony. "Are you sure?" tells an
 * operator nothing they did not already know; what they need is what will happen
 * that they might not have expected — that deleting a person reaches every
 * terminal in the company, that replacing site settings reconfigures hardware in
 * the field. `consequence` is therefore a required prop rather than an optional
 * flourish, so a caller cannot ship a dialog that only asks.
 *
 * `confirmPhrase` adds a typed confirmation for the genuinely irreversible. It
 * is deliberately not the default: asking someone to type a name for a routine
 * action trains them to type it without reading, which costs the protection
 * exactly when it is needed.
 *
 * The dialog OWNS THE IN-FLIGHT AND ERROR STATE of the action. Every caller
 * otherwise reinvents "disable the button, show a spinner, keep the dialog open
 * if it failed" — and the one that gets it wrong closes on failure, leaving the
 * operator believing something happened that did not.
 */

export interface ConfirmDialogProps {
  open: boolean
  title: string
  /** What will happen. Required — see the note above. */
  consequence: ReactNode
  /** Extra detail: what is NOT affected, or how to reverse it. */
  detail?: ReactNode
  confirmLabel?: string
  cancelLabel?: string
  /** Exact text the operator must type. Reserve for the irreversible. */
  confirmPhrase?: string
  /**
   * Why this action cannot be taken right now. Present means REFUSED.
   *
   * FOR WHEN THE CONSOLE CANNOT ESTABLISH WHAT THE ACTION WOULD DO — not for a
   * permission, which should not have offered the control at all, and not for a
   * validation error, which belongs on the field.
   *
   * The case this exists for is the firmware rollout: its confirmation counts
   * the terminals a promotion would reach, and that count comes from a separate
   * request. When that request fails, "0 affected" and "we do not know" are the
   * same value — and the second must never be confirmable, because the operator
   * would be agreeing to something nobody has described to them.
   *
   * Rendered where the typed phrase would be and disables the confirm button, so
   * a blocked dialog still says what the action WOULD do; it simply will not let
   * it happen yet.
   */
  blocked?: ReactNode
  tone?: 'danger' | 'default'
  /**
   * Extra input the action itself needs — a reason for the audit trail, most
   * obviously.
   *
   * Rendered ABOVE the typed confirmation, so the order reads: what will happen,
   * what you want to record about it, then the deliberate act of confirming. A
   * field placed after the phrase would be filled in after the operator has
   * already committed mentally, and usually not at all.
   *
   * The caller owns this state. It cannot live here, because only the caller
   * knows what to do with it.
   */
  children?: ReactNode
  onConfirm: () => Promise<unknown> | unknown
  onClose: () => void
}

export function ConfirmDialog({
  open,
  title,
  consequence,
  detail,
  confirmLabel = 'Confirm',
  cancelLabel = 'Cancel',
  confirmPhrase,
  blocked,
  tone = 'danger',
  children,
  onConfirm,
  onClose,
}: ConfirmDialogProps) {
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [typed, setTyped] = useState('')
  const phraseId = useId()

  // Reopening must not inherit the previous attempt's error or half-typed
  // phrase; a stale error above a fresh question reads as a failure that has
  // just happened.
  useEffect(() => {
    if (open) {
      setError(null)
      setTyped('')
      setPending(false)
    }
  }, [open])

  const phraseSatisfied = !confirmPhrase || typed.trim() === confirmPhrase
  // `blocked` outranks everything, including a correctly typed phrase: it means
  // the console cannot describe the action, and a phrase confirms a description.
  const canConfirm = phraseSatisfied && !pending && !blocked

  async function confirm() {
    if (!canConfirm) return
    setPending(true)
    setError(null)
    try {
      await onConfirm()
      onClose()
    } catch (caught) {
      // Stay open. Closing here would leave the operator believing the action
      // succeeded, which for a destructive action is the worst available outcome.
      setError(caught)
      setPending(false)
    }
  }

  return (
    <Dialog
      open={open}
      title={title}
      tone={tone}
      dismissible={!pending}
      onClose={onClose}
      description={consequence}
      footer={
        <>
          <button
            type="button"
            className="button button--quiet"
            onClick={onClose}
            disabled={pending}
          >
            {cancelLabel}
          </button>
          <button
            type="button"
            className={`button ${tone === 'danger' ? 'button--danger' : 'button--primary'}`}
            onClick={() => void confirm()}
            disabled={!canConfirm}
          >
            {pending ? 'Working…' : confirmLabel}
          </button>
        </>
      }
    >
      {detail ? <p className="confirm__detail">{detail}</p> : null}

      {children}

      {blocked ? (
        <div className="notice notice--danger" role="status">
          <h3 className="notice__title">This cannot be confirmed yet</h3>
          <div>{blocked}</div>
        </div>
      ) : null}

      {confirmPhrase ? (
        <div className="field">
          <label className="field__label" htmlFor={phraseId}>
            Type <code>{confirmPhrase}</code> to confirm
          </label>
          <input
            id={phraseId}
            className="field__input"
            value={typed}
            onChange={(event) => setTyped(event.target.value)}
            autoComplete="off"
            spellCheck={false}
            disabled={pending}
          />
        </div>
      ) : null}

      {error ? (
        <p className="confirm__error" role="alert">
          {error instanceof ApiError || error instanceof Error
            ? error.message
            : 'The action could not be completed.'}
          {error instanceof ApiError && error.requestId ? (
            <>
              {' '}
              <span className="state__meta">
                Reference <code>{error.requestId}</code>
              </span>
            </>
          ) : null}
        </p>
      ) : null}
    </Dialog>
  )
}
