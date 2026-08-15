import { useCallback, useEffect, useId, useRef, type ReactNode } from 'react'
import { createPortal } from 'react-dom'

/**
 * A modal dialog.
 *
 * Built by hand rather than on `<dialog>`: `showModal()` is well supported but
 * its focus and scroll behaviour still differ between engines in ways that show
 * up as a trapped screen reader, and the accessibility contract below is the
 * whole point of having this component at all.
 *
 * WHAT IT GUARANTEES, all of which are requirements rather than polish:
 *
 *   - Focus MOVES IN when it opens, to the first focusable control or the
 *     dialog itself. A modal that leaves focus behind it is invisible to anyone
 *     not using a mouse.
 *   - Focus is TRAPPED while it is open, cycling at both ends, so Tab cannot
 *     walk into the page behind.
 *   - Focus RETURNS to whatever opened it on close. Losing it dumps a keyboard
 *     user back at the top of the document with their place gone.
 *   - Escape closes it, and so does a click on the backdrop.
 *   - The background does not scroll underneath.
 *   - It is announced: role="dialog", aria-modal, and a title wired through
 *     aria-labelledby.
 *
 * `dismissible` exists for the one case where those defaults are wrong: a
 * destructive confirmation mid-flight should not vanish because Escape was
 * pressed reflexively.
 */

const FOCUSABLE = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled]):not([type="hidden"])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(',')

function focusableWithin(container: HTMLElement): HTMLElement[] {
  return Array.from(container.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
    (element) => element.offsetParent !== null || element === document.activeElement,
  )
}

export interface DialogProps {
  open: boolean
  title: string
  /** Optional supporting line under the title, wired to aria-describedby. */
  description?: ReactNode
  children?: ReactNode
  /** Rendered in the footer. Put the primary action last, as platforms do. */
  footer?: ReactNode
  onClose: () => void
  /** False while a destructive action is in flight. Default true. */
  dismissible?: boolean
  /** Widens the panel for content like a settings editor. */
  size?: 'default' | 'wide'
  /** Tones the header for a destructive confirmation. */
  tone?: 'default' | 'danger'
}

export function Dialog({
  open,
  title,
  description,
  children,
  footer,
  onClose,
  dismissible = true,
  size = 'default',
  tone = 'default',
}: DialogProps) {
  const panelRef = useRef<HTMLDivElement | null>(null)
  const restoreFocusTo = useRef<HTMLElement | null>(null)
  const titleId = useId()
  const descriptionId = useId()

  const requestClose = useCallback(() => {
    if (dismissible) onClose()
  }, [dismissible, onClose])

  // Remember what had focus BEFORE the dialog mounts anything of its own.
  useEffect(() => {
    if (!open) return
    restoreFocusTo.current = document.activeElement as HTMLElement | null
    return () => {
      // The opener may itself have been removed while the dialog was open --
      // deleting a row from its own confirmation, for instance -- so check that
      // it is still in the document before handing focus back to a detached node.
      const target = restoreFocusTo.current
      if (target && document.contains(target)) {
        target.focus()
      }
    }
  }, [open])

  // Move focus in.
  useEffect(() => {
    if (!open) return
    const panel = panelRef.current
    if (!panel) return
    const [first] = focusableWithin(panel)
    ;(first ?? panel).focus()
  }, [open])

  // Escape to close, Tab to cycle.
  useEffect(() => {
    if (!open) return

    function onKeyDown(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        event.stopPropagation()
        requestClose()
        return
      }
      if (event.key !== 'Tab') return

      const panel = panelRef.current
      if (!panel) return
      const focusable = focusableWithin(panel)
      if (focusable.length === 0) {
        // Nothing to move between; keep focus on the panel rather than letting
        // it escape to the page behind.
        event.preventDefault()
        panel.focus()
        return
      }

      const first = focusable[0] as HTMLElement
      const last = focusable[focusable.length - 1] as HTMLElement
      const active = document.activeElement

      if (event.shiftKey && (active === first || active === panel)) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && active === last) {
        event.preventDefault()
        first.focus()
      }
    }

    document.addEventListener('keydown', onKeyDown, true)
    return () => document.removeEventListener('keydown', onKeyDown, true)
  }, [open, requestClose])

  // Hold the page still behind the dialog.
  useEffect(() => {
    if (!open) return
    const previous = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => {
      document.body.style.overflow = previous
    }
  }, [open])

  if (!open) return null

  return createPortal(
    <div
      className="dialog-layer"
      // A backdrop click closes, but only when the backdrop ITSELF was clicked.
      // Without the target check, releasing a drag that began inside the panel
      // closes the dialog and discards whatever was being typed.
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) requestClose()
      }}
    >
      <div
        className={`dialog dialog--${size}${tone === 'danger' ? ' dialog--danger' : ''}`}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={description ? descriptionId : undefined}
        tabIndex={-1}
        ref={panelRef}
      >
        {/*
          A DIV, NOT A <header>. `<header>` maps to the `banner` landmark
          unless it is inside article/aside/main/nav/section — and a
          div[role="dialog"] is none of those, so this became a SECOND banner
          for as long as the dialog was open. A screen reader user then has two
          "banner" entries in their landmark list with no way to tell which is
          the site chrome. The same trap is documented on PageHeader, which is
          how it was found here.

          The <h2> below carries this block's meaning in the outline, and does
          it without a landmark role. The footer is a div for the same reason:
          `<footer>` maps to `contentinfo`.
        */}
        <div className="dialog__header">
          <h2 className="dialog__title" id={titleId}>
            {title}
          </h2>
          {dismissible ? (
            <button
              type="button"
              className="dialog__close"
              onClick={onClose}
              aria-label="Close dialog"
            >
              <span aria-hidden="true">×</span>
            </button>
          ) : null}
        </div>

        {description ? (
          <p className="dialog__description" id={descriptionId}>
            {description}
          </p>
        ) : null}

        {children ? <div className="dialog__body">{children}</div> : null}

        {footer ? <div className="dialog__footer">{footer}</div> : null}
      </div>
    </div>,
    document.body,
  )
}
