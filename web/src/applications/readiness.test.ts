import { describe, expect, it } from 'vitest'

import { anyOperational, implementationOf, readinessOf, summariseReadiness } from './readiness'

/**
 * Capability readiness.
 *
 * The property under test is a claim about the PRODUCT, not about a component:
 * a capability that is available, enabled and configured is still not
 * operational unless the platform carries out the workflow. Every other state
 * here is a fact about a database row, and a console can report all three
 * truthfully and still tell a customer something false.
 */

const catalogue = {
  available: ['ACCESS_CONTROL', 'ATTENDANCE', 'CHECK_IN'],
  enabled: ['ATTENDANCE'],
  configured: [
    {
      id: 'app-1',
      code: 'ATTENDANCE',
      enabled: true,
      settings: { rounding: 15 },
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-02T00:00:00Z',
    },
  ],
}

describe('the four states', () => {
  it('reports enabled and configured WITHOUT reporting operational', () => {
    // The whole point. Attendance is switched on and has stored settings, and
    // the platform does nothing with either.
    const readiness = readinessOf('ATTENDANCE', catalogue)

    expect(readiness.available).toBe(true)
    expect(readiness.enabled).toBe(true)
    expect(readiness.configured).toBe(true)
    expect(readiness.operational).toBe(false)
  })

  it('does not call an unbuilt capability operational just because it is enabled', () => {
    expect(readinessOf('ATTENDANCE', catalogue).implementation).toBe('NOT_IMPLEMENTED')
    expect(summariseReadiness(readinessOf('ATTENDANCE', catalogue))).toMatch(/not operational/i)
  })

  it('does not call a built capability operational while it is switched off', () => {
    // Operational is a conjunction: enabled AND implemented. Anything else makes
    // the word useless.
    const built = readinessOf('ACCESS_CONTROL', catalogue)
    expect(built.enabled).toBe(false)
    expect(built.operational).toBe(false)
  })

  it('separates configured from enabled', () => {
    const neverConfigured = readinessOf('CHECK_IN', catalogue)
    expect(neverConfigured.available).toBe(true)
    expect(neverConfigured.enabled).toBe(false)
    expect(neverConfigured.configured).toBe(false)
  })

  it('treats a capability the platform does not offer as unavailable', () => {
    const absent = readinessOf('TIME_TRACKING', catalogue)
    expect(absent.available).toBe(false)
    expect(summariseReadiness(absent)).toBe('Not offered by this platform')
  })
})

describe('what this build will vouch for', () => {
  it('carries a specific gap for everything unfinished', () => {
    // A generic "in development" is not actionable and is not what a buyer is
    // entitled to. Every unfinished capability has to say what is missing.
    for (const code of [
      'ACCESS_CONTROL',
      'ATTENDANCE',
      'REGISTRATION',
      'CHECK_IN',
      'VERIFICATION',
      'TIME_TRACKING',
      'VISITOR_MANAGEMENT',
    ]) {
      const record = implementationOf(code)
      expect(record.status).not.toBe('IMPLEMENTED')
      expect(record.gap, `${code} must say what is missing`).toBeTruthy()
    }
  })

  it('refuses to vouch for a capability newer than this console', () => {
    // The safe direction to be wrong in is the modest one: a code this build has
    // never met is one it cannot make claims about.
    const unknown = implementationOf('QUANTUM_TURNSTILES')
    expect(unknown.status).toBe('NOT_IMPLEMENTED')
    expect(unknown.gap).toMatch(/newer than this version/i)
  })

  it('reports that NOTHING a company can enable is operational today', () => {
    // The honest summary of the product as it stands. This test is expected to
    // change — when it does, a capability has genuinely started working.
    expect(
      anyOperational({
        available: [
          'ACCESS_CONTROL',
          'ATTENDANCE',
          'REGISTRATION',
          'CHECK_IN',
          'VERIFICATION',
          'TIME_TRACKING',
          'VISITOR_MANAGEMENT',
        ],
        enabled: [
          'ACCESS_CONTROL',
          'ATTENDANCE',
          'REGISTRATION',
          'CHECK_IN',
          'VERIFICATION',
          'TIME_TRACKING',
          'VISITOR_MANAGEMENT',
        ],
        configured: [],
      }),
    ).toBe(false)
  })
})
