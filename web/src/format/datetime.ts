/**
 * Rendering instants.
 *
 * THE API SENDS TRUE UTC INSTANTS. Every timestamp is RFC 3339 with a `Z`, and
 * as of migration 010 that `Z` is accurate — the columns are TIMESTAMPTZ and the
 * API pins its database sessions to UTC. Before that migration the values were
 * wall-clock readings mislabelled `Z`, and a browser rendering them believed the
 * label. That is exactly the bug this module must never reintroduce.
 *
 * So: parse as an instant, render in the VIEWER'S OWN zone, and never construct
 * a date from parts or strip a suffix. An operator in Lagos and one in London
 * looking at the same access event should see their own local time for the same
 * moment, which is what `Date` plus `Intl` already does correctly given a
 * correctly-labelled input.
 *
 * A SITE HAS ITS OWN TIMEZONE, and that is a different question this module does
 * not answer. `site.timezone` describes where the hardware stands; these
 * functions render for whoever is reading. When a screen needs "what time was it
 * AT THE SITE", it must pass that zone explicitly — `timeZone` exists for that
 * and the distinction is deliberate rather than a default anyone can drift into.
 */

/** Returned wherever a value is absent or unparseable, so a cell is never blank. */
export const NO_VALUE = '—'

/**
 * Parses an API timestamp into a Date, or null.
 *
 * Returns null rather than an Invalid Date so callers cannot accidentally render
 * "Invalid Date" to an operator; every formatter here funnels through it.
 */
export function parseInstant(value: string | null | undefined): Date | null {
  if (!value) return null
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? null : parsed
}

export interface FormatOptions {
  /**
   * IANA zone to render in. Omitted means the viewer's own, which is the right
   * default for a console. Pass a site's zone only when the question is
   * explicitly "what time was it there".
   */
  timeZone?: string
  locale?: string
}

function formatter(options: Intl.DateTimeFormatOptions, format: FormatOptions): Intl.DateTimeFormat {
  return new Intl.DateTimeFormat(format.locale, {
    ...options,
    ...(format.timeZone ? { timeZone: format.timeZone } : {}),
  })
}

/** "14 Aug 2026, 17:20" — the default for a table cell or a detail row. */
export function formatDateTime(
  value: string | null | undefined,
  format: FormatOptions = {},
): string {
  const date = parseInstant(value)
  if (!date) return NO_VALUE
  return formatter(
    { dateStyle: 'medium', timeStyle: 'short' },
    format,
  ).format(date)
}

/** "14 Aug 2026" — where the time of day is noise. */
export function formatDate(value: string | null | undefined, format: FormatOptions = {}): string {
  const date = parseInstant(value)
  if (!date) return NO_VALUE
  return formatter({ dateStyle: 'medium' }, format).format(date)
}

/** "17:20:31" — for a dense event list where the date is already established. */
export function formatTime(value: string | null | undefined, format: FormatOptions = {}): string {
  const date = parseInstant(value)
  if (!date) return NO_VALUE
  return formatter({ timeStyle: 'medium' }, format).format(date)
}

/**
 * The full, unambiguous rendering, for a tooltip behind a relative time.
 *
 * Includes the zone name because this is the value someone quotes when
 * reporting an incident, and "17:20" without a zone is not quotable.
 */
export function formatAbsolute(
  value: string | null | undefined,
  format: FormatOptions = {},
): string {
  const date = parseInstant(value)
  if (!date) return NO_VALUE
  return formatter(
    { dateStyle: 'full', timeStyle: 'long' },
    format,
  ).format(date)
}

const MINUTE = 60
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR
const WEEK = 7 * DAY
const MONTH = 30 * DAY
const YEAR = 365 * DAY

/**
 * "3 minutes ago", "in 2 days".
 *
 * Relative time is what makes a fleet view readable — "last seen 4 minutes ago"
 * answers the question directly, where a timestamp makes the reader do the
 * arithmetic. It is deliberately paired with an absolute value in the UI
 * (see the Timestamp component) because relative time alone is useless for
 * anything anyone has to report or correlate.
 *
 * `now` is injectable so the behaviour is testable without freezing the clock.
 */
export function formatRelative(
  value: string | null | undefined,
  format: FormatOptions & { now?: Date } = {},
): string {
  const date = parseInstant(value)
  if (!date) return NO_VALUE

  const now = format.now ?? new Date()
  const seconds = Math.round((date.getTime() - now.getTime()) / 1000)
  const magnitude = Math.abs(seconds)

  // Under a minute either way reads better as a word than as "in 0 seconds".
  if (magnitude < 45) return 'just now'

  const relative = new Intl.RelativeTimeFormat(format.locale, { numeric: 'auto' })
  const [unit, size]: [Intl.RelativeTimeFormatUnit, number] =
    magnitude < HOUR
      ? ['minute', MINUTE]
      : magnitude < DAY
        ? ['hour', HOUR]
        : magnitude < WEEK
          ? ['day', DAY]
          : magnitude < MONTH
            ? ['week', WEEK]
            : magnitude < YEAR
              ? ['month', MONTH]
              : ['year', YEAR]

  return relative.format(Math.round(seconds / size), unit)
}

/**
 * Seconds elapsed since an instant, or null.
 *
 * The primitive behind staleness decisions — "is this terminal's heartbeat
 * recent enough to call it healthy". Returned as a number so the CALLER owns the
 * threshold: what counts as stale depends on the site's configured sync
 * interval, which this module has no business guessing.
 */
export function secondsSince(value: string | null | undefined, now: Date = new Date()): number | null {
  const date = parseInstant(value)
  if (!date) return null
  return Math.round((now.getTime() - date.getTime()) / 1000)
}

/**
 * A duration in seconds as "2h 15m" / "45s".
 *
 * Used for session expiry and retry-after, both of which the API expresses as
 * durations precisely so no clock has to be agreed on.
 */
export function formatDuration(totalSeconds: number | null | undefined): string {
  if (totalSeconds === null || totalSeconds === undefined || !Number.isFinite(totalSeconds)) {
    return NO_VALUE
  }

  const seconds = Math.max(0, Math.round(totalSeconds))
  if (seconds < MINUTE) return `${seconds}s`
  if (seconds < HOUR) {
    const minutes = Math.floor(seconds / MINUTE)
    const rest = seconds % MINUTE
    return rest === 0 ? `${minutes}m` : `${minutes}m ${rest}s`
  }
  if (seconds < DAY) {
    const hours = Math.floor(seconds / HOUR)
    const minutes = Math.floor((seconds % HOUR) / MINUTE)
    return minutes === 0 ? `${hours}h` : `${hours}h ${minutes}m`
  }
  const days = Math.floor(seconds / DAY)
  const hours = Math.floor((seconds % DAY) / HOUR)
  return hours === 0 ? `${days}d` : `${days}d ${hours}h`
}
