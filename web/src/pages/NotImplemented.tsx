import { useParams } from 'react-router-dom'

import { implementationOf } from '../applications/readiness'
import { findApplicationBySlug } from '../applications/registry'
import { useAuthenticatedSession } from '../session/useSession'

/**
 * The placeholder every screen shows in Phase 1.
 *
 * Deliberately explicit about WHY there is nothing here. A blank page reads as a
 * failure; naming the thing and saying it is not built yet is honest and stops
 * the console looking broken while the foundation is being laid.
 */
export function NotImplemented({
  title,
  description,
  detail,
}: {
  title: string
  description?: string
  detail?: string
}) {
  return (
    <div className="page">
      <header className="page__header">
        <h1>{title}</h1>
        {description ? <p className="page__lead">{description}</p> : null}
      </header>

      <div className="notice">
        <h2 className="notice__title">Not implemented yet</h2>
        <p>
          {detail ??
            'This screen is not built yet. The navigation entry is here because the platform reports this area as available to you.'}
        </p>
      </div>
    </div>
  )
}

/**
 * The page behind an application module's navigation entry.
 *
 * Resolves the capability from the URL rather than from a hard-coded route per
 * module, so a capability this build has never heard of still lands somewhere
 * sensible instead of a 404.
 */
export function ApplicationPlaceholder() {
  const { slug = '' } = useParams()
  const session = useAuthenticatedSession()
  const definition = findApplicationBySlug(slug)

  const label =
    definition?.label ??
    slug
      .split('-')
      .filter(Boolean)
      .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
      .join(' ')

  const enabled = session.applications.some(
    (application) => application.code === definition?.code || slugMatches(application.code, slug),
  )

  if (!enabled) {
    // Reached by typing a URL for a capability the company has not enabled.
    // Not an error page: the operator has done nothing wrong, and the capability
    // may simply be switched off.
    return (
      <div className="page">
        <header className="page__header">
          <h1>{label}</h1>
        </header>
        <div className="notice notice--muted">
          <h2 className="notice__title">Not enabled</h2>
          <p>
            This application is not enabled for {session.company.name}. An owner can turn it on
            from Applications.
          </p>
        </div>
      </div>
    )
  }

  /*
    THE SPECIFIC GAP, NOT A GENERIC "COMING SOON". An operator who followed a
    navigation entry here has already been told the capability is enabled, and
    "screens are not built yet" invites them to assume the WORK is happening
    somewhere and only the view is missing. For every capability except access
    control and registration, nothing is happening at all — and saying which is
    the difference between an honest gap and a misleading one.
  */
  const readiness = definition ? implementationOf(definition.code) : null

  return (
    <div className="page">
      <header className="page__header">
        <h1>{label}</h1>
        {definition?.description ? (
          <p className="page__lead">{definition.description}</p>
        ) : null}
      </header>

      <div className="notice notice--warning">
        <h2 className="notice__title">
          {readiness?.status === 'PARTIAL'
            ? 'Partly built, with no screens yet'
            : 'Not built yet'}
        </h2>
        {readiness?.gap ? <p>{readiness.gap}</p> : null}
        <p>
          This capability is enabled for {session.company.name}, so terminals may
          be assigned to it and it appears in your navigation. It has no screens
          of its own yet.
        </p>
      </div>
    </div>
  )
}

function slugMatches(code: string, slug: string): boolean {
  return code.toLowerCase().replace(/_/g, '-') === slug
}
