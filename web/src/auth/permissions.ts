import type { ApplicationCode, Role, Session } from '../api/types'
import { roleAtLeast } from './roles'

/**
 * What the signed-in operator may do.
 *
 * THIS IS NOT AN AUTHORIZATION BOUNDARY, and the distinction is not pedantry.
 * The API authenticates and authorizes every request on its own; it would refuse
 * anything this file wrongly permitted, and it does not consult this file for
 * anything. The job here is to stop the console offering a control that would
 * only produce a 403 — a courtesy to the operator, not a security measure.
 *
 * The rules below MIRROR the server's, and every one names the file it mirrors.
 * When they disagree the server wins, and the visible symptom is a button that
 * errors when pressed. Anyone tempted to relax a rule here to "make the UI work"
 * has found a server rule they disagree with and should change that instead.
 */

// ---------------------------------------------------------------------------
// Site reachability
// ---------------------------------------------------------------------------

/**
 * Whether the operator may act on one site.
 *
 * Mirrors RequireSiteGrant in middleware/operator_auth.go, including the part
 * that is counter-intuitive:
 *
 *   - OWNER and ADMIN reach every site in the company, grants or no grants.
 *   - An operator holding NO grants is likewise unscoped — absence is the
 *     default, not a denial. An empty grant list means EVERY site, not none.
 *   - Once an operator holds any grant they are scoped to exactly those.
 *
 * Getting the empty case backwards would show a fully-privileged operator an
 * empty console and tell them they have no access.
 */
export function canReachSite(session: Session, sitePublicId: string): boolean {
  if (session.all_sites) return true
  return session.sites.some((grant) => grant.site_id === sitePublicId)
}

/**
 * Whether the operator may act on a terminal, decided by the site it stands at.
 *
 * Mirrors RequireTerminalGrant. A terminal is bolted to a door at exactly one
 * site, so the grant that governs it is the grant on that site — there is no
 * second rule. Takes the site's PUBLIC id, which is what `site_public_id` on a
 * terminal carries; matching on `site_name` would be wrong the moment two sites
 * are named alike.
 */
export function canReachTerminal(session: Session, terminal: { site_public_id: string }): boolean {
  return canReachSite(session, terminal.site_public_id)
}

/**
 * The sites an operator may act on, as public ids, or `null` for "unscoped".
 *
 * `null` rather than a list of every site id, deliberately: the console does not
 * necessarily know the full set, and "not narrowed" is a different statement
 * from "narrowed to this list which happens to be everything". Callers filtering
 * a collection should treat null as "keep everything".
 */
export function siteScope(session: Session): string[] | null {
  if (session.all_sites) return null
  return session.sites.map((grant) => grant.site_id)
}

// ---------------------------------------------------------------------------
// Role-derived capabilities
// ---------------------------------------------------------------------------

/**
 * The role each action requires, mirroring the route groups in router.go.
 *
 * Named actions rather than raw role comparisons scattered through components:
 * when a route's minimum role changes on the server, there is one line to change
 * here and no component to hunt through.
 */
export const ACTION_ROLES = {
  /** Reads: any authenticated operator in the company. */
  viewPeople: 'VIEWER',
  viewTerminals: 'VIEWER',
  viewSites: 'VIEWER',
  viewApplications: 'VIEWER',

  /** Day-to-day writes. */
  managePeople: 'MANAGER',
  configureTerminals: 'MANAGER',
  manageSiteSettings: 'MANAGER',

  /** Operator administration. */
  manageOperators: 'ADMIN',

  /** What the whole company's deployment is for. */
  configureApplications: 'OWNER',
} as const satisfies Record<string, Role>

export type Action = keyof typeof ACTION_ROLES

export function can(session: Session | null, action: Action): boolean {
  if (!session) return false
  return roleAtLeast(session.role, ACTION_ROLES[action])
}

/**
 * Whether the caller may create or modify an account at the given role.
 *
 * Mirrors mayManageRole in handlers/console.go. An ADMIN manages everyone below
 * an OWNER but may not create one, promote anyone to one, or touch an existing
 * one — otherwise ADMIN would be a synonym for OWNER one request later and the
 * distinction the role matrix draws would be decorative.
 */
export function canManageRole(session: Session | null, targetRole: Role): boolean {
  if (!session) return false
  if (targetRole === 'OWNER') return session.role === 'OWNER'
  return roleAtLeast(session.role, 'ADMIN')
}

/**
 * Whether the caller may modify a particular operator account.
 *
 * Two server rules, both mirrored: the target's role must be one the caller may
 * manage, and NOBODY MAY CHANGE THEIR OWN ROLE OR DISABLE THEMSELVES. The second
 * is not a courtesy — the sole OWNER of a company demoting themselves would
 * leave nobody able to manage operators and no way back that does not involve
 * the database.
 */
export function canManageOperator(
  session: Session | null,
  target: { id: string; role: Role },
): boolean {
  if (!session) return false
  return canManageRole(session, target.role)
}

export function isSelf(session: Session | null, target: { id: string }): boolean {
  return Boolean(session) && session?.operator.id === target.id
}

/** Changing your own role or active state is refused by the server. */
export function canChangeOperatorRole(
  session: Session | null,
  target: { id: string; role: Role },
): boolean {
  return canManageOperator(session, target) && !isSelf(session, target)
}

/** Removing your own account is refused by the server. */
export function canDeleteOperator(
  session: Session | null,
  target: { id: string; role: Role },
): boolean {
  return canManageOperator(session, target) && !isSelf(session, target)
}

/** The roles this caller may assign, for populating a role selector. */
export function assignableRoles(session: Session | null): Role[] {
  const all: Role[] = ['VIEWER', 'MANAGER', 'ADMIN', 'OWNER']
  return all.filter((role) => canManageRole(session, role))
}

// ---------------------------------------------------------------------------
// Capabilities
// ---------------------------------------------------------------------------

/**
 * Whether the company has a capability switched on.
 *
 * A company with NOTHING enabled is a legitimate, fully-working state — it is
 * the state every company starts in. This must never be treated as an error or
 * as grounds for falling back to a default set of screens, which would be the
 * console quietly deciding what the platform is for.
 */
export function hasApplication(session: Session | null, code: ApplicationCode): boolean {
  return Boolean(session?.applications.some((application) => application.code === code))
}

export function enabledApplicationCodes(session: Session | null): ApplicationCode[] {
  return session?.applications.map((application) => application.code) ?? []
}
