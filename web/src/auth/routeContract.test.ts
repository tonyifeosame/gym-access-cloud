import { readFileSync } from 'node:fs'
import { join } from 'node:path'

import { describe, expect, it } from 'vitest'

import { ACTION_ROLES } from './permissions'

/**
 * The console's permission table, checked against the server that enforces it.
 *
 * ---------------------------------------------------------------------------
 * WHY A COMMENT WAS NOT ENOUGH
 * ---------------------------------------------------------------------------
 *
 * ACTION_ROLES says it mirrors the route groups in router.go, and until now that
 * claim was maintained by whoever remembered to. The failure it invites is
 * specific and unpleasant: the server tightens a route, the table keeps the old
 * minimum, and the console goes on showing a button to somebody the server will
 * refuse. They press it and get a 403 for work they were told they could do --
 * which reads as a broken product, not as a permission boundary, and is exactly
 * the kind of thing nobody notices because the person who wrote the change was
 * an OWNER and never saw it.
 *
 * Drifting the other way is quieter and no better: the table stays stricter than
 * the server, and a MANAGER who is allowed to do something is shown no way to do
 * it. Nothing errors. The capability is simply missing, and the customer
 * concludes the product cannot do it.
 *
 * So this reads router.go -- the actual file, not a copy -- resolves what each
 * route really requires, and asserts the two agree.
 *
 * ---------------------------------------------------------------------------
 * WHAT IT DOES NOT CHECK
 * ---------------------------------------------------------------------------
 *
 * That the mapping below names the RIGHT route for an action. That is a human
 * judgement and stating it is the point: each entry is a claim that pressing
 * this control makes this request, written down where a reviewer can disagree
 * with it. What the test guarantees is that the claim, once made, cannot drift
 * out of step with the server without something going red.
 *
 * It also does not check per-site grants (RequireTerminalGrant and the site
 * scope) or CSRF. Those are enforced server-side per request and asserted in the
 * Go suite; a role minimum is the part the UI has to predict in advance in order
 * to decide whether to render a control at all.
 */

const ROUTER = join(process.cwd(), '..', 'router.go')

/** Role names in the order the server's roleAtLeast ranks them. */
type ServerRole = 'VIEWER' | 'MANAGER' | 'ADMIN' | 'OWNER'

/**
 * The gin groups router.go declares, and the role each one carries.
 *
 * READ FROM THE FILE rather than hard-coded, so a group whose minimum is changed
 * is picked up here instead of being quietly assumed. `act` is the announcement
 * write group, whose Use() sits on its own line above the routes.
 */
function groupRoles(source: string): Map<string, ServerRole> {
  const roles = new Map<string, ServerRole>()
  for (const match of source.matchAll(
    /^\s*(\w+)\.Use\([^\n]*RequireRole\(models\.Role(\w+)\)/gm,
  )) {
    roles.set(match[1]!, match[2]!.toUpperCase() as ServerRole)
  }
  return roles
}

/**
 * What one route requires: the group's role, unless the call names its own.
 *
 * An inline RequireRole on the route itself wins, which is how the announcement
 * reads sit at MANAGER inside a group that would otherwise not gate them.
 */
function routeRoles(source: string): Map<string, ServerRole> {
  const groups = groupRoles(source)
  const lines = source.split('\n')
  const found = new Map<string, ServerRole>()

  for (const [index, line] of lines.entries()) {
    const call = /\b(\w+)\.(GET|POST|PUT|PATCH|DELETE)\("([^"]*)"/.exec(line)
    if (!call) continue
    const [, group, method, path] = call

    // A route's arguments can wrap; look at the call, not just its first line.
    const window = lines.slice(index, index + 3).join(' ')
    const inline = /RequireRole\(models\.Role(\w+)\)/.exec(window)
    const role = inline
      ? (inline[1]!.toUpperCase() as ServerRole)
      : groups.get(group!)
    if (!role) continue

    found.set(`${method} ${path}`, role)
  }
  return found
}

/**
 * The request each action unlocks.
 *
 * ONE ROUTE PER ACTION, chosen as the one the control actually calls -- the
 * cheapest claim to check by reading the component. Where an action gates
 * several routes at the same minimum (operator administration is nine), naming
 * one is enough: they share a group, so they move together.
 */
const ACTION_ROUTES: Record<keyof typeof ACTION_ROLES, string> = {
  viewPeople: 'GET /people',
  viewTerminals: 'GET /terminals',
  viewSites: 'GET /sites',
  viewApplications: 'GET /applications',

  managePeople: 'POST /people',
  configureTerminals: 'PUT /terminals/:serial/application-mode',
  manageSiteSettings: 'PUT /sites/:site_id/settings',
  manageAccess: 'POST /people/:external_id/permissions',
  viewEvents: 'GET /events',

  manageOperators: 'POST /operators',
  manageAPICredentials: 'POST /api-credentials',
  manageSites: 'POST /sites',
  manageTerminalLifecycle: 'PUT /terminals/:serial/state',
  changeTerminalWifi: 'POST /terminals/:serial/wifi-recovery',
  viewAudit: 'GET /audit',
  manageFirmware: 'POST /firmware',

  // The pairing list and acting on it are DIFFERENT minimums on the server, and
  // the split in the table exists to match: a MANAGER may see that hardware is
  // waiting, and only an ADMIN may adopt it.
  // The path is empty because the group IS the path: router.go registers
  // `announcements.GET("")` on /console/terminal-announcements.
  viewPendingTerminals: 'GET ',
  addTerminals: 'POST /:id/approve',

  configureApplications: 'PUT /applications/:code',
}

describe('the console asks for what the server requires', () => {
  const source = readFileSync(ROUTER, 'utf8')
  const routes = routeRoles(source)

  it('finds the router and its role groups', () => {
    // A guard on the parse itself: if router.go is restructured so nothing
    // matches, every assertion below would pass vacuously.
    expect(routes.size).toBeGreaterThan(40)
    expect([...groupRoles(source).values()]).toEqual(
      expect.arrayContaining(['VIEWER', 'MANAGER', 'ADMIN', 'OWNER']),
    )
  })

  it.each(Object.entries(ACTION_ROUTES))(
    '%s matches the role its route enforces',
    (action, route) => {
      const server = routes.get(route)
      expect(server, `no route "${route}" in router.go for ${action}`).toBeDefined()
      expect(
        ACTION_ROLES[action as keyof typeof ACTION_ROLES],
        `${action} would ${
          server === undefined ? '' : 'show a control that the server answers with 403, or hide one it allows'
        } — router.go enforces ${server}`,
      ).toBe(server)
    },
  )

  it('leaves no action unmapped', () => {
    // Adding an action without saying which request it unlocks would opt it out
    // of the check silently, which is the failure mode this whole file is about.
    expect(Object.keys(ACTION_ROUTES).sort()).toEqual(Object.keys(ACTION_ROLES).sort())
  })
})
