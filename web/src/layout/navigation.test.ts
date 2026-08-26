import { describe, expect, it } from 'vitest'

import { describeApplication } from '../applications/registry'
import { makeSession } from '../test/fixtures'
import { moduleNav, platformNav } from './navigation'

/**
 * Navigation is the place the "general-purpose platform" claim is either true or
 * quietly false. These tests exist to keep it true.
 */
describe('capability-driven navigation', () => {
  /*
   * A CAPABILITY EARNS A MENU ENTRY BY HAVING SOMEWHERE TO GO.
   *
   * Every enabled capability used to produce one, pointing at a shared route
   * that rendered a page explaining its screens had not been built. A company
   * with six capabilities enabled got six such entries, and following any of
   * them cost an operator a click to be told about the state of our
   * development. No definition declares a `route` today, so no capability
   * produces an entry -- which is the behaviour these tests pin.
   *
   * The MECHANISM is intact and still the product's claim: navigation is
   * derived from the session, nothing is hard-coded, and two companies with
   * different capabilities are still served by one build. That claim is now
   * carried by Features and by terminal application modes rather than by menu
   * entries that lead nowhere.
   */

  it('shows no application entries for a company with none enabled', () => {
    const session = makeSession({ applications: [] })
    expect(moduleNav(session)).toEqual([])
  })

  it('gives no entry to an enabled capability that has no screen', () => {
    const session = makeSession({
      applications: [
        { code: 'ATTENDANCE', settings: {} },
        { code: 'VISITOR_MANAGEMENT', settings: {} },
      ],
    })

    expect(moduleNav(session)).toEqual([])
  })

  it('leads nowhere rather than to a page about unbuilt screens', () => {
    // The specific regression: an entry whose path was /applications/:slug.
    const session = makeSession({
      applications: [
        { code: 'ACCESS_CONTROL', settings: {} },
        { code: 'TIME_TRACKING', settings: {} },
        { code: 'CHECK_IN', settings: {} },
      ],
    })

    expect(moduleNav(session).map((item) => item.path)).toEqual([])
  })

  it('gives a capability this build has never heard of no entry either', () => {
    // The API's catalog is still the authority for what EXISTS -- an unknown
    // code is described and configurable under Features. It simply does not
    // acquire navigation this console has no screen for.
    const session = makeSession({
      applications: [{ code: 'OCCUPANCY_MONITORING', settings: {} }],
    })

    expect(moduleNav(session)).toEqual([])
    // Still describable, which is what keeps it visible where it matters.
    expect(describeApplication('OCCUPANCY_MONITORING').label).toBe('Occupancy Monitoring')
  })

  it('describes a capability without inventing a route for it', () => {
    // `route` is the single switch that puts one back in the menu, so an
    // accidental default would restore every dead entry at once.
    for (const code of ['ACCESS_CONTROL', 'ATTENDANCE', 'REGISTRATION', 'CHECK_IN',
      'VERIFICATION', 'TIME_TRACKING', 'VISITOR_MANAGEMENT', 'OCCUPANCY_MONITORING']) {
      expect(describeApplication(code).route, `${code} must not claim a screen`).toBeUndefined()
    }
  })

  it('never lists MULTI_PURPOSE as an application', () => {
    // It is a terminal mode, not a company capability. The API rejects it as
    // one; the console must not invent it either.
    const known = ['ACCESS_CONTROL', 'ATTENDANCE', 'REGISTRATION', 'CHECK_IN',
      'VERIFICATION', 'TIME_TRACKING', 'VISITOR_MANAGEMENT']
    for (const code of known) {
      expect(describeApplication(code).code).not.toBe('MULTI_PURPOSE')
    }
  })
})

describe('platform navigation', () => {
  it('is present regardless of which capabilities are enabled', () => {
    const nothing = platformNav('OWNER').map((item) => item.id)
    expect(nothing).toContain('people')
    expect(nothing).toContain('terminals')
    expect(nothing).toContain('sites')
  })

  it('gates operator and application administration by role', () => {
    expect(platformNav('VIEWER').map((item) => item.id)).not.toContain('operators')
    expect(platformNav('MANAGER').map((item) => item.id)).not.toContain('operators')
    expect(platformNav('ADMIN').map((item) => item.id)).toContain('operators')

    // Applications are ADMIN to SEE and OWNER to CHANGE, so the nav entry
    // appears for both. The write gate lives on the controls rather than on the
    // route: what a company is configured for is administrative context an
    // administrator needs, while deciding it stays a company-level decision.
    // Below ADMIN it is not offered at all.
    expect(platformNav('MANAGER').map((item) => item.id)).not.toContain('applications')
    expect(platformNav('ADMIN').map((item) => item.id)).toContain('applications')
    expect(platformNav('OWNER').map((item) => item.id)).toContain('applications')
  })
})
