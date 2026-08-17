import type { Role } from '../api/types'

/**
 * Role ordering, mirroring middleware/operator_auth.go: OWNER > ADMIN > MANAGER
 * > VIEWER. A route names the lowest role that may reach it.
 *
 * THIS IS NOT AN AUTHORIZATION BOUNDARY. The server enforces every one of these
 * gates and would refuse the request regardless of what this file says; hiding a
 * control the API would reject is a courtesy to the operator, not a security
 * measure. Anyone tempted to move a check here from the backend should read that
 * sentence again.
 */
const ROLE_RANK: Record<Role, number> = {
  VIEWER: 1,
  MANAGER: 2,
  ADMIN: 3,
  OWNER: 4,
}

/**
 * Reports whether `role` meets or exceeds `minimum`.
 *
 * An unrecognised role on either side ranks below everything and is refused.
 * A build that does not know a role cannot reason about what it may do, and
 * refusing is the safe direction to be wrong in.
 */
export function roleAtLeast(role: string | undefined, minimum: Role): boolean {
  if (!role) return false
  const held = ROLE_RANK[role as Role]
  const required = ROLE_RANK[minimum]
  if (held === undefined || required === undefined) return false
  return held >= required
}

export const ROLE_LABELS: Record<Role, string> = {
  OWNER: 'Owner',
  ADMIN: 'Administrator',
  MANAGER: 'Manager',
  VIEWER: 'Viewer',
}

export function roleLabel(role: string): string {
  return ROLE_LABELS[role as Role] ?? role
}
