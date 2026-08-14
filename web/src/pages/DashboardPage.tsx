import { Link } from 'react-router-dom'

import { applicationPath, describeApplication } from '../applications/registry'
import { roleLabel } from '../auth/roles'
import { useSiteContext } from '../context/SiteContext'
import { useAuthenticatedSession } from '../session/useSession'

/**
 * The one screen with real content in Phase 1.
 *
 * It reports what the platform says about this deployment: who is signed in,
 * which company, what site scope they have, and which capabilities are enabled.
 * That is deliberately the whole of it -- it proves the session, the company and
 * site context, and the capability model are wired end to end, without assuming
 * anything about what the company uses AccessLink for.
 */
export function DashboardPage() {
  const session = useAuthenticatedSession()
  const { grants, allSites, selectedSite } = useSiteContext()

  const applications = session.applications.map((application) =>
    describeApplication(application.code),
  )

  return (
    <div className="page">
      <header className="page__header">
        <h1>Overview</h1>
        <p className="page__lead">
          {session.company.name} · signed in as {session.operator.full_name} (
          {roleLabel(session.role)})
        </p>
      </header>

      <section className="cards">
        <article className="card">
          <h2 className="card__title">Site access</h2>
          {allSites ? (
            <p className="card__value">All sites</p>
          ) : (
            <p className="card__value">
              {grants.length} {grants.length === 1 ? 'site' : 'sites'}
            </p>
          )}
          <p className="card__detail">
            {selectedSite ? `Viewing ${selectedSite.site_name}` : 'Viewing every site'}
          </p>
        </article>

        <article className="card">
          <h2 className="card__title">Applications enabled</h2>
          <p className="card__value">{applications.length}</p>
          <p className="card__detail">
            {applications.length === 0
              ? 'This company has not enabled any yet'
              : applications.map((definition) => definition.label).join(', ')}
          </p>
        </article>

        <article className="card">
          <h2 className="card__title">Session</h2>
          <p className="card__value">{formatHours(session.session_expires_in_seconds)}</p>
          <p className="card__detail">until you are signed out</p>
        </article>
      </section>

      <section className="panel">
        <h2 className="panel__title">Applications</h2>

        {applications.length === 0 ? (
          <div className="notice notice--muted">
            <p>
              No applications are enabled for {session.company.name}. AccessLink is a
              general-purpose platform: what a deployment does is configuration, and a company
              with nothing enabled is a valid starting point rather than a problem.
            </p>
            <p>An owner can enable capabilities from Applications.</p>
          </div>
        ) : (
          <ul className="tiles">
            {applications.map((definition) => (
              <li key={definition.code}>
                <Link className="tile" to={applicationPath(definition)}>
                  <span className="tile__label">{definition.label}</span>
                  <span className="tile__description">{definition.description}</span>
                </Link>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  )
}

function formatHours(seconds: number): string {
  if (seconds <= 0) return 'Expired'
  const hours = Math.round(seconds / 3600)
  if (hours < 1) return '<1 hour'
  if (hours < 48) return `${hours} hours`
  return `${Math.round(hours / 24)} days`
}
