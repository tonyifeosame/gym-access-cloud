import { useEffect, useRef, useState } from 'react'

/**
 * Copies a value to the clipboard and says so.
 *
 * THE CONFIRMATION IS THE FEATURE. Copying a one-time credential is a step an
 * operator cannot verify by looking at the screen — the clipboard is invisible —
 * so a button that silently succeeds leaves them unsure whether to move on. The
 * state change is announced as well as drawn, because someone using a screen
 * reader has even less to go on.
 *
 * FAILURE IS REPORTED, NOT SWALLOWED. `navigator.clipboard` is unavailable over
 * plain HTTP and can be refused by permissions policy; a button that appears to
 * work and did not is how a provisioning key gets lost. When it fails the value
 * is selected instead, so the operator can copy it by hand, and they are told to.
 */
export function CopyButton({
  value,
  label = 'Copy',
  onCopied,
  describedBy,
}: {
  value: string
  label?: string
  /** Told whether the copy actually succeeded. */
  onCopied?: (ok: boolean) => void
  describedBy?: string
}) {
  const [state, setState] = useState<'idle' | 'copied' | 'failed'>('idle')
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(
    () => () => {
      if (timer.current) clearTimeout(timer.current)
    },
    [],
  )

  async function copy() {
    let ok = false
    try {
      // Optional chaining rather than a feature test up front: jsdom and older
      // browsers differ in whether the object exists at all.
      await navigator.clipboard?.writeText(value)
      ok = true
    } catch {
      ok = false
    }

    setState(ok ? 'copied' : 'failed')
    onCopied?.(ok)

    if (timer.current) clearTimeout(timer.current)
    // The failure notice stays: it needs acting on, and a message that vanishes
    // is one the operator may never have seen.
    if (ok) {
      timer.current = setTimeout(() => setState('idle'), 4000)
    }
  }

  return (
    <span className="copy">
      <button
        type="button"
        className="button button--quiet"
        onClick={() => void copy()}
        aria-describedby={describedBy}
      >
        {state === 'copied' ? 'Copied' : label}
      </button>
      {/* Always present, so the announcement is not competing with the element
          being inserted at the same moment. */}
      <span className="copy__status" role="status" aria-live="polite">
        {state === 'copied' ? 'Copied to clipboard' : ''}
        {state === 'failed'
          ? 'Could not reach the clipboard. Select the value and copy it manually.'
          : ''}
      </span>
    </span>
  )
}
