import type {
  DecisionReason,
  Permission,
  PermissionEffect,
  PermissionScope,
  ScheduleWindow,
} from '../../api/types'
import { DAY_BITS, EVERY_DAY } from '../../api/types'

/**
 * How the access-control model READS.
 *
 * The engine's vocabulary is precise and, written out literally, unreadable: a
 * rule is a scope type, an effect, a bitmask, two wall-clock strings and two
 * optional instants. This module turns each of those into something an operator
 * can check against what they meant, and it is a pure module so the awkward
 * cases are testable without a screen.
 *
 * THREE THINGS HERE ARE EASY TO GET WRONG AND EXPENSIVE WHEN THEY ARE, and each
 * has its own note below: a day mask is a bitmask and not a list, a window whose
 * end is not after its start crosses midnight, and a denial's reason is the
 * whole value of a denial.
 */

// ---------------------------------------------------------------------------
// Days
// ---------------------------------------------------------------------------

/**
 * Ordered Monday-first, matching the bit order the server defines.
 *
 * Not locale-ordered, deliberately: the ORDER here is the bit order, and a
 * Sunday-first rendering of a Monday-first mask is the kind of off-by-one that
 * produces a rule that works six days a week.
 */
export const DAYS: { bit: number; label: string; short: string }[] = [
  { bit: DAY_BITS.MONDAY, label: 'Monday', short: 'Mon' },
  { bit: DAY_BITS.TUESDAY, label: 'Tuesday', short: 'Tue' },
  { bit: DAY_BITS.WEDNESDAY, label: 'Wednesday', short: 'Wed' },
  { bit: DAY_BITS.THURSDAY, label: 'Thursday', short: 'Thu' },
  { bit: DAY_BITS.FRIDAY, label: 'Friday', short: 'Fri' },
  { bit: DAY_BITS.SATURDAY, label: 'Saturday', short: 'Sat' },
  { bit: DAY_BITS.SUNDAY, label: 'Sunday', short: 'Sun' },
]

const WEEKDAYS =
  DAY_BITS.MONDAY | DAY_BITS.TUESDAY | DAY_BITS.WEDNESDAY | DAY_BITS.THURSDAY | DAY_BITS.FRIDAY
const WEEKEND = DAY_BITS.SATURDAY | DAY_BITS.SUNDAY

export function daysOf(mask: number): number[] {
  return DAYS.filter((day) => (mask & day.bit) !== 0).map((day) => day.bit)
}

export function maskOf(bits: number[]): number {
  return bits.reduce((mask, bit) => mask | bit, 0)
}

/**
 * A day mask as words.
 *
 * The common patterns get their own names — "Every day", "Weekdays", "Weekends"
 * — because that is what somebody meant when they set them, and reading back
 * "Mon, Tue, Wed, Thu, Fri" makes them check five items to confirm one idea.
 * An empty mask is called out as never rather than rendered as an empty string,
 * which would look like a formatting bug rather than a rule that matches nothing.
 */
export function describeDays(mask: number): string {
  if (mask === 0) return 'Never'
  if (mask === EVERY_DAY) return 'Every day'
  if (mask === WEEKDAYS) return 'Weekdays'
  if (mask === WEEKEND) return 'Weekends'

  return DAYS.filter((day) => (mask & day.bit) !== 0)
    .map((day) => day.short)
    .join(', ')
}

// ---------------------------------------------------------------------------
// Windows
// ---------------------------------------------------------------------------

/** Trims a stored "HH:MM:SS" to the "HH:MM" a person typed. */
export function shortTime(value: string): string {
  return /^\d{2}:\d{2}:\d{2}$/.test(value) ? value.slice(0, 5) : value
}

/**
 * Whether a window runs into the following day.
 *
 * END NOT AFTER START MEANS IT CROSSES MIDNIGHT — a 22:00–06:00 night shift is
 * ONE window, and its day mask names the day it STARTS on. This is not a
 * validation failure and must never be presented as one: refusing it would make
 * night shifts inexpressible, and silently splitting it in two would make Sunday
 * night's shift look like Monday's.
 */
