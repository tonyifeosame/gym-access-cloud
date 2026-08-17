import { Link } from 'react-router-dom'

import { MULTI_PURPOSE, type ApplicationCode } from '../../api/types'
import { describeApplication } from '../../applications/registry'
import { readinessOf } from '../../applications/readiness'
import { can } from '../../auth/permissions'
import { Badge } from '../../components/Badge'
import { useNotifications } from '../../components/Notifications'
import { ErrorState, InfoNote, LoadingState, PageHeader } from '../../components/states'
import { useApplications, useUpdateApplication } from '../../data/console'
import { useSession } from '../../session/useSession'

/**
 * Which capabilities this company has switched on.
 *
 * FOUR CONCEPTS MEET ON THIS SCREEN AND ARE ROUTINELY CONFUSED. Keeping them
 * apart is most of the design:
 *
 *   Platform capability      what AccessLink can do at all. The server's
 *                            `available` list, not a constant in this build.
 *   Company application      which of those THIS company has enabled. Owned
 *                            here, and the only thing this page writes.
 *   Terminal mode            what ONE terminal is pointed at. Owned on the
 *                            terminal, and never set from here.
 *   Operator permission      who may read or change any of the above.
 *
 * MULTI_PURPOSE IS NOT AN APPLICATION. It is a terminal mode meaning "serve
 * whatever this company has enabled", and the server rejects it as a company
 * capability. It is filtered out defensively below as well, so that a future
 * server which mistakenly listed it could not turn it into a toggle here.
 *
 * THE CATALOG COMES FROM THE SERVER. `available` is the authority, so a
 * capability added to the platform appears on this page without a frontend
 * release — described generically if this build has never heard of it, rather
 * than dropped. A console that hard-coded the list would silently hide part of
 * what a customer is paying for.
 */
