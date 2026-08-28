import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'

import type { AuditRecord, FieldEvent, FleetSummary } from '../api/types'
import { describeApplication } from '../applications/registry'
import { can } from '../auth/permissions'
import { roleAtLeast, roleLabel } from '../auth/roles'
import { Badge, humaniseCode } from '../components/Badge'
import { DataTable, type Column } from '../components/DataTable'
import { Meter } from '../components/Meter'
import { ErrorState, LoadingState, PageHeader } from '../components/states'
import { Timestamp } from '../components/Timestamp'
import { ALL_SITES, useSiteContext } from '../context/SiteContext'
import {
  useAuditEvents,
  useEvents,
  useOnboardingState,
  usePendingTerminals,
  usePeople,
  useSites,
  useTerminalSummary,
  useTerminals,
} from '../data/console'
import { SiteScopeSelect } from '../layout/SiteSwitcher'
import { useAuthenticatedSession } from '../session/useSession'
import { fleetStanding } from './firmware/standing'
import { UNCONFIRMED_TIME_LABEL, describeReason } from './access/accessVocabulary'
import { describeAction } from './activity/auditVocabulary'
import {
  DECISION_TONES,
  type AttentionItem,
  collectAttention,
  countFleet,
  decisionSegments,
  firmwareCompliance,
  fleetSegments,
  neverReported,
  siteDistribution,
  startOfLocalDay,
  type SiteRow,
} from './dashboard/overview'

/**
 * The operational overview.
 *
 * EVERY NUMBER ON THIS PAGE IS ONE THE API ACTUALLY COMPUTES. Nothing is
 * estimated, extrapolated or assembled from a partial page. Where a figure an
 * operations dashboard would normally carry does not exist — how many people
 * hold a credential, how full a terminal's roster is — the page says so plainly
 * instead of inventing something plausible. A dashboard that guesses is worse
 * than one that admits a gap, because the guess is indistinguishable from a
 * fact. What is missing and why is recorded in web/docs/backend-dependencies.md.
 *
 * IT IS ALSO HONEST ABOUT WHAT ENABLING A CAPABILITY DOES. The platform records
 * which capabilities a company wants and what each terminal is pointed at, and
 * evaluates none of them yet. An overview that listed "Attendance" beside a
 * terminal count would imply attendance is being recorded. It is not, and this
 * page says so — in the footer, where a statement of intent belongs, rather than
 * above the numbers.
 *
 * DOMAIN-NEUTRAL THROUGHOUT. People, terminals, sites, applications. The same
 * screen has to read correctly for a school, a depot, a factory, a venue and a
 * residential block, so it names none of them.
 *
 * ---------------------------------------------------------------------------
 * THE SHAPE, AND WHY IT IS THIS SHAPE
 * ---------------------------------------------------------------------------
 *
 * An operator opening this screen asks three questions in order: is anything
 * broken, how big is the thing I run, is it working right now. The regions run
 * in that order, and session context — which company you are in, which
 * capabilities are switched on — sits last, because nobody opens a console to
 * find out who they are.
 *
 * THE PAGE MUST NOT GET EMPTIER AS THE DEPLOYMENT GETS HEALTHIER. The attention
 * list is conditional by design, so when it is absent everything below it has to
 * carry the screen on its own. That is why the fleet, the day's decisions, the
 * door log, the site spread and firmware compliance are all present
 * unconditionally: a well-run deployment should read as calm, not as broken.
 */

/** Rows of the door log on the overview. Eight fills the lane without paging. */
const RECENT_EVENT_LIMIT = 8

/** Rows of the audit trail in the footer. Enough to see today, not to read it. */
const RECENT_CHANGE_LIMIT = 5