export function crossesMidnight(window: ScheduleWindow): boolean {
  return shortTime(window.end_time) <= shortTime(window.start_time)
}

export function describeWindow(window: ScheduleWindow): string {
  const span = `${shortTime(window.start_time)}–${shortTime(window.end_time)}`
  return crossesMidnight(window)
    ? `${describeDays(window.days_of_week)} ${span} (into the next day)`
    : `${describeDays(window.days_of_week)} ${span}`
}

// ---------------------------------------------------------------------------
// Rules
// ---------------------------------------------------------------------------

export const SCOPE_LABELS: Record<PermissionScope, string> = {
  COMPANY: 'Everywhere',
  SITE: 'One site',
  TERMINAL: 'One terminal',
}

/**
 * What each scope actually reaches, in the operator's terms.
 *
 * COMPANY's description carries the part nobody expects: it covers terminals
 * that DO NOT EXIST YET. Somebody granting company-wide access today is granting
 * access to whatever is installed next year, and that is worth one sentence at
 * the point of choosing.
 */
export const SCOPE_DESCRIPTIONS: Record<PermissionScope, string> = {
  COMPANY:
    'Every terminal in the company — including ones installed after this rule is written.',
  SITE: 'Every terminal at one site, including ones installed there later.',
  TERMINAL: 'Exactly one terminal.',
}

export const EFFECT_LABELS: Record<PermissionEffect, string> = {
  ALLOW: 'Allow',
  DENY: 'Deny',
}

/**
 * Where a rule applies, as a phrase.
 *
 * Uses the name and falls back to the identifier: a site or terminal the
 * projection could not name is still a rule that exists, and rendering it blank
 * would hide it.
 */
export function describeScope(permission: Permission): string {
  switch (permission.scope_type) {
    case 'SITE':
      return permission.site_name ?? permission.site_id ?? 'a site'
    case 'TERMINAL':
      return permission.device_name ?? permission.device_serial ?? 'a terminal'
    default:
      return 'Everywhere'
  }
}

/**
 * Whether a rule is doing anything RIGHT NOW.
 *
 * Validity is a window and a rule may be inactive, expired or not yet in force,
 * and all three look identical in a list that only shows dates. An operator
 * asking "why can she not get in" needs the one that applies today, and a rule
 * that reads as granted but expired last month is precisely the confusion this
 * prevents.
 *
 * SCHEDULES ARE NOT EVALUATED HERE. "In force" means the rule exists and its
 * validity period covers now — whether the schedule permits this minute is the
 * engine's answer, not the console's, and guessing at it in the browser would
 * produce a second implementation that disagrees with the door.
 */
export type RuleStanding = 'IN_FORCE' | 'NOT_YET' | 'EXPIRED' | 'INACTIVE'

export function standingOf(permission: Permission, now: Date = new Date()): RuleStanding {
  if (!permission.active) return 'INACTIVE'
  if (permission.starts_at && new Date(permission.starts_at) > now) return 'NOT_YET'
  if (permission.ends_at && new Date(permission.ends_at) <= now) return 'EXPIRED'
  return 'IN_FORCE'
}

export const STANDING_LABELS: Record<RuleStanding, string> = {
  IN_FORCE: 'In force',
  NOT_YET: 'Not yet',
  EXPIRED: 'Expired',
  INACTIVE: 'Inactive',
}

// ---------------------------------------------------------------------------
// Decisions
// ---------------------------------------------------------------------------

/**
 * Why a decision went the way it did, as a sentence AND as what to do about it.
 *
 * THE REASON IS THE ENTIRE VALUE OF A DENIAL. "Denied" tells an operator
 * standing next to a confused person nothing they cannot already see; "no rule
 * covers this terminal" and "outside the schedule on this rule" send them to
 * two completely different places. So each carries a remedy as well as a
 * meaning.
 *
 * An UNKNOWN reason still renders — the set is open so an application may define
 * its own — humanised rather than dropped.
 */
interface ReasonDefinition {
  label: string
  meaning: string
  remedy?: string
}

