import type { FirmwareVersion, Terminal } from '../../api/types'
import { firmwareOfferability, terminalsOffered } from './offerability'

/**
 * Where the fleet stands on firmware, and what — if anything — to do about it.
 *
 * ---------------------------------------------------------------------------
 * WHY THIS IS A MODULE AND NOT A SCREEN
 * ---------------------------------------------------------------------------
 *
 * The Firmware screen used to be organised around the CATALOGUE: builds grouped
 * by device type and release channel, each row offering "Make current". That is
 * the shape of the data. It is not the shape of the question, which is "are my
 * terminals all right, and do I need to do anything" — and the two are
 * transposes of each other, so the screen answered a question nobody asked.
 *
 * Everything here turns the first shape into the second, as pure functions,
 * because the arithmetic is where the expensive mistakes live: miscounting what
 * an update would reach, or calling something an update when it is not one.
 *
 * THE ONE THING THIS MODULE WILL NOT DO IS ORDER VERSION STRINGS. Ordering is
 * the DEVICE's job — `firmware_update.cpp` refuses anything not strictly newer
 * than what it runs — and a second implementation here that could disagree with
 * the one that actually gates the flash write would be worse than no comparison
 * at all. Where this module needs "newer", it means "published later than the
 * version currently set as the target", which is a fact the catalogue carries.
 */

// ---------------------------------------------------------------------------
// What the console knows about the fleet
// ---------------------------------------------------------------------------

/**
 * Whether the terminal list is actually in hand.
 *
 * THE POINT OF THE TYPE IS THAT THERE IS NO NUMBER TO READ WHEN IT IS NOT.
 * A plain `Terminal[]` made "none" and "no idea" the same value, which is how
 * the page once came to promise that a fleet-wide rollout would change nothing.
 */
export type FleetKnowledge =
  | { known: true; terminals: Terminal[] }
  | { known: false; reason: 'loading' | 'unavailable' }

// ---------------------------------------------------------------------------
// Fleet standing
// ---------------------------------------------------------------------------

/**
 * The three states a terminal can be in, from the customer's point of view.
 *
 * ---------------------------------------------------------------------------
 * "HASN'T REPORTED" IS NOT "BEHIND", AND THE API CANNOT MAKE THAT DISTINCTION
 * ---------------------------------------------------------------------------
 *
 * `database/firmware.go` computes `firmware_outdated` as:
 *
 *     WHEN fv.version IS NULL          THEN FALSE   -- no target, nothing to compare
 *     WHEN d.firmware_version IS NULL  THEN TRUE    -- <-- this one
 *     ELSE d.firmware_version <> fv.version
 *
 * So a terminal that has never checked in — a unit registered this morning and
 * not yet powered on at the door — is reported as OUTDATED. That is a defensible
 * server-side default: it is certainly not known to be current. It is a bad
 * thing to show a customer, because "1 terminal needs an update" sends somebody
 * to look for an update that does not exist, when what actually happened is that
 * a terminal has not been switched on yet.
 *
 * THE FIX IS HERE AND ONLY HERE. `firmware_outdated` is unchanged, the API is
 * unchanged, and the flag still drives everything it drove before. What changes
 * is that this module reads `firmware_version` FIRST: a terminal with no
 * reported version is counted as having no reported version, whatever the flag
 * says about it, and it is never added to the number a customer would act on.
 */
export interface FleetStanding {
  /** Running the version set as the target for their type and channel. */
  upToDate: number
  /** Reported a version, and it is not the target. */
  updateAvailable: number
  /** Has never told the platform what it runs. NOT counted as behind. */
  neverReported: number
  total: number
}

export function fleetStanding(terminals: Terminal[]): FleetStanding {
  let upToDate = 0
  let updateAvailable = 0
  let neverReported = 0

  for (const terminal of terminals) {
    // ORDER MATTERS. The absence of a reported version outranks the flag,
    // because the flag is true for exactly this case and means something else.
    if (!terminal.firmware_version) {
      neverReported += 1
    } else if (terminal.firmware_outdated) {
      updateAvailable += 1
    } else {
      upToDate += 1
    }
  }

  return { upToDate, updateAvailable, neverReported, total: terminals.length }
}

// ---------------------------------------------------------------------------
// Grouping — the pair "current" is actually scoped to
// ---------------------------------------------------------------------------

export interface TargetGroup {
  key: string
  deviceType: string
  releaseChannel: string
  versions: FirmwareVersion[]
  current: FirmwareVersion | null
  /** Terminals the OPERATOR CAN SEE on this combination. */
  terminals: Terminal[]
  terminalCount: number
  /** How many of those are already running the current version. */
  onCurrent: number
}