export function DashboardPage() {
  const session = useAuthenticatedSession()
  const { selected, selectedSite } = useSiteContext()

  const sites = useSites()
  const summary = useTerminalSummary()
  const terminals = useTerminals()
  // limit=1 fetches ONE row to read `total` from the envelope. The count is the
  // server's, over the whole roster; the page itself is not used.
  const people = usePeople({ limit: 1 })

  /*
    GATED, AND THE GATE IS NOT COSMETIC.

    `GET /console/terminal-announcements` is MANAGER on the server. This hook
    also polls every ten seconds, so calling it unconditionally meant a VIEWER
    session took a 403 on load and another six times a minute for as long as the
    tab stayed open — for a figure that was never going to arrive.

    The tile below still renders for a VIEWER. It says the count is not theirs to
    see rather than showing a zero, because "no terminals are waiting" and
    "nobody asked on your behalf" are different statements and only one of them
    is true.
  */
  const mayViewPending = can(session, 'viewPendingTerminals')
  const pending = usePendingTerminals({ enabled: mayViewPending })

  const scopedToOneSite = selected !== ALL_SITES && selectedSite !== null

  // Fleet figures come from the server's own rollup when the view is
  // scope-wide, and from the terminal list when narrowed to one site. Both are
  // grant-scoped and derived from the same data.
  //
  // COUNTING THE LIST IS EXACT BECAUSE `fetchTerminals` COMPLETES IT. The
  // endpoint has been paged since D2 and defaults to fifty rows; the client
  // follows `has_more` to the end, so what lands in `terminals.data` is the
  // whole scoped fleet rather than its first page. If that ever stops being
  // true, this line starts reporting a sample as a total -- silently, because
  // fifty terminals is a plausible number.
  const siteTerminals = scopedToOneSite
    ? (terminals.data?.terminals ?? []).filter(
        (terminal) => terminal.site_public_id === selected,
      )
    : null
  const scopedTerminals = siteTerminals ?? terminals.data?.terminals ?? []
  const fleet = siteTerminals ? countFleet(siteTerminals) : summary.data

  const fleetLoading = scopedToOneSite ? terminals.isPending : summary.isPending
  const fleetError = scopedToOneSite ? terminals.error : summary.error

  /*
    THE DAY'S BOUNDARY, FIXED ONCE PER MOUNT.

    Recomputed on every render it would be a new query key every render, which
    is an infinite refetch rather than a live figure. Fixed at mount it is stable
    and cacheable, and the cost is that a console left open across midnight keeps
    yesterday's boundary until it is reloaded — which is the right trade for a
    screen an operator refreshes, and is why the card names the day it is
    describing rather than just saying "today".
  */
  const dayStart = useMemo(() => startOfLocalDay().toISOString(), [])

  /*
    NARROWED SERVER-SIDE WHEN THE SELECTOR IS. The events endpoint takes a
    `site_id`, so a scoped view asks for that site's events rather than fetching
    everything and filtering — which it could not do anyway: a FieldEvent carries
    a site NAME and no id, and names are editable and not unique.
  */
  const siteFilter = scopedToOneSite ? selected : undefined

  // Three counts, each `limit=1`, each read off `total`. The server counts over
  // the whole filtered trail before paging, so these are exact rather than the
  // size of a page. `RECORDED` and `ERROR` are derived from the three — see
  // decisionSegments — instead of costing two more round trips.
  const todayAll = useEvents({ from: dayStart, limit: 1, site_id: siteFilter })
  const todayGranted = useEvents({
    from: dayStart,
    decision: 'GRANTED',
    limit: 1,
    site_id: siteFilter,
  })
  const todayDenied = useEvents({
    from: dayStart,
    decision: 'DENIED',
    limit: 1,
    site_id: siteFilter,
  })
  const recent = useEvents({ limit: RECENT_EVENT_LIMIT, site_id: siteFilter })

  /*
    THE ONE SETUP FACT THIS PAGE CANNOT DERIVE.

    Every other onboarding item is a count already on screen. "Has anybody been
    granted access" is not: rules are readable one person at a time, so working
    it out here would be one request per person and a sample rather than a count
    past the first page. The server answers it with a single aggregate.
  */
  const onboarding = useOnboardingState()

  const attention = collectAttention({
    fleet,
    terminals: scopedTerminals,
    applicationCount: session.applications.length,
    peopleTotal: people.data?.total,
    // COMPANY-WIDE rather than narrowed by the site selector, deliberately: an
    // announcement has no site until it is approved, so there is no honest way
    // to filter it by one. Showing it whatever the selector says is the correct
    // reading — it genuinely is not "at" the selected site yet.
    pendingTerminals: pending.data?.count ?? 0,
    /*
      COMPANY-WIDE, and correctly so: people are not site-scoped on this
      platform -- the schema has no person-to-site relationship, which the people
      list says on its own screen -- so there is nothing to narrow this by and
      pretending otherwise would report a figure the selector did not produce.

      UNDEFINED WHILE IT LOADS, passed through rather than defaulted to zero. The
      item it feeds is a claim about the customer's configuration, and a zero
      standing in for "not known yet" would be a claim made from nothing.
    */
    peopleWithoutAccess: onboarding.data?.people_without_access,
    /*
      HAS THIS DEPLOYMENT EVER BEEN SEEN TO WORK.

      `recent` is the unbounded trail, so its `total` is every event ever
      recorded in the current scope — not today's, which would raise the last
      item again on every quiet morning. Undefined while it loads, for the same
      reason as the figure above.
    */
    eventsRecorded: recent.data?.total,
  })

  /*
    "10 of 15", with "events today" beneath it — not "64.3% of today's events".

    A share to one decimal place is precision the reader cannot use and cannot
    check, and a denial rate stated as a percentage reads as a verdict on the
    deployment. Both counts are already on this page, so the plain form is also
    the one somebody can verify by looking.

    UNDEFINED ON AN EMPTY DAY, so the tile reads "0" rather than "0 of 0".
  */
  const deniedOutOf =
    todayAll.data && todayAll.data.total > 0 ? todayAll.data.total : undefined

  return (
    <div className="page page--wide dash">
      <PageHeader
        title="Overview"
        lead={
          <>
            {session.company.name} · {session.operator.full_name} ({roleLabel(session.role)})
          </>
        }
        /*
          THE SITE SCOPE LIVES ON THE SCREEN IT SCOPES.

          It used to sit in the shell's top bar, above the navigation and on
          every page — a position that claims to govern the console. It governs
          this page and nothing else: no other screen reads
          `SiteContext.selected`, and the two that can be narrowed by site
          (Terminals and Events) each carry their own filter over their own
          rows. Here it is beside the heading whose figures it changes, and the
          tiles below say "Terminals here" rather than "Terminals" when it is
          set, so the narrowing is visible in the numbers as well as the control.

          Renders nothing unless there is a genuine choice — see SiteSwitcher.
        */
        actions={<SiteScopeSelect />}
      />

      {/* --- A · attention -------------------------------------------------- */}
      {attention.length > 0 ? <Attention items={attention} /> : null}

      {/* --- B · the signal band -------------------------------------------- */}
      <section className="tiles dash__band" aria-label="Platform totals">
        <Tile
          label={scopedToOneSite ? 'Terminals here' : 'Terminals'}
          value={fleet?.total}
          detail={fleet ? describeFleet(fleet) : undefined}
          loading={fleetLoading}
          href="/terminals"
        />
        <Tile
          label="People"
          value={people.data?.total}
          // Company-wide always: the schema has no person-to-site relationship,
          // so this figure cannot be narrowed and must not appear to be.
          detail={scopedToOneSite ? 'company-wide' : 'on the roster'}
          loading={people.isPending}
          href="/people"
        />
        <Tile
          label="Events today"
          value={todayAll.data?.total}
          detail="since midnight, your time"
          loading={todayAll.isPending}
          href="/events"
        />
        <Tile
          label="Denied today"
          value={todayDenied.data?.total}
          outOf={deniedOutOf}
          detail="events today"
          tone={todayDenied.data && todayDenied.data.total > 0 ? 'danger' : undefined}
          loading={todayDenied.isPending}
          href="/events"
        />
        {/*
          RENDERED FOR EVERYONE, ANSWERED FOR MANAGERS. A viewer sees the tile
          and is told the figure is above their role, rather than seeing a zero
          that would read as "nothing is waiting".
        */}
        <Tile
          label="Awaiting setup"
          value={mayViewPending ? pending.data?.count : undefined}
          detail={
            mayViewPending
              ? 'terminals showing a code on screen'
              : 'visible to managers and above'
          }
          tone={pending.data && pending.data.count > 0 ? 'warning' : undefined}
          loading={mayViewPending && pending.isPending}
          href={mayViewPending ? '/terminals' : undefined}
        />
      </section>

      <div className="dash__split">
        {/* --- the operational lane ---------------------------------------- */}
        <div className="dash__lane">
          {/* --- C · fleet ------------------------------------------------- */}
          <section className="panel" aria-labelledby="dashboard-fleet-heading">
            <div className="panel__header panel__header--split">
              <h2 className="panel__title" id="dashboard-fleet-heading">
                Terminal health
              </h2>
              <Link to="/terminals" className="panel__link">
                All terminals
              </Link>
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
              <>
                <Meter
                  segments={fleetSegments(fleet)}
                  caption={`${fleet.total} terminal${fleet.total === 1 ? '' : 's'} in view`}
                />
                <FleetFollowUps
                  neverReportedCount={neverReported(scopedTerminals)}
                  outdated={fleet.firmware_outdated}
                />
              </>
            )}
          </section>

          {/* --- E · the door log ------------------------------------------ */}
          <RecentActivity query={recent} scopedToOneSite={scopedToOneSite} />
        </div>

        {/* --- the supporting rail ------------------------------------------ */}
        <div className="dash__lane">
          {/* --- D · today ------------------------------------------------- */}
          <TodayAtTheDoor
            total={todayAll.data?.total}
            granted={todayGranted.data?.total}
            denied={todayDenied.data?.total}
            isPending={todayAll.isPending || todayGranted.isPending || todayDenied.isPending}
            error={todayAll.error ?? todayGranted.error ?? todayDenied.error}
            onRetry={() => {
              void todayAll.refetch()
              void todayGranted.refetch()
              void todayDenied.refetch()
            }}
          />

          {/* --- F · sites -------------------------------------------------- */}
          <SiteSpread
            rows={siteDistribution(sites.data?.sites ?? [], terminals.data?.terminals ?? [])}
            isPending={sites.isPending || terminals.isPending}
            error={sites.error ?? terminals.error}
            onRetry={() => {
              void sites.refetch()
              void terminals.refetch()
            }}
          />

          {/* --- G · firmware ----------------------------------------------- */}
          <FirmwareStanding
            terminals={scopedTerminals}
            isPending={terminals.isPending}
            error={terminals.error}
            onRetry={() => void terminals.refetch()}
            mayReachCatalogue={can(session, 'manageFirmware')}
          />

          {/* --- H1 · what this company uses AccessLink for ----------------- */}
          <EnabledFeatures />

          {/*
            ADMIN ONLY, AND OMITTED RATHER THAN LOCKED. The audit trail is ADMIN
            on the server because it names which operators did what, which is
            administrative information about colleagues. A card that rendered for
            everyone and said "you may not see this" would advertise the
            existence of a record without adding anything an operator below ADMIN
            can act on — and it would cost them a 403 on every load to do it.

            A SEPARATE COMPONENT so the hook inside it is only mounted when the
            card is. A conditional call in this function would be a hook order
            violation; a call gated by `enabled` would leave a permanently empty
            query in the cache.
          */}
          {can(session, 'viewAudit') ? <RecentChanges /> : null}
        </div>
      </div>

      {/*
        --- H · features and the audit trail -----------------------------

        THESE USED TO SIT IN A THIRD ROW BENEATH BOTH LANES, and that row was
        the reason the page had a hole in it: the left lane runs to the bottom
        of the door log while the right lane ended at Firmware, leaving roughly
        a third of a screen of empty right column before the row began. Moving
        them into the right lane closes the gap with content that was already
        on the page and shortens it by a full row.
      */}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Pieces
