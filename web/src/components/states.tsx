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

/**
 * A page heading.
 *
 * Every screen gets exactly one `<h1>`, which is what a screen reader's document
 * outline is built from and how "skip to content" lands somewhere useful.
 * `actions` sits with the heading rather than floating above the content, so the
 * tab order runs title → actions → content in the order they read.
 */
export function PageHeader({
  title,
  lead,
  actions,
  breadcrumb,
}: {
  title: string
  lead?: ReactNode
  actions?: ReactNode
  breadcrumb?: ReactNode
}) {
  return (
    <header className="page__header">
      {breadcrumb ? <div className="page__breadcrumb">{breadcrumb}</div> : null}
      <div className="page__heading">
        <div>
          <h1>{title}</h1>
          {lead ? <p className="page__lead">{lead}</p> : null}
        </div>
        {actions ? <div className="page__actions">{actions}</div> : null}
      </div>
    </header>
  )
}

/**
 * A note that is not a failure.
 *
 * For the states that are legitimate but need explaining — a capability that is
 * switched off, a scope the API cannot narrow. Distinct from ErrorState on
 * purpose: presenting a working state as an error teaches operators to ignore
 * error styling.
 */
export function InfoNote({
  title,
  children,
  tone = 'muted',
}: {
  title?: string
  children: ReactNode
  tone?: 'muted' | 'warning'
}) {
  return (
    <div className={`notice${tone === 'warning' ? ' notice--warning' : ' notice--muted'}`}>
      {title ? <h2 className="notice__title">{title}</h2> : null}
      <div>{children}</div>
    </div>
  )
}

/**
 * A quiet indicator for a refetch that is NOT blanking the page.
 *
 * Paired with keeping stale content on screen: the operator sees the previous
 * answer plus "updating", rather than a spinner where their data used to be.
 */
export function RefreshingIndicator({ active }: { active: boolean }) {
  if (!active) return null
  return (
    <span className="refreshing" role="status">
      <span className="spinner spinner--inline" aria-hidden="true" />
      <span className="visually-hidden">Updating</span>
    </span>
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