export function targetKey(version: { device_type: string; release_channel: string }): string {
  return `${version.device_type}--${version.release_channel}`
}

/**
 * Groups versions by the pair that "current" is actually scoped to.
 *
 * The terminal counts come from the fleet list the console already holds, and
 * are therefore narrowed by the operator's site grants — which the screen
 * states, because a number that looks company-wide and is not is worse than no
 * number.
 *
 * Exported for the tests: the grouping is where an off-by-one in "how many are
 * already on this version" would hide, and that number is the difference
 * between an update that reaches nobody and one that reaches a fleet.
 */
export function groupByTarget(
  versions: FirmwareVersion[],
  terminals: Terminal[],
): TargetGroup[] {
  const groups = new Map<string, TargetGroup>()

  for (const version of versions) {
    const key = targetKey(version)
    const group = groups.get(key) ?? {
      key,
      deviceType: version.device_type,
      releaseChannel: version.release_channel,
      versions: [],
      current: null,
      terminals: [],
      terminalCount: 0,
      onCurrent: 0,
    }
    group.versions.push(version)
    if (version.is_current) group.current = version
    groups.set(key, group)
  }

  for (const group of groups.values()) {
    const matching = terminals.filter(
      (terminal) =>
        terminal.device_type === group.deviceType &&
        terminal.release_channel === group.releaseChannel,
    )
    group.terminals = matching
    group.terminalCount = matching.length
    group.onCurrent = group.current
      ? matching.filter((terminal) => terminal.firmware_version === group.current?.version).length
      : 0

    // Newest first, with the version in use pinned to the top: it is the one
    // the group is about, and hunting for a badge in a version-sorted list is
    // work the screen can do instead.
    group.versions.sort((a, b) => {
      if (a.is_current !== b.is_current) return a.is_current ? -1 : 1
      return (b.published_at ?? b.created_at).localeCompare(a.published_at ?? a.created_at)
    })
  }

  /*
    THE GROUP WITH HARDWARE BEHIND IT COMES FIRST.

    This once sorted by key, which is alphabetical on a machine name — so a
    combination with no terminals at all outranked the one carrying the entire
    fleet. Ties fall back to the key so the order stays stable, and when the
    terminal list is unavailable every count is zero and the whole list falls
    back to it, which is the honest outcome: with nothing known about the fleet
    there is no relevance to sort by.
  */
  return [...groups.values()].sort((a, b) => {
    if (a.terminalCount !== b.terminalCount) return b.terminalCount - a.terminalCount
    return a.key.localeCompare(b.key)
  })
}

/** Groups with hardware on them. The only ones the main view ever shows. */
export function groupsWithTerminals(groups: TargetGroup[]): TargetGroup[] {
  return groups.filter((group) => group.terminalCount > 0)
}

// ---------------------------------------------------------------------------
// How a version stands against the one in use
// ---------------------------------------------------------------------------

export type Standing = 'CURRENT' | 'NEWER' | 'OLDER' | 'UNCOMPARED'

/**
 * How a version compares to its group's target.
 *
 * BY PUBLICATION DATE, not by parsing the version string — see the module note.
 * A date is a fact the catalogue already carries; an ordering of version strings
 * is a second opinion about the only comparison that actually matters, which
 * happens on the device.
 *
 * Exported for the tests: "which of these is the upgrade" is the question this
 * screen exists to answer, and getting it backwards points a customer at a
 * version their terminals will refuse.
 */
export function standingOf(
  version: FirmwareVersion,
  current: FirmwareVersion | null,
): Standing {
  if (version.is_current) return 'CURRENT'
  if (!current) return 'UNCOMPARED'
  const at = version.published_at ?? version.created_at
  const currentAt = current.published_at ?? current.created_at
  return at > currentAt ? 'NEWER' : 'OLDER'
}

// ---------------------------------------------------------------------------
// What, if anything, the customer should do
// ---------------------------------------------------------------------------

interface FindingBase {
  /** The group's key, so a finding can be matched back to its panel. */
  key: string
  deviceType: string
  releaseChannel: string
  terminalCount: number
}

/**
 * A real update: newer than the version in use, deliverable, and it would
 * actually reach somebody.
 */
export interface UpdateFinding extends FindingBase {
  kind: 'UPDATE'
  version: FirmwareVersion
  /** The version it would replace, for the confirmation. */
  previous: FirmwareVersion | null
  /** Terminals that would be offered it, narrowed as the server narrows it. */
  wouldReach: number
}

