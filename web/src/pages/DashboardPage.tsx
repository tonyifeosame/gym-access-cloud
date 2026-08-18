import { Link } from 'react-router-dom'

import type { FleetSummary, Terminal } from '../api/types'
import { describeApplication } from '../applications/registry'
import { can } from '../auth/permissions'
import { roleLabel } from '../auth/roles'
import { Badge } from '../components/Badge'
import { ErrorState, InfoNote, LoadingState, PageHeader } from '../components/states'
import { ALL_SITES, useSiteContext } from '../context/SiteContext'
import {
  usePendingTerminals,
  usePeople,
  useSites,
  useTerminalSummary,
  useTerminals,
} from '../data/console'
import { useAuthenticatedSession, useSession } from '../session/useSession'

/**
 * The operational overview.
 *
 * EVERY NUMBER ON THIS PAGE IS ONE THE API ACTUALLY COMPUTES. Nothing is
 * estimated, extrapolated or assembled from a partial page. Where a figure an
 * operations dashboard would normally carry does not exist -- recent activity,
 * how many people hold a credential -- the page says so plainly instead of
 * inventing something plausible. A dashboard that guesses is worse than one that
 * admits a gap, because the guess is indistinguishable from a fact.
 *
 * IT IS ALSO HONEST ABOUT WHAT ENABLING A CAPABILITY DOES. The platform records
 * which capabilities a company wants and what each terminal is pointed at, and
 * evaluates none of them yet. An overview that listed "Attendance" beside a
 * terminal count would imply attendance is being recorded. It is not, and this
 * page says so.
 *
 * DOMAIN-NEUTRAL THROUGHOUT. People, terminals, sites, applications. The same
 * screen has to read correctly for a school, a depot, a factory, a venue and a
 * residential block, so it names none of them.
 */
