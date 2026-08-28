import type { BadgeTone } from '../../components/Badge'
import type { MeterSegment } from '../../components/Meter'
import type { EventDecision, FleetSummary, Role, Site, Terminal } from '../../api/types'

/**
 * Everything the overview DERIVES, with no React in it.
 *
 * THE RULE THIS FILE EXISTS TO KEEP: every number the dashboard shows is either
 * one the API computed, or arithmetic over a set the API returned COMPLETE. It
 * is never a figure read off a page of a paginated collection and presented as a
 * total — that is the difference between a count and a sample, and the two are
 * indistinguishable once they are rendered as a number.
 *
 * Which of the two applies is a property of the endpoint, not a preference:
 *
 *   `GET /console/terminals`   is PAGED since D2 and defaults to fifty rows, but
 *                              `fetchTerminals` follows `has_more` to the end,
 *                              so the array that reaches this file is the whole
 *                              scoped fleet and counting it is exact. Counting
 *                              a raw page response would NOT be.
 *                              `filterTerminals` in pages/terminals/health.ts
 *                              depends on the same completion and says so.
 *   `GET /console/sites`       likewise — an envelope with every granted site.
 *   `GET /console/people`      is PAGED, so nothing here counts people. The page
 *                              reads `total` off the envelope instead.
 *   `GET /console/events`      is PAGED, so nothing here counts events either.
 *                              The page asks the server for each count it wants
 *                              with `limit=1` and reads `total`.
 */

// ---------------------------------------------------------------------------
// Fleet
// ---------------------------------------------------------------------------

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

/** One band of a proportion bar: a labelled count with a tone. */
/**
 * A meter's slice.
 *
 * ALIASED RATHER THAN REDECLARED. `Meter` moved into components/ when the
 * Firmware screen started drawing one too, and it owns the shape it renders.
 * Two structurally identical interfaces in two files is how a `tone` gains a
 * value in one of them and not the other.
 */
export type Segment = MeterSegment

/**
 * The fleet's states, in the order they are read.
 *
 * ONLINE FIRST AND DISABLED LAST, which is severity order rather than
 * alphabetical: an operator scanning this wants the two ends — how much is up,
 * and how much is deliberately out of service — and the states that need a
 * person sit between them.
 *
 * THE TONES MATCH `Badge.tsx` EXACTLY, including that OFFLINE is a warning and
 * not an error. A terminal is expected to be offline sometimes and keeps working
 * at the door while it is, because the roster is held locally; ERROR is the
 * state that means somebody has to go and look. Two surfaces disagreeing about
 * which colour offline is would teach operators to ignore the one that matters.
 *
 * ZERO-VALUED STATES ARE KEPT, not filtered out. "Reporting an error: 0" is a
 * fact worth reading, and a legend whose rows appear and disappear as the fleet
 * changes is one an operator cannot learn the shape of.
 */
export function fleetSegments(fleet: FleetSummary): Segment[] {
  return [
    { id: 'online', label: 'Online', value: fleet.online, tone: 'positive' },
    { id: 'offline', label: 'Offline', value: fleet.offline, tone: 'warning' },
    { id: 'error', label: 'Reporting an error', value: fleet.error, tone: 'danger' },
    { id: 'updating', label: 'Updating', value: fleet.updating, tone: 'info' },
    { id: 'provisioning', label: 'Provisioning', value: fleet.provisioning, tone: 'info' },
    { id: 'disabled', label: 'Disabled', value: fleet.disabled, tone: 'neutral' },
  ]
}

/**
 * Terminals registered that have never sent a heartbeat.
 *
 * A DIFFERENT PROBLEM FROM ONE THAT HAS GONE QUIET, which is why it is counted
 * separately rather than folded into `offline`: it means the unit was registered
 * and then never managed to call home, so nobody should be waiting for it to
 * come back on its own.
 */
export function neverReported(terminals: Terminal[]): number {
  return terminals.filter((terminal) => !terminal.last_heartbeat_at).length
}

