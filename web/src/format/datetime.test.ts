import { describe, expect, it } from 'vitest'

import {
  NO_VALUE,
  formatDate,
  formatDateTime,
  formatDuration,
  formatRelative,
  parseInstant,
  secondsSince,
} from './datetime'

/**
 * Timestamp rendering.
 *
 * The bug behind these tests was real and cost a database migration: the API
 * used to return wall-clock readings labelled `Z`, so a browser trusting the
 * label displayed a time that was wrong by the database server's offset. The
 * contract now is that a `Z` means what it says, and these assertions pin the
 * client half of it — parse as an INSTANT, render in the reader's zone, and
 * never reconstruct a date from its parts.
 */

describe('parsing', () => {
  it('reads a Z-suffixed timestamp as the instant it names', () => {
    const parsed = parseInstant('2026-08-14T17:00:00Z')
    expect(parsed?.toISOString()).toBe('2026-08-14T17:00:00.000Z')
  })

  it('honours an explicit offset rather than discarding it', () => {
    // The same instant, spelled two ways. Treating "+01:00" as if it were UTC
    // is exactly the class of error this whole area exists to prevent.
    expect(parseInstant('2026-08-14T18:00:00+01:00')?.toISOString()).toBe(
      parseInstant('2026-08-14T17:00:00Z')?.toISOString(),
    )
  })

  it('returns null for absent or unparseable values rather than an Invalid Date', () => {
    for (const value of [null, undefined, '', 'not a date']) {
      expect(parseInstant(value)).toBeNull()
    }
  })
})

describe('formatting', () => {
  it('renders a placeholder rather than a blank or "Invalid Date"', () => {
    expect(formatDateTime(null)).toBe(NO_VALUE)
    expect(formatDate(undefined)).toBe(NO_VALUE)
    expect(formatDateTime('nonsense')).toBe(NO_VALUE)
  })

  it('renders an instant in the zone it is asked for', () => {
    // 17:00Z is 18:00 in Lagos and 17:00 in UTC. Asking for a zone must change
    // the rendering and nothing else.
    const value = '2026-08-14T17:00:00Z'
    const utc = formatDateTime(value, { timeZone: 'UTC', locale: 'en-GB' })
    const lagos = formatDateTime(value, { timeZone: 'Africa/Lagos', locale: 'en-GB' })

    expect(utc).toContain('17:00')
    expect(lagos).toContain('18:00')
  })

  it('renders the same instant differently across zones without changing it', () => {
    const value = '2026-08-14T23:30:00Z'
    // Late UTC evening is already the next day in Lagos.
    expect(formatDate(value, { timeZone: 'UTC', locale: 'en-GB' })).toContain('14')
    expect(formatDate(value, { timeZone: 'Africa/Lagos', locale: 'en-GB' })).toContain('15')
  })
})

describe('relative time', () => {
  const now = new Date('2026-08-14T12:00:00Z')

  it('says "just now" inside the first minute, in either direction', () => {
    expect(formatRelative('2026-08-14T12:00:00Z', { now })).toBe('just now')
    expect(formatRelative('2026-08-14T11:59:30Z', { now })).toBe('just now')
  })

  it('scales the unit with the distance', () => {
    const cases: [string, string][] = [
      ['2026-08-14T11:50:00Z', 'minute'],
      ['2026-08-14T09:00:00Z', 'hour'],
      ['2026-08-12T12:00:00Z', 'day'],
      ['2026-07-20T12:00:00Z', 'week'],
      ['2026-02-14T12:00:00Z', 'month'],
      ['2024-08-14T12:00:00Z', 'year'],
    ]
    for (const [value, unit] of cases) {
      const rendered = formatRelative(value, { now, locale: 'en' })
      expect(rendered, `${value} should read in ${unit}s`).toMatch(
        new RegExp(`${unit}|yesterday|last`, 'i'),
      )
    }
  })

  it('handles the future, which a heartbeat from a terminal with a fast clock can be', () => {
    const rendered = formatRelative('2026-08-14T13:00:00Z', { now, locale: 'en' })
    expect(rendered).toMatch(/in .*hour/i)
  })

  it('renders a placeholder for an absent value', () => {
    expect(formatRelative(null, { now })).toBe(NO_VALUE)
  })
})

describe('elapsed seconds', () => {
  it('measures forward from the instant', () => {
    const now = new Date('2026-08-14T12:00:00Z')
    expect(secondsSince('2026-08-14T11:59:00Z', now)).toBe(60)
    expect(secondsSince('2026-08-14T12:00:30Z', now)).toBe(-30)
  })

  it('reports null rather than a number for an absent value', () => {
    // A caller deciding staleness must not read "never seen" as "seen 0s ago".
    expect(secondsSince(undefined)).toBeNull()
  })
})

describe('durations', () => {
  it('renders at a sensible granularity', () => {
    expect(formatDuration(0)).toBe('0s')
    expect(formatDuration(45)).toBe('45s')
    expect(formatDuration(60)).toBe('1m')
    expect(formatDuration(90)).toBe('1m 30s')
    expect(formatDuration(3600)).toBe('1h')
    expect(formatDuration(3900)).toBe('1h 5m')
    expect(formatDuration(86_400)).toBe('1d')
    expect(formatDuration(90_000)).toBe('1d 1h')
  })

  it('never renders a negative duration', () => {
    // Session expiry can arrive already elapsed; "-4s remaining" is nonsense.
    expect(formatDuration(-10)).toBe('0s')
  })

  it('renders a placeholder for absent or non-finite input', () => {
    expect(formatDuration(null)).toBe(NO_VALUE)
    expect(formatDuration(undefined)).toBe(NO_VALUE)
    expect(formatDuration(Number.NaN)).toBe(NO_VALUE)
  })
})