// ---------------------------------------------------------------------------

/** "19 online · 3 offline · 1 fault", built only from states that are non-zero. */
function describeFleet(fleet: FleetSummary): string {
  const parts: string[] = [`${fleet.online} online`]
  if (fleet.offline > 0) parts.push(`${fleet.offline} offline`)
  if (fleet.error > 0) parts.push(`${fleet.error} reporting a fault`)
  return parts.join(' · ')
}


/**
 * What needs looking at, TRIMMED TO THE FIRST THREE.
 *
 * ---------------------------------------------------------------------------
 * WHY A LIMIT AT ALL
 * ---------------------------------------------------------------------------
 *
 * A real deployment produces five or six of these at once -- a fault, an
 * offline unit, one never connected, one behind on software, plus whatever
 * setup is incomplete. Rendered in full they filled the entire first screen, so
 * somebody opening the console saw six problems of equal visual weight before a
 * single figure about their business, and the one that actually had a person
 * waiting at a door was the fourth item down.
 *
 * Three is what fits above the totals without pushing them off the screen.
 *
 * NOTHING IS DROPPED. The rest are one click away and the button says how many
 * there are, so the count is honest even when the list is short. `collectAttention`
 * already orders by severity, so the three shown are the three that matter most.
 */

/**
 * What this company uses AccessLink for.
 *
 * A LIST OF NAMES AND A LINK, AND NOTHING ELSE NOW. This panel used to carry a
 * warning headed "These are not running yet", explaining that turning a feature
 * on records an intention and that the platform does not yet evaluate these
 * workflows — no attendance calculated, no access decisions made against them.
 *
 * Accurate, and it was a development status report on the first screen of a
 * product somebody is paying for. It is the same content P0 removed from
 * Features itself, and it lives in docs/market-readiness.md, which is the
 * document that is actually owned and read by the people who need it.
 *
 * The names are still derived from the session, so a company with none enabled
 * still gets the honest empty state below rather than an empty box.
 */
