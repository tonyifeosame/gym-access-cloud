import type { Terminal, TerminalStatus } from '../../api/types'
import { secondsSince } from '../../format/datetime'

/**
 * Reading a terminal's health.
 *
 * THE SERVER OWNS "ONLINE" AND THIS FILE MUST NOT SECOND-GUESS IT. A maintenance
 * sweep marks devices offline after `DEVICE_OFFLINE_AFTER_SECONDS`, which is
 * configured per deployment and is not exposed to the browser. A console that
 * invented its own threshold would sooner or later disagree with the platform
 * about whether a terminal is up — and an operator looking at two different
 * answers has no way to tell which is right.
 *
 * So `status` is authoritative and is rendered as-is. The heartbeat is shown
 * ALONGSIDE it as supporting context, because "online" and "last seen four
 * minutes ago" answer different questions and a fleet view needs both.
 *
 * The one thing worth flagging is the CONTRADICTION: a terminal reported ONLINE
 * that has never checked in. That is not a stale device — it is a sign that the
 * device registered and then never called home, or that the sweep is not
 * running. Naming it is useful; guessing a cutoff is not.
 *
 * WHAT THIS FILE ADDED, AND WHY IT IS NOT SECOND-GUESSING THE SERVER. Every
 * state except ONLINE used to be described to the operator as "the terminal is
 * offline", which is false for four of them: an ERROR unit is checking in and
 * reporting a fault, an UPDATING one is mid-download, a DISABLED one is in
 * contact and refusing people on purpose, and a PROVISIONING one may never have
 * connected at all. `reachable` names the ONE question those screens were
 * actually asking — can the platform get a message to this terminal right now —
 * and it is derived from the server's own status and heartbeat, not from a
 * locally invented cutoff.
 */

export type HealthTone = 'positive' | 'warning' | 'danger' | 'neutral' | 'info'

export interface TerminalHealth {
  /** The server's own word for the state. */
  status: TerminalStatus
  tone: HealthTone
  /** Seconds since the last heartbeat, or null if there has never been one. */
  heartbeatAgeSeconds: number | null
  /** True when the device is reported up but has never reported in. */
  neverReported: boolean
  /** True when the device is behind the current build for its channel. */
  firmwareOutdated: boolean
  /**
   * True when the platform has a live path to this terminal.
   *
   * FALSE MEANS EXACTLY ONE THING: the platform cannot get a message to it, so
   * anything queued for it waits. That is OFFLINE — the server's own sweep
   * decided it — or a terminal that has never checked in at all. It is NOT
   * "anything other than ONLINE": a terminal reporting a fault, installing an
   * update or deliberately disabled is still in contact, and telling an operator
   * otherwise sends them to the site for nothing.
   */
  reachable: boolean
  /** Short sentence for a detail view. Empty when there is nothing to say. */
  note: string
}

/**
 * Tone per reported state.
 *
 * OFFLINE IS A WARNING, NOT AN ERROR. A terminal is expected to be offline
 * sometimes — power cycled, network down for a moment — and it keeps working at
 * the door while it is, because the roster is held locally. ERROR is the state
 * that means somebody has to go and look. Overstating offline trains operators
 * to ignore the colour that matters.
 */
const STATUS_TONES: Record<string, HealthTone> = {
  ONLINE: 'positive',
  OFFLINE: 'warning',
  UPDATING: 'info',
  ERROR: 'danger',
  DISABLED: 'neutral',
  PROVISIONING: 'info',
}