/**
 * Firmware compliance, read off the fleet the console already holds.
 *
 * NO CATALOGUE REQUEST. Every terminal row carries `current_firmware_version` —
 * the build the SERVER considers current for that terminal's device type and
 * release channel, resolved in `database/firmware.go` — alongside the
 * `firmware_outdated` flag computed from it. So the target is already in hand,
 * and asking `GET /console/firmware` for it would be a second, ADMIN-only
 * request for an answer the VIEWER-readable list already gave.
 *
 * `targets` is a SET because a fleet can straddle channels, and one of them
 * being behind says nothing about the other. One entry renders as a version;
 * several render as a count, because naming one of them would be picking a
 * winner the platform did not pick.
 */
export interface FirmwareCompliance {
  onTarget: number
  behind: number
  total: number
  targets: string[]
}

export function firmwareCompliance(terminals: Terminal[]): FirmwareCompliance {
  const behind = terminals.filter((terminal) => terminal.firmware_outdated).length
  const targets = [
    ...new Set(
      terminals
        .map((terminal) => terminal.current_firmware_version)
        .filter((version): version is string => Boolean(version)),
    ),
  ].sort()

  return { onTarget: terminals.length - behind, behind, total: terminals.length, targets }
}

// ---------------------------------------------------------------------------
// Sites
// ---------------------------------------------------------------------------

export interface SiteRow {
  id: string
  name: string
  active: boolean
  total: number
  online: number
  offline: number
  /** Anything neither online nor offline: updating, provisioning, error, disabled. */
  other: number
}

/**
 * How the fleet is spread across sites.
 *
 * BOTH HALVES COME FROM THE TERMINAL LIST, deliberately, even though `Site`
 * carries its own `terminal_count`. The two are computed by different queries at
 * different moments, and a card whose total came from one source and whose
 * online/offline split came from the other would sooner or later show a split
 * that does not add up to its own total. Reading one source keeps the row
 * internally consistent, which matters more here than agreeing with a number on
 * another screen.
 *
 * SITES WITH NO TERMINALS ARE KEPT AND SORTED LAST. A site standing empty is
 * exactly the thing an operator would want to notice, and dropping the row would
 * hide it.
 *
 * MATCHED ON THE PUBLIC ID, NEVER THE NAME — names are editable and not unique,
 * so a name match would quietly pool two sites' hardware into one row.
 */
export function siteDistribution(sites: Site[], terminals: Terminal[]): SiteRow[] {
  const rows = new Map<string, SiteRow>()

  for (const site of sites) {
    rows.set(site.id, {
      id: site.id,
      name: site.name,
      active: site.active,
      total: 0,
      online: 0,
      offline: 0,
      other: 0,
    })
  }

  for (const terminal of terminals) {
    let row = rows.get(terminal.site_public_id)
    if (!row) {
      // A terminal at a site the caller cannot read is possible in principle;
      // its own row carries the name, so the fleet total still adds up rather
      // than silently losing a unit.
      row = {
        id: terminal.site_public_id,
        name: terminal.site_name,
        active: true,
        total: 0,
        online: 0,
        offline: 0,
        other: 0,
      }
      rows.set(terminal.site_public_id, row)
    }

    row.total += 1
    if (terminal.status === 'ONLINE') row.online += 1
    else if (terminal.status === 'OFFLINE') row.offline += 1
    else row.other += 1
  }

  return [...rows.values()].sort(
    (a, b) => b.total - a.total || a.name.localeCompare(b.name),
  )
}

// ---------------------------------------------------------------------------
// Today
// ---------------------------------------------------------------------------

/**
 * Midnight this morning, in the READER'S timezone.
 *
 * THERE IS NO COMPANY-WIDE "TODAY" AND THE PAGE MUST NOT IMPLY ONE. A site
 * carries an IANA zone because it describes where the hardware stands, and a
 * company spanning two of them has two midnights — so there is no single instant
 * the platform could call the start of the day even if it wanted to.
 *
 * The browser's own midnight is the one boundary that is defensible without
 * inventing anything, and every figure derived from it is labelled with whose
 * day it is. `/console/events` bounds on `occurred_at` — when somebody stood at
 * the terminal — rather than on when the platform heard about it, which is the
 * right end for this question and the reason a buffered upload can raise a count
 * for an hour that has already passed.
 */
export function startOfLocalDay(now: Date = new Date()): Date {
  return new Date(now.getFullYear(), now.getMonth(), now.getDate(), 0, 0, 0, 0)
}

