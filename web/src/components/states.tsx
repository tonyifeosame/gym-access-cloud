import type { ReactNode } from 'react'

import { ApiError } from '../api/client'

/** The three states every asynchronous view needs, in one place so they look alike. */

export function LoadingState({ label = 'Loading…' }: { label?: string }) {
  return (
    <div className="state state--loading" role="status" aria-live="polite">
      <span className="spinner" aria-hidden="true" />
      <p>{label}</p>
    </div>
  )
}

export function EmptyState({
  title,
  description,
  action,
}: {
  title: string
  description?: string
  action?: ReactNode
}) {
  return (
    <div className="state state--empty">
      <h2>{title}</h2>
      {description ? <p>{description}</p> : null}
      {action}
    </div>
  )
}

/**
 * A failed request.
 *
 * Shows the request id when there is one. Every response carries X-Request-ID,
 * the server logs the same id against the failure, and it is exposed
 * cross-origin specifically so an operator reporting a problem can quote
 * something that leads straight to the log line.
 *
 * The API's error strings are explicitly not stable, so they are shown as-is and
 * never parsed; anything conditional keys off the status code.
 */
export function ErrorState({
  error,
  title = 'Something went wrong',
  onRetry,
}: {
  error: unknown
  title?: string
  onRetry?: () => void
}) {
  const apiError = error instanceof ApiError ? error : null
  const message =
    apiError?.message ??
    (error instanceof Error ? error.message : 'The request could not be completed.')

  return (
    <div className="state state--error" role="alert">
      <h2>{title}</h2>
      <p>{message}</p>
      {apiError?.requestId ? (
        <p className="state__meta">
          Reference <code>{apiError.requestId}</code>
        </p>
      ) : null}
      {onRetry ? (
        <button type="button" className="button" onClick={onRetry}>
          Try again
        </button>
      ) : null}
    </div>
  )
}
