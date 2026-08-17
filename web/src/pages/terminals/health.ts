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
 * that has never sent a heartbeat. That is not a stale device — it is a sign
 * that the device registered and then never called home, or that the sweep is
 * not running. Naming it is useful; guessing a cutoff is not.
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

  let note = ''
  if (neverReported && terminal.status === 'ONLINE') {
    note =
      'Reported online but has never sent a heartbeat. It may have registered without ever connecting.'
  } else if (neverReported) {
    note = 'This terminal has never sent a heartbeat.'
  } else if (terminal.status === 'ERROR') {
    note = 'The terminal is reporting a fault and needs attention on site.'
  } else if (terminal.status === 'DISABLED') {
    note = 'Disabled. It will not authenticate until it is re-enabled.'
  } else if (terminal.status === 'PROVISIONING') {
    note = 'Registered but not yet reporting as a working terminal.'
  }

  return {
    status: terminal.status,
    tone: STATUS_TONES[terminal.status] ?? 'neutral',
    heartbeatAgeSeconds,
    neverReported,
    firmwareOutdated: terminal.firmware_outdated,
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
 * `GET /console/terminals` returns the caller's WHOLE scoped fleet in one
 * response — there is no limit, offset or `q` — so filtering here narrows the
 * complete set rather than one page of it. Doing the same thing to the people
 * list would search a page and call it a search, which is why that one is done
 * in SQL.
 *
 * That is a property of the current API, not a principle. It stops being true
 * the moment the endpoint is paginated, and it does not scale to a very large
 * fleet — both recorded as market-readiness items rather than papered over here.
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