export function readHealth(terminal: Terminal, now: Date = new Date()): TerminalHealth {
  const heartbeatAgeSeconds = secondsSince(terminal.last_heartbeat_at, now)
  const neverReported = terminal.last_heartbeat_at === undefined || terminal.last_heartbeat_at === null

  /*
    ORDER MATTERS, and "has it ever checked in" comes first for every state but
    two. A terminal that has never been in contact is described by that fact
    rather than by a status it has not earned yet — except for PROVISIONING,
    where never having checked in is the NORMAL state seconds after approval and
    saying so plainly stops a customer thinking their new unit is broken.
  */
  let note = ''
  if (neverReported && terminal.status === 'ONLINE') {
    note =
      'Reported online, but it has never checked in. It may have been set up without ever reaching the network.'
  } else if (neverReported && terminal.status === 'PROVISIONING') {
    note =
      'Set up, but it has not checked in yet. It finishes on its own the first time it reaches the network.'
  } else if (neverReported) {
    note = 'This terminal has never checked in.'
  } else if (terminal.status === 'ERROR') {
    // STILL IN CONTACT, and saying so is the point: this used to be presented
    // as an unreachable terminal, which sent people to the site to look at
    // something that was telling them what was wrong from where it stood.
    note =
      'The terminal is reporting a fault and needs attention on site. It is still checking in.'
  } else if (terminal.status === 'OFFLINE') {
    // WHAT IT DOES AT THE DOOR IS NOT ASSERTED HERE. That is the site's offline
    // policy, which is on this page in its own card; a terminal at a DENY_ALL
    // site refuses everybody the moment it drops, and promising otherwise would
    // describe a door wrongly.
    note =
      'The platform has not heard from this terminal recently, so changes will not reach it until it is back. What it does meanwhile is set by its site.'
  } else if (terminal.status === 'UPDATING') {
    note = 'It is installing a firmware update and restarts on its own when it finishes.'
  } else if (terminal.status === 'DISABLED') {
    note = 'Disabled. It will not let anybody in until it is re-enabled.'
  } else if (terminal.status === 'PROVISIONING') {
    note = 'Checking in, but not yet reporting as a working terminal.'
  }

  return {
    status: terminal.status,
    tone: STATUS_TONES[terminal.status] ?? 'neutral',
    heartbeatAgeSeconds,
    neverReported,
    firmwareOutdated: terminal.firmware_outdated,
    reachable: !neverReported && terminal.status !== 'OFFLINE',
    note,
  }
}

// ---------------------------------------------------------------------------
// Filtering
// ---------------------------------------------------------------------------

export interface TerminalFilter {
  /** Matches serial, name or site name, anywhere, case-insensitively. */
  search?: string
  /** Reported status, or 'ALL'. */
  status?: TerminalStatus | 'ALL'
  /** A site's PUBLIC id, or 'ALL'. Never matched on site name. */
  siteId?: string | 'ALL'
  /** Only terminals behind the current build for their channel. */
  outdatedOnly?: boolean
}

/**
 * Narrows a terminal list in the browser.
 *
 * CLIENT-SIDE IS CORRECT HERE, AND IT IS THE OPPOSITE OF THE RULE FOR PEOPLE.
 * It narrows the COMPLETE scoped fleet rather than one page of it. Doing the
 * same thing to the people list would search a page and call it a search, which
 * is why that one is done in SQL.
 *
 * THE ENDPOINT IS PAGED AND THAT IS NOT A CONTRADICTION. This comment used to
 * say `GET /console/terminals` had no limit, offset or `q`, and warned that it
 * would stop being true the moment the endpoint was paginated. D2 paginated it,
 * with a default of fifty. What keeps this function honest is not the endpoint
 * any more but `fetchTerminals` in api/endpoints.ts, which follows `has_more` to
 * the end and hands back every terminal in scope. **Do not call this on the
 * `terminals` array of a raw page response** -- that is fifty rows, and
 * filtering it would report a fleet from a sample.
 *
 * It still does not scale to a very large fleet: the whole set crosses the wire
 * and is filtered in the browser. Recorded as a market-readiness item rather
 * than papered over here.
 *
 * SITE MATCHING IS BY PUBLIC ID, never by name: names are editable and not
 * unique, so a name match would quietly include another site's hardware.
 */
export function filterTerminals(terminals: Terminal[], filter: TerminalFilter): Terminal[] {
  const search = filter.search?.trim().toLowerCase() ?? ''

  return terminals.filter((terminal) => {
    if (filter.status && filter.status !== 'ALL' && terminal.status !== filter.status) {
      return false
    }
    if (filter.siteId && filter.siteId !== 'ALL' && terminal.site_public_id !== filter.siteId) {
      return false
    }
    if (filter.outdatedOnly && !terminal.firmware_outdated) {
      return false
    }
    if (search === '') return true

    return (
      terminal.serial_number.toLowerCase().includes(search) ||
      terminal.device_name.toLowerCase().includes(search) ||
      terminal.site_name.toLowerCase().includes(search)
    )
  })
}

/** The statuses present in a fleet, for populating a filter without guessing. */
export function presentStatuses(terminals: Terminal[]): TerminalStatus[] {
  return [...new Set(terminals.map((terminal) => terminal.status))].sort()
}
