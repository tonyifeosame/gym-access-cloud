import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react'

import { ApiError } from '../api/client'

/**
 * Transient notifications.
 *
 * ANNOUNCED, NOT JUST DRAWN. The container is a live region, so a confirmation
 * reaches someone who is not watching that corner of the screen. Success is
 * `polite` and failure is `assertive`: an operator should be interrupted when a
 * change did not take effect, and not when it did.
 *
 * ERRORS DO NOT AUTO-DISMISS. A success message that disappears is fine — the
 * screen behind it already shows the new state. A failure that disappears leaves
 * someone who looked away believing their change went through, so failures stay
 * until dismissed.
 *
 * This is for feedback ABOUT AN ACTION JUST TAKEN. It is not where a form's
 * validation goes (that belongs beside the field) and not where a failed page
 * load goes (that belongs in the page, with a retry).
 */

export type NotificationTone = 'success' | 'error' | 'info'

export interface Notification {
  id: string
  tone: NotificationTone
  message: string
  /** Correlates a failure with the server log. */
  requestId?: string | null
}

export interface NotificationsContextValue {
  notifications: Notification[]
  notify: (notification: Omit<Notification, 'id'>) => void
  success: (message: string) => void
  /** Pulls the message and request id off an ApiError automatically. */
  failure: (message: string, error?: unknown) => void
  dismiss: (id: string) => void
}

const NotificationsContext = createContext<NotificationsContextValue | null>(null)

/**
 * How long a non-error notification stays.
 *
 * Injectable rather than a fixed constant so a test can assert the dismissal
 * behaviour without taking over the clock — fake timers have to be threaded
 * through the mock service worker and user-event as well, and the resulting
 * test proves more about that plumbing than about this component.
 */
const AUTO_DISMISS_MS = 6000

export function NotificationsProvider({
  children,
  autoDismissMs = AUTO_DISMISS_MS,
}: {
  children: ReactNode
  autoDismissMs?: number
}) {
  const [notifications, setNotifications] = useState<Notification[]>([])
  const counter = useRef(0)
  const timers = useRef(new Map<string, ReturnType<typeof setTimeout>>())

  const dismiss = useCallback((id: string) => {
    setNotifications((current) => current.filter((entry) => entry.id !== id))
    const timer = timers.current.get(id)
    if (timer) {
      clearTimeout(timer)
      timers.current.delete(id)
    }
  }, [])

  const notify = useCallback(
    (notification: Omit<Notification, 'id'>) => {
      counter.current += 1
      const id = `notification-${counter.current}`
      setNotifications((current) => [...current, { ...notification, id }])

      if (notification.tone !== 'error') {
        timers.current.set(
          id,
          setTimeout(() => dismiss(id), autoDismissMs),
        )
      }
    },
    [autoDismissMs, dismiss],
  )

  const success = useCallback(
    (message: string) => notify({ tone: 'success', message }),
    [notify],
  )

  const failure = useCallback(
    (message: string, error?: unknown) => {
      const detail = error instanceof ApiError ? error.message : null
      notify({
        tone: 'error',
        // The API's strings are for humans and are never parsed; appending one
        // to our own sentence gives the operator both the context and the cause.
        message: detail ? `${message} ${detail}` : message,
        requestId: error instanceof ApiError ? error.requestId : null,
      })
    },
    [notify],
  )

  // Timers outlive the component if a notification is on screen at unmount.
  useEffect(() => {
    const pending = timers.current
    return () => {
      pending.forEach((timer) => clearTimeout(timer))
      pending.clear()
    }
  }, [])

  const value = useMemo<NotificationsContextValue>(
    () => ({ notifications, notify, success, failure, dismiss }),
    [dismiss, failure, notifications, notify, success],
  )

  return (
    <NotificationsContext.Provider value={value}>
      {children}
      <NotificationList notifications={notifications} onDismiss={dismiss} />
    </NotificationsContext.Provider>
  )
}

function NotificationList({
  notifications,
  onDismiss,
}: {
  notifications: Notification[]
  onDismiss: (id: string) => void
}) {
  const failures = notifications.filter((entry) => entry.tone === 'error')
  const rest = notifications.filter((entry) => entry.tone !== 'error')

  return (
    <div className="notifications">
      {/* Two regions, because politeness cannot vary per message inside one. */}
      <div aria-live="assertive" aria-relevant="additions">
        {failures.map((entry) => (
          <NotificationItem key={entry.id} notification={entry} onDismiss={onDismiss} />
        ))}
      </div>
      <div aria-live="polite" aria-relevant="additions">
        {rest.map((entry) => (
          <NotificationItem key={entry.id} notification={entry} onDismiss={onDismiss} />
        ))}
      </div>
    </div>
  )
}

function NotificationItem({
  notification,
  onDismiss,
}: {
  notification: Notification
  onDismiss: (id: string) => void
}) {
  return (
    <div className={`notification notification--${notification.tone}`}>
      <div className="notification__body">
        <p className="notification__message">{notification.message}</p>
        {notification.requestId ? (
          <p className="state__meta">
            Reference <code>{notification.requestId}</code>
          </p>
        ) : null}
      </div>
      <button
        type="button"
        className="notification__dismiss"
        onClick={() => onDismiss(notification.id)}
        aria-label="Dismiss notification"
      >
        <span aria-hidden="true">×</span>
      </button>
    </div>
  )
}

export function useNotifications(): NotificationsContextValue {
  const context = useContext(NotificationsContext)
  if (!context) {
    throw new Error('useNotifications must be used inside a NotificationsProvider')
  }
  return context
}
