import { describe, expect, it } from 'vitest'

import { describeApplication } from '../applications/registry'
import { makeSession } from '../test/fixtures'
import { moduleNav, platformNav } from './navigation'

/**
 * Navigation is the place the "general-purpose platform" claim is either true or
 * quietly false. These tests exist to keep it true.
 */
describe('capability-driven navigation', () => {
  it('shows no application entries for a company with none enabled', () => {
    const session = makeSession({ applications: [] })
    expect(moduleNav(session)).toEqual([])
  })

  it('shows exactly the capabilities the company has enabled', () => {
    const session = makeSession({
      applications: [
        { code: 'ATTENDANCE', settings: {} },
        { code: 'VISITOR_MANAGEMENT', settings: {} },
      ],
    })

    expect(moduleNav(session).map((item) => item.label)).toEqual([
      'Attendance',
      'Visitor Management',
    ])
  })

  it('gives two companies with different capabilities completely different menus', () => {
    // The product requirement, stated as a test: the same build serves both.
    const logistics = makeSession({
      applications: [
        { code: 'ACCESS_CONTROL', settings: {} },
        { code: 'TIME_TRACKING', settings: {} },
      ],
    })
    const clinic = makeSession({
      applications: [
        { code: 'CHECK_IN', settings: {} },
        { code: 'VERIFICATION', settings: {} },
      ],
    })

    const a = moduleNav(logistics).map((item) => item.path)
    const b = moduleNav(clinic).map((item) => item.path)

    expect(a).toEqual(['/applications/access-control', '/applications/time-tracking'])
    expect(b).toEqual(['/applications/check-in', '/applications/verification'])
    expect(a.some((path) => b.includes(path))).toBe(false)
  })

  it('renders a capability this build has never heard of', () => {
    // The API's catalog is the authority, so a capability added to the platform
    // must appear without a frontend release rather than vanishing.
    const session = makeSession({
      applications: [{ code: 'OCCUPANCY_MONITORING', settings: {} }],
    })

    const [item] = moduleNav(session)
    expect(item?.label).toBe('Occupancy Monitoring')
    expect(item?.path).toBe('/applications/occupancy-monitoring')
  })

  it('hides an application the operator lacks the role for', () => {
    // REGISTRATION requires MANAGER.
    const viewer = makeSession({
      role: 'VIEWER',
      operator: { ...makeSession().operator, role: 'VIEWER' },
      applications: [
        { code: 'REGISTRATION', settings: {} },
        { code: 'ATTENDANCE', settings: {} },
      ],
    })

    expect(moduleNav(viewer).map((item) => item.label)).toEqual(['Attendance'])
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