const REASONS: Record<string, ReasonDefinition> = {
  ALLOWED: {
    label: 'Allowed',
    meaning: 'A rule permits this person here, and nothing denies them.',
  },
  NO_PERMISSION: {
    label: 'No rule covers this',
    meaning:
      'Nothing grants this person access here. Absence of permission is not permission — a person with no rules reaches nothing.',
    remedy: 'Grant them access at this terminal, its site, or company-wide.',
  },
  EXPLICIT_DENY: {
    label: 'Denied by a rule',
    meaning:
      'A DENY rule matched. A deny beats every allow at every scope, so a company-wide grant does not override it.',
    remedy: 'Find the DENY rule on this person and remove it if it is no longer meant.',
  },
  OUTSIDE_SCHEDULE: {
    label: 'Outside the schedule',
    meaning: 'A rule covers this person here, but its schedule does not include this moment.',
    remedy: 'Check the schedule’s windows, and whether it is in the right timezone.',
  },
  PERMISSION_EXPIRED: {
    label: 'The rule has expired',
    meaning: 'A rule covered this person here, and its validity period has ended.',
    remedy: 'Extend the rule’s end date, or grant a new one.',
  },
  PERMISSION_NOT_YET_VALID: {
    label: 'The rule has not started',
    meaning: 'A rule covers this person here, but its validity period begins later.',
    remedy: 'Check the start date — a contractor starting Monday is refused on Sunday.',
  },
  PERSON_INACTIVE: {
    label: 'The person is inactive',
    meaning: 'Their record is deactivated, so no rule about them applies.',
    remedy: 'Reactivate them from their record if they should be admitted.',
  },
  PERSON_UNKNOWN: {
    label: 'Nobody matched',
    meaning:
      'The terminal read an identifier the platform does not recognise. This is a real outcome and often the most interesting one in the trail.',
  },
  CREDENTIAL_UNKNOWN: {
    label: 'The credential is not known',
    meaning: 'The terminal matched a credential the platform has no record of.',
  },
  CREDENTIAL_REVOKED: { label: 'The credential was revoked', meaning: 'It has been withdrawn.' },
  CREDENTIAL_SUSPENDED: {
    label: 'The credential is suspended',
    meaning: 'Temporarily withdrawn rather than removed.',
  },
  CREDENTIAL_EXPIRED: { label: 'The credential has expired', meaning: 'Its validity has ended.' },
  CREDENTIAL_NOT_YET_VALID: {
    label: 'The credential is not yet valid',
    meaning: 'Its validity begins later.',
  },
  APPLICATION_NOT_ENABLED: {
    label: 'The capability is not enabled',
    meaning:
      'The terminal is acting under a capability this company has switched off, so it resolves to nothing.',
    remedy: 'An owner can enable it under Applications, or the terminal can be reassigned.',
  },
  TERMINAL_DISABLED: {
    label: 'The terminal is disabled',
    meaning: 'It has been disabled or its credential revoked, so it authorises nothing.',
    remedy: 'Re-enable it from the terminal page if that was not intended.',
  },
  SITE_INACTIVE: {
    label: 'The site is deactivated',
    meaning: 'Every terminal at it stops working while it is deactivated.',
  },
  COMPANY_INACTIVE: {
    label: 'The company is suspended',
    meaning: 'The whole tenant is suspended at the platform level.',
  },
  OFFLINE_POLICY: {
    label: 'Refused by the offline policy',
    meaning:
      'The terminal could not reach the platform and its site’s offline policy refused rather than using a cached answer.',
  },
}

export function describeReason(reason: DecisionReason): ReasonDefinition {
  return (
    REASONS[reason] ?? {
      label: humanise(reason),
      meaning: 'This console has no description for that reason code.',
    }
  )
}

function humanise(code: string): string {
  const words = code.toLowerCase().split('_').filter(Boolean)
  const [first, ...rest] = words
  if (!first) return code
  return [first.charAt(0).toUpperCase() + first.slice(1), ...rest].join(' ')
}