export interface TodayCounts {
  /** Every event since local midnight, whatever it decided. */
  total: number
  granted: number
  denied: number
}

/**
 * The day's decisions as bands of a proportion bar.
 *
 * `RECORDED` AND `ERROR` ARE DERIVED RATHER THAN FETCHED, and the subtraction is
 * exact rather than an estimate: `EventDecision` is a closed set of four that
 * the server validates on the way in (`models.IsEventDecision`), so anything
 * that is not granted and not denied is one of the other two. Asking for each of
 * them separately would be two more round trips to split a band that is usually
 * empty.
 *
 * They are reported TOGETHER and labelled as both, because the console cannot
 * tell which without asking, and "Recorded" alone would quietly absorb faults.
 *
 * Clamped at zero: the three counts are three separate requests, so a very busy
 * moment can return a `granted` read a fraction of a second newer than `total`.
 * A negative band is a rendering artefact of that race and not a fact about the
 * day.
 */
export function decisionSegments(counts: TodayCounts): Segment[] {
  const other = Math.max(0, counts.total - counts.granted - counts.denied)
  return [
    { id: 'granted', label: 'Granted', value: counts.granted, tone: 'positive' },
    { id: 'denied', label: 'Denied', value: counts.denied, tone: 'danger' },
    { id: 'other', label: 'Other outcomes', value: other, tone: 'neutral' },
  ]
}

/** How each decision reads, matching the events page so the two never diverge. */
export const DECISION_TONES: Record<EventDecision, BadgeTone> = {
  GRANTED: 'positive',
  DENIED: 'danger',
  ERROR: 'warning',
  RECORDED: 'neutral',
}

// ---------------------------------------------------------------------------
// Attention
// ---------------------------------------------------------------------------

