import { describe, expect, it } from 'vitest'

import { readinessOf, summariseReadiness } from './readiness'

/**
 * Capability readiness.
 *
 * WHAT THIS MODULE NO LONGER DOES IS PART OF THE CONTRACT. It used to carry a
 * hard-coded, per-capability table of how much of each workflow had been built,
 * and a fourth "operational" state derived from it, both of which the console
 * rendered to operators. That is a fact about the build rather than about the
 * customer's configuration; it is tracked in docs/market-readiness.md and is
 * deliberately absent here. The three states below are all facts about this
 * company's own configuration, which is why they belong on a screen a customer
 * administers their company from.
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

describe('the three states', () => {
  it('reports available, enabled and configured independently', () => {
    const readiness = readinessOf('ATTENDANCE', catalogue)

    expect(readiness.available).toBe(true)
    expect(readiness.enabled).toBe(true)
    expect(readiness.configured).toBe(true)
    expect(readiness.record?.settings).toEqual({ rounding: 15 })
  })

  it('separates configured from enabled', () => {
    // A company can enable something it has never configured, and configure
    // something it has not enabled. Collapsing the two would leave an owner
    // unable to tell which of the two they still have to do.
    const neverConfigured = readinessOf('CHECK_IN', catalogue)
    expect(neverConfigured.available).toBe(true)
    expect(neverConfigured.enabled).toBe(false)
    expect(neverConfigured.configured).toBe(false)
  })

  it('reports an available capability that is switched off', () => {
    const off = readinessOf('ACCESS_CONTROL', catalogue)
    expect(off.available).toBe(true)
    expect(off.enabled).toBe(false)
  })

  it('treats a capability the platform does not offer as unavailable', () => {
    const absent = readinessOf('TIME_TRACKING', catalogue)
    expect(absent.available).toBe(false)
    expect(absent.enabled).toBe(false)
  })

  it('counts an empty settings object as configured', () => {
    // A row exists. A company that deliberately configured a capability with no
    // options has still configured it.
    const readiness = readinessOf('CHECK_IN', {
      available: ['CHECK_IN'],
      enabled: [],
      configured: [
        {
          id: 'app-2',
          code: 'CHECK_IN',
          enabled: false,
          settings: {},
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:00:00Z',
        },
      ],
    })

    expect(readiness.configured).toBe(true)
  })
})

describe('the one-line summary', () => {
  it('names the three states a customer can act on', () => {
    expect(summariseReadiness(readinessOf('TIME_TRACKING', catalogue))).toBe(
      'Not offered by this platform',
    )
    expect(summariseReadiness(readinessOf('ACCESS_CONTROL', catalogue))).toBe(
      'Available, not enabled',
    )
    expect(summariseReadiness(readinessOf('ATTENDANCE', catalogue))).toBe(
      'Enabled and configured',
    )
  })

  it('says nothing about how much of the platform is built', () => {
    // The regression this guards: every summary this function can return used to
    // be capable of ending in "and not yet built" or "nothing acts on it yet".
    for (const code of ['ACCESS_CONTROL', 'ATTENDANCE', 'CHECK_IN', 'TIME_TRACKING']) {
      const summary = summariseReadiness(readinessOf(code, catalogue))
      expect(summary).not.toMatch(/built|operational|implement|coming soon|yet/i)
    }
  })
})