export function DashboardPage() {
  const session = useAuthenticatedSession()
  const { selected, selectedSite, allSites, grants } = useSiteContext()

  const sites = useSites()
  const summary = useTerminalSummary()
  const terminals = useTerminals()
  // limit=1 fetches ONE row to read `total` from the envelope. The count is the
  // server's, over the whole roster; the page itself is not used.
  const people = usePeople({ limit: 1 })
  const pending = usePendingTerminals()

  const scopedToOneSite = selected !== ALL_SITES && selectedSite !== null

  // Fleet figures come from the server's own rollup when the view is
  // scope-wide, and from the terminal list when narrowed to one site. Both are
  // grant-scoped and derived from the same data; the list is complete for the
  // caller's scope (it is not paginated), so filtering it is exact rather than
  // an approximation of a page.
  const siteTerminals = scopedToOneSite
    ? (terminals.data?.terminals ?? []).filter(
        (terminal) => terminal.site_public_id === selected,
      )
    : null
  const fleet = siteTerminals ? countFleet(siteTerminals) : summary.data

  const fleetLoading = scopedToOneSite ? terminals.isPending : summary.isPending
  const fleetError = scopedToOneSite ? terminals.error : summary.error

  const attention = collectAttention({
    fleet,
    terminals: siteTerminals ?? terminals.data?.terminals ?? [],
    inactiveSites: (sites.data?.sites ?? []).filter((site) => !site.active).length,
    applicationCount: session.applications.length,
    peopleTotal: people.data?.total,
    // COMPANY-WIDE rather than narrowed by the site selector, deliberately: an
    // announcement has no site until it is approved, so there is no honest way
    // to filter it by one. Showing it whatever the selector says is the correct
    // reading — it genuinely is not "at" the selected site yet.
    pendingTerminals: pending.data?.count ?? 0,
  })

  return (
    <div className="page">
      <PageHeader
        title="Overview"
        lead={
          <>
            {session.company.name} · {session.operator.full_name} ({roleLabel(session.role)})
          </>
        }
      />

      {/* --- context -------------------------------------------------------- */}
      <section className="panel" aria-labelledby="dashboard-context-heading">
        <div className="panel__header">
          <h2 className="panel__title" id="dashboard-context-heading">
            Your context
          </h2>
        </div>
        <dl className="detail-list">
          <div className="detail-list__row">
            <dt>Company</dt>
            <dd>{session.company.name}</dd>
          </div>
          <div className="detail-list__row">
            <dt>Site access</dt>
            <dd>
              {allSites ? (
                'Every site in this company'
              ) : grants.length === 0 ? (
                'No sites'
              ) : (
                <>
                  {grants.length} site{grants.length === 1 ? '' : 's'}:{' '}
                  {grants.map((grant) => grant.site_name).join(', ')}
                </>
              )}
            </dd>
          </div>
          <div className="detail-list__row">
            <dt>Showing</dt>
            <dd>
              {scopedToOneSite ? (
                <>
                  {selectedSite?.site_name} only — change this with the site
                  selector above.
                </>
              ) : (
                'Everything you can reach'
              )}
            </dd>
          </div>
        </dl>
      </section>

      {/* --- attention ------------------------------------------------------ */}
      {attention.length > 0 ? (
        <section className="panel" aria-labelledby="dashboard-attention-heading">
          <div className="panel__header">
            <h2 className="panel__title" id="dashboard-attention-heading">
              Needs your attention
            </h2>
          </div>
          <ul className="attention-list">
            {attention.map((item) => (
              <li key={item.id} className={`attention attention--${item.tone}`}>
                <div>
                  <p className="attention__title">{item.title}</p>
                  <p className="attention__detail">{item.detail}</p>
                </div>
                {item.href ? (
                  <Link to={item.href} className="button button--quiet">
                    {item.action}
                  </Link>
                ) : null}
              </li>
            ))}
          </ul>
        </section>
      ) : null}

      {/* --- counts --------------------------------------------------------- */}
      <section className="tiles" aria-label="Platform totals">
        <Tile
          label="Sites"
          value={sites.data?.sites.length}
          detail={
            sites.data
              ? `${sites.data.sites.filter((site) => site.active).length} active`
              : undefined
          }
          loading={sites.isPending}
          href="/sites"
        />
        <Tile
          label={scopedToOneSite ? 'Terminals here' : 'Terminals'}
          value={fleet?.total}
          detail={fleet ? `${fleet.online} online` : undefined}
          loading={fleetLoading}
          href="/terminals"
        />
        <Tile
          label="People"
          value={people.data?.total}
          // Company-wide always: the schema has no person-to-site relationship,
          // so this figure cannot be narrowed and must not appear to be.
          detail={scopedToOneSite ? 'company-wide' : undefined}
          loading={people.isPending}
          href="/people"
        />
        <Tile
          label="Applications enabled"
          value={session.applications.length}
          detail={`of ${session.applications.length === 0 ? 'none yet' : 'the platform catalog'}`}
          href={can(session, 'manageOperators') ? '/settings/applications' : undefined}
        />
      </section>

      {/* --- fleet health --------------------------------------------------- */}
      <section className="panel" aria-labelledby="dashboard-fleet-heading">
        <div className="panel__header">
          <h2 className="panel__title" id="dashboard-fleet-heading">
            Terminal health
          </h2>
        </div>

        {fleetError ? (
          <ErrorState
            error={fleetError}
            onRetry={() => void (scopedToOneSite ? terminals.refetch() : summary.refetch())}
          />
        ) : fleetLoading ? (
          <LoadingState label="Loading terminal health…" />
        ) : !fleet || fleet.total === 0 ? (
          <p className="field__hint">
            No terminals {scopedToOneSite ? 'at this site' : 'yet'}. A terminal appears
            here once it has been registered against one of your sites.
          </p>
        ) : (
          <dl className="detail-list">
            <HealthRow label="Online" value={fleet.online} total={fleet.total} />
            <HealthRow label="Offline" value={fleet.offline} total={fleet.total} />
            <HealthRow label="Reporting an error" value={fleet.error} total={fleet.total} />
            <HealthRow label="Updating" value={fleet.updating} total={fleet.total} />
            <HealthRow label="Provisioning" value={fleet.provisioning} total={fleet.total} />
            <HealthRow label="Disabled" value={fleet.disabled} total={fleet.total} />
            <HealthRow
              label="Behind on firmware"
              value={fleet.firmware_outdated}
              total={fleet.total}
            />
          </dl>
        )}
      </section>

      {/* --- applications --------------------------------------------------- */}
      <section className="panel" aria-labelledby="dashboard-applications-heading">
        <div className="panel__header">
          <h2 className="panel__title" id="dashboard-applications-heading">
            Enabled applications
          </h2>
        </div>

        {session.applications.length === 0 ? (
          <p className="field__hint">
            No capabilities are enabled for {session.company.name}. That is a normal
            starting state — the platform works as an identity and terminal
            management system without any.
          </p>
        ) : (
          <>
            <ul className="chip-list">
              {session.applications.map((application) => {
                const definition = describeApplication(application.code)
                return (
                  <li key={application.code}>
                    <span className="chip">{definition.label}</span>
                  </li>
                )
              })}
            </ul>
            {/*
              The honest caveat, and it belongs here rather than buried in
              Applications: a list of enabled capabilities on an operations
              dashboard reads as a list of things that are happening.
            */}
            <InfoNote tone="warning" title="These are not running yet">
              Enabling a capability records what your company intends to use
              AccessLink for, and lets a terminal be assigned to it. The platform
              does not yet evaluate these workflows on its own — no attendance is
              being calculated, and no access decisions are being made against
              them.
            </InfoNote>
          </>
        )}
      </section>

      {/* --- activity ------------------------------------------------------- */}
      <section className="panel" aria-labelledby="dashboard-activity-heading">
        <div className="panel__header">
          <h2 className="panel__title" id="dashboard-activity-heading">
            Recent activity
          </h2>
        </div>
        {/*
          Deliberately empty rather than filled with something adjacent. Door
          events are recorded, but only reachable with a site provisioning key,
          which a browser must never hold -- so this console genuinely cannot
          show them. Nor is there any audit trail of operator actions.
        */}
        <p className="field__hint">
          Not available in this console yet. Terminals record door events, but there
          is no operator-facing feed for them, and no audit trail of changes made
          here. Both are planned.
        </p>
      </section>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Pieces
// ---------------------------------------------------------------------------

function Tile({
  label,
  value,
  detail,
  loading,
  href,
}: {
  label: string
  value: number | undefined
  detail?: string
  loading?: boolean
  href?: string
}) {
  const body = (
    <>
      <p className="tile__label">{label}</p>
      {/* An em dash while loading or unavailable, never a zero. "0 terminals"
          is a claim, and it is the alarming one to make by accident. */}
      <p className="tile__value">{loading || value === undefined ? '—' : value}</p>
      {detail ? <p className="tile__description">{detail}</p> : null}
    </>
  )

  return href ? (
    <Link to={href} className="tile tile--link">
      {body}
    </Link>
  ) : (
    <article className="tile">{body}</article>
  )
}

function HealthRow({ label, value, total }: { label: string; value: number; total: number }) {
  return (
    <div className="detail-list__row">
      <dt>{label}</dt>
      <dd>
        {value}
        {total > 0 && value > 0 ? (
          <span className="muted"> of {total}</span>
        ) : null}
      </dd>
    </div>
  )
}

/** Rolls a terminal list into the same shape the summary endpoint returns. */
export function countFleet(terminals: Terminal[]): FleetSummary {
  const count = (status: string) => terminals.filter((t) => t.status === status).length
  return {
    total: terminals.length,
    online: count('ONLINE'),
    offline: count('OFFLINE'),
    updating: count('UPDATING'),
    error: count('ERROR'),
    disabled: count('DISABLED'),
    provisioning: count('PROVISIONING'),
    firmware_outdated: terminals.filter((t) => t.firmware_outdated).length,
  }
}

export interface AttentionItem {
  id: string
  tone: 'danger' | 'warning' | 'info'
  title: string
  detail: string
  href?: string
  action?: string
}

/**
 * What an operator should look at, derived ONLY from facts the API reports.
 *
 * Ordered by how much it matters: hardware faults first, then things that are
 * silently not working, then configuration that is merely incomplete. Nothing
 * here is a heuristic about health — every item restates something the platform
 * actually said.
 */
export function collectAttention({
  fleet,
  terminals,
  inactiveSites,
  applicationCount,
  peopleTotal,
  pendingTerminals = 0,
}: {
  fleet: FleetSummary | undefined
  terminals: Terminal[]
  inactiveSites: number
  applicationCount: number
  peopleTotal: number | undefined
  /** Terminals that have announced themselves and are waiting to be approved. */
  pendingTerminals?: number
}): AttentionItem[] {
  const items: AttentionItem[] = []

  // HIGH, and above everything except an actual fault, because somebody is
  // usually standing next to the hardware while this is true. A terminal
  // waiting for approval is a person waiting, and the wait ends the moment
  // anybody looks at this list.
  if (pendingTerminals > 0) {
    items.push({
      id: 'terminals-waiting',
      tone: 'warning',
      title: `${pendingTerminals} terminal${pendingTerminals === 1 ? '' : 's'} waiting to be set up`,
      detail:
        'A terminal has connected and is showing a code on its screen. It will not open any doors until it is approved.',
      href: '/terminals',
      action: 'Set up',
    })
  }

  if (fleet && fleet.error > 0) {
    items.push({
      id: 'terminals-error',
      tone: 'danger',
      title: `${fleet.error} terminal${fleet.error === 1 ? '' : 's'} reporting a fault`,
      detail: 'A fault needs attention on site — the terminal has told the platform something is wrong.',
      href: '/terminals',
      action: 'View terminals',
    })
  }

  if (fleet && fleet.offline > 0) {
    items.push({
      id: 'terminals-offline',
      tone: 'warning',
      title: `${fleet.offline} terminal${fleet.offline === 1 ? '' : 's'} offline`,
      detail:
        'An offline terminal keeps working at the door on its local records, but is not receiving changes.',
      href: '/terminals',
      action: 'View terminals',
    })
  }

  // A terminal that has never reported at all is a different problem from one
  // that has gone quiet: it suggests it was registered and never connected.
  const neverReported = terminals.filter((terminal) => !terminal.last_heartbeat_at).length
  if (neverReported > 0) {
    items.push({
      id: 'terminals-never-reported',
      tone: 'warning',
      title: `${neverReported} terminal${neverReported === 1 ? '' : 's'} never reported in`,
      detail: 'Registered, but has never sent a heartbeat. It may not have been able to connect.',
      href: '/terminals',
      action: 'View terminals',
    })
  }

  if (fleet && fleet.firmware_outdated > 0) {
    items.push({
      id: 'terminals-outdated',
      tone: 'info',
      title: `${fleet.firmware_outdated} terminal${fleet.firmware_outdated === 1 ? '' : 's'} behind on firmware`,
      detail: 'Not running the build marked current for their release channel.',
      href: '/terminals',
      action: 'View terminals',
    })
  }

  if (inactiveSites > 0) {
    items.push({
      id: 'sites-inactive',
      tone: 'warning',
      title: `${inactiveSites} site${inactiveSites === 1 ? '' : 's'} deactivated`,
      detail:
        'A deactivated site refuses its provisioning key and all of its terminals. Nothing is deleted.',
      href: '/sites',
      action: 'View sites',
    })
  }

  // THE FIRST STEP FOR A NEW CUSTOMER, and the reason it comes before
  // applications and people: signup creates a company, an owner and Main Site,
  // and then there is nothing. Without a terminal there is no door, so enabling
  // a capability and adding people are both preparation for something that does
  // not exist yet.
  //
  // Suppressed while one is already waiting — the item above is the same job,
  // further along, and showing both would read as two things to do.
  if (fleet && fleet.total === 0 && pendingTerminals === 0) {
    items.push({
      id: 'no-terminals',
      tone: 'info',
      title: 'Add your first terminal',
      // The version is named for the same reason it is named on the terminals
      // page: this instruction is true of firmware 1.2.0 and newer and false of
      // everything shipped before it, and a new customer following it on an
      // older unit waits for a code that never appears.
      detail:
        'Power a terminal on and connect it to Wi-Fi from your phone. On firmware 1.2.0 or newer it shows a code on its screen — add it with that code, no serial number and no cable. An older terminal needs a claim code from its site.',
      href: '/terminals',
      action: 'Add a terminal',
    })
  }

  if (applicationCount === 0) {
    items.push({
      id: 'no-applications',
      tone: 'info',
      title: 'No applications enabled',
      detail:
        'The platform works without any, and every company starts here. Enabling one decides what your terminals may be assigned to.',
      href: '/settings/applications',
      action: 'View applications',
    })
  }

  if (peopleTotal === 0) {
    items.push({
      id: 'no-people',
      tone: 'info',
      title: 'Nobody has been added yet',
      detail: 'Terminals recognise the people recorded here. Without any, there is nobody to recognise.',
      href: '/people',
      action: 'Add people',
    })
  }

  return items
}

/** Exported for the shell, which shows a compact count of the same items. */
export function useAttentionCount(): number {
  const { session } = useSession()
  const summary = useTerminalSummary()
  const terminals = useTerminals()
  const sites = useSites()
  const people = usePeople({ limit: 1 })

  if (!session) return 0
  return collectAttention({
    fleet: summary.data,
    terminals: terminals.data?.terminals ?? [],
    inactiveSites: (sites.data?.sites ?? []).filter((site) => !site.active).length,
    applicationCount: session.applications.length,
    peopleTotal: people.data?.total,
  }).length
}

export { Badge }