export interface AttentionItem {
  id: string
  tone: 'danger' | 'warning' | 'info'
  title: string
  detail: string
  href?: string
  action?: string
  /**
   * Render the action as the page's primary button rather than a quiet one.
   *
   * SETTING UP A TERMINAL IS THE ONE THING SOMEBODY IS USUALLY WAITING FOR.
   * Every other item here is read-then-decide; this one has a person standing
   * at a door that will not open until it is done. It was previously a text
   * link indistinguishable from the thirteen others on the screen.
   */
  primary?: boolean
  /**
   * The lowest role that can actually finish this step.
   *
   * ---------------------------------------------------------------------------
   * THE CHECKLIST USED TO BE THE SAME FOR EVERYBODY
   * ---------------------------------------------------------------------------
   *
   * Every item below was rendered with its action whatever the reader's role, so
   * a VIEWER opening the console for the first time was handed a three-item list
   * of things to do, none of which they could do. Two led to a page where the
   * button simply is not there; the third, "View features", led to
   * `/settings/applications` -- which is `RequireRole minimum="ADMIN"` and
   * redirects straight to the forbidden page. The first screen of the product
   * told a read-only operator to go somewhere they are not allowed.
   *
   * A ROLE RATHER THAN AN `Action`, deliberately, and it is the one case where
   * the capability list is the wrong tool. `can()` answers "may this operator do
   * X"; what matters here is "will the place this link goes let them in", and for
   * Features those two disagree: `viewApplications` is VIEWER while the ROUTE is
   * ADMIN. Gating the link on the capability would have kept sending viewers to
   * the 403. Both `can()` and `RequireRole` are `roleAtLeast` underneath, so this
   * is the same machinery expressed at the level the question is actually asked.
   *
   * Undefined means anybody who can see the item can act on it -- true of every
   * "View terminals" item, which is a read.
   */
  requiresRole?: Role
  /**
   * What to say instead of the action, to somebody who cannot take it.
   *
   * THE ITEM STILL APPEARS. A manager who cannot approve a terminal still needs
   * to know one is waiting -- silence would be a different lie, and they are
   * often the person who will go and find an administrator. Only the button is
   * replaced, by a sentence naming who can finish it.
   */
  unavailable?: string
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
  applicationCount,
  peopleTotal,
  pendingTerminals = 0,
  peopleWithoutAccess,
  eventsRecorded,
}: {
  fleet: FleetSummary | undefined
  terminals: Terminal[]
  applicationCount: number
  peopleTotal: number | undefined
  /** Terminals that have announced themselves and are waiting to be approved. */
  pendingTerminals?: number
  /**
   * Active people with no access rule at all, counted by the server.
   *
   * UNDEFINED MEANS NOT KNOWN YET, and is treated as "say nothing" rather than
   * as zero. The item below is a claim about a customer's configuration; making
   * it from a figure that has not arrived would raise and then retract it on
   * every load.
   */
  peopleWithoutAccess?: number
  /**
   * How many field events this deployment has ever recorded, in the current
   * scope. UNDEFINED MEANS NOT KNOWN YET, as above.
   *
   * NOT "TODAY", DELIBERATELY. The last item below asks whether the deployment
   * has ever been seen to work, and a quiet Tuesday is not the same fact as a
   * deployment nobody has tested.
   */
  eventsRecorded?: number
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
        'A terminal has connected and is showing a code on its screen. It will not let anybody in until it is approved.',
      href: '/terminals',
      action: 'Set up terminals',
      primary: true,
      requiresRole: 'ADMIN',
      unavailable: 'Setting a terminal up needs an administrator or owner.',
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
        'An offline terminal keeps working on its local records, but is not receiving changes.',
      href: '/terminals',
      action: 'View terminals',
    })
  }

  // A terminal that has never reported at all is a different problem from one
  // that has gone quiet: it suggests it was registered and never connected.
  const unreported = neverReported(terminals)
  if (unreported > 0) {
    items.push({
      id: 'terminals-never-reported',
      tone: 'warning',
      title: `${unreported} terminal${unreported === 1 ? '' : 's'} never reported in`,
      detail: 'Added, but it has never connected. It may not be able to reach your network.',
      href: '/terminals',
      action: 'View terminals',
    })
  }

  if (fleet && fleet.firmware_outdated > 0) {
    items.push({
      id: 'terminals-outdated',
      tone: 'info',
      title: `${fleet.firmware_outdated} terminal${fleet.firmware_outdated === 1 ? '' : 's'} behind on firmware`,
      detail: 'Running older software than the version you have made current.',
      href: '/terminals',
      action: 'View terminals',
    })
  }

  /*
    A DEACTIVATED SITE IS NOT AN ALERT. It is the state somebody deliberately
    chose, and reporting a customer's own decision back to them as something
    needing attention is how an alert list stops being read. It stays visible
    where it belongs -- the Sites panel below marks it "Deactivated" -- so
    nothing is hidden, it simply is not raised.
  */

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
      requiresRole: 'ADMIN',
      unavailable: 'Adding a terminal needs an administrator or owner.',
    })
  }

  if (applicationCount === 0) {
    items.push({
      id: 'no-features',
      tone: 'info',
      title: 'No features turned on',
      detail:
        'The platform works without any, and every company starts here. Turning one on decides what your terminals may be assigned to.',
      href: '/settings/applications',
      action: 'View features',
      requiresRole: 'ADMIN',
      unavailable: 'Turning a feature on needs an administrator or owner.',
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
      requiresRole: 'MANAGER',
      unavailable: 'Adding people needs a manager or above.',
    })
  }

  /*
    THE STEP THE CHECKLIST USED TO STOP SHORT OF, and the one that decides
    whether the deployment admits anybody.

    Absence of permission is not permission. A person with no rule reaches
    nothing -- deliberately, and it is the opposite of what an operator assumes
    coming from other products -- so a customer who added a terminal and a roster
    and watched this list go quiet had a deployment that refused everybody, with
    nothing on screen having said so.

    THE COUNT IS THE SERVER'S. Rules are readable one person at a time, so the
    browser cannot work this out; `GET /console/onboarding` answers it with one
    aggregate, on the same predicate the person's own Access panel uses.

    SUPPRESSED WHILE THE ROSTER IS EMPTY, because "nobody has access" is not a
    useful thing to say about nobody -- the item above is the step that comes
    first. And suppressed when the figure has not arrived, rather than assumed.
  */
  if (peopleTotal !== undefined && peopleTotal > 0 && (peopleWithoutAccess ?? 0) > 0) {
    const all = peopleWithoutAccess === peopleTotal
    items.push({
      id: 'people-without-access',
      tone: 'info',
      // COUNTED, NEVER GUESSED. "Nobody can get in yet" is only said when the
      // number genuinely covers the whole roster.
      title: all
        ? 'Nobody can get in yet'
        : `${peopleWithoutAccess} ${peopleWithoutAccess === 1 ? 'person has' : 'people have'} no access`,
      /*
        DOMAIN-NEUTRAL. This has to read correctly for a gym, an office, a
        school, a warehouse and a factory, so it names no door, no shift, no
        member and no student -- only people, terminals and rules, which are the
        platform's own nouns.
      */
      detail: all
        ? 'Everyone you have added is refused until a rule says where they may go. Open somebody and grant them access — at one terminal, one site, or everywhere.'
        : 'They are refused everywhere until a rule says where they may go. Open somebody and grant them access — at one terminal, one site, or everywhere.',
      href: '/people',
      action: 'Grant access',
      requiresRole: 'MANAGER',
      unavailable: 'Granting access needs a manager or above.',
      // PRIMARY WHEN IT IS THE LAST THING LEFT. A customer whose roster is
      // entirely without access has finished every other step and is one action
      // from a working deployment; anything quieter reads as optional.
      primary: all,
    })
  }

  /*
    THE LAST STEP, AND THE ONE THE CHECKLIST USED TO STOP SHORT OF.

    A customer who has added a terminal, added people and granted access has
    finished configuring — and has never seen the product do anything. The list
    went silent at exactly that moment, which reads as "done" when what has
    actually been reached is "should work". Nothing on the overview said how to
    find out, and the honest ways to find out are (a) send somebody to stand at
    an access point, or (b) use the check that already exists two clicks away.

    IT DOES NOT CLAIM ANYTHING HAPPENED. The item exists BECAUSE nothing has:
    `eventsRecorded === 0`. The first real presentation retires it for good,
    which is the correct end of the journey and needs no acknowledgement of its
    own -- the events themselves are the acknowledgement.

    EVERY OTHER STEP MUST BE DONE FIRST. Raised while people still have no
    access it would be noise competing with the step that actually blocks them,
    and raised with no terminals it would point at a page with nothing on it.

    QUIET, NEVER PRIMARY. Nobody is waiting on this and nothing is broken; it is
    the one item here that is an invitation rather than a problem.
  */
  const somewhereToCheck = checkableTerminal(terminals)
  if (
    somewhereToCheck &&
    peopleTotal !== undefined &&
    peopleTotal > 0 &&
    peopleWithoutAccess === 0 &&
    pendingTerminals === 0 &&
    eventsRecorded === 0
  ) {
    items.push({
      id: 'verify-access',
      tone: 'info',
      title: 'Check that access works',
      /*
        NAMES THE CONTROL IT IS SENDING THEM TO, because the reader has never
        used this product and the terminal page is dense. "Check access" is the
        exact label of the button they are looking for.

        DOMAIN-NEUTRAL: an access point, not a door -- this has to read correctly
        for an office, a school, a depot and a residential block.
      */
      detail:
        'Everything is set up and nothing has been recorded yet. Open a terminal and choose Check access: it answers from the same rules the terminal uses, records nothing, and needs nobody standing at the access point.',
      href: `/terminals/${encodeURIComponent(somewhereToCheck)}`,
      action: 'Check access',
      // `configureTerminals` (MANAGER) is what the button on that page requires.
      requiresRole: 'MANAGER',
      unavailable: 'Checking access needs a manager or above.',
    })
  }

  return items
}

/**
 * The terminal to send somebody to in order to try the check.
 *
 * ONLINE FIRST. The check asks the platform's engine rather than the hardware,
 * so it answers for an offline terminal too -- but a customer verifying their
 * installation for the first time should not be handed the one unit that is not
 * talking, and be left wondering which of the two facts they are looking at.
 *
 * Falls back to the first terminal of any state, because a fleet that is
 * entirely offline still deserves the answer, and returns undefined for an empty
 * fleet so the caller raises nothing.
 */
function checkableTerminal(terminals: Terminal[]): string | undefined {
  const online = terminals.find((terminal) => terminal.status === 'ONLINE')
  return (online ?? terminals[0])?.serial_number
}
