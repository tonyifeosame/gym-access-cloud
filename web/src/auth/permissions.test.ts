import { describe, expect, it } from 'vitest'

import type { Role, Session } from '../api/types'
import { makeSession, SITE_A, SITE_B } from '../test/fixtures'
import {
  ACTION_ROLES,
  assignableRoles,
  can,
  canChangeOperatorRole,
  canDeleteOperator,
  canManageOperator,
  canManageRole,
  canReachSite,
  canReachTerminal,
  enabledApplicationCodes,
  hasApplication,
  isSelf,
  siteScope,
} from './permissions'

/**
 * Permission helpers.
 *
 * These MIRROR server rules, so the tests are written against the rules as the
 * server states them rather than against what the components happen to need. A
 * disagreement here is a button that 403s when pressed.
 */

function sessionAs(role: Role, overrides: Partial<Session> = {}): Session {
  return makeSession({
    role,
    operator: { id: 'operator-1', email: 'ops@example.com', full_name: 'Ops', role },
    ...overrides,
  })
}

describe('site reachability', () => {
  it('lets OWNER and ADMIN reach every site regardless of grants', () => {
    for (const role of ['OWNER', 'ADMIN'] as const) {
      const session = sessionAs(role, { all_sites: true, sites: [] })
      expect(canReachSite(session, 'any-site-at-all')).toBe(true)
    }
  })

  it('treats an EMPTY grant list as every site, not as none', () => {
    // The rule most easily got backwards. An unscoped MANAGER reaches
    // everything; reading empty as "no access" would show a fully-privileged
    // operator an empty console and blame their permissions.
    const session = sessionAs('MANAGER', { all_sites: true, sites: [] })
    expect(canReachSite(session, 'site-a')).toBe(true)
    expect(siteScope(session)).toBeNull()
  })

  it('scopes an operator to exactly their grants once they hold any', () => {
    const session = sessionAs('MANAGER', { all_sites: false, sites: [SITE_A] })

    expect(canReachSite(session, SITE_A.site_id)).toBe(true)
    expect(canReachSite(session, SITE_B.site_id)).toBe(false)
    expect(siteScope(session)).toEqual([SITE_A.site_id])
  })

  it('decides a terminal by the site it stands at, matching on the public id', () => {
    const session = sessionAs('MANAGER', { all_sites: false, sites: [SITE_A] })

    expect(canReachTerminal(session, { site_public_id: SITE_A.site_id })).toBe(true)
    expect(canReachTerminal(session, { site_public_id: SITE_B.site_id })).toBe(false)
  })
})

describe('role-derived actions', () => {
  const ladder: Role[] = ['VIEWER', 'MANAGER', 'ADMIN', 'OWNER']
  const rank = (role: Role) => ladder.indexOf(role)

  it('permits exactly the roles at or above each action’s minimum', () => {
    for (const [action, minimum] of Object.entries(ACTION_ROLES)) {
      for (const role of ladder) {
        const session = sessionAs(role)
        const expected = rank(role) >= rank(minimum as Role)
        expect(
          can(session, action as keyof typeof ACTION_ROLES),
          `${role} ${expected ? 'should' : 'should not'} be able to ${action}`,
        ).toBe(expected)
      }
    }
  })

  it('refuses everything without a session', () => {
    for (const action of Object.keys(ACTION_ROLES)) {
      expect(can(null, action as keyof typeof ACTION_ROLES)).toBe(false)
    }
  })

  it('keeps application configuration to OWNER and terminal config to MANAGER', () => {
    // Named explicitly because these two are the boundaries the product cares
    // about: what the deployment is FOR versus running it day to day.
    expect(can(sessionAs('ADMIN'), 'configureApplications')).toBe(false)
    expect(can(sessionAs('OWNER'), 'configureApplications')).toBe(true)
    expect(can(sessionAs('VIEWER'), 'configureTerminals')).toBe(false)
    expect(can(sessionAs('MANAGER'), 'configureTerminals')).toBe(true)
  })
})

describe('managing other operators', () => {
  it('stops an ADMIN creating, promoting to, or touching an OWNER', () => {
    // Otherwise ADMIN is a synonym for OWNER one request later.
    const admin = sessionAs('ADMIN')
    expect(canManageRole(admin, 'OWNER')).toBe(false)
    expect(canManageRole(admin, 'ADMIN')).toBe(true)
    expect(canManageOperator(admin, { id: 'other', role: 'OWNER' })).toBe(false)
    expect(assignableRoles(admin)).toEqual(['VIEWER', 'MANAGER', 'ADMIN'])
  })

  it('lets an OWNER manage every role including OWNER', () => {
    const owner = sessionAs('OWNER')
    expect(canManageRole(owner, 'OWNER')).toBe(true)
    expect(assignableRoles(owner)).toEqual(['VIEWER', 'MANAGER', 'ADMIN', 'OWNER'])
  })

  it('refuses a MANAGER any operator administration at all', () => {
    const manager = sessionAs('MANAGER')
    expect(can(manager, 'manageOperators')).toBe(false)
    expect(assignableRoles(manager)).toEqual([])
  })

  it('will not let anyone change their own role or disable themselves', () => {
    // Not a courtesy: the sole OWNER demoting themselves leaves nobody able to
    // manage operators and no way back that does not involve the database.
    const owner = sessionAs('OWNER')
    const self = { id: owner.operator.id, role: 'OWNER' as const }

    expect(isSelf(owner, self)).toBe(true)
    expect(canChangeOperatorRole(owner, self)).toBe(false)
    expect(canDeleteOperator(owner, self)).toBe(false)

    const other = { id: 'someone-else', role: 'ADMIN' as const }
    expect(canChangeOperatorRole(owner, other)).toBe(true)
    expect(canDeleteOperator(owner, other)).toBe(true)
  })
})

describe('capabilities', () => {
  it('reports no capabilities for a company with none enabled', () => {
    // The state every company starts in, and a fully working one. It must never
    // read as an error or trigger a fallback set of screens.
    const session = makeSession({ applications: [] })
    expect(enabledApplicationCodes(session)).toEqual([])
    expect(hasApplication(session, 'ACCESS_CONTROL')).toBe(false)
  })

  it('reports exactly what the company has enabled, including unknown codes', () => {
    const session = makeSession({
      applications: [
        { code: 'ATTENDANCE', settings: {} },
        // A capability newer than this build. It must still be reported rather
        // than dropped, or the console hides part of what is switched on.
        { code: 'SOMETHING_NEW', settings: {} },
      ],
    })

    expect(enabledApplicationCodes(session)).toEqual(['ATTENDANCE', 'SOMETHING_NEW'])
    expect(hasApplication(session, 'ATTENDANCE')).toBe(true)
    expect(hasApplication(session, 'SOMETHING_NEW')).toBe(true)
    expect(hasApplication(session, 'ACCESS_CONTROL')).toBe(false)
  })
})