function EnabledFeatures() {
  const session = useAuthenticatedSession()

  return (
    <section className="panel" aria-labelledby="dashboard-features-heading">
      <div className="panel__header panel__header--split">
        <h2 className="panel__title" id="dashboard-features-heading">
          Features
        </h2>
        {can(session, 'manageOperators') ? (
          <Link to="/settings/applications" className="panel__link">
            Manage
          </Link>
        ) : null}
      </div>

      {session.applications.length === 0 ? (
        <p className="field__hint">
          No features turned on for {session.company.name}. That is a normal
          starting state — the platform works as an identity and terminal
          management system without any.
        </p>
      ) : (
        <ul className="chip-list">
          {session.applications.map((application) => (
            <li key={application.code}>
              <span className="chip">{describeApplication(application.code).label}</span>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

/**
 * What needs doing, and only the parts this operator can actually do.
 *
 * ---------------------------------------------------------------------------
 * AN HONEST CHECKLIST IS ONE YOU CAN FINISH
 * ---------------------------------------------------------------------------
 *
 * Every item used to render its action for every role. A VIEWER meeting this
 * screen for the first time was given three things to do and could do none of
 * them: two led to a page where the button is simply absent, and "View features"
 * led to `/settings/applications`, which is `RequireRole minimum="ADMIN"` and
 * bounces to the forbidden page. Being told to go somewhere you are not allowed,
 * on the first screen of the product, reads as a broken account rather than as a
 * role working correctly.
 *
 * THE ITEM STAYS, THE BUTTON GOES. A manager who cannot approve a terminal still
 * needs to know one is waiting — they are usually the person who will go and ask
 * — so what replaces the action is a sentence naming who can finish it, not
 * silence.
 *
 * `roleAtLeast` rather than `can()`: see the note on `requiresRole` in
 * dashboard/overview.ts for why the route's own minimum is the right question
 * for a link.
 */
function Attention({ items }: { items: AttentionItem[] }) {
  const session = useAuthenticatedSession()
  const [expanded, setExpanded] = useState(false)
  const VISIBLE = 3
  const hidden = items.length - VISIBLE
  const shown = expanded ? items : items.slice(0, VISIBLE)

  return (
    <section className="panel" aria-labelledby="dashboard-attention-heading">
      <div className="panel__header">
        <h2 className="panel__title" id="dashboard-attention-heading">
          Needs your attention
        </h2>
      </div>

      <ul className="attention-list" id="dashboard-attention-list">
        {shown.map((item) => (
          <li key={item.id} className={`attention attention--${item.tone}`}>
            <div>
              <p className="attention__title">{item.title}</p>
              <p className="attention__detail">{item.detail}</p>
            </div>
            {item.href && (!item.requiresRole || roleAtLeast(session.role, item.requiresRole)) ? (
              <Link
                to={item.href}
                className={item.primary ? 'button button--primary' : 'button button--quiet'}
              >
                {item.action}
              </Link>
            ) : item.unavailable ? (
              <p className="attention__unavailable">{item.unavailable}</p>
            ) : null}
          </li>
        ))}
      </ul>

      {hidden > 0 ? (
        <button
          type="button"
          className="button button--quiet"
          aria-expanded={expanded}
          aria-controls="dashboard-attention-list"
          onClick={() => setExpanded((open) => !open)}
        >
          {expanded ? 'Show fewer' : `View ${hidden} more`}
        </button>
      ) : null}
    </section>
  )
}

function Tile({
  label,
  value,
  outOf,
  detail,
  loading,
  href,
  tone,
}: {
  label: string
  value: number | undefined
  /**
   * Renders the figure as "10 of 15" rather than "10".
   *
   * FOR A COUNT THAT ONLY MEANS SOMETHING AGAINST A TOTAL. Denials are the
   * case: nine of ten events denied and nine of nine hundred are the same
   * number and completely different days. The denominator used to sit in the
   * description underneath as "10 of 15 events today", which restated the
   * figure directly above it; putting it in the figure says it once.
   *
   * Omitted when there is no total to divide by, so an empty day reads "0"
   * rather than "0 of 0".
   */
  outOf?: number
  detail?: string
  loading?: boolean
  href?: string
  tone?: 'danger' | 'warning'
}) {
  const body = (
    <>
      <p className="tile__label">{label}</p>
      {/* An em dash while loading or unavailable, never a zero. "0 terminals"
          is a claim, and it is the alarming one to make by accident. */}
      <p className="tile__value">
        {loading || value === undefined ? '—' : outOf === undefined ? value : `${value} of ${outOf}`}
      </p>
      {detail ? <p className="tile__description">{detail}</p> : null}
    </>
  )

  const className = ['tile', tone ? `tile--${tone}` : null].filter(Boolean).join(' ')

  return href ? (
    <Link to={href} className={`${className} tile--link`}>
      {body}
    </Link>
  ) : (
    <article className={className}>{body}</article>
  )
}


/**
 * The two fleet facts that are not a status.
 *
 * Both are always shown, including at zero, because their absence is the good
 * news and an operator should be able to see that it is still true.
 */
function FleetFollowUps({
  neverReportedCount,
  outdated,
}: {
  neverReportedCount: number
  outdated: number
}) {
  return (
    <ul className="follow-ups">
      <li className="follow-up">
        <span className="follow-up__count">{neverReportedCount}</span>
        <span className="follow-up__label">
          never reported in
          <span className="follow-up__detail">
            Added, but has never connected.
          </span>
        </span>
        {neverReportedCount > 0 ? (
          <Badge tone="warning">Never connected</Badge>
        ) : (
          <Badge tone="positive">All reporting</Badge>
        )}
      </li>
      <li className="follow-up">
        <span className="follow-up__count">{outdated}</span>
        <span className="follow-up__label">
          behind on firmware
          <span className="follow-up__detail">
            Running older software than the version you have made current.
          </span>
        </span>
        {outdated > 0 ? (
          <Badge tone="info">Update available</Badge>
        ) : (
          <Badge tone="positive">Up to date</Badge>
        )}
      </li>
    </ul>
  )
}

/**
 * What happened at the doors since local midnight.
 *
 * THE ONE REGION THAT SAYS THE PRODUCT IS WORKING rather than merely configured.
 * Everything else on this page describes an installation; this describes it
 * doing its job.
 */
function TodayAtTheDoor({
  total,
  granted,
  denied,
  isPending,
  error,
  onRetry,
}: {
  total: number | undefined
  granted: number | undefined
  denied: number | undefined
  isPending: boolean
  error: unknown
  onRetry: () => void
}) {
  return (
    <section className="panel" aria-labelledby="dashboard-today-heading">
      <div className="panel__header panel__header--split">
        <h2 className="panel__title" id="dashboard-today-heading">
          Today at your access points
        </h2>
        <Link to="/events" className="panel__link">
          All events
        </Link>
      </div>

      {error ? (
        <ErrorState error={error} onRetry={onRetry} />
      ) : isPending ? (
        <LoadingState label="Loading today’s events…" />
      ) : total === undefined || granted === undefined || denied === undefined ? (
        <p className="field__hint">Today’s figures are not available.</p>
      ) : (
        <>
          <p className="headline">
            <span className="headline__value">{total}</span>
            <span className="headline__label">
              event{total === 1 ? '' : 's'} since midnight, your time
            </span>
          </p>
          <Meter segments={decisionSegments({ total, granted, denied })} />
          {/*
            A terminal buffers while it is offline and replays its log on
            reconnect, and the trail is bounded on when somebody stood at the
            door rather than on when the platform heard about it. So this figure
            can rise for an hour that has already passed, which is correct and
            surprising enough to be worth saying once.
          */}
          <p className="field__hint">
            Counted by when each event happened. A terminal that was offline
            reports late, so a past hour can still gain events.
          </p>
        </>
      )}
    </section>
  )
}

/** Where the fleet stands, per site. */
function SiteSpread({
  rows,
  isPending,
  error,
  onRetry,
}: {
  rows: SiteRow[]
  isPending: boolean
  error: unknown
  onRetry: () => void
}) {
  return (
    <section className="panel" aria-labelledby="dashboard-sites-heading">
      <div className="panel__header panel__header--split">
        <h2 className="panel__title" id="dashboard-sites-heading">
          Sites
        </h2>
        <Link to="/sites" className="panel__link">
          All sites
        </Link>
      </div>

      {error ? (
        <ErrorState error={error} onRetry={onRetry} />
      ) : isPending ? (
        <LoadingState label="Loading sites…" />
      ) : rows.length === 0 ? (
        <p className="field__hint">
          No sites yet. A site is the location a terminal is registered against.
        </p>
      ) : (
        <ul className="site-spread">
          {rows.map((row) => (
            <li key={row.id} className="site-spread__row">
              <span className="site-spread__name">
                <Link to={`/sites/${encodeURIComponent(row.id)}`}>{row.name}</Link>
                {!row.active ? <Badge tone="neutral">Deactivated</Badge> : null}
              </span>
              <span className="site-spread__count">
                {row.total} terminal{row.total === 1 ? '' : 's'}
                {row.total > 0 ? (
                  <span className="muted"> · {row.online} online</span>
                ) : null}
              </span>
              <span
                className={`meter__track meter__track--slim${row.total === 0 ? ' meter__track--empty' : ''}`}
                aria-hidden="true"
              >
                {row.online > 0 ? (
                  <span
                    className="meter__part meter__part--positive"
                    style={{ flexGrow: row.online }}
                  />
                ) : null}
                {row.offline > 0 ? (
                  <span
                    className="meter__part meter__part--warning"
                    style={{ flexGrow: row.offline }}
                  />
                ) : null}
                {row.other > 0 ? (
                  <span
                    className="meter__part meter__part--info"
                    style={{ flexGrow: row.other }}
                  />
                ) : null}
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

/**
 * How much of the fleet is on the build the platform considers current.
 *
 * READ OFF THE TERMINAL LIST, not the firmware catalogue. Every terminal row
 * already carries `current_firmware_version` — the server's answer for that
 * unit's device type and channel — so the catalogue read would be a second,
 * ADMIN-only request for a number a VIEWER already has. The LINK to the
 * catalogue is gated, because changing what the fleet is measured against is
 * what ADMIN buys; seeing where the fleet stands is not.
 */
function FirmwareStanding({
  terminals,
  isPending,
  error,
  onRetry,
  mayReachCatalogue,
}: {
  terminals: Parameters<typeof firmwareCompliance>[0]
  isPending: boolean
  error: unknown
  onRetry: () => void
  mayReachCatalogue: boolean
}) {
  const standing = firmwareCompliance(terminals)
  const fleet = fleetStanding(terminals)

  return (
    <section className="panel" aria-labelledby="dashboard-firmware-heading">
      <div className="panel__header panel__header--split">
        <h2 className="panel__title" id="dashboard-firmware-heading">
          Firmware
        </h2>
        {/*
          "All versions", not "Catalogue". The word the destination uses for
          itself, and the one the console stopped showing customers -- a link
          whose label is a word that appears nowhere on the page it opens is a
          link somebody has to press to find out what it was.
        */}
        {mayReachCatalogue ? (
          <Link to="/settings/firmware" className="panel__link">
            All versions
          </Link>
        ) : null}
      </div>

      {error ? (
        <ErrorState error={error} onRetry={onRetry} />
      ) : isPending ? (
        <LoadingState label="Loading firmware standing…" />
      ) : standing.total === 0 ? (
        <p className="field__hint">
          Nothing to measure yet — firmware standing appears once a terminal has
          been registered.
        </p>
      ) : (
        <>
          {/*
            THE SAME THREE SEGMENTS THE FIRMWARE SCREEN DRAWS, from the same
            function.

            This used to be two: "On the current build" and "Behind", both read
            off `firmware_outdated`. That flag is TRUE for a terminal that has
            never reported a version at all -- a defensible server default, and
            a bad thing to show a customer, because "1 behind" sends somebody
            looking for an update when what happened is that a terminal has not
            been switched on yet.

            Sharing `fleetStanding` is what stops the two screens disagreeing
            about the same fleet. A customer who reads "1 behind" here and
            "0 update available, 1 hasn't reported" one click away has no way to
            know which is true. NOTHING SERVER-SIDE CHANGED: `firmware_outdated`
            still means what it meant, and is still what this reads.
          */}
          <Meter
            segments={[
              { id: 'up-to-date', label: 'Up to date', value: fleet.upToDate, tone: 'positive' },
              {
                id: 'update-available',
                label: 'Update available',
                value: fleet.updateAvailable,
                tone: 'warning',
              },
              {
                id: 'never-reported',
                label: 'Hasn’t reported a version yet',
                value: fleet.neverReported,
                tone: 'neutral',
              },
            ]}
          />
          <p className="field__hint">
            {standing.targets.length === 0
              ? 'No version is set for these terminals yet.'
              : standing.targets.length === 1
                ? `Current version: ${standing.targets[0]}`
                : `Measured against ${standing.targets.length} current versions.`}
          </p>
        </>
      )}
    </section>
  )
}

/**
 * The door log, most recent first.
 *
 * THE PANEL THAT USED TO SAY THIS DID NOT EXIST. It does:
 * `GET /api/v1/console/events` is mounted at VIEWER and has its own page in the
 * navigation. The old copy predated both and told an operator, on the screen
 * they open first, that a feature two clicks away was unbuilt.
 *
 * Eight rows and no paging. This is a window onto the trail, not the trail —
 * anything an operator wants to search, filter or read past belongs on the
 * events page, which is what the header links to.
 */
function RecentActivity({
  query,
  scopedToOneSite,
}: {
  query: ReturnType<typeof useEvents>
  scopedToOneSite: boolean
}) {
  const columns: Column<FieldEvent>[] = [
    {
      id: 'when',
      header: 'When',
      primary: true,
      render: (event) => (
        <span className="event__when">
          <Timestamp value={event.occurred_at} relative />
          {/*
            The same badge as the events page, and the same words BY
            CONSTRUCTION now: both read UNCONFIRMED_TIME_LABEL from
            access/accessVocabulary rather than holding their own copy of the
            string. Two surfaces phrasing one condition differently would read
            as two conditions, and a rename of one copy is how that happens.
          */}
          {!event.occurred_at_trusted ? (
            <Badge tone="warning">{UNCONFIRMED_TIME_LABEL}</Badge>
          ) : null}
        </span>
      ),
    },
    {
      id: 'who',
      header: 'Who',
      render: (event) =>
        event.person_name ? (
          <Link to={`/people/${encodeURIComponent(event.subject_external_id ?? '')}`}>
            {event.person_name}
          </Link>
        ) : event.subject_external_id ? (
          // Matched nobody. The identifier the terminal read is kept so the
          // attempt is traceable, and it is marked rather than left looking like
          // a person whose name failed to load.
          <span className="event__unknown">
            <code className="mono">{event.subject_external_id}</code>
            <Badge tone="warning">Not recognised</Badge>
          </span>
        ) : (
          <span className="muted">—</span>
        ),
    },
    {
      id: 'where',
      header: 'Where',
      secondary: true,
      render: (event) =>
        event.device_serial ? (
          <Link to={`/terminals/${encodeURIComponent(event.device_serial)}`}>
            {event.device_name || event.device_serial}
          </Link>
        ) : (
          <span className="muted">—</span>
        ),
    },
    {
      id: 'decision',
      header: 'Outcome',
      align: 'end',
      render: (event) => (
        <span className="event__outcome">
          <Badge tone={DECISION_TONES[event.decision] ?? 'neutral'}>
            {humaniseCode(event.decision)}
          </Badge>
          {/*
            describeReason, NOT humaniseCode. The raw code humanises to
            "Explicit Deny", which is the engine's word for it; the Events page
            has always called the same thing "Denied by a rule". Two surfaces
            naming one condition differently reads as two conditions.
          */}
          {event.reason && event.decision === 'DENIED' ? (
            <span className="muted">{describeReason(event.reason).label}</span>
          ) : null}
        </span>
      ),
    },
  ]

  return (
    <section className="panel" aria-labelledby="dashboard-activity-heading">
      <div className="panel__header panel__header--split">
        <h2 className="panel__title" id="dashboard-activity-heading">
          Recent access activity
        </h2>
        <Link to="/events" className="panel__link">
          All events
        </Link>
      </div>
      <DataTable
        columns={columns}
        rows={query.data?.events}
        rowKey={(event) => event.id}
        caption="Recent access activity"
        isLoading={query.isPending}
        isFetching={query.isFetching}
        error={query.error}
        onRetry={() => void query.refetch()}
        emptyTitle="Nothing at your access points yet"
        emptyDescription={
          scopedToOneSite
            ? 'No events have been recorded at this site. A terminal records one every time somebody presents themselves.'
            : 'No events have been recorded. A terminal records one every time somebody presents themselves at an access point.'
        }
      />
    </section>
  )
}

/**
 * What operators changed here, most recent first. ADMIN.
 *
 * `changes` IS DELIBERATELY NOT RENDERED. It is free-form and redacted
 * server-side, and a dashboard is the wrong altitude for a diff — the activity
 * page shows it in full. This card answers "has anything moved today", which is
 * a question four short rows can settle.
 */
function RecentChanges() {
  const audit = useAuditEvents({ limit: RECENT_CHANGE_LIMIT })

  return (
    <section className="panel" aria-labelledby="dashboard-changes-heading">
      <div className="panel__header panel__header--split">
        <h2 className="panel__title" id="dashboard-changes-heading">
          Recent changes here
        </h2>
        <Link to="/activity" className="panel__link">
          Full trail
        </Link>
      </div>

      {audit.error ? (
        <ErrorState error={audit.error} onRetry={() => void audit.refetch()} />
      ) : audit.isPending ? (
        <LoadingState label="Loading recent changes…" />
      ) : (audit.data?.entries.length ?? 0) === 0 ? (
        <p className="field__hint">
          Nothing has been changed in this console yet. Every operator action is
          recorded here once one is.
        </p>
      ) : (
        <ul className="change-list">
          {(audit.data?.entries ?? []).map((entry) => (
            <li key={entry.id} className="change">
              <span className="change__what">{describeChange(entry)}</span>
              <span className="change__who">
                {entry.actor_email ?? 'Unknown operator'} ·{' '}
                <Timestamp value={entry.occurred_at} relative />
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

/**
 * "Terminal disabled — AT-0001", with the label omitted when there is none.
 *
 * THROUGH `describeAction`, NOT `humaniseCode`, and the difference is two
 * screens telling a customer different things about the same record.
 *
 * `auditVocabulary` is the one place audit action names are decided, and the
 * Activity page reads it. This panel humanised the stored code instead, so the
 * SAME row rendered as "Person Created" here and "Person added" one click away,
 * as "Terminal Credential Revoked" here and "Terminal credential revoked"
 * there — and, worst of the set, as "Site Key Rotated" here while every other
 * surface in the console calls that credential a PROVISIONING KEY. An owner
 * scanning the overview for the rotation they just performed was looking for a
 * phrase the product had used nowhere else.
 *
 * NOTHING IS LOST FOR AN ACTION THIS BUILD HAS NEVER HEARD OF: `describeAction`
 * falls back to the same `humaniseCode` this used to call, so an
 * application-defined event still renders, humanised, rather than disappearing.
 */
function describeChange(entry: AuditRecord): string {
  const action = describeAction(entry.action).label
  return entry.target_label ? `${action} — ${entry.target_label}` : action
}

// ---------------------------------------------------------------------------
// Re-exported for the tests and for pages/terminals, which assert on the pure
// helpers. They live in ./dashboard/overview so the page is composition and the
// arithmetic is testable without a render.
// ---------------------------------------------------------------------------

export { collectAttention, countFleet }
export type { AttentionItem } from './dashboard/overview'