/**
 * Something relevant cannot be installed, and the customer needs to know.
 *
 * TWO SHAPES, BOTH VISIBLE IN THE MAIN VIEW and neither of them behind the
 * Advanced disclosure:
 *
 *   TARGET_BLOCKED  the version the fleet is pointed at will never be sent, so
 *                   nothing is being offered to anybody and the fleet is frozen
 *                   wherever it happens to be. This is a live fault.
 *   UPDATE_BLOCKED  a newer version exists and the platform will not send it, so
 *                   the update a customer is entitled to expect is not
 *                   happening. Hiding it would make the newest version simply
 *                   vanish from the screen.
 */
export interface BlockedFinding extends FindingBase {
  kind: 'TARGET_BLOCKED' | 'UPDATE_BLOCKED'
  version: FirmwareVersion
  /** Every unmet rule, in the customer's terms, naming the field to fix. */
  problems: string[]
}

/**
 * Terminals exist on this combination and no version is set as their target.
 *
 * NOT AN ACTION, DELIBERATELY. With nothing set as the target, nothing is being
 * offered and every terminal reports as up to date whatever it is running — so
 * this is worth saying. But choosing WHICH version to set would require the
 * console to know which of them is newest, and the console does not order
 * version strings. So it says what is true and sends the decision to Advanced,
 * where a person who knows the answer makes it.
 */
export interface NoTargetFinding extends FindingBase {
  kind: 'NO_TARGET'
  versionCount: number
}

export type Finding = UpdateFinding | BlockedFinding | NoTargetFinding

/**
 * What the main view has to say, in the order it says it.
 *
 * ONLY GROUPS WITH TERMINALS. A catalogue combination nobody owns hardware for
 * produces no finding at all: it is not a fault, it is not an opportunity, and
 * a panel explaining that it affects nothing is a panel about nothing. Those
 * live under Advanced with the rest of the catalogue.
 *
 * NOTHING AT ALL WHEN THE FLEET IS UNKNOWN. Every finding here carries or
 * implies a count, and a count derived from a list that failed to load is the
 * exact failure this screen has been bitten by before. The caller renders the
 * fleet's own "unavailable" state instead.
 */
export function findings(groups: TargetGroup[], fleet: FleetKnowledge): Finding[] {
  if (!fleet.known) return []

  const found: Finding[] = []

  for (const group of groupsWithTerminals(groups)) {
    const base: FindingBase = {
      key: group.key,
      deviceType: group.deviceType,
      releaseChannel: group.releaseChannel,
      terminalCount: group.terminalCount,
    }

    if (!group.current) {
      found.push({ ...base, kind: 'NO_TARGET', versionCount: group.versions.length })
      continue
    }

    // The target itself first: if the fleet is pointed at something that cannot
    // be sent, that outranks any opportunity, because nothing is moving at all.
    const target = firmwareOfferability(group.current)
    if (!target.deliverable) {
      found.push({
        ...base,
        kind: 'TARGET_BLOCKED',
        version: group.current,
        problems: target.problems,
      })
    }

    const newest = newestNewer(group)
    if (!newest) continue

    const offerability = firmwareOfferability(newest)
    if (!offerability.deliverable) {
      found.push({
        ...base,
        kind: 'UPDATE_BLOCKED',
        version: newest,
        problems: offerability.problems,
      })
      continue
    }

    const wouldReach = terminalsOffered(newest, group.terminals).length
    // Reaching nobody is not an update. Every terminal on this combination is
    // already running it, so offering the action would be offering a change
    // that changes nothing.
    if (wouldReach === 0) continue

    found.push({ ...base, kind: 'UPDATE', version: newest, previous: group.current, wouldReach })
  }

  return found
}

/**
 * The newest version in a group that was published after the one in use.
 *
 * `group.versions` is already sorted newest-first with the target pinned to the
 * top, so this cannot simply take the first element — the pin would return the
 * target itself. It filters on standing instead, which is the same comparison
 * the badges use, so the row a customer sees marked "Update available" is
 * exactly the one this offers.
 */
function newestNewer(group: TargetGroup): FirmwareVersion | null {
  const newer = group.versions.filter((version) => standingOf(version, group.current) === 'NEWER')
  if (newer.length === 0) return null

  return newer.reduce((newest, version) =>
    (version.published_at ?? version.created_at) > (newest.published_at ?? newest.created_at)
      ? version
      : newest,
  )
}