export function ApplicationsPage() {
  const { session } = useSession()
  const query = useApplications()
  const update = useUpdateApplication()
  const notifications = useNotifications()

  // Reading is ADMIN in this console. The API itself allows any operator to
  // read, but what a company is FOR is administrative context rather than
  // day-to-day information, and the write is OWNER either way.
  const mayConfigure = can(session, 'configureApplications')

  if (query.isPending) return <LoadingState label="Loading applications…" />
  if (query.isError) {
    return (
      <div className="page">
        <PageHeader title="Applications" />
        <ErrorState error={query.error} onRetry={() => void query.refetch()} />
      </div>
    )
  }

  const { available, enabled } = query.data

  // Defensive: MULTI_PURPOSE must never appear as something a company enables,
  // whatever the server sends.
  const catalog = available.filter((code) => code !== MULTI_PURPOSE)
  const enabledSet = new Set(enabled.filter((code) => code !== MULTI_PURPOSE))

  async function toggle(code: ApplicationCode, next: boolean) {
    const label = describeApplication(code).label
    try {
      await update.mutateAsync({ code, body: { enabled: next } })
      notifications.success(next ? `${label} enabled` : `${label} disabled`)
    } catch (error) {
      notifications.failure(`Could not ${next ? 'enable' : 'disable'} ${label}.`, error)
    }
  }

  return (
    <div className="page">
      <PageHeader
        title="Applications"
        lead="The capabilities your company has enabled. AccessLink is a general-purpose platform — what it does for you is configuration, not a fixed product."
      />

      {/*
        THE MOST IMPORTANT SENTENCE ON THIS PAGE. Nothing in the platform yet
        acts on an enabled capability: no attendance is computed, no access
        decision is evaluated, no check-in is recorded. Enabling one changes what
        the console offers and what a terminal may be assigned to, and nothing
        else. An owner who reads "Access Control" and expects doors to start
        behaving differently has been misled, and this is where that would
        happen.
      */}
      <InfoNote tone="warning" title="Enabled is not the same as operational">
        <p>
          Four different things are easy to confuse here, and only the last one
          affects anybody standing at a door:
        </p>
        <ul className="capability__legend">
          <li>
            <strong>Available</strong> — the platform offers it at all.
          </li>
          <li>
            <strong>Enabled</strong> — your company has switched it on. This
            decides what the console offers and what a terminal may be assigned
            to.
          </li>
          <li>
            <strong>Configured</strong> — settings have been stored for it.
          </li>
          <li>
            <strong>Operational</strong> — the platform actually carries out the
            workflow.
          </li>
        </ul>
        <p>
          Every capability below is marked with where it stands. Enabling one
          that is not yet built changes what this console offers and nothing
          else — no attendance is computed, no access decision is evaluated, no
          check-in is recorded.
        </p>
      </InfoNote>

      {!mayConfigure ? (
        <InfoNote title="Read only">
          You can see what is enabled but not change it. Only an owner decides which
          capabilities a company has.
        </InfoNote>
      ) : null}

      {catalog.length === 0 ? (
        <InfoNote title="No capabilities offered">
          This build of the platform reports no available capabilities.
        </InfoNote>
      ) : (
        <ul className="capability-list">
          {catalog.map((code) => {
            const definition = describeApplication(code)
            const isEnabled = enabledSet.has(code)
            // A code the server offers that this build cannot describe. It is
            // still rendered — humanised and marked — rather than hidden.
            const known = definition.description !== UNKNOWN_DESCRIPTION
            const readiness = readinessOf(code, query.data)

            return (
              <li key={code} className="capability" data-enabled={isEnabled}>
                <div className="capability__main">
                  <h2 className="capability__title">
                    <Link to={`/settings/applications/${definition.slug}`}>
                      {definition.label}
                    </Link>
                    {/*
                      TWO BADGES, NEVER ONE. "Enabled" describes a row in a
                      database; "operational" describes whether the platform
                      does the work. An owner who enables Attendance and sees a
                      single green tick has been told the product records
                      attendance, and it does not.
                    */}
                    {isEnabled ? (
                      <Badge tone="positive">Enabled</Badge>
                    ) : (
                      <Badge>Not enabled</Badge>
                    )}
                    {readiness.operational ? (
                      <Badge tone="positive">Operational</Badge>
                    ) : readiness.implementation === 'PARTIAL' ? (
                      <Badge tone="warning">Partly built</Badge>
                    ) : (
                      <Badge tone="warning">Not built yet</Badge>
                    )}
                    {!known ? <Badge tone="warning">Unrecognised</Badge> : null}
                  </h2>
                  <p className="capability__description">{definition.description}</p>
                  {/*
                    The specific gap, not a generic disclaimer. "Nothing records
                    attendance" is actionable in a way that "some features are
                    in development" is not, and it is what a buyer is entitled
                    to know before they are sold this.
                  */}
                  {readiness.gap ? (
                    <p className="capability__gap">{readiness.gap}</p>
                  ) : null}
                  <p className="capability__meta">
                    <code className="mono">{code}</code>
                    {readiness.configured ? ' · configured' : ' · never configured'}
                  </p>
                </div>

                {mayConfigure ? (
                  <div className="capability__action">
                    <button
                      type="button"
                      className={isEnabled ? 'button' : 'button button--primary'}
                      disabled={update.isPending}
                      onClick={() => void toggle(code, !isEnabled)}
                    >
                      {isEnabled ? 'Disable' : 'Enable'}
                    </button>
                  </div>
                ) : null}
              </li>
            )
          })}
        </ul>
      )}

      <section className="panel" aria-labelledby="applications-model-heading">
        <div className="panel__header">
          <h2 className="panel__title" id="applications-model-heading">
            How this relates to your terminals
          </h2>
        </div>
        <p>
          A capability enabled here becomes available to the whole company. Each
          terminal is then <strong>assigned</strong> to one of them, or left
          multi-purpose to serve all of them — that assignment is made per terminal
          under <Link to="/terminals">Terminals</Link>.
        </p>
        <p className="field__hint">
          Multi-purpose is a terminal setting, not a capability, so it does not
          appear in the list above. Disabling a capability here leaves any terminal
          assigned to it configured but resolving to nothing, rather than silently
          reassigning it.
        </p>
      </section>
    </div>
  )
}

/**
 * The description the registry invents for a code it does not know.
 *
 * Compared against rather than duplicated, so "is this recognised" has one
 * answer and the registry stays the only place that decides it.
 */
export const UNKNOWN_DESCRIPTION = 'This capability is newer than this version of the console.'
