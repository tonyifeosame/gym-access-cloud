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
  tone?: 'danger' | 'default'
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
  tone = 'danger',
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
  const canConfirm = phraseSatisfied && !pending

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
