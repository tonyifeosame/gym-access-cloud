import { NO_VALUE, formatAbsolute, formatDateTime, formatRelative, parseInstant } from '../format/datetime'

/**
 * An instant, rendered for a reader.
 *
 * PAIRED, ALWAYS. Relative time ("4 minutes ago") answers the question a fleet
 * view is actually asking; absolute time is what someone quotes in an incident
 * report or correlates against a log. Showing only one is wrong in the other
 * case, so the relative form is displayed and the absolute form is carried in
 * `title` and in a `<time datetime>` attribute that machines and assistive
 * technology can read.
 *
 * The value is a true UTC instant — see src/format/datetime.ts for why that
 * sentence needed a database migration behind it — and is rendered in the
 * VIEWER'S zone. A site's own timezone is a different question; pass `timeZone`
 * explicitly when the question is "what time was it at the site".
 */
export function Timestamp({
  value,
  relative = false,
  timeZone,
  fallback = NO_VALUE,
}: {
  value: string | null | undefined
  /** Show "4 minutes ago" instead of the date. Absolute stays in the tooltip. */
  relative?: boolean
  timeZone?: string
  fallback?: string
}) {
  const parsed = parseInstant(value)
  if (!parsed) {
    return <span className="timestamp timestamp--empty">{fallback}</span>
  }

  const absolute = formatAbsolute(value, { timeZone })
  const shown = relative ? formatRelative(value) : formatDateTime(value, { timeZone })

  return (
    <time className="timestamp" dateTime={parsed.toISOString()} title={absolute}>
      {shown}
    </time>
  )
}
